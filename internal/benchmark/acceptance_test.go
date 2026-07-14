package benchmark

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"midgard/internal/gitrepo"
	"midgard/internal/state"
	midgardtask "midgard/internal/task"
	"midgard/internal/workbench"
)

func TestAcceptanceRejectsConcurrentTaskOwner(t *testing.T) {
	ctx := context.Background()
	root, item, _ := prepareAcceptanceFixture(t, ctx, 1)
	item.Checks = []string{"git diff --check"}
	execution, err := midgardtask.AcquireExecution(ctx, root, item.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Close()
	_, err = RunAcceptanceChecks(ctx, root, item, AcceptanceOptions{})
	var heldErr state.ExecutionLeaseHeldError
	if !errors.As(err, &heldErr) || heldErr.Lease.ResourceType != state.LeaseResourceTask {
		t.Fatalf("acceptance contention error = %v", err)
	}
	if acceptanceRunCount(t, root) != 0 {
		t.Fatal("contended acceptance persisted a run")
	}
}

func TestAcceptanceChecksAreAuthoritativeImmutableAndTamperEvident(t *testing.T) {
	ctx := context.Background()
	root, item, worktrees := prepareAcceptanceFixture(t, ctx, 1)
	item.Checks = []string{"git diff --check"}
	wrongReference := filepath.Join(root, "wrong-reference.patch")
	if err := os.WriteFile(wrongReference, []byte("--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-wrong\n+reference\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	item.HiddenReferencePatch = wrongReference

	before, err := gitrepo.WorktreeStatus(ctx, worktrees["repo1"])
	if err != nil {
		t.Fatal(err)
	}
	run, err := RunAcceptanceChecks(ctx, root, item, AcceptanceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "passed" || len(run.Checks) != 1 || run.Checks[0].Status != "passed" || run.ArtifactChecksum == "" {
		t.Fatalf("acceptance run = %#v", run)
	}
	after, err := gitrepo.WorktreeStatus(ctx, worktrees["repo1"])
	if err != nil {
		t.Fatal(err)
	}
	if after.Porcelain != before.Porcelain {
		t.Fatalf("canonical worktree changed by acceptance: before=%q after=%q", before.Porcelain, after.Porcelain)
	}
	scored, err := ScoreItem(ctx, root, item)
	if err != nil {
		t.Fatal(err)
	}
	if scored.Score != ScorePass || !scored.Evidence.AcceptanceValid || !scored.Evidence.AcceptancePassed || scored.Evidence.ReferencePatchMatched {
		t.Fatalf("authoritative score = %#v", scored)
	}
	changedManifest := item
	changedManifest.Checks = []string{"git status --short"}
	manifestMismatch, err := ScoreItem(ctx, root, changedManifest)
	if err != nil {
		t.Fatal(err)
	}
	if manifestMismatch.Score != ScoreInvalid || !strings.Contains(manifestMismatch.Evidence.AcceptanceReason, "manifest") {
		t.Fatalf("changed manifest score = %#v", manifestMismatch)
	}
	report, err := WriteReport(root, Manifest{ID: "acceptance-report", Items: []Item{item}}, []ItemResult{scored})
	if err != nil {
		t.Fatal(err)
	}
	reportData, err := os.ReadFile(report.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reportData), "acceptance: status:passed valid:true passed:true") ||
		!strings.Contains(string(reportData), "acceptance_checksum: sha256:") {
		t.Fatalf("acceptance report:\n%s", reportData)
	}

	stdoutPath := filepath.Join(root, ".midgard", "artifacts", item.TaskID, filepath.FromSlash(strings.TrimPrefix(run.Checks[0].StdoutArtifactRef, "artifact:")))
	if err := os.WriteFile(stdoutPath, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tampered, err := ScoreItem(ctx, root, item)
	if err != nil {
		t.Fatal(err)
	}
	if tampered.Score != ScoreInvalid || !strings.Contains(tampered.Evidence.AcceptanceReason, "checksum") {
		t.Fatalf("tampered score = %#v", tampered)
	}

	second, err := RunAcceptanceChecks(ctx, root, item, AcceptanceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second.RunID == run.RunID {
		t.Fatal("acceptance rerun reused an immutable run id")
	}
	if err := os.WriteFile(filepath.Join(worktrees["repo1"], "README.md"), []byte("# changed after acceptance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale, err := ScoreItem(ctx, root, item)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Score != ScoreInvalid || !strings.Contains(stale.Evidence.AcceptanceReason, "changed after acceptance") {
		t.Fatalf("stale score = %#v", stale)
	}

	db, err := state.Open(ctx, workbench.NewLayout(root).State)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var runs, artifacts int
	if err := db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM benchmark_acceptance_runs WHERE task_id = ?`, item.TaskID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE task_id = ? AND path LIKE 'benchmark/acceptance/%'`, item.TaskID).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if runs != 2 || artifacts != 8 {
		t.Fatalf("immutable acceptance rows/artifacts = %d/%d, want 2/8", runs, artifacts)
	}
}

func TestAcceptanceOutputCapsAreRecordedWithoutChangingExitAuthority(t *testing.T) {
	ctx := context.Background()
	root, item, _ := prepareAcceptanceFixture(t, ctx, 1)
	item.AcceptanceChecks = []AcceptanceCheck{{ID: "bounded-diff", RepoID: "repo1", Command: "git diff"}}
	run, err := RunAcceptanceChecks(ctx, root, item, AcceptanceOptions{MaxStdoutBytes: 8, MaxStderrBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	check := run.Checks[0]
	if run.Status != "passed" || check.Status != "passed" || !check.StdoutTruncated || check.StderrTruncated {
		t.Fatalf("bounded run = %#v", run)
	}
	stdoutPath := filepath.Join(root, ".midgard", "artifacts", item.TaskID, filepath.FromSlash(strings.TrimPrefix(check.StdoutArtifactRef, "artifact:")))
	stdout, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(stdout) != 8 {
		t.Fatalf("bounded stdout bytes = %d, want 8", len(stdout))
	}
	scored, err := ScoreItem(ctx, root, item)
	if err != nil {
		t.Fatal(err)
	}
	if scored.Score != ScorePass || !scored.Evidence.AcceptanceChecks[0].StdoutTruncated {
		t.Fatalf("bounded score = %#v", scored)
	}
}

func TestAcceptanceCanRequireAControlledNonzeroExit(t *testing.T) {
	ctx := context.Background()
	root, item, _ := prepareAcceptanceFixture(t, ctx, 1)
	item.AcceptanceChecks = []AcceptanceCheck{{
		ID: "old-text-absent", RepoID: "repo1", Command: "grep -F 'text that is not present' README.md", ExpectedExitCode: 1,
	}}
	run, err := RunAcceptanceChecks(ctx, root, item, AcceptanceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "passed" || run.Checks[0].ExitCode != 1 || run.Checks[0].ExpectedExitCode != 1 {
		t.Fatalf("nonzero acceptance run = %#v", run)
	}
	scored, err := ScoreItem(ctx, root, item)
	if err != nil {
		t.Fatal(err)
	}
	if scored.Score != ScorePass || scored.Evidence.AcceptanceChecks[0].ExpectedExitCode != 1 {
		t.Fatalf("nonzero acceptance score = %#v", scored)
	}
}

func TestAcceptanceFailureAndPolicyErrorCannotPass(t *testing.T) {
	ctx := context.Background()
	t.Run("command failure", func(t *testing.T) {
		root, item, _ := prepareAcceptanceFixture(t, ctx, 1)
		item.Checks = []string{"git diff --exit-code"}
		run, err := RunAcceptanceChecks(ctx, root, item, AcceptanceOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "failed" || run.Checks[0].ExitCode == 0 || run.Checks[0].TimedOut {
			t.Fatalf("failed run = %#v", run)
		}
		scored, err := ScoreItem(ctx, root, item)
		if err != nil {
			t.Fatal(err)
		}
		if scored.Score != ScoreFail || !scored.Evidence.AcceptanceValid || scored.Evidence.AcceptancePassed {
			t.Fatalf("failed score = %#v", scored)
		}
	})

	t.Run("policy error", func(t *testing.T) {
		root, item, worktrees := prepareAcceptanceFixture(t, ctx, 1)
		item.AcceptanceChecks = []AcceptanceCheck{{ID: "denied", RepoID: "repo1", Command: "rm README.md"}}
		before, err := os.ReadFile(filepath.Join(worktrees["repo1"], "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		run, err := RunAcceptanceChecks(ctx, root, item, AcceptanceOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "error" || !strings.Contains(run.Checks[0].Error, "denied by policy") {
			t.Fatalf("policy run = %#v", run)
		}
		after, err := os.ReadFile(filepath.Join(worktrees["repo1"], "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatal("denied acceptance command changed canonical source")
		}
		scored, err := ScoreItem(ctx, root, item)
		if err != nil {
			t.Fatal(err)
		}
		if scored.Score != ScoreInvalid || scored.Evidence.AcceptanceValid {
			t.Fatalf("policy score = %#v", scored)
		}
	})
}

func TestAcceptanceTimeoutIsRecordedAsFailure(t *testing.T) {
	ctx := context.Background()
	root, item, worktrees := prepareAcceptanceFixture(t, ctx, 1)
	worktree := worktrees["repo1"]
	if err := os.WriteFile(filepath.Join(worktree, "go.mod"), []byte("module acceptancefixture\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "slow_test.go"), []byte("package acceptancefixture\n\nimport (\"testing\"; \"time\")\n\nfunc TestSlow(t *testing.T) { time.Sleep(3 * time.Second) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	item.AcceptanceChecks = []AcceptanceCheck{{ID: "slow", RepoID: "repo1", Command: "go test ./...", TimeoutSeconds: 1}}
	run, err := RunAcceptanceChecks(ctx, root, item, AcceptanceOptions{DefaultTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || !run.Checks[0].TimedOut || run.Checks[0].ExitCode != -1 {
		t.Fatalf("timeout run = %#v", run)
	}
	scored, err := ScoreItem(ctx, root, item)
	if err != nil {
		t.Fatal(err)
	}
	if scored.Score != ScoreFail || !scored.Evidence.AcceptanceChecks[0].TimedOut {
		t.Fatalf("timeout score = %#v", scored)
	}
}

func TestAcceptanceChecksRequireExplicitReposForMultiRepoTasks(t *testing.T) {
	ctx := context.Background()
	root, item, _ := prepareAcceptanceFixture(t, ctx, 2)
	item.Checks = []string{"git diff --check"}
	if _, err := RunAcceptanceChecks(ctx, root, item, AcceptanceOptions{}); err == nil || !strings.Contains(err.Error(), "requires repo_id") {
		t.Fatalf("ambiguous multi-repo error = %v", err)
	}
	item.Checks = nil
	item.AcceptanceChecks = []AcceptanceCheck{
		{ID: "repo1-diff", RepoID: "repo1", Command: "git diff --check"},
		{ID: "repo2-diff", RepoID: "repo2", Command: "git diff --check"},
	}
	run, err := RunAcceptanceChecks(ctx, root, item, AcceptanceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "passed" || len(run.Checks) != 2 {
		t.Fatalf("multi-repo run = %#v", run)
	}
	scored, err := ScoreItem(ctx, root, item)
	if err != nil {
		t.Fatal(err)
	}
	if scored.Score != ScorePass || len(scored.Evidence.AcceptanceChecks) != 2 {
		t.Fatalf("multi-repo score = %#v", scored)
	}
}

func prepareAcceptanceFixture(t *testing.T, ctx context.Context, repoCount int) (string, Item, map[string]string) {
	t.Helper()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "acceptance-test"}); err != nil {
		t.Fatal(err)
	}
	repoIDs := make([]string, 0, repoCount)
	for i := 1; i <= repoCount; i++ {
		repoID := "repo" + string(rune('0'+i))
		repoIDs = append(repoIDs, repoID)
		repo := initBenchmarkRepo(t)
		if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: repoID, Path: repo, MainRef: "main"}); err != nil {
			t.Fatal(err)
		}
	}
	created, err := midgardtask.Create(ctx, root, midgardtask.CreateOptions{ID: "task_acceptance", Objective: "change benchmark fixture", RepoIDs: repoIDs})
	if err != nil {
		t.Fatal(err)
	}
	worktrees := make(map[string]string, len(created.Worktrees))
	var patch strings.Builder
	var expectedFiles []string
	for _, wt := range created.Worktrees {
		worktrees[wt.RepoID] = wt.Path
		if err := os.WriteFile(filepath.Join(wt.Path, "README.md"), []byte("# authoritative acceptance\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		diff, err := gitrepo.Diff(ctx, wt.Path)
		if err != nil {
			t.Fatal(err)
		}
		patch.WriteString("# repo:" + wt.RepoID + "\n" + diff + "\n")
		expectedFiles = append(expectedFiles, "README.md")
	}
	artifactRoot := filepath.Join(root, ".midgard", "artifacts", "task_acceptance")
	for path, data := range map[string]string{
		"plan.mdx": "# Plan\n", "implementation.mdx": "# Implementation\n",
		"review.mdx": "# Review\n\nApproved.\n", "patch.diff": patch.String(),
	} {
		if err := os.WriteFile(filepath.Join(artifactRoot, path), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db, err := state.Open(ctx, workbench.NewLayout(root).State)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateTaskState(ctx, "task_acceptance", "completed"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	item := Item{
		ID: "acceptance-item", TaskID: "task_acceptance", RepoIDs: repoIDs,
		ExpectedTouchedFiles: normalizedFiles(expectedFiles),
	}
	return root, item, worktrees
}
