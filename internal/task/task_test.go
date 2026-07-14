package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"midgard/internal/gitrepo"
	"midgard/internal/lease"
	"midgard/internal/model"
	"midgard/internal/model/providers/fake"
	"midgard/internal/state"
	"midgard/internal/workbench"
)

func TestExpiredTaskOwnerIsFencedAfterReclaim(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "lease-fence-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, root, CreateOptions{ID: "task_fenced_owner", Objective: "test fenced reclaim"}); err != nil {
		t.Fatal(err)
	}
	stale, err := AcquireExecutionWithOptions(ctx, root, "task_fenced_owner", lease.Options{
		OwnerID: "stale-owner", TTL: 40 * time.Millisecond, RenewInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	current, err := AcquireExecution(ctx, root, "task_fenced_owner")
	if err != nil {
		t.Fatal(err)
	}
	provider := fake.New(fake.Response{Text: "@report plan.mdx\n# Plan\n\nReady.\n@result status:ready artifact:plan.mdx checks:none\n"})
	if _, err := RunRole(stale.Context, root, "task_fenced_owner", model.RolePlanner, RunnerOptions{
		ModelID: "fake-model", Providers: RoleProviders{model.RolePlanner: provider},
	}); !errors.Is(err, state.ErrExecutionLeaseLost) {
		t.Fatalf("stale owner role error = %v", err)
	}
	if provider.Calls() != 0 {
		t.Fatalf("stale owner provider calls = %d, want 0", provider.Calls())
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stale.Close(); !errors.Is(err, state.ErrExecutionLeaseLost) {
		t.Fatalf("stale close error = %v", err)
	}
}

func TestCreateTaskOwnsWorktree(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "task-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}

	created, err := Create(ctx, root, CreateOptions{ID: "task_test", Objective: "verify worktree lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Task.ID != "task_test" {
		t.Fatalf("task id = %s", created.Task.ID)
	}
	if len(created.Worktrees) != 1 {
		t.Fatalf("worktrees = %#v, want 1", created.Worktrees)
	}
	wt := created.Worktrees[0]
	if wt.Dirty {
		t.Fatalf("new worktree is dirty: %s", wt.Porcelain)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "README.md")); err != nil {
		t.Fatal(err)
	}
	sourceStatus, err := gitrepo.WorktreeStatus(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if sourceStatus.Dirty {
		t.Fatalf("source checkout dirty:\n%s", sourceStatus.Porcelain)
	}
	if _, err := os.Stat(created.ReportPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".midgard", "artifacts", "task_test")); err != nil {
		t.Fatal(err)
	}

	status, err := Status(ctx, root, "task_test")
	if err != nil {
		t.Fatal(err)
	}
	if status.NextAction != "implement" {
		t.Fatalf("next action = %s", status.NextAction)
	}
	diff, err := Diff(ctx, root, "task_test", "repo1")
	if err != nil {
		t.Fatal(err)
	}
	if diff != "" {
		t.Fatalf("new task diff = %q, want empty", diff)
	}
}

func TestContextPacketIncludesForgeDigest(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "task-forge-context-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	created, err := Create(ctx, root, CreateOptions{ID: "task_forge_context", Objective: "respect linked PR state"})
	if err != nil {
		t.Fatal(err)
	}
	layout := workbench.NewLayout(root)
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.UpsertForgeAccount(ctx, state.ForgeAccount{ID: "github-main", Kind: "github", BaseURL: "https://github.com"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertForgeRepo(ctx, state.ForgeRepo{RepoID: "repo1", ForgeID: "github-main", Owner: "example", Name: "project", URL: "https://github.com/example/project"}); err != nil {
		t.Fatal(err)
	}
	link := state.TaskPRLink{
		ID:      "task_forge_context_repo1_github-main_9",
		TaskID:  "task_forge_context",
		RepoID:  "repo1",
		ForgeID: "github-main",
		Number:  9,
		URL:     "https://github.com/example/project/pull/9",
		HeadSHA: created.Worktrees[0].StartCommit,
		Source:  "test",
	}
	if err := db.UpsertTaskPRLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertForgePRSnapshot(ctx, state.ForgePRSnapshot{
		ID:                    "snap_1",
		LinkID:                link.ID,
		FetchedAt:             time.Now().UTC().Format(time.RFC3339Nano),
		State:                 "open",
		CheckConclusion:       "success",
		ReviewDecision:        "approved",
		UnresolvedThreadCount: 0,
		ReviewThreadsComplete: true,
		ArtifactRef:           "artifact:forge/snap.json",
	}); err != nil {
		t.Fatal(err)
	}
	status, err := Status(ctx, root, "task_forge_context")
	if err != nil {
		t.Fatal(err)
	}
	packet := contextPacket(ctx, status, layout)
	if !strings.Contains(packet, "forge_prs:\nrepo:repo1 pr:github-main#9") ||
		!strings.Contains(packet, "state:open checks:success review:approved threads:0") ||
		!strings.Contains(packet, "warnings:pr-open") ||
		!strings.Contains(packet, "refs:forge:artifact:forge/snap.json") {
		t.Fatalf("context packet missing forge digest:\n%s", packet)
	}
	config, err := workbench.ReadConfig(layout.Config)
	if err != nil {
		t.Fatal(err)
	}
	config.Forge = workbench.ForgeConfig{ReadinessGates: true, MaxSnapshotAge: "1h"}
	if err := workbench.WriteConfig(layout.Config, config); err != nil {
		t.Fatal(err)
	}
	gated, err := Status(ctx, root, "task_forge_context")
	if err != nil {
		t.Fatal(err)
	}
	if !gated.ForgeGates || gated.ForgeReady || gated.NextAction != "resolve-forge-blockers" ||
		!slices.Contains(gated.ForgeBlockers, "github-main#9:pr-open") {
		t.Fatalf("gated task status = %#v", gated)
	}
}

func TestAcquireExecutionRejectsConcurrentTaskOwner(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "lease-resume-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, root, CreateOptions{ID: "task_lease_resume", Objective: "test lease recovery"}); err != nil {
		t.Fatal(err)
	}
	first, err := AcquireExecution(ctx, root, "task_lease_resume")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := AcquireExecution(ctx, root, "task_lease_resume"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("concurrent task execution error = %v", err)
	}
}

func TestCleanupRemovesTaskRuntimeDirs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "cleanup-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	created, err := Create(ctx, root, CreateOptions{ID: "task_cleanup", Objective: "cleanup runtime"})
	if err != nil {
		t.Fatal(err)
	}
	artifactDir := filepath.Join(root, ".midgard", "artifacts", "task_cleanup")
	if err := os.WriteFile(filepath.Join(artifactDir, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Cleanup(ctx, root, "task_cleanup", CleanupOptions{Worktrees: true, Artifacts: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RemovedWorktrees) != 1 || result.RemovedArtifacts == "" {
		t.Fatalf("cleanup result = %#v", result)
	}
	if _, err := os.Stat(created.Worktrees[0].Path); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	if _, err := os.Stat(artifactDir); !os.IsNotExist(err) {
		t.Fatalf("artifact dir still exists: %v", err)
	}
}

func TestObjectiveSnippetsIncludeMatchingSourceLines(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(strings.Join([]string{
		"# Fixture",
		"",
		"To target TOML specifically you can implement `UnmarshalTOML` TOML interface in",
		"a similar way.",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	got := objectiveSnippets("Fix the README sentence describing UnmarshalTOML.", []WorktreeStatus{{RepoID: "repo1", Path: root}})
	if !strings.Contains(got, "file:README.md") || !strings.Contains(got, "UnmarshalTOML") {
		t.Fatalf("snippets = %q", got)
	}
}

func TestObjectiveSnippetsIncludeClojureSourceLines(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bases", "flows-api", "src", "breyta", "flows_api", "search")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path, "flows.clj")
	if err := os.WriteFile(file, []byte(strings.Join([]string{
		"(ns breyta.flows-api.search.flows)",
		"",
		"(defn build-flow-doc [flow]",
		"  {:display-connections (:display-connections flow)",
		"   :publish-description (:publish-description flow)})",
	}, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	got := objectiveSnippets("Public app search shows conflicting display_connections and publish_description metadata.", []WorktreeStatus{{RepoID: "breyta", Path: root}})
	if !strings.Contains(got, "file:bases/flows-api/src/breyta/flows_api/search/flows.clj") ||
		!strings.Contains(got, ":display-connections") {
		t.Fatalf("snippets = %q", got)
	}
}

func TestObjectiveFileIndexIncludesLikelySourcePaths(t *testing.T) {
	root := t.TempDir()
	for _, file := range []string{
		"bases/flows-api/src/breyta/flows_api/search/flows.clj",
		"bases/flows-api/test/breyta/flows_api/search/flows_elastic_query_test.clj",
		"docs/public/search.md",
	} {
		path := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := objectiveFileIndex("Fix flows search display_connections metadata.", []WorktreeStatus{{RepoID: "breyta", Path: root}})
	if !strings.Contains(got, "repo:breyta file:bases/flows-api/src/breyta/flows_api/search/flows.clj") ||
		!strings.Contains(got, "repo:breyta file:bases/flows-api/test/breyta/flows_api/search/flows_elastic_query_test.clj") {
		t.Fatalf("file index = %q", got)
	}
}

func TestObjectiveSnippetsDoNotInventTrailingBlankLine(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Fixture App\n\nThis README is used for a Midgard Codex e2e benchmark.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := objectiveSnippets("Update Fixture App heading.", []WorktreeStatus{{RepoID: "repo1", Path: root}})
	if strings.Contains(got, "4:") {
		t.Fatalf("snippet included phantom fourth line:\n%s", got)
	}
	if !strings.Contains(got, "3:This README is used") {
		t.Fatalf("snippet missing real context:\n%s", got)
	}
}

func TestWorktreeDiffContextIncludesDirtyDiff(t *testing.T) {
	ctx := context.Background()
	repo := initLifecycleRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := gitrepo.WorktreeStatus(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	got := worktreeDiffContext(ctx, []WorktreeStatus{{RepoID: "repo1", Path: repo, Dirty: status.Dirty}})
	if !strings.Contains(got, "repo:repo1") || !strings.Contains(got, "# changed") {
		t.Fatalf("diff context = %q", got)
	}
}

func TestLatestReportContextIncludesReviewFeedback(t *testing.T) {
	root := t.TempDir()
	layout := workbench.NewLayout(root)
	taskID := "task_review_context"
	artifactDir := filepath.Join(layout.Artifacts, taskID)
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "review.mdx"), []byte("# Review\n\nchanges requested: remove duplicate line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := latestReportContext(layout, taskID)
	if !strings.Contains(got, "artifact:review.mdx") || !strings.Contains(got, "remove duplicate line") {
		t.Fatalf("report context = %q", got)
	}
}

func initLifecycleRepo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	if _, err := gitrepo.Run(ctx, repo, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "config", "user.email", "midgard@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "config", "user.name", "Midgard Test"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "commit", "-m", "initial"); err != nil {
		t.Fatal(err)
	}
	return repo
}
