package policy

import (
	"path/filepath"
	"testing"

	"midgard/internal/artifact"
)

func TestValidateCWD(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "nested")
	policy := DefaultCommandPolicy(root)
	if err := policy.ValidateCWD(inside); err != nil {
		t.Fatal(err)
	}
	if err := policy.ValidateCWD(t.TempDir()); err == nil {
		t.Fatal("cwd outside allowed roots was accepted")
	}
}

func TestCanExecuteArtifact(t *testing.T) {
	if decision := CanExecuteArtifact(artifact.Record{Type: artifact.TypePayload, State: artifact.StateDraft}); decision.Allowed {
		t.Fatal("draft payload should not be executable")
	}
	if decision := CanExecuteArtifact(artifact.Record{Type: artifact.TypePayload, State: artifact.StateSealed}); !decision.Allowed {
		t.Fatalf("sealed payload rejected: %s", decision.Reason)
	}
}
