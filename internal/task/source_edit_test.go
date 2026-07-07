package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"midgard/internal/stream"
)

func TestSanitizePatchPayloadRemovesStandaloneFences(t *testing.T) {
	patch := []byte(strings.Join([]string{
		"```diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"```",
		"",
	}, "\n"))
	got := string(sanitizePatchPayload(patch))
	if strings.Contains(got, "```") {
		t.Fatalf("sanitized patch still contains fence:\n%s", got)
	}
	if !strings.Contains(got, "@@ -1 +1 @@") || !strings.Contains(got, "+new") {
		t.Fatalf("sanitized patch dropped diff content:\n%s", got)
	}
}

func TestSanitizePatchPayloadPreservesInternalFenceContext(t *testing.T) {
	patch := []byte(strings.Join([]string{
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1,4 +1,4 @@",
		" ```",
		"-old",
		"+new",
		" ```",
		"",
	}, "\n"))
	got := string(sanitizePatchPayload(patch))
	if strings.Count(got, "```") != 2 {
		t.Fatalf("sanitized patch should preserve internal fence context:\n%s", got)
	}
	if !strings.Contains(got, "-old") || !strings.Contains(got, "+new") {
		t.Fatalf("sanitized patch dropped diff content:\n%s", got)
	}
}

func TestSourceEditContextDoesNotInventTrailingBlankLine(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Fixture App\n\nThis README is used for a Midgard Codex e2e benchmark.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := string(sourceEditContext(context.Background(), WorktreeStatus{RepoID: "repo1", Path: root}, stream.EditIntent{File: "README.md"}, ""))
	if strings.Contains(got, "   4 |") {
		t.Fatalf("source edit context included phantom fourth line:\n%s", got)
	}
	if !strings.Contains(got, "   3 | This README is used") {
		t.Fatalf("source edit context missing real third line:\n%s", got)
	}
}

func TestSourceEditContextCentersFailedPatchLine(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 220; i++ {
		fmt.Fprintf(&b, "line %03d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got := string(sourceEditContext(
		context.Background(),
		WorktreeStatus{RepoID: "repo1", Path: root},
		stream.EditIntent{File: "README.md"},
		"error: patch failed: README.md:180\nerror: README.md: patch does not apply",
	))
	if !strings.Contains(got, "around failed patch line 180") ||
		!strings.Contains(got, " 180 | line 180") {
		t.Fatalf("source edit context missing failed line:\n%s", got)
	}
	if strings.Contains(got, "   1 | line 001") {
		t.Fatalf("source edit context should not start at file head for late failures:\n%s", got)
	}
}

func TestSourceEditContextIncludesRejectedPatchFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "primary.clj"), []byte("(ns primary)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 1; i <= 220; i++ {
		fmt.Fprintf(&b, "secondary line %03d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "secondary.clj"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := strings.Join([]string{
		"error: patch failed: src/secondary.clj:180",
		"error: src/secondary.clj: patch does not apply",
		"rejected hunks:",
		"file:src/secondary.clj.rej",
	}, "\n")
	got := string(sourceEditContext(
		context.Background(),
		WorktreeStatus{RepoID: "repo1", Path: root},
		stream.EditIntent{File: "src/primary.clj"},
		stderr,
	))
	if !strings.Contains(got, "file:src/primary.clj primary:true") {
		t.Fatalf("source edit context missing primary edit file:\n%s", got)
	}
	if !strings.Contains(got, "file:src/secondary.clj rejected:true") {
		t.Fatalf("source edit context missing rejected file:\n%s", got)
	}
	if !strings.Contains(got, "around failed patch line 180") ||
		!strings.Contains(got, " 180 | secondary line 180") {
		t.Fatalf("source edit context missing rejected file failed line:\n%s", got)
	}
}
