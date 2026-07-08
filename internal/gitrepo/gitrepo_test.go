package gitrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddWorktreeDoesNotEditSourceCheckout(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	worktree := filepath.Join(t.TempDir(), "task-wt")

	startCommit, err := CurrentCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := AddWorktree(ctx, repo, worktree, "midgard/test-task/main", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "README.md")); err != nil {
		t.Fatal(err)
	}
	sourceStatus, err := WorktreeStatus(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if sourceStatus.Dirty {
		t.Fatalf("source checkout is dirty:\n%s", sourceStatus.Porcelain)
	}
	worktreeCommit, err := CurrentCommit(ctx, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if worktreeCommit != startCommit {
		t.Fatalf("worktree commit = %s, want %s", worktreeCommit, startCommit)
	}
}

func TestApplyPatchRecountsModelGeneratedHunks(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	patch := []byte(`--- a/README.md
+++ b/README.md
@@ -1,7 +1,7 @@
-# fixture
+# changed
`)
	if err := ApplyPatch(ctx, repo, patch); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# changed\n" {
		t.Fatalf("README.md = %q", data)
	}
}

func TestApplyPatchReturnsTypedStderr(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	err := ApplyPatch(ctx, repo, []byte(`--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-# missing
+# changed
`))
	var applyErr *ApplyPatchError
	if !errors.As(err, &applyErr) {
		t.Fatalf("error = %T %v, want ApplyPatchError", err, err)
	}
	if !strings.Contains(applyErr.Stderr, "patch") {
		t.Fatalf("stderr = %q", applyErr.Stderr)
	}
}

func TestApplyPatchWithRejectsKeepsPartialApplication(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	err := ApplyPatchWithRejects(ctx, repo, []byte(`--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-# fixture
+# partially changed
@@ -2 +2 @@
-missing
+second change
`))
	var applyErr *ApplyPatchError
	if !errors.As(err, &applyErr) {
		t.Fatalf("error = %T %v, want ApplyPatchError", err, err)
	}
	if !applyErr.Partial {
		t.Fatalf("partial = false, stderr:\n%s", applyErr.Stderr)
	}
	data, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# partially changed\n" {
		t.Fatalf("README.md = %q", data)
	}
	if _, err := os.Stat(filepath.Join(repo, "README.md.rej")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("README.md.rej err = %v, want not exist", err)
	}
	if !strings.Contains(applyErr.Stderr, "rejected hunks") {
		t.Fatalf("stderr missing rejected hunks:\n%s", applyErr.Stderr)
	}
}

func TestApplyPatchWithRejectsIgnoresContextWhitespaceDrift(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	original := strings.Join([]string{
		"                        not-empty))",
		"              connections)))",
		"",
		"(defn- without-nil-values",
		"  [m]",
		"  (into {} (remove (comp nil? val)) m))",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	err := ApplyPatchWithRejects(ctx, repo, []byte(`--- a/README.md
+++ b/README.md
@@ -1,6 +1,11 @@
                          not-empty))
                connections)))
 
-(defn- without-nil-values
+(defn- installer-facing-primary-display-connection-slot
+  [display-slots primary-display-connection-slot]
+  (when (contains? display-slots primary-display-connection-slot)
+    primary-display-connection-slot))
+
+(defn- without-nil-values
   [m]
   (into {} (remove (comp nil? val)) m))
`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "installer-facing-primary-display-connection-slot") {
		t.Fatalf("README.md missing inserted helper:\n%s", got)
	}
	if !strings.Contains(got, "              connections)))") {
		t.Fatalf("README.md context whitespace changed unexpectedly:\n%s", got)
	}
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	if _, err := Run(ctx, repo, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, repo, "config", "user.email", "midgard@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, repo, "config", "user.name", "Midgard Test"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, repo, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, repo, "commit", "-m", "initial"); err != nil {
		t.Fatal(err)
	}
	return repo
}
