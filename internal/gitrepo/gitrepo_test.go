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

func TestSnapshotWorktreeCopiesCurrentDiffAndIsolatesMutations(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "review-snapshot")
	if err := AddSnapshotWorktree(ctx, repo, snapshot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = RemoveSnapshotWorktree(context.Background(), repo, snapshot)
	})
	for path, want := range map[string]string{
		"README.md": "# changed\n",
		"notes.txt": "untracked\n",
	} {
		data, err := os.ReadFile(filepath.Join(snapshot, path))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("snapshot %s = %q, want %q", path, data, want)
		}
	}
	if err := os.WriteFile(filepath.Join(snapshot, "README.md"), []byte("# reviewer mutation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != "# changed\n" {
		t.Fatalf("source README.md = %q", source)
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

func TestApplyPatchWithRejectsDoesNotTreatPreexistingDirtAsPartialApplication(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("already dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := ApplyPatchWithRejects(ctx, repo, []byte(`--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-# missing
+# changed
`))
	var applyErr *ApplyPatchError
	if !errors.As(err, &applyErr) {
		t.Fatalf("error = %T %v, want ApplyPatchError", err, err)
	}
	if applyErr.Partial {
		t.Fatalf("preexisting dirty worktree was misclassified as partial:\n%s", applyErr.Stderr)
	}
	data, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# fixture\n" {
		t.Fatalf("README.md = %q", data)
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

func TestApplyUniqueReplacementRecoversObservedLocalDatePatches(t *testing.T) {
	patches := map[string]string{
		"duplicated context": strings.Join([]string{
			"--- a/unstable/kind.go",
			"+++ b/unstable/kind.go",
			"@@ -33,7 +33,7 @@ const (",
			" \t// Integer represents an integer value.",
			" \tInteger",
			" \t// LocalDate represents a a local date value.",
			"-\t// LocalDate represents a a local date value.",
			"+\t// LocalDate represents a local date value.",
			" \tLocalDate",
			" \t// LocalTime represents a local time value.",
			" \tLocalTime",
			"",
		}, "\n"),
		"omitted enum context": strings.Join([]string{
			"--- a/unstable/kind.go",
			"+++ b/unstable/kind.go",
			"@@ -34,7 +34,7 @@",
			" \t// Integer represents an integer value.",
			" \tInteger",
			"-\t// LocalDate represents a a local date value.",
			"+\t// LocalDate represents a local date value.",
			" \t// LocalTime represents a local time value.",
			" \tLocalTime",
			" \t// LocalDateTime represents a local date/time value.",
			"",
		}, "\n"),
	}
	for name, patch := range patches {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeLocalDateFixture(t, root, false)
			result, err := ApplyUniqueReplacement(root, "unstable/kind.go", []byte(patch))
			if err != nil {
				t.Fatal(err)
			}
			if result.File != "unstable/kind.go" || result.BeforeChecksum == "" || result.AfterChecksum == "" || result.BeforeChecksum == result.AfterChecksum {
				t.Fatalf("result = %#v", result)
			}
			data, err := os.ReadFile(filepath.Join(root, "unstable", "kind.go"))
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if strings.Contains(text, "represents a a local") || strings.Count(text, "represents a local date value") != 1 {
				t.Fatalf("kind.go:\n%s", text)
			}
		})
	}
}

func TestApplyUniqueReplacementRejectsAmbiguousMatch(t *testing.T) {
	root := t.TempDir()
	writeLocalDateFixture(t, root, true)
	path := filepath.Join(root, "unstable", "kind.go")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ApplyUniqueReplacement(root, "unstable/kind.go", []byte(`--- a/unstable/kind.go
+++ b/unstable/kind.go
@@ -1 +1 @@
-	// LocalDate represents a a local date value.
+	// LocalDate represents a local date value.
`))
	var replacementErr *UniqueReplacementError
	if !errors.As(err, &replacementErr) || !strings.Contains(replacementErr.Reason, "2 line-bounded matches") {
		t.Fatalf("error = %T %v", err, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("ambiguous replacement changed file:\n%s", after)
	}
}

func TestApplyUniqueReplacementRejectsUnsafePatchShapes(t *testing.T) {
	tests := map[string]string{
		"path mismatch": `--- a/other.go
+++ b/other.go
@@ -1 +1 @@
-old
+new
`,
		"multiple files": `--- a/unstable/kind.go
+++ b/unstable/kind.go
@@ -1 +1 @@
-old
+new
--- a/other.go
+++ b/other.go
@@ -1 +1 @@
-old
+new
`,
		"multiple hunks": `--- a/unstable/kind.go
+++ b/unstable/kind.go
@@ -1 +1 @@
-old
+new
@@ -3 +3 @@
-old again
+new again
`,
		"multiple change blocks": `--- a/unstable/kind.go
+++ b/unstable/kind.go
@@ -1,3 +1,3 @@
-old
+new
 context
-old again
+new again
`,
		"insertion only": `--- a/unstable/kind.go
+++ b/unstable/kind.go
@@ -1 +1,2 @@
 context
+new
`,
		"binary": `GIT binary patch
literal 1
A
`,
	}
	for name, patch := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeLocalDateFixture(t, root, false)
			path := filepath.Join(root, "unstable", "kind.go")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyUniqueReplacement(root, "unstable/kind.go", []byte(patch)); err == nil {
				t.Fatal("unsafe patch unexpectedly applied")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("rejected patch changed file:\n%s", after)
			}
		})
	}
}

func writeLocalDateFixture(t *testing.T, root string, duplicate bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "unstable"), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		"const (",
		"\t// Integer represents an integer value.",
		"\tInteger",
		"\t// LocalDate represents a a local date value.",
		"\tLocalDate",
		"\t// LocalTime represents a local time value.",
		"\tLocalTime",
		"\t// LocalDateTime represents a local date/time value.",
		"\tLocalDateTime",
		")",
		"",
	}
	if duplicate {
		lines = append(lines[:5], append([]string{"\t// LocalDate represents a a local date value."}, lines[5:]...)...)
	}
	if err := os.WriteFile(filepath.Join(root, "unstable", "kind.go"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
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
