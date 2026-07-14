package policy

import (
	"errors"
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

func TestReadOnlyCommandPolicy(t *testing.T) {
	commandPolicy := ReadOnlyCommandPolicy(t.TempDir())
	for _, command := range []string{
		"git diff -- README.md",
		"sed -n '1,20p' README.md",
		"rg -n TODO .",
		"go test ./...",
		"python3 -m pytest -q",
		"grep -F '$args[2..-1] $lastArg' fish_completions.go",
	} {
		if err := commandPolicy.ValidateCommand(command); err != nil {
			t.Fatalf("%q rejected: %v", command, err)
		}
	}
	for _, command := range []string{
		"printf changed > README.md",
		"rm -rf .",
		"git checkout -- README.md",
		"python3 -c 'open(\"README.md\", \"w\").write(\"changed\")'",
		"cat ../../.env",
		"go test ./... && rm -rf .",
		"grep -F \"$HOME\" README.md",
	} {
		err := commandPolicy.ValidateCommand(command)
		var denied CommandDeniedError
		if !errors.As(err, &denied) {
			t.Fatalf("%q error = %v, want CommandDeniedError", command, err)
		}
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
