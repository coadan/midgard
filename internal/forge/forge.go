package forge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"midgard/internal/artifact"
	"midgard/internal/gitrepo"
	"midgard/internal/lease"
	"midgard/internal/state"
	"midgard/internal/workbench"
)

const defaultGitHubBaseURL = "https://github.com"

type RepoLinkOptions struct {
	Root          string
	RepoID        string
	ForgeID       string
	Kind          string
	Remote        string
	BaseURL       string
	DefaultBranch string
	AuthProfile   string
}

type TaskPRLinkOptions struct {
	Root       string
	TaskID     string
	RepoID     string
	ForgeID    string
	PR         string
	GroupID    string
	BaseBranch string
	HeadBranch string
	HeadSHA    string
	Source     string
}

type RefreshOptions struct {
	Root         string
	TaskID       string
	RepoID       string
	ForgeID      string
	Number       int
	SnapshotPath string
}

type LiveRefreshOptions struct {
	Root    string
	TaskID  string
	RepoID  string
	ForgeID string
	Number  int
	Client  *GitHubClient
}

type UnlinkOptions struct {
	Root    string
	TaskID  string
	RepoID  string
	ForgeID string
	Number  int
}

type AuthStatusOptions struct {
	Root        string
	ForgeID     string
	BaseURL     string
	AuthProfile string
	Client      *GitHubClient
}

type StatusEntry struct {
	Link         state.TaskPRLink
	Snapshot     *state.ForgePRSnapshot
	Checks       []state.ForgeCheckRun
	Threads      []state.ForgeReviewThread
	RefreshAge   time.Duration
	Stale        bool
	HeadMismatch bool
	BaseMismatch bool
	Blockers     []string
	Warnings     []string
}

type ReadinessResult struct {
	Enabled  bool
	Ready    bool
	Blockers []string
	Warnings []string
}

type TaskPRStatus struct {
	Entries   []StatusEntry
	Readiness ReadinessResult
}

type RefreshResult struct {
	Link       state.TaskPRLink
	SnapshotID string
	Artifact   string
	Checks     int
	Threads    int
}

type forgeEvidence struct {
	Pull    []byte
	Checks  []byte
	Threads []byte
	Reviews []byte
}

type SnapshotFile struct {
	FetchedAt             string           `json:"fetched_at"`
	State                 string           `json:"state"`
	Draft                 bool             `json:"draft"`
	Title                 string           `json:"title"`
	Author                string           `json:"author"`
	Labels                []string         `json:"labels"`
	MergeableState        string           `json:"mergeable_state"`
	ReviewDecision        string           `json:"review_decision"`
	CheckConclusion       string           `json:"check_conclusion"`
	UnresolvedThreadCount int              `json:"unresolved_thread_count"`
	ReviewThreadsComplete bool             `json:"review_threads_complete"`
	URL                   string           `json:"url"`
	BaseBranch            string           `json:"base_branch"`
	HeadBranch            string           `json:"head_branch"`
	HeadSHA               string           `json:"head_sha"`
	MergedAt              string           `json:"merged_at"`
	ClosedAt              string           `json:"closed_at"`
	SnapshotArtifactRef   string           `json:"snapshot_artifact_ref,omitempty"`
	ChecksArtifactRef     string           `json:"checks_artifact_ref,omitempty"`
	ThreadsArtifactRef    string           `json:"threads_artifact_ref,omitempty"`
	ReviewsArtifactRef    string           `json:"reviews_artifact_ref,omitempty"`
	Checks                []SnapshotCheck  `json:"checks"`
	Threads               []SnapshotThread `json:"threads"`
	ReviewThreads         []SnapshotThread `json:"review_threads"`
}

type SnapshotCheck struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	URL         string `json:"url"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
}

type SnapshotThread struct {
	ID            string `json:"id"`
	Path          string `json:"path"`
	Line          int    `json:"line"`
	Resolved      bool   `json:"resolved"`
	LastAuthor    string `json:"last_author"`
	LastUpdatedAt string `json:"last_updated_at"`
	ArtifactRef   string `json:"artifact_ref"`
}

func LinkRepo(ctx context.Context, opts RepoLinkOptions) (state.ForgeRepo, error) {
	if opts.RepoID == "" {
		return state.ForgeRepo{}, fmt.Errorf("repo id is required")
	}
	remote, err := parseRemote(opts.Remote)
	if err != nil {
		return state.ForgeRepo{}, err
	}
	kind := firstNonBlank(opts.Kind, "github")
	forgeID := firstNonBlank(opts.ForgeID, kind+"-main")
	baseURL := firstNonBlank(opts.BaseURL, remote.BaseURL, defaultGitHubBaseURL)
	wbStatus, err := workbench.Status(opts.Root)
	if err != nil {
		return state.ForgeRepo{}, err
	}
	db, err := state.Open(ctx, workbench.NewLayout(wbStatus.Root).State)
	if err != nil {
		return state.ForgeRepo{}, err
	}
	defer db.Close()
	if _, err := db.Repo(ctx, opts.RepoID); err != nil {
		return state.ForgeRepo{}, fmt.Errorf("repo %q is not registered: %w", opts.RepoID, err)
	}
	if err := db.UpsertForgeAccount(ctx, state.ForgeAccount{
		ID:          forgeID,
		Kind:        kind,
		BaseURL:     baseURL,
		AuthProfile: opts.AuthProfile,
	}); err != nil {
		return state.ForgeRepo{}, err
	}
	repo := state.ForgeRepo{
		RepoID:        opts.RepoID,
		ForgeID:       forgeID,
		Owner:         remote.Owner,
		Name:          remote.Name,
		DefaultBranch: opts.DefaultBranch,
		URL:           strings.TrimRight(baseURL, "/") + "/" + remote.Owner + "/" + remote.Name,
	}
	if err := db.UpsertForgeRepo(ctx, repo); err != nil {
		return state.ForgeRepo{}, err
	}
	return repo, nil
}

func LinkTaskPR(ctx context.Context, opts TaskPRLinkOptions) (result state.TaskPRLink, retErr error) {
	if opts.TaskID == "" || opts.RepoID == "" {
		return state.TaskPRLink{}, fmt.Errorf("task and repo are required")
	}
	wbStatus, err := workbench.Status(opts.Root)
	if err != nil {
		return state.TaskPRLink{}, err
	}
	db, err := state.Open(ctx, workbench.NewLayout(wbStatus.Root).State)
	if err != nil {
		return state.TaskPRLink{}, err
	}
	defer db.Close()
	scope, err := lease.Ensure(ctx, db, state.LeaseResourceTask, opts.TaskID, lease.Options{})
	if err != nil {
		return state.TaskPRLink{}, err
	}
	defer func() {
		if err := scope.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	ctx = scope.Context
	if _, err := db.Task(ctx, opts.TaskID); err != nil {
		return state.TaskPRLink{}, fmt.Errorf("task %q is not registered: %w", opts.TaskID, err)
	}
	worktrees, err := db.WorktreesForTask(ctx, opts.TaskID)
	if err != nil {
		return state.TaskPRLink{}, err
	}
	ownedRepo := false
	for _, worktree := range worktrees {
		if worktree.RepoID == opts.RepoID {
			ownedRepo = true
			break
		}
	}
	if !ownedRepo {
		return state.TaskPRLink{}, fmt.Errorf("task %q does not own repo %q", opts.TaskID, opts.RepoID)
	}
	forgeID := opts.ForgeID
	var repo state.ForgeRepo
	if forgeID == "" {
		repos, listErr := db.ForgeReposForRepo(ctx, opts.RepoID)
		if listErr != nil {
			return state.TaskPRLink{}, listErr
		}
		switch len(repos) {
		case 0:
			err = sql.ErrNoRows
		case 1:
			repo = repos[0]
		default:
			return state.TaskPRLink{}, fmt.Errorf("multiple forge accounts are linked to repo %q; specify --account", opts.RepoID)
		}
	} else {
		repo, err = db.ForgeRepo(ctx, opts.RepoID, forgeID)
	}
	if err != nil {
		return state.TaskPRLink{}, fmt.Errorf("forge repo for %q is not linked: %w", opts.RepoID, err)
	}
	pr, err := parsePR(opts.PR, repo)
	if err != nil {
		return state.TaskPRLink{}, err
	}
	link := state.TaskPRLink{
		ID:         taskPRLinkID(opts.TaskID, opts.RepoID, repo.ForgeID, pr.Number),
		TaskID:     opts.TaskID,
		RepoID:     opts.RepoID,
		GroupID:    opts.GroupID,
		ForgeID:    repo.ForgeID,
		Number:     pr.Number,
		URL:        pr.URL,
		BaseBranch: opts.BaseBranch,
		HeadBranch: opts.HeadBranch,
		HeadSHA:    opts.HeadSHA,
		Source:     firstNonBlank(opts.Source, "manual"),
	}
	if err := db.UpsertTaskPRLink(ctx, link); err != nil {
		return state.TaskPRLink{}, err
	}
	payload, _ := json.Marshal(map[string]any{"link_id": link.ID, "repo_id": link.RepoID, "forge_id": link.ForgeID, "pr_number": link.Number, "source": link.Source})
	if _, err := db.InsertEvent(ctx, state.Event{TaskID: link.TaskID, Type: "forge.pr.linked", Payload: string(payload)}); err != nil {
		return state.TaskPRLink{}, err
	}
	return link, nil
}

func RefreshFromSnapshot(ctx context.Context, opts RefreshOptions) (result RefreshResult, retErr error) {
	if opts.TaskID == "" || opts.RepoID == "" || opts.Number <= 0 || opts.SnapshotPath == "" {
		return RefreshResult{}, fmt.Errorf("task, repo, pr, and snapshot are required")
	}
	wbStatus, err := workbench.Status(opts.Root)
	if err != nil {
		return RefreshResult{}, err
	}
	layout := workbench.NewLayout(wbStatus.Root)
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return RefreshResult{}, err
	}
	defer db.Close()
	scope, err := lease.Ensure(ctx, db, state.LeaseResourceTask, opts.TaskID, lease.Options{})
	if err != nil {
		return RefreshResult{}, err
	}
	defer func() {
		if err := scope.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	ctx = scope.Context
	links, err := matchingTaskPRLinks(ctx, db, opts.TaskID, opts.RepoID, opts.ForgeID, opts.Number)
	if err != nil {
		return RefreshResult{}, err
	}
	link, err := exactlyOneTaskPRLink(links)
	if err != nil {
		return RefreshResult{}, err
	}
	data, err := os.ReadFile(opts.SnapshotPath)
	if err != nil {
		return RefreshResult{}, err
	}
	var snapshotFile SnapshotFile
	if err := json.Unmarshal(data, &snapshotFile); err != nil {
		return RefreshResult{}, err
	}
	if snapshotFile.FetchedAt == "" {
		snapshotFile.FetchedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if !snapshotFile.ReviewThreadsComplete && (len(snapshotFile.Threads) > 0 || len(snapshotFile.ReviewThreads) > 0) {
		snapshotFile.ReviewThreadsComplete = true
	}
	checksEvidence, _ := json.MarshalIndent(snapshotFile.Checks, "", "  ")
	threadsEvidence, _ := json.MarshalIndent(firstThreads(snapshotFile), "", "  ")
	return persistRefresh(ctx, db, layout, link, snapshotFile, forgeEvidence{
		Pull:    append(data, '\n'),
		Checks:  append(checksEvidence, '\n'),
		Threads: append(threadsEvidence, '\n'),
		Reviews: []byte("[]\n"),
	})
}

func RefreshFromGitHub(ctx context.Context, opts LiveRefreshOptions) (results []RefreshResult, retErr error) {
	if opts.TaskID == "" {
		return nil, fmt.Errorf("task is required")
	}
	wbStatus, err := workbench.Status(opts.Root)
	if err != nil {
		return nil, err
	}
	layout := workbench.NewLayout(wbStatus.Root)
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	scope, err := lease.Ensure(ctx, db, state.LeaseResourceTask, opts.TaskID, lease.Options{})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := scope.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	ctx = scope.Context
	links, err := matchingTaskPRLinks(ctx, db, opts.TaskID, opts.RepoID, opts.ForgeID, opts.Number)
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("no linked PRs match task %q", opts.TaskID)
	}
	results = make([]RefreshResult, 0, len(links))
	for _, link := range links {
		repo, err := db.ForgeRepo(ctx, link.RepoID, link.ForgeID)
		if err != nil {
			return nil, err
		}
		account, err := db.ForgeAccount(ctx, link.ForgeID)
		if err != nil {
			return nil, err
		}
		if account.Kind != "github" {
			return nil, fmt.Errorf("forge account %q is %q, not github", account.ID, account.Kind)
		}
		client := opts.Client
		if client == nil {
			token, source, err := ResolveGitHubToken(ctx, account.BaseURL, account.AuthProfile)
			if err != nil {
				return nil, err
			}
			client = NewGitHubClient(account.BaseURL, token, source)
		}
		fetched, err := client.FetchPullRequest(ctx, repo.Owner, repo.Name, link.Number)
		if err != nil {
			return nil, fmt.Errorf("refresh %s#%d: %w", link.ForgeID, link.Number, err)
		}
		result, err := persistRefresh(ctx, db, layout, link, fetched.Snapshot, forgeEvidence{
			Pull:    fetched.PullEvidence,
			Checks:  fetched.ChecksEvidence,
			Threads: fetched.ThreadsEvidence,
			Reviews: fetched.ReviewsEvidence,
		})
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func GitHubAuthenticationStatus(ctx context.Context, opts AuthStatusOptions) (GitHubAuthStatus, error) {
	baseURL := firstNonBlank(opts.BaseURL, defaultGitHubBaseURL)
	authProfile := opts.AuthProfile
	if opts.Root != "" {
		wbStatus, err := workbench.Status(opts.Root)
		if err != nil {
			return GitHubAuthStatus{}, err
		}
		db, err := state.Open(ctx, workbench.NewLayout(wbStatus.Root).State)
		if err != nil {
			return GitHubAuthStatus{}, err
		}
		defer db.Close()
		accounts, err := db.ForgeAccounts(ctx)
		if err != nil {
			return GitHubAuthStatus{}, err
		}
		var selected *state.ForgeAccount
		for i := range accounts {
			account := &accounts[i]
			if opts.ForgeID != "" && account.ID != opts.ForgeID {
				continue
			}
			if account.Kind != "github" {
				continue
			}
			if selected != nil && opts.ForgeID == "" {
				return GitHubAuthStatus{}, fmt.Errorf("multiple GitHub accounts are configured; specify --account")
			}
			selected = account
		}
		if opts.ForgeID != "" && selected == nil {
			return GitHubAuthStatus{}, fmt.Errorf("GitHub account %q is not configured", opts.ForgeID)
		}
		if selected != nil {
			baseURL = selected.BaseURL
			authProfile = selected.AuthProfile
		}
	}
	client := opts.Client
	if client == nil {
		token, source, err := ResolveGitHubToken(ctx, baseURL, authProfile)
		if err != nil {
			return GitHubAuthStatus{}, err
		}
		client = NewGitHubClient(baseURL, token, source)
	}
	return client.AuthStatus(ctx)
}

func UnlinkTaskPR(ctx context.Context, opts UnlinkOptions) (result state.TaskPRLink, retErr error) {
	if opts.TaskID == "" || opts.Number <= 0 {
		return state.TaskPRLink{}, fmt.Errorf("task and pr are required")
	}
	wbStatus, err := workbench.Status(opts.Root)
	if err != nil {
		return state.TaskPRLink{}, err
	}
	db, err := state.Open(ctx, workbench.NewLayout(wbStatus.Root).State)
	if err != nil {
		return state.TaskPRLink{}, err
	}
	defer db.Close()
	scope, err := lease.Ensure(ctx, db, state.LeaseResourceTask, opts.TaskID, lease.Options{})
	if err != nil {
		return state.TaskPRLink{}, err
	}
	defer func() {
		if err := scope.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	ctx = scope.Context
	links, err := matchingTaskPRLinks(ctx, db, opts.TaskID, opts.RepoID, opts.ForgeID, opts.Number)
	if err != nil {
		return state.TaskPRLink{}, err
	}
	link, err := exactlyOneTaskPRLink(links)
	if err != nil {
		return state.TaskPRLink{}, err
	}
	if err := db.DeleteTaskPRLink(ctx, link.ID); err != nil {
		return state.TaskPRLink{}, err
	}
	payload, _ := json.Marshal(map[string]any{"link_id": link.ID, "repo_id": link.RepoID, "forge_id": link.ForgeID, "pr_number": link.Number})
	_, err = db.InsertEvent(ctx, state.Event{TaskID: link.TaskID, Type: "forge.pr.unlinked", Payload: string(payload)})
	return link, err
}

func TaskPRLinks(ctx context.Context, root, taskID string) ([]state.TaskPRLink, error) {
	wbStatus, err := workbench.Status(root)
	if err != nil {
		return nil, err
	}
	db, err := state.Open(ctx, workbench.NewLayout(wbStatus.Root).State)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return db.TaskPRLinks(ctx, taskID)
}

func persistRefresh(ctx context.Context, db *state.DB, layout workbench.Layout, link state.TaskPRLink, snapshotFile SnapshotFile, evidence forgeEvidence) (RefreshResult, error) {
	if snapshotFile.FetchedAt == "" {
		snapshotFile.FetchedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if snapshotFile.URL != "" {
		link.URL = snapshotFile.URL
	}
	if snapshotFile.BaseBranch != "" {
		link.BaseBranch = snapshotFile.BaseBranch
	}
	if snapshotFile.HeadBranch != "" {
		link.HeadBranch = snapshotFile.HeadBranch
	}
	if snapshotFile.HeadSHA != "" {
		link.HeadSHA = snapshotFile.HeadSHA
	}
	if err := db.UpsertTaskPRLink(ctx, link); err != nil {
		return RefreshResult{}, err
	}
	snapshotID := "forge_snap_" + shortHash(link.ID+"\x00"+snapshotFile.FetchedAt+"\x00"+snapshotFile.HeadSHA)
	prefix := filepath.ToSlash(filepath.Join("forge", safeID(link.ID), snapshotID))
	snapshotFile.SnapshotArtifactRef = "artifact:" + prefix + "/snapshot.json"
	snapshotFile.ChecksArtifactRef = "artifact:" + prefix + "/checks.json"
	snapshotFile.ThreadsArtifactRef = "artifact:" + prefix + "/threads.json"
	snapshotFile.ReviewsArtifactRef = "artifact:" + prefix + "/reviews.json"
	store := artifact.NewStore(filepath.Join(layout.Artifacts, link.TaskID))
	for _, item := range []struct {
		path string
		data []byte
	}{
		{prefix + "/pull.json", evidence.Pull},
		{prefix + "/checks.json", evidence.Checks},
		{prefix + "/threads.json", evidence.Threads},
		{prefix + "/reviews.json", evidence.Reviews},
	} {
		if len(item.data) == 0 {
			item.data = []byte("{}\n")
		}
		if _, err := putForgeArtifact(ctx, db, store, link.TaskID, item.path, item.data); err != nil {
			return RefreshResult{}, err
		}
	}
	summaryData, err := json.MarshalIndent(snapshotFile, "", "  ")
	if err != nil {
		return RefreshResult{}, err
	}
	summaryRec, err := putForgeArtifact(ctx, db, store, link.TaskID, prefix+"/snapshot.json", append(summaryData, '\n'))
	if err != nil {
		return RefreshResult{}, err
	}
	snapshot := state.ForgePRSnapshot{
		ID:                    snapshotID,
		LinkID:                link.ID,
		FetchedAt:             snapshotFile.FetchedAt,
		State:                 normalize(snapshotFile.State, "unknown"),
		Draft:                 snapshotFile.Draft,
		Title:                 snapshotFile.Title,
		Author:                snapshotFile.Author,
		Labels:                strings.Join(snapshotFile.Labels, ","),
		MergeableState:        normalize(snapshotFile.MergeableState, "unknown"),
		ReviewDecision:        normalize(snapshotFile.ReviewDecision, "unknown"),
		CheckConclusion:       normalize(snapshotFile.CheckConclusion, "unknown"),
		UnresolvedThreadCount: snapshotFile.UnresolvedThreadCount,
		ReviewThreadsComplete: snapshotFile.ReviewThreadsComplete,
		ArtifactRef:           "artifact:" + summaryRec.Path,
		ChecksArtifactRef:     snapshotFile.ChecksArtifactRef,
		ThreadsArtifactRef:    snapshotFile.ThreadsArtifactRef,
		ReviewsArtifactRef:    snapshotFile.ReviewsArtifactRef,
		MergedAt:              snapshotFile.MergedAt,
		ClosedAt:              snapshotFile.ClosedAt,
	}
	if err := db.InsertForgePRSnapshot(ctx, snapshot); err != nil {
		return RefreshResult{}, err
	}
	checks := make([]state.ForgeCheckRun, 0, len(snapshotFile.Checks))
	for i, check := range snapshotFile.Checks {
		checks = append(checks, state.ForgeCheckRun{
			ID:          fmt.Sprintf("%s_check_%d_%s", safeID(link.ID), i+1, shortHash(check.Name+check.URL)),
			LinkID:      link.ID,
			Name:        check.Name,
			Status:      normalize(check.Status, "unknown"),
			Conclusion:  normalize(check.Conclusion, "unknown"),
			URL:         check.URL,
			StartedAt:   check.StartedAt,
			CompletedAt: check.CompletedAt,
		})
	}
	if err := db.ReplaceForgeCheckRuns(ctx, link.ID, checks); err != nil {
		return RefreshResult{}, err
	}
	threadsInput := firstThreads(snapshotFile)
	threads := make([]state.ForgeReviewThread, 0, len(threadsInput))
	for i, thread := range threadsInput {
		threadID := firstNonBlank(thread.ID, fmt.Sprintf("thread-%d", i+1))
		threads = append(threads, state.ForgeReviewThread{
			ID:            fmt.Sprintf("%s_thread_%d_%s", safeID(link.ID), i+1, shortHash(threadID)),
			LinkID:        link.ID,
			ThreadID:      threadID,
			Path:          thread.Path,
			Line:          thread.Line,
			Resolved:      thread.Resolved,
			LastAuthor:    thread.LastAuthor,
			LastUpdatedAt: thread.LastUpdatedAt,
			ArtifactRef:   snapshotFile.ThreadsArtifactRef,
		})
	}
	if err := db.ReplaceForgeReviewThreads(ctx, link.ID, threads); err != nil {
		return RefreshResult{}, err
	}
	payload, _ := json.Marshal(map[string]any{
		"link_id": link.ID, "forge_id": link.ForgeID, "pr_number": link.Number,
		"snapshot_id": snapshotID, "artifact_ref": snapshot.ArtifactRef, "fetched_at": snapshot.FetchedAt,
	})
	if _, err := db.InsertEvent(ctx, state.Event{TaskID: link.TaskID, Type: "forge.pr.refreshed", Payload: string(payload)}); err != nil {
		return RefreshResult{}, err
	}
	return RefreshResult{Link: link, SnapshotID: snapshotID, Artifact: snapshot.ArtifactRef, Checks: len(checks), Threads: len(threads)}, nil
}

func putForgeArtifact(ctx context.Context, db *state.DB, store artifact.Store, taskID, path string, data []byte) (artifact.Record, error) {
	if err := lease.Check(ctx); err != nil {
		return artifact.Record{}, err
	}
	rec, err := store.Put(artifact.Record{Path: path, Type: artifact.TypePayload, State: artifact.StateSealed, PayloadType: "json"}, data)
	if err != nil {
		return artifact.Record{}, err
	}
	err = db.UpdateArtifact(ctx, state.Artifact{
		ID: artifactID(taskID, rec.Path), TaskID: taskID, Type: rec.Type,
		Path: rec.Path, Checksum: rec.Checksum, State: rec.State,
	})
	return rec, err
}

func firstThreads(snapshot SnapshotFile) []SnapshotThread {
	if len(snapshot.Threads) > 0 {
		return snapshot.Threads
	}
	return snapshot.ReviewThreads
}

func matchingTaskPRLinks(ctx context.Context, db *state.DB, taskID, repoID, forgeID string, number int) ([]state.TaskPRLink, error) {
	links, err := db.TaskPRLinks(ctx, taskID)
	if err != nil {
		return nil, err
	}
	matched := make([]state.TaskPRLink, 0, len(links))
	for _, link := range links {
		if repoID != "" && link.RepoID != repoID {
			continue
		}
		if forgeID != "" && link.ForgeID != forgeID {
			continue
		}
		if number > 0 && link.Number != number {
			continue
		}
		matched = append(matched, link)
	}
	return matched, nil
}

func exactlyOneTaskPRLink(links []state.TaskPRLink) (state.TaskPRLink, error) {
	switch len(links) {
	case 0:
		return state.TaskPRLink{}, fmt.Errorf("linked PR was not found")
	case 1:
		return links[0], nil
	default:
		return state.TaskPRLink{}, fmt.Errorf("multiple linked PRs match; specify repo, account, and PR number")
	}
}

func Status(ctx context.Context, root, taskID string) ([]StatusEntry, error) {
	status, err := Inspect(ctx, root, taskID)
	return status.Entries, err
}

func Inspect(ctx context.Context, root, taskID string) (TaskPRStatus, error) {
	wbStatus, err := workbench.Status(root)
	if err != nil {
		return TaskPRStatus{}, err
	}
	db, err := state.Open(ctx, workbench.NewLayout(wbStatus.Root).State)
	if err != nil {
		return TaskPRStatus{}, err
	}
	defer db.Close()
	entries, err := statusFromDB(ctx, db, taskID)
	if err != nil {
		return TaskPRStatus{}, err
	}
	return enrichStatus(ctx, db, taskID, wbStatus.Config.Forge, entries, time.Now().UTC())
}

func Readiness(ctx context.Context, root, taskID string) (ReadinessResult, error) {
	status, err := Inspect(ctx, root, taskID)
	return status.Readiness, err
}

func Digest(ctx context.Context, root string, db *state.DB, taskID string) string {
	entries, err := statusFromDB(ctx, db, taskID)
	if err != nil || len(entries) == 0 {
		return ""
	}
	wbStatus, err := workbench.Status(root)
	if err != nil {
		return ""
	}
	status, err := enrichStatus(ctx, db, taskID, wbStatus.Config.Forge, entries, time.Now().UTC())
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, entry := range status.Entries {
		appendDigestLine(&b, entry)
	}
	appendReadinessLine(&b, status.Readiness)
	return b.String()
}

func enrichStatus(ctx context.Context, db *state.DB, taskID string, config workbench.ForgeConfig, entries []StatusEntry, now time.Time) (TaskPRStatus, error) {
	maxAge := 15 * time.Minute
	if config.MaxSnapshotAge != "" {
		parsed, err := time.ParseDuration(config.MaxSnapshotAge)
		if err != nil || parsed <= 0 {
			return TaskPRStatus{}, fmt.Errorf("invalid forge max_snapshot_age %q", config.MaxSnapshotAge)
		}
		maxAge = parsed
	}
	worktrees, err := db.WorktreesForTask(ctx, taskID)
	if err != nil {
		return TaskPRStatus{}, err
	}
	worktreeHeads := map[string]string{}
	for _, wt := range worktrees {
		head, err := gitrepo.CurrentCommit(ctx, wt.Path)
		if err != nil {
			continue
		}
		worktreeHeads[wt.RepoID] = head
	}
	readiness := ReadinessResult{Enabled: config.ReadinessGates, Ready: true}
	for i := range entries {
		entry := &entries[i]
		add := func(reason string) {
			qualified := fmt.Sprintf("%s#%d:%s", entry.Link.ForgeID, entry.Link.Number, reason)
			if config.ReadinessGates {
				entry.Blockers = append(entry.Blockers, reason)
				readiness.Blockers = append(readiness.Blockers, qualified)
				readiness.Ready = false
			} else {
				entry.Warnings = append(entry.Warnings, reason)
				readiness.Warnings = append(readiness.Warnings, qualified)
			}
		}
		if entry.Snapshot == nil {
			add("snapshot-missing")
			continue
		}
		fetchedAt, err := parseForgeTime(entry.Snapshot.FetchedAt)
		if err != nil {
			add("snapshot-time-invalid")
		} else {
			entry.RefreshAge = now.Sub(fetchedAt)
			if entry.RefreshAge < 0 {
				entry.RefreshAge = 0
			}
			entry.Stale = entry.RefreshAge > maxAge
			if entry.Stale {
				add("snapshot-stale")
			}
		}
		snapshot := entry.Snapshot
		switch {
		case snapshot.Draft, snapshot.State == "open":
			add("pr-open")
		case snapshot.State == "closed":
			add("pr-closed-unmerged")
		case snapshot.State != "merged":
			add("pr-state-unknown")
		}
		switch snapshot.CheckConclusion {
		case "failure":
			add("checks-failure")
		case "pending":
			add("checks-pending")
		case "cancelled":
			add("checks-cancelled")
		case "unknown":
			add("checks-unknown")
		}
		switch snapshot.ReviewDecision {
		case "changes_requested":
			add("changes-requested")
		case "review_required":
			add("review-required")
		case "unknown":
			add("review-unknown")
		}
		if !snapshot.ReviewThreadsComplete {
			add("threads-unknown")
		} else if snapshot.UnresolvedThreadCount > 0 {
			add("unresolved-threads")
		}
		worktreeHead := worktreeHeads[entry.Link.RepoID]
		if worktreeHead == "" || entry.Link.HeadSHA == "" {
			add("head-unknown")
		} else if worktreeHead != entry.Link.HeadSHA {
			entry.HeadMismatch = true
			add("head-mismatch")
		}
		forgeRepo, err := db.ForgeRepo(ctx, entry.Link.RepoID, entry.Link.ForgeID)
		if err == sql.ErrNoRows {
			add("forge-repo-missing")
		} else if err != nil {
			return TaskPRStatus{}, err
		} else if forgeRepo.DefaultBranch != "" && entry.Link.BaseBranch != forgeRepo.DefaultBranch {
			entry.BaseMismatch = true
			add("base-mismatch")
		}
	}
	if !readiness.Enabled {
		readiness.Ready = true
	}
	return TaskPRStatus{Entries: entries, Readiness: readiness}, nil
}

func parseForgeTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid forge timestamp %q", value)
}

func statusFromDB(ctx context.Context, db *state.DB, taskID string) ([]StatusEntry, error) {
	links, err := db.TaskPRLinks(ctx, taskID)
	if err != nil {
		return nil, err
	}
	entries := make([]StatusEntry, 0, len(links))
	for _, link := range links {
		entry := StatusEntry{Link: link}
		if snapshot, err := db.LatestForgePRSnapshot(ctx, link.ID); err == nil {
			entry.Snapshot = &snapshot
		} else if err != sql.ErrNoRows {
			return nil, err
		}
		if checks, err := db.ForgeCheckRuns(ctx, link.ID); err == nil {
			entry.Checks = checks
		} else {
			return nil, err
		}
		if threads, err := db.ForgeReviewThreads(ctx, link.ID); err == nil {
			entry.Threads = threads
		} else {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func FormatTaskPRStatus(status TaskPRStatus, forAgent bool) string {
	var b strings.Builder
	for _, entry := range status.Entries {
		if forAgent {
			appendDigestLine(&b, entry)
			continue
		}
		link := entry.Link
		fmt.Fprintf(&b, "pr: %s#%d repo:%s url:%s\n", link.ForgeID, link.Number, link.RepoID, link.URL)
		if entry.Snapshot != nil {
			snapshot := entry.Snapshot
			threads := strconv.Itoa(snapshot.UnresolvedThreadCount)
			if !snapshot.ReviewThreadsComplete {
				threads = "unknown"
			}
			fmt.Fprintf(
				&b,
				"state: %s draft:%t checks:%s review:%s unresolved_threads:%s artifact:%s\n",
				snapshot.State,
				snapshot.Draft,
				snapshot.CheckConclusion,
				snapshot.ReviewDecision,
				threads,
				snapshot.ArtifactRef,
			)
			fmt.Fprintf(&b, "refresh: fetched:%s age:%s stale:%t head_mismatch:%t base_mismatch:%t\n", snapshot.FetchedAt, formatAge(entry.RefreshAge), entry.Stale, entry.HeadMismatch, entry.BaseMismatch)
			fmt.Fprintf(&b, "refs: checks:%s threads:%s reviews:%s\n", snapshot.ChecksArtifactRef, snapshot.ThreadsArtifactRef, snapshot.ReviewsArtifactRef)
		} else {
			b.WriteString("state: unknown refresh:missing\n")
		}
		for _, check := range entry.Checks {
			fmt.Fprintf(&b, "check: %s status:%s conclusion:%s\n", check.Name, check.Status, check.Conclusion)
		}
		for _, thread := range entry.Threads {
			fmt.Fprintf(&b, "thread: %s path:%s line:%d resolved:%t\n", thread.ThreadID, thread.Path, thread.Line, thread.Resolved)
		}
		if len(entry.Blockers) > 0 {
			fmt.Fprintf(&b, "blockers: %s\n", strings.Join(entry.Blockers, ","))
		}
		if len(entry.Warnings) > 0 {
			fmt.Fprintf(&b, "warnings: %s\n", strings.Join(entry.Warnings, ","))
		}
	}
	appendReadinessLine(&b, status.Readiness)
	return b.String()
}

func appendDigestLine(b *strings.Builder, entry StatusEntry) {
	link := entry.Link
	fmt.Fprintf(b, "repo:%s pr:%s#%d", link.RepoID, link.ForgeID, link.Number)
	if link.HeadSHA != "" {
		fmt.Fprintf(b, " head:%s", shortRef(link.HeadSHA))
	}
	if entry.Snapshot == nil {
		b.WriteString(" state:unknown refresh:missing")
		if len(entry.Blockers) > 0 {
			fmt.Fprintf(b, " blockers:%s", strings.Join(entry.Blockers, ","))
		}
		if len(entry.Warnings) > 0 {
			fmt.Fprintf(b, " warnings:%s", strings.Join(entry.Warnings, ","))
		}
		b.WriteByte('\n')
		return
	}
	snapshot := entry.Snapshot
	stateValue := snapshot.State
	if snapshot.Draft {
		stateValue = "draft"
	}
	threads := strconv.Itoa(snapshot.UnresolvedThreadCount)
	if !snapshot.ReviewThreadsComplete {
		threads = "unknown"
	}
	fmt.Fprintf(b, " state:%s checks:%s review:%s threads:%s refresh_age:%s stale:%t head_mismatch:%t", stateValue, snapshot.CheckConclusion, snapshot.ReviewDecision, threads, formatAge(entry.RefreshAge), entry.Stale, entry.HeadMismatch)
	if len(entry.Blockers) > 0 {
		fmt.Fprintf(b, " blockers:%s", strings.Join(entry.Blockers, ","))
	}
	if len(entry.Warnings) > 0 {
		fmt.Fprintf(b, " warnings:%s", strings.Join(entry.Warnings, ","))
	}
	if snapshot.ArtifactRef != "" {
		fmt.Fprintf(b, " refs:forge:%s", snapshot.ArtifactRef)
	}
	b.WriteByte('\n')
}

func appendReadinessLine(b *strings.Builder, readiness ReadinessResult) {
	if !readiness.Enabled {
		b.WriteString("readiness:disabled")
		if len(readiness.Warnings) > 0 {
			fmt.Fprintf(b, " warnings:%s", strings.Join(readiness.Warnings, ","))
		}
		b.WriteByte('\n')
		return
	}
	if readiness.Ready {
		b.WriteString("readiness:ready\n")
		return
	}
	fmt.Fprintf(b, "readiness:blocked blockers:%s\n", strings.Join(readiness.Blockers, ","))
}

func formatAge(age time.Duration) string {
	if age <= 0 {
		return "0s"
	}
	return age.Truncate(time.Second).String()
}

type remoteParts struct {
	Owner   string
	Name    string
	BaseURL string
}

type prParts struct {
	Number int
	URL    string
}

func parseRemote(raw string) (remoteParts, error) {
	raw = strings.TrimSpace(strings.TrimSuffix(raw, ".git"))
	if raw == "" {
		return remoteParts{}, fmt.Errorf("remote is required")
	}
	if strings.Count(raw, "/") == 1 && !strings.Contains(raw, "://") {
		owner, name, _ := strings.Cut(raw, "/")
		if strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" {
			return remoteParts{}, fmt.Errorf("invalid remote %q", raw)
		}
		return remoteParts{Owner: owner, Name: name, BaseURL: defaultGitHubBaseURL}, nil
	}
	if strings.HasPrefix(raw, "git@") {
		re := regexp.MustCompile(`^git@([^:]+):([^/]+)/(.+)$`)
		match := re.FindStringSubmatch(raw)
		if len(match) == 4 {
			return remoteParts{Owner: match[2], Name: strings.TrimSuffix(match[3], ".git"), BaseURL: "https://" + match[1]}, nil
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return remoteParts{}, fmt.Errorf("invalid remote %q", raw)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 {
		return remoteParts{}, fmt.Errorf("invalid remote %q", raw)
	}
	return remoteParts{Owner: parts[0], Name: strings.TrimSuffix(parts[1], ".git"), BaseURL: parsed.Scheme + "://" + parsed.Host}, nil
}

func parsePR(raw string, repo state.ForgeRepo) (prParts, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return prParts{}, fmt.Errorf("pr is required")
	}
	if number, err := strconv.Atoi(raw); err == nil && number > 0 {
		return prParts{Number: number, URL: repo.URL + "/pull/" + raw}, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return prParts{}, fmt.Errorf("invalid pr %q", raw)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "pull" || parts[i] == "merge_requests" {
			number, err := strconv.Atoi(parts[i+1])
			if err != nil || number <= 0 {
				return prParts{}, fmt.Errorf("invalid pr number in %q", raw)
			}
			repoURL, err := url.Parse(repo.URL)
			if err != nil || repoURL.Host == "" {
				return prParts{}, fmt.Errorf("forge repo %q has an invalid URL", repo.URL)
			}
			urlRepoPath := strings.Join(parts[:i], "/")
			expectedRepoPath := strings.Trim(repoURL.Path, "/")
			if !strings.EqualFold(parsed.Host, repoURL.Host) || !strings.EqualFold(urlRepoPath, expectedRepoPath) {
				return prParts{}, fmt.Errorf("pr %q does not belong to forge repo %s/%s", raw, repo.Owner, repo.Name)
			}
			return prParts{Number: number, URL: raw}, nil
		}
	}
	return prParts{}, fmt.Errorf("invalid pr %q", raw)
}

func ParsePRNumber(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if number, err := strconv.Atoi(raw); err == nil && number > 0 {
		return number, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return 0, fmt.Errorf("invalid pr %q", raw)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != "pull" && parts[i] != "merge_requests" {
			continue
		}
		number, err := strconv.Atoi(parts[i+1])
		if err == nil && number > 0 {
			return number, nil
		}
	}
	return 0, fmt.Errorf("invalid pr %q", raw)
}

func taskPRLinkID(taskID, repoID, forgeID string, number int) string {
	return safeID(taskID + "_" + repoID + "_" + forgeID + "_" + strconv.Itoa(number))
}

func artifactID(taskID, path string) string {
	return safeID(taskID + "_" + path)
}

func safeID(value string) string {
	replacer := strings.NewReplacer("/", "_", ".", "_", ":", "_", "#", "_")
	return replacer.Replace(value)
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

func shortRef(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func normalize(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
