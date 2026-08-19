package context_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	contextview "midgard/internal/context"
	"midgard/internal/eventlog"
	"midgard/internal/session"
	"midgard/internal/workspace"
)

func TestBuildIsBoundedDeterministicAndExcludesSecrets(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	run(t, repo, "git", "init", "-b", "main")
	run(t, repo, "git", "config", "user.email", "test@example.com")
	run(t, repo, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("repository guidance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", "AGENTS.md")
	run(t, repo, "git", "commit", "-m", "initial")
	head := output(t, repo, "git", "rev-parse", "HEAD")
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := (session.Service{Log: store}).Create(ctx, "session-1", "objective"); err != nil {
		t.Fatal(err)
	}
	appendEvent := func(id string, visibility eventlog.Visibility) {
		head, err := store.Head(ctx, "session-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(ctx, eventlog.Draft{EventID: id, SessionID: "session-1", Actor: eventlog.ActorModel,
			Kind: "model.message", SchemaVersion: 1, Visibility: visibility, Payload: []byte(`{"text":"bounded"}`)}, head); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent("public", eventlog.VisibilityPublic)
	appendEvent("secret", eventlog.VisibilitySecret)
	binding := workspace.Binding{SessionID: "session-1", RepositoryRoot: repo, WorktreeRoot: repo, StartCommit: head}
	assembler := contextview.Assembler{Log: store, MaxRecentEvents: 2, MaxBytes: 4096}
	first, err := assembler.Build(ctx, "objective", binding)
	if err != nil {
		t.Fatal(err)
	}
	second, err := assembler.Build(ctx, "objective", binding)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Recent) != 2 || first.Recent[1].EventID != "public" {
		t.Fatalf("recent events = %#v", first.Recent)
	}
	if len(first.Guidance) != 1 || first.Guidance[0].Path != "AGENTS.md" {
		t.Fatalf("guidance = %#v", first.Guidance)
	}
	if first.Repository != second.Repository || len(first.Recent) != len(second.Recent) {
		t.Fatal("context reconstruction changed without state change")
	}
}

func run(t *testing.T, dir string, argv ...string) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v: %s", argv, err, raw)
	}
}

func output(t *testing.T, dir string, argv ...string) string {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(raw[:len(raw)-1])
}
