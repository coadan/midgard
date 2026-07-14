package forge

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"midgard/internal/gitrepo"
	"midgard/internal/state"
	"midgard/internal/workbench"
)

func TestRefreshFromGitHubPersistsImmutableEvidenceAndUnlinksExactly(t *testing.T) {
	ctx := context.Background()
	root, _, head := setupForgeTask(t, ctx)
	db := openForgeTestDB(t, ctx, root)
	link := addForgeTestLink(t, ctx, db, head, 42)
	other := addForgeTestLink(t, ctx, db, head, 43)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	server := newForgeTestGitHubServer(t)
	defer server.Close()
	fetchedAt := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	client := &GitHubClient{
		HTTPClient:  server.Client(),
		APIBaseURL:  server.URL,
		GraphQLURL:  server.URL + "/graphql",
		Token:       "test-token",
		TokenSource: "test",
		Now:         func() time.Time { return fetchedAt },
	}

	results, err := RefreshFromGitHub(ctx, LiveRefreshOptions{
		Root: root, TaskID: "task_forge", RepoID: "repo1", ForgeID: "github-main", Number: 42, Client: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Link.ID != link.ID || results[0].Checks != 2 || results[0].Threads != 1 {
		t.Fatalf("refresh results = %#v", results)
	}

	db = openForgeTestDB(t, ctx, root)
	assertForgeRefreshState(t, ctx, db, root, link, other, 1, 5)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	fetchedAt = fetchedAt.Add(time.Minute)
	results, err = RefreshFromGitHub(ctx, LiveRefreshOptions{
		Root: root, TaskID: "task_forge", RepoID: "repo1", ForgeID: "github-main", Number: 42, Client: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].SnapshotID == "" {
		t.Fatal("second refresh has no snapshot id")
	}
	db = openForgeTestDB(t, ctx, root)
	assertForgeRefreshState(t, ctx, db, root, link, other, 2, 10)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	unlinked, err := UnlinkTaskPR(ctx, UnlinkOptions{Root: root, TaskID: "task_forge", RepoID: "repo1", ForgeID: "github-main", Number: 42})
	if err != nil {
		t.Fatal(err)
	}
	if unlinked.ID != link.ID {
		t.Fatalf("unlinked = %#v", unlinked)
	}
	db = openForgeTestDB(t, ctx, root)
	defer db.Close()
	links, err := db.TaskPRLinks(ctx, "task_forge")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ID != other.ID {
		t.Fatalf("links after unlink = %#v", links)
	}
	for _, table := range []string{"forge_pr_snapshots", "forge_check_runs", "forge_review_threads"} {
		var count int
		if err := db.Conn().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE link_id = ?", link.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows after unlink = %d", table, count)
		}
	}
	var artifactCount int
	if err := db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE task_id = ?`, "task_forge").Scan(&artifactCount); err != nil {
		t.Fatal(err)
	}
	if artifactCount != 10 {
		t.Fatalf("audit artifacts after unlink = %d, want 10", artifactCount)
	}
}

func TestReadinessGatesAreOptInAndUseFreshForgeAndWorktreeState(t *testing.T) {
	ctx := context.Background()
	root, _, head := setupForgeTask(t, ctx)
	db := openForgeTestDB(t, ctx, root)
	link := addForgeTestLink(t, ctx, db, head, 42)
	now := time.Now().UTC().Add(-5 * time.Second)
	if err := db.InsertForgePRSnapshot(ctx, state.ForgePRSnapshot{
		ID: "ready", LinkID: link.ID, FetchedAt: now.Format(time.RFC3339Nano), State: "merged",
		CheckConclusion: "success", ReviewDecision: "approved", ReviewThreadsComplete: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	readiness, err := Readiness(ctx, root, "task_forge")
	if err != nil {
		t.Fatal(err)
	}
	if readiness.Enabled || !readiness.Ready || len(readiness.Blockers) != 0 || len(readiness.Warnings) != 0 {
		t.Fatalf("default readiness = %#v", readiness)
	}
	setForgeTestConfig(t, root, workbench.ForgeConfig{ReadinessGates: true, MaxSnapshotAge: "1h"})
	readiness, err = Readiness(ctx, root, "task_forge")
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Enabled || !readiness.Ready || len(readiness.Blockers) != 0 {
		t.Fatalf("ready gated state = %#v", readiness)
	}

	db = openForgeTestDB(t, ctx, root)
	link.HeadSHA = "different-head"
	link.BaseBranch = "release"
	if err := db.UpsertTaskPRLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertForgePRSnapshot(ctx, state.ForgePRSnapshot{
		ID: "blocked", LinkID: link.ID, FetchedAt: now.Add(time.Second).Format(time.RFC3339Nano), State: "open",
		CheckConclusion: "failure", ReviewDecision: "changes_requested", UnresolvedThreadCount: 2, ReviewThreadsComplete: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	readiness, err = Readiness(ctx, root, "task_forge")
	if err != nil {
		t.Fatal(err)
	}
	wantReasons := []string{"pr-open", "checks-failure", "changes-requested", "unresolved-threads", "head-mismatch", "base-mismatch"}
	if !readiness.Enabled || readiness.Ready {
		t.Fatalf("blocked readiness = %#v", readiness)
	}
	for _, reason := range wantReasons {
		if !slices.Contains(readiness.Blockers, "github-main#42:"+reason) {
			t.Errorf("blockers %v missing %q", readiness.Blockers, reason)
		}
	}

	setForgeTestConfig(t, root, workbench.ForgeConfig{MaxSnapshotAge: "1ns"})
	readiness, err = Readiness(ctx, root, "task_forge")
	if err != nil {
		t.Fatal(err)
	}
	if readiness.Enabled || !readiness.Ready || !slices.Contains(readiness.Warnings, "github-main#42:snapshot-stale") {
		t.Fatalf("warning-only readiness = %#v", readiness)
	}
}

func TestLinkTaskPRRequiresExactAccountAndMatchingRepoURL(t *testing.T) {
	ctx := context.Background()
	root, _, _ := setupForgeTask(t, ctx)
	db := openForgeTestDB(t, ctx, root)
	if err := db.UpsertForgeAccount(ctx, state.ForgeAccount{ID: "github-alt", Kind: "github", BaseURL: "https://github.com"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertForgeRepo(ctx, state.ForgeRepo{
		RepoID: "repo1", ForgeID: "github-alt", Owner: "acme", Name: "widgets", DefaultBranch: "main", URL: "https://github.com/acme/widgets",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := LinkTaskPR(ctx, TaskPRLinkOptions{Root: root, TaskID: "task_forge", RepoID: "repo1", PR: "42"})
	if err == nil || !strings.Contains(err.Error(), "multiple forge accounts") {
		t.Fatalf("ambiguous account error = %v", err)
	}
	_, err = LinkTaskPR(ctx, TaskPRLinkOptions{
		Root: root, TaskID: "task_forge", RepoID: "repo1", ForgeID: "github-main", PR: "https://github.com/other/project/pull/42",
	})
	if err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("foreign PR URL error = %v", err)
	}
	link, err := LinkTaskPR(ctx, TaskPRLinkOptions{
		Root: root, TaskID: "task_forge", RepoID: "repo1", ForgeID: "github-main", PR: "https://github.com/acme/widgets/pull/42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if link.ForgeID != "github-main" || link.Number != 42 {
		t.Fatalf("link = %#v", link)
	}
}

func setupForgeTask(t *testing.T, ctx context.Context) (root, repo, head string) {
	t.Helper()
	root = t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "forge-test"}); err != nil {
		t.Fatal(err)
	}
	repo = t.TempDir()
	if _, err := gitrepo.Run(ctx, repo, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "config", "user.email", "midgard@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "config", "user.name", "Midgard Test"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("forge test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "commit", "-m", "initial"); err != nil {
		t.Fatal(err)
	}
	var err error
	head, err = gitrepo.CurrentCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	layout := workbench.NewLayout(root)
	db := openForgeTestDB(t, ctx, root)
	if err := db.UpsertWorkbench(ctx, state.Workbench{ID: "forge-test", Root: root, ConfigPath: layout.Config}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRepo(ctx, state.Repo{ID: "repo1", WorkbenchID: "forge-test", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertTask(ctx, state.Task{ID: "task_forge", WorkbenchID: "forge-test", State: "open", Objective: "test forge"}); err != nil {
		t.Fatal(err)
	}
	if err := db.LinkTaskRepo(ctx, "task_forge", "repo1"); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(layout.Worktrees, "task_forge", "repo1")
	if err := gitrepo.AddWorktree(ctx, repo, worktree, "midgard/task_forge/repo1", head); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertWorktree(ctx, state.Worktree{
		ID: "task_forge:repo1", TaskID: "task_forge", RepoID: "repo1", Path: worktree, StartRef: "main", StartCommit: head,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertForgeAccount(ctx, state.ForgeAccount{ID: "github-main", Kind: "github", BaseURL: "https://github.com"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertForgeRepo(ctx, state.ForgeRepo{
		RepoID: "repo1", ForgeID: "github-main", Owner: "acme", Name: "widgets", DefaultBranch: "main", URL: "https://github.com/acme/widgets",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return root, repo, head
}

func addForgeTestLink(t *testing.T, ctx context.Context, db *state.DB, head string, number int) state.TaskPRLink {
	t.Helper()
	link := state.TaskPRLink{
		ID: taskPRLinkID("task_forge", "repo1", "github-main", number), TaskID: "task_forge", RepoID: "repo1", ForgeID: "github-main",
		Number: number, URL: "https://github.com/acme/widgets/pull/" + strconv.Itoa(number), BaseBranch: "main", HeadBranch: "feature", HeadSHA: head, Source: "test",
	}
	if err := db.UpsertTaskPRLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	return link
}

func openForgeTestDB(t *testing.T, ctx context.Context, root string) *state.DB {
	t.Helper()
	db, err := state.Open(ctx, workbench.NewLayout(root).State)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func setForgeTestConfig(t *testing.T, root string, forgeConfig workbench.ForgeConfig) {
	t.Helper()
	layout := workbench.NewLayout(root)
	config, err := workbench.ReadConfig(layout.Config)
	if err != nil {
		t.Fatal(err)
	}
	config.Forge = forgeConfig
	if err := workbench.WriteConfig(layout.Config, config); err != nil {
		t.Fatal(err)
	}
}

func newForgeTestGitHubServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/pulls/42":
			_, _ = w.Write([]byte(`{"state":"closed","draft":false,"title":"Merged change","html_url":"https://github.com/acme/widgets/pull/42","mergeable_state":"unknown","merged_at":"2026-07-10T09:00:00Z","closed_at":"2026-07-10T09:00:00Z","user":{"login":"alice"},"labels":[{"name":"ready"}],"base":{"ref":"main"},"head":{"ref":"feature","sha":"placeholder"}}`))
		case "/repos/acme/widgets/commits/placeholder/check-runs":
			_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"id":1,"name":"unit","status":"completed","conclusion":"success","details_url":"https://ci.test/unit"}]}`))
		case "/repos/acme/widgets/commits/placeholder/status":
			_, _ = w.Write([]byte(`{"state":"success","statuses":[{"id":2,"state":"success","context":"lint","target_url":"https://ci.test/lint"}]}`))
		case "/repos/acme/widgets/pulls/42/reviews":
			_, _ = w.Write([]byte(`[{"id":7,"state":"APPROVED","user":{"login":"bob"}}]`))
		case "/graphql":
			var request struct {
				Variables map[string]any `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Variables["number"] != float64(42) {
				t.Errorf("graphql PR number = %#v", request.Variables["number"])
			}
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewDecision":"APPROVED","reviewThreads":{"nodes":[{"id":"thread-1","isResolved":true,"path":"README.md","line":1,"originalLine":1,"comments":{"nodes":[{"author":{"login":"bob"},"updatedAt":"2026-07-10T09:30:00Z"}]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func assertForgeRefreshState(t *testing.T, ctx context.Context, db *state.DB, root string, link, other state.TaskPRLink, snapshots, artifacts int) {
	t.Helper()
	latest, err := db.LatestForgePRSnapshot(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.State != "merged" || latest.CheckConclusion != "success" || latest.ReviewDecision != "approved" || !latest.ReviewThreadsComplete {
		t.Fatalf("latest snapshot = %#v", latest)
	}
	if latest.ArtifactRef == "" || latest.ChecksArtifactRef == "" || latest.ThreadsArtifactRef == "" || latest.ReviewsArtifactRef == "" {
		t.Fatalf("artifact refs = %#v", latest)
	}
	checks, err := db.ForgeCheckRuns(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	threads, err := db.ForgeReviewThreads(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 || len(threads) != 1 || !threads[0].Resolved {
		t.Fatalf("checks=%#v threads=%#v", checks, threads)
	}
	if _, err := db.LatestForgePRSnapshot(ctx, other.ID); err != sql.ErrNoRows {
		t.Fatalf("unselected link snapshot error = %v", err)
	}
	var snapshotCount, artifactCount int
	if err := db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM forge_pr_snapshots WHERE link_id = ?`, link.ID).Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE task_id = ?`, link.TaskID).Scan(&artifactCount); err != nil {
		t.Fatal(err)
	}
	if snapshotCount != snapshots || artifactCount != artifacts {
		t.Fatalf("snapshot/artifact counts = %d/%d, want %d/%d", snapshotCount, artifactCount, snapshots, artifacts)
	}
	rel := strings.TrimPrefix(latest.ArtifactRef, "artifact:")
	summaryPath := filepath.Join(workbench.NewLayout(root).Artifacts, link.TaskID, filepath.FromSlash(rel))
	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(summary) || !strings.Contains(string(summary), `"reviews_artifact_ref"`) {
		t.Fatalf("summary artifact = %s", summary)
	}
}
