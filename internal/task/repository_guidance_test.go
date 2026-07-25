package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryGuidanceLoadsRootAgentsFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Rules\n\nRun focused tests.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := repositoryGuidance([]WorktreeStatus{{RepoID: "repo1", Path: root}})
	for _, want := range []string{"repo:repo1 file:AGENTS.md", "# Rules", "Run focused tests."} {
		if !strings.Contains(got, want) {
			t.Fatalf("guidance missing %q:\n%s", want, got)
		}
	}
}

func TestRepositoryGuidanceIsBounded(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("instruction\n", maxGuidanceFileBytes)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := repositoryGuidance([]WorktreeStatus{{RepoID: "repo1", Path: root}})
	if len(got) > maxRepositoryGuidanceBytes+256 {
		t.Fatalf("guidance is not bounded: %d bytes", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Fatal("bounded guidance did not disclose truncation")
	}
}
