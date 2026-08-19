package workspace_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"midgard/internal/action"
	runtimeenv "midgard/internal/environment"
	"midgard/internal/eventlog"
	"midgard/internal/session"
	"midgard/internal/workspace"
)

type workspaceTestSecrets map[string]string

func (s workspaceTestSecrets) Set(account, secret string) error   { s[account] = secret; return nil }
func (s workspaceTestSecrets) Get(account string) (string, error) { return s[account], nil }

func TestLandedWorktreeCleanupIsDurable(t *testing.T) {
	ctx := context.Background()
	repo := createRepository(t)
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-clean", "land a change"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(t.TempDir(), "worktrees"), DefaultBranch: "main", LandingStrategy: "direct", CleanupWhenLanded: true}
	binding, err := workspaces.Bind(ctx, "session-clean", repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binding.WorktreeRoot, "landed.txt"), []byte("landed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, binding.WorktreeRoot, "git", "add", "landed.txt")
	run(t, binding.WorktreeRoot, "git", "commit", "-m", "land change")
	landedHead := output(t, binding.WorktreeRoot, "git", "rev-parse", "HEAD")
	run(t, repo, "git", "merge", "--ff-only", strings.TrimSpace(landedHead))
	if _, err := sessions.Finish(ctx, "session-clean", "completed", "test landed"); err != nil {
		t.Fatal(err)
	}
	cleaned, err := workspaces.CleanupIfLanded(ctx, "session-clean")
	if err != nil || !cleaned {
		t.Fatalf("CleanupIfLanded() = %v, %v", cleaned, err)
	}
	if _, err := os.Stat(binding.WorktreeRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree still exists: %v", err)
	}
	current, err := workspaces.Get(ctx, "session-clean")
	if err != nil || current.CleanupState != "cleaned" {
		t.Fatalf("cleanup projection = %#v, %v", current, err)
	}
	events, err := store.Events(ctx, "session-clean", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 || events[len(events)-2].Kind != "workspace.cleanup_committed" || events[len(events)-1].Kind != "workspace.cleaned" {
		t.Fatalf("cleanup events = %#v", events)
	}
}

func TestSessionCanBindTwoNamedProjectRepositories(t *testing.T) {
	ctx := context.Background()
	firstRepo := createRepository(t)
	secondRepo := createRepository(t)
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := (session.Service{Log: store}).CreateInProject(ctx, "session-multi", "project-multi", "change both repositories"); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(t.TempDir(), "worktrees")
	firstService := workspace.Service{Log: store, WorktreeBase: base, ProjectID: "project-multi", RepositoryName: "first", DefaultBranch: "main"}
	secondService := workspace.Service{Log: store, WorktreeBase: base, ProjectID: "project-multi", RepositoryName: "second", DefaultBranch: "main"}
	first, err := firstService.Bind(ctx, "session-multi", firstRepo)
	if err != nil {
		t.Fatal(err)
	}
	second, err := secondService.Bind(ctx, "session-multi", secondRepo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = firstService.Cleanup(context.Background(), "session-multi")
		_ = secondService.Cleanup(context.Background(), "session-multi")
	})
	if first.WorktreeRoot == second.WorktreeRoot || first.RepositoryName != "first" || second.RepositoryName != "second" {
		t.Fatalf("bindings = %#v, %#v", first, second)
	}
	loadedFirst, err := firstService.GetRepository(ctx, "session-multi", "first")
	if err != nil || !sameFile(t, loadedFirst.RepositoryRoot, firstRepo) {
		t.Fatalf("first binding = %#v, %v", loadedFirst, err)
	}
	loadedSecond, err := secondService.GetRepository(ctx, "session-multi", "second")
	if err != nil || !sameFile(t, loadedSecond.RepositoryRoot, secondRepo) {
		t.Fatalf("second binding = %#v, %v", loadedSecond, err)
	}
}

func sameFile(t *testing.T, left, right string) bool {
	t.Helper()
	leftInfo, err := os.Stat(left)
	if err != nil {
		t.Fatal(err)
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(leftInfo, rightInfo)
}

func TestCleanupRecoversAfterRemovalBeforeResultEvent(t *testing.T) {
	ctx := context.Background()
	repo := createRepository(t)
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := eventlog.Open(ctx, path, session.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (session.Service{Log: store}).Create(ctx, "session-recover-cleanup", "recover cleanup"); err != nil {
		t.Fatal(err)
	}
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	workspaces := workspace.Service{Log: store, WorktreeBase: worktreeBase, DefaultBranch: "main", LandingStrategy: "direct", CleanupWhenLanded: true}
	binding, err := workspaces.Bind(ctx, "session-recover-cleanup", repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendCurrent(ctx, eventlog.Draft{EventID: "cleanup-commit", SessionID: "session-recover-cleanup", Actor: eventlog.ActorServer,
		Kind: "workspace.cleanup_committed", SchemaVersion: 1, Visibility: eventlog.VisibilityInternal, Payload: json.RawMessage(`{"evidence":"test"}`)}); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "worktree", "remove", "--force", binding.WorktreeRoot)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = eventlog.Open(ctx, path, session.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspaces.Log = store
	cleaned, err := workspaces.CleanupIfLanded(ctx, "session-recover-cleanup")
	if err != nil || !cleaned {
		t.Fatalf("recovered cleanup = %v, %v", cleaned, err)
	}
	current, err := workspaces.Get(ctx, "session-recover-cleanup")
	if err != nil || current.CleanupState != "cleaned" {
		t.Fatalf("cleanup projection = %#v, %v", current, err)
	}
}

func TestRepositoryPreflightExplainsRequiredGitState(t *testing.T) {
	ctx := context.Background()
	nonGit := t.TempDir()
	if _, err := workspace.InspectRepository(ctx, nonGit, "main"); err == nil || !strings.Contains(err.Error(), "git init -b main") {
		t.Fatalf("non-Git error = %v", err)
	}

	empty := t.TempDir()
	run(t, empty, "git", "init", "-b", "main")
	if _, err := workspace.InspectRepository(ctx, empty, "main"); err == nil || !strings.Contains(err.Error(), "Initial commit") {
		t.Fatalf("no-commit error = %v", err)
	}

	repo := createRepository(t)
	nested := filepath.Join(repo, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.InspectRepository(ctx, nested, "main"); err == nil || !strings.Contains(err.Error(), "Git top level") {
		t.Fatalf("nested error = %v", err)
	}
}

func TestDefaultBranchMustExistAndCanBeOverridden(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	run(t, repo, "git", "init", "-b", "trunk")
	run(t, repo, "git", "config", "user.email", "test@example.com")
	run(t, repo, "git", "config", "user.name", "Test")
	run(t, repo, "git", "commit", "--allow-empty", "-m", "initial")
	if _, err := workspace.InspectRepository(ctx, repo, "main"); err == nil || !strings.Contains(err.Error(), "-default-branch") {
		t.Fatalf("missing default branch error = %v", err)
	}
	info, err := workspace.InspectRepository(ctx, repo, "trunk")
	if err != nil || info.DefaultBranch != "trunk" {
		t.Fatalf("InspectRepository() = %#v, %v", info, err)
	}
}

func TestBindingPersistsDefaultBranchAcrossRebuildAndReopen(t *testing.T) {
	ctx := context.Background()
	repo := createRepository(t)
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := eventlog.Open(ctx, path, session.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (session.Service{Log: store}).Create(ctx, "session-branch", "remember branch"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(t.TempDir(), "worktrees"), DefaultBranch: "main", ProjectID: "project-test", RepositoryName: "source"}
	binding, err := workspaces.Bind(ctx, "session-branch", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-branch") })
	if binding.DefaultBranch != "main" || binding.ProjectID != "project-test" || binding.RepositoryName != "source" {
		t.Fatalf("bound identity = %#v", binding)
	}
	if err := store.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = eventlog.Open(ctx, path, session.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspaces.Log = store
	reopened, err := workspaces.Get(ctx, "session-branch")
	if err != nil || reopened.DefaultBranch != "main" || reopened.ProjectID != "project-test" || reopened.RepositoryName != "source" {
		t.Fatalf("reopened binding = %#v, %v", reopened, err)
	}
}

func TestFileReplaceRequiresCurrentInspectionHash(t *testing.T) {
	ctx := context.Background()
	repo := createRepository(t)
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := (session.Service{Log: store}).Create(ctx, "session-1", "replace README"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(t.TempDir(), "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-1", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-1") })
	actions := action.Service{Log: store, Validator: action.CapabilitySet{"file.inspect": nil, "file.replace": nil}}
	runner := workspace.Runner{Actions: &actions, Binding: binding}

	runAction := func(id, capability, arguments string) workspace.Output {
		t.Helper()
		if _, err := actions.Intent(ctx, "session-1", id, capability, json.RawMessage(arguments), false); err != nil {
			t.Fatal(err)
		}
		if _, err := actions.Validate(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, err := actions.Commit(ctx, id, id); err != nil {
			t.Fatal(err)
		}
		claim, err := actions.Dispatch(ctx, id, "worker")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := runner.Execute(ctx, claim)
		if err != nil {
			t.Fatal(err)
		}
		var output workspace.Output
		if err := json.Unmarshal(raw, &output); err != nil {
			t.Fatal(err)
		}
		return output
	}

	inspected := runAction("inspect", "file.inspect", `{"path":"README.md"}`)
	digest := sha256.Sum256([]byte("hello\n"))
	wantHash := "sha256:" + hex.EncodeToString(digest[:])
	if inspected.Stdout != "hello\n" || inspected.SHA256 != wantHash {
		t.Fatalf("inspection = %#v", inspected)
	}
	replacement, _ := json.Marshal(map[string]string{"path": "README.md", "expected_sha256": inspected.SHA256, "content": "changed\n"})
	replaced := runAction("replace", "file.replace", string(replacement))
	if replaced.ExitCode != 0 || replaced.ErrorCode != "" || replaced.SHA256 == inspected.SHA256 {
		t.Fatalf("replacement = %#v", replaced)
	}
	stale := runAction("stale", "file.replace", string(replacement))
	if stale.ExitCode == 0 || stale.ErrorCode != "stale_file" || stale.SHA256 != replaced.SHA256 {
		t.Fatalf("stale replacement = %#v", stale)
	}
	source, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil || string(source) != "hello\n" {
		t.Fatalf("source repository changed: %q, %v", source, err)
	}
}

func TestRepositorySearchUsesBundledYggdrasilAndReturnsBoundedCitations(t *testing.T) {
	ctx := context.Background()
	repo := createRepository(t)
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := (session.Service{Log: store}).Create(ctx, "session-search", "find relevant code"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(t.TempDir(), "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-search", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-search") })
	if err := os.WriteFile(filepath.Join(binding.WorktreeRoot, "draft.go"), []byte("package draft\n\n// unique search needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ygg := writeYggFixture(t)
	actions := action.Service{Log: store, Validator: action.CapabilitySet{"repo.search": nil}}
	runner := workspace.Runner{Actions: &actions, Binding: binding, YggBinary: ygg, YggStorageRoot: filepath.Join(t.TempDir(), "ygg"), YggConfiguration: []byte(`{"schema":"ygg.config/v1"}`)}
	runSearch := func(id, arguments string) workspace.Output {
		t.Helper()
		if _, err := actions.Intent(ctx, "session-search", id, "repo.search", json.RawMessage(arguments), false); err != nil {
			t.Fatal(err)
		}
		if _, err := actions.Validate(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, err := actions.Commit(ctx, id, id); err != nil {
			t.Fatal(err)
		}
		claim, err := actions.Dispatch(ctx, id, "worker")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := runner.Execute(ctx, claim)
		if err != nil {
			t.Fatal(err)
		}
		var output workspace.Output
		if err := json.Unmarshal(raw, &output); err != nil {
			t.Fatal(err)
		}
		return output
	}
	tracked := runSearch("tracked", `{"query":"hello","path":"README.md"}`)
	if tracked.ExitCode != 0 || !strings.Contains(tracked.Stdout, "README.md:1-2: hello") {
		t.Fatalf("tracked search = %#v", tracked)
	}
	untracked := runSearch("untracked", `{"query":"unique search needle"}`)
	if untracked.ExitCode != 0 || !strings.Contains(untracked.Stdout, "draft.go:3: // unique search needle") || !strings.Contains(untracked.Stdout, "More paths: internal/todo/store.go") {
		t.Fatalf("untracked search = %#v", untracked)
	}
	missing := runSearch("missing", `{"query":"not present anywhere"}`)
	if missing.ExitCode != 0 || missing.Stdout != "" {
		t.Fatalf("no-match search = %#v", missing)
	}
	runner.YggBinary = filepath.Join(t.TempDir(), "missing-ygg")
	unavailable := runSearch("unavailable", `{"query":"hello"}`)
	if unavailable.ExitCode != -1 || unavailable.ErrorCode != "search_unavailable" || !strings.Contains(unavailable.Stderr, "Reinstall Midgard") {
		t.Fatalf("missing bundled search = %#v", unavailable)
	}
}

func TestBundledHeimdalRunsOnlyItsSupportedCommandInTheWorktree(t *testing.T) {
	ctx := context.Background()
	repo := createRepository(t)
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := (session.Service{Log: store}).Create(ctx, "session-browser", "check browser behavior"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(t.TempDir(), "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-browser", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-browser") })
	heimdal := filepath.Join(t.TempDir(), "heimdal")
	if err := os.WriteFile(heimdal, []byte("#!/bin/sh\nprintf 'browser cwd=%s args=%s\\n' \"$PWD\" \"$*\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: action.CapabilitySet{"browser.run": nil}}
	if _, err := actions.Intent(ctx, "session-browser", "browser", "browser.run", json.RawMessage(`{"command":"doctor --json"}`), false); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Validate(ctx, "browser"); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Commit(ctx, "browser", "browser"); err != nil {
		t.Fatal(err)
	}
	claim, err := actions.Dispatch(ctx, "browser", "worker")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := (workspace.Runner{Actions: &actions, Binding: binding, HeimdalBinary: heimdal}).Execute(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	var output workspace.Output
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	worktreeRoot, err := filepath.EvalSymlinks(binding.WorktreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if output.ExitCode != 0 || !strings.Contains(output.Stdout, "cwd="+worktreeRoot) || !strings.Contains(output.Stdout, "args=doctor --json") {
		t.Fatalf("browser output = %#v", output)
	}
	arguments, err := workspace.ParseCommand(`session start --url "http://127.0.0.1:3000/a b"`)
	if err != nil || !slices.Equal(arguments, []string{"session", "start", "--url", "http://127.0.0.1:3000/a b"}) {
		t.Fatalf("ParseCommand() = %#v, %v", arguments, err)
	}
}

func TestBoundWorkspaceRunsOnlyDispatchedClaims(t *testing.T) {
	ctx := context.Background()
	repo := createRepository(t)
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := (session.Service{Log: store}).Create(ctx, "session-1", "inspect README"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(t.TempDir(), "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-1", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-1") })

	actions := action.Service{Log: store, Validator: action.CapabilitySet{"file.inspect": nil, "shell": nil, "check.run": nil}}
	if _, err := actions.Intent(ctx, "session-1", "inspect-1", "file.inspect", json.RawMessage(`{"path":"README.md"}`), false); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Validate(ctx, "inspect-1"); err != nil {
		t.Fatal(err)
	}
	committed, err := actions.Commit(ctx, "inspect-1", "inspect-1")
	if err != nil {
		t.Fatal(err)
	}
	runner := workspace.Runner{Actions: &actions, Binding: binding}
	if _, err := runner.Execute(ctx, action.Claim{ActionID: "inspect-1", CommitID: committed.CommitID, Owner: "worker", Fence: 1}); err == nil {
		t.Fatal("runner executed before dispatch")
	}
	claim, err := actions.Dispatch(ctx, "inspect-1", "worker")
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Execute(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) == "" {
		t.Fatal("empty inspection result")
	}
}

func TestFileInspectionRejectsSymlinkEscapeAndShellNeedsSandbox(t *testing.T) {
	ctx := context.Background()
	repo := createRepository(t)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "escape")); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", "escape")
	run(t, repo, "git", "commit", "-m", "add escape fixture")
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	(session.Service{Log: store}).Create(ctx, "session-1", "test containment")
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(t.TempDir(), "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-1", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-1") })
	actions := action.Service{Log: store, Validator: action.CapabilitySet{"file.inspect": nil, "shell": nil, "check.run": nil}}
	runner := workspace.Runner{Actions: &actions, Binding: binding}
	for _, spec := range []struct{ id, capability, args string }{
		{"escape", "file.inspect", `{"path":"escape"}`},
		{"shell", "shell", `{"command":"pwd"}`},
		{"check-smuggle", "check.run", `{"argv":["sh","-c","pwd"]}`},
	} {
		if _, err := actions.Intent(ctx, "session-1", spec.id, spec.capability, json.RawMessage(spec.args), false); err != nil {
			t.Fatal(err)
		}
		if _, err := actions.Validate(ctx, spec.id); err != nil {
			t.Fatal(err)
		}
		if _, err := actions.Commit(ctx, spec.id, spec.id); err != nil {
			t.Fatal(err)
		}
		claim, err := actions.Dispatch(ctx, spec.id, "worker")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Execute(ctx, claim); err == nil {
			t.Fatalf("%s action escaped containment gate", spec.capability)
		}
	}
}

func TestCancellationBeforeExecutionStopsDispatchedAction(t *testing.T) {
	ctx := context.Background()
	repo := createRepository(t)
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-1", "cancel before execute"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(t.TempDir(), "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-1", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-1") })
	actions := action.Service{Log: store, Validator: action.CapabilitySet{"file.inspect": nil}}
	if _, err := actions.Intent(ctx, "session-1", "inspect", "file.inspect", json.RawMessage(`{"path":"README.md"}`), false); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Validate(ctx, "inspect"); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Commit(ctx, "inspect", "cancel-key"); err != nil {
		t.Fatal(err)
	}
	claim, err := actions.Dispatch(ctx, "inspect", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Cancel(ctx, "session-1", "interrupt"); err != nil {
		t.Fatal(err)
	}
	if _, err := (workspace.Runner{Actions: &actions, Binding: binding}).Execute(ctx, claim); err == nil {
		t.Fatal("cancelled session executed dispatched action")
	}
}

func TestUnsafeHostExecutionIsExplicitAndStillRequiresDispatch(t *testing.T) {
	ctx := context.Background()
	repo := createRepository(t)
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := (session.Service{Log: store}).Create(ctx, "session-1", "prototype command"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(t.TempDir(), "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-1", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-1") })
	actions := action.Service{Log: store, Validator: action.CapabilitySet{"shell": nil}}
	if _, err := actions.Intent(ctx, "session-1", "shell", "shell", json.RawMessage(`{"command":"printf midgard"}`), false); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Validate(ctx, "shell"); err != nil {
		t.Fatal(err)
	}
	committed, err := actions.Commit(ctx, "shell", "unsafe-shell")
	if err != nil {
		t.Fatal(err)
	}
	runner := workspace.Runner{Actions: &actions, Binding: binding, Unsafe: workspace.UnsafeHostExecutor{}}
	if _, err := runner.Execute(ctx, action.Claim{ActionID: "shell", CommitID: committed.CommitID, Owner: "worker", Fence: 1}); err == nil {
		t.Fatal("unsafe executor bypassed durable dispatch")
	}
	claim, err := actions.Dispatch(ctx, "shell", "worker")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := runner.Execute(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	var output workspace.Output
	if err := json.Unmarshal(raw, &output); err != nil || output.Stdout != "midgard" || output.ExitCode != 0 {
		t.Fatalf("output = %#v, %v", output, err)
	}
}

func TestManagedBackgroundShellReturnsIncrementalOutputAndCanStop(t *testing.T) {
	ctx := context.Background()
	repo := createRepository(t)
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := (session.Service{Log: store}).Create(ctx, "session-jobs", "run background work"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(t.TempDir(), "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-jobs", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-jobs") })
	actions := action.Service{Log: store, Validator: action.CapabilitySet{"shell": nil, "shell.poll": nil, "shell.stop": nil}}
	jobs := &workspace.BackgroundJobs{}
	defer jobs.Close()
	runner := workspace.Runner{Actions: &actions, Binding: binding, Unsafe: workspace.UnsafeHostExecutor{}, Jobs: jobs}
	runAction := func(id, capability, arguments string) workspace.Output {
		t.Helper()
		if _, err := actions.Intent(ctx, "session-jobs", id, capability, json.RawMessage(arguments), false); err != nil {
			t.Fatal(err)
		}
		if _, err := actions.Validate(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, err := actions.Commit(ctx, id, id); err != nil {
			t.Fatal(err)
		}
		claim, err := actions.Dispatch(ctx, id, "worker")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := runner.Execute(ctx, claim)
		if err != nil {
			t.Fatal(err)
		}
		var output workspace.Output
		if err := json.Unmarshal(raw, &output); err != nil {
			t.Fatal(err)
		}
		return output
	}
	started := runAction("start", "shell", `{"command":"printf first; sleep 0.2; printf second","background":true}`)
	if started.JobID == "" || started.Status != "running" {
		t.Fatalf("start = %#v", started)
	}
	time.Sleep(50 * time.Millisecond)
	first := runAction("poll-1", "shell.poll", fmt.Sprintf(`{"job_id":%q}`, started.JobID))
	if first.Status != "running" || first.Stdout != "first" {
		t.Fatalf("first poll = %#v", first)
	}
	time.Sleep(250 * time.Millisecond)
	second := runAction("poll-2", "shell.poll", fmt.Sprintf(`{"job_id":%q}`, started.JobID))
	if second.Status != "completed" || second.Stdout != "second" || second.JobExitCode == nil || *second.JobExitCode != 0 {
		t.Fatalf("second poll = %#v", second)
	}
	long := runAction("start-long", "shell", `{"command":"sleep 10","background":true}`)
	stopped := runAction("stop-long", "shell.stop", fmt.Sprintf(`{"job_id":%q}`, long.JobID))
	if stopped.Status != "stopped" {
		t.Fatalf("stop = %#v", stopped)
	}
	secret, err := jobs.Start(ctx, "session-jobs", binding.RepositoryName, binding.WorktreeRoot,
		"printf abc; sleep 0.2; printf def", nil, map[string]string{"TOKEN": "abcdef"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	partial, err := jobs.Poll("session-jobs", secret.JobID)
	if err != nil || partial.Stdout != "" {
		t.Fatalf("partial secret escaped: %#v, %v", partial, err)
	}
	time.Sleep(250 * time.Millisecond)
	redacted, err := jobs.Poll("session-jobs", secret.JobID)
	if err != nil || redacted.Stdout != "[REDACTED:TOKEN]" {
		t.Fatalf("background secret redaction = %#v, %v", redacted, err)
	}
}

func TestUnsafeShellCancellationKillsTheProcessGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	output, err := (workspace.UnsafeHostExecutor{}).RunShell(ctx, t.TempDir(), "sleep 10", nil, 1024)
	if !errors.Is(err, context.DeadlineExceeded) || output.ErrorCode != "timeout" {
		t.Fatalf("timeout = %#v, %v", output, err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("process group was not reclaimed promptly: %s", time.Since(started))
	}
}

func TestCommittedEnvironmentIsInjectedAndSecretOutputIsRedacted(t *testing.T) {
	ctx := context.Background()
	repo := createRepository(t)
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := (session.Service{Log: store}).Create(ctx, "session-env", "use environment"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(t.TempDir(), "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-env", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-env") })
	actions := action.Service{Log: store, Validator: action.CapabilitySet{"shell": nil}}
	arguments := json.RawMessage(`{"command":"printf '%s|%s' \"$PLAIN_VALUE\" \"$SECRET_TOKEN\"","_midgard_environment":"env_snapshot"}`)
	if _, err := actions.Intent(ctx, "session-env", "shell-env", "shell", arguments, false); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Validate(ctx, "shell-env"); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Commit(ctx, "shell-env", "shell-env"); err != nil {
		t.Fatal(err)
	}
	claim, err := actions.Dispatch(ctx, "shell-env", "worker")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := runtimeenv.Snapshot{ID: "env_snapshot", Variables: []runtimeenv.Variable{
		{Name: "PLAIN_VALUE", Value: "visible"},
		{Name: "SECRET_TOKEN", Secret: true, SecretAccount: "secret-account"},
	}}
	runner := workspace.Runner{Actions: &actions, Binding: binding, Unsafe: workspace.UnsafeHostExecutor{},
		Environment: runtimeenv.Resolver{Snapshot: snapshot, Secrets: workspaceTestSecrets{"secret-account": "must-not-escape"}}}
	raw, err := runner.Execute(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	var output workspace.Output
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	if output.Stdout != "visible|[REDACTED:SECRET_TOKEN]" || strings.Contains(string(raw), "must-not-escape") {
		t.Fatalf("redacted output = %s", raw)
	}
}

func createRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run(t, repo, "git", "init", "-b", "main")
	run(t, repo, "git", "config", "user.email", "test@example.com")
	run(t, repo, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", "README.md")
	run(t, repo, "git", "commit", "-m", "initial")
	return repo
}

func writeYggFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ygg")
	script := `#!/bin/sh
if [ -z "$YGG_STORAGE_ROOT" ] || [ -z "$YGG_CONFIG" ] || [ ! -f "$YGG_CONFIG" ]; then
  exit 9
fi
case "$*" in
  *"not present anywhere"*)
    printf '%s\n' '{"schema":"ygg.cli/v1","ok":true,"data":{"schema":"ygg.search.result/v4","activeMode":"lexical","records":[]}}'
    ;;
  *"unique search needle"*)
    printf '%s\n' '{"schema":"ygg.cli/v1","ok":true,"data":{"schema":"ygg.search.result/v4","activeMode":"lexical","records":[{"path":"draft.go","startLine":3,"endLine":3,"excerpt":"// unique search needle"}],"morePaths":["internal/todo/store.go"]}}'
    ;;
  *)
    printf '%s\n' '{"schema":"ygg.cli/v1","ok":true,"data":{"schema":"ygg.search.result/v4","activeMode":"lexical","records":[{"path":"README.md","startLine":1,"endLine":2,"excerpt":"hello"}]}}'
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func run(t *testing.T, dir string, argv ...string) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v: %s", argv, err, out)
	}
}

func output(t *testing.T, dir string, argv ...string) string {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	result, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %v", argv, err)
	}
	return string(result)
}
