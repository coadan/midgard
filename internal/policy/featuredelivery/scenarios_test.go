package featuredelivery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"midgard/internal/action"
	"midgard/internal/eventlog"
	"midgard/internal/observe"
	"midgard/internal/policy"
	"midgard/internal/policy/featuredelivery"
	"midgard/internal/session"
	"midgard/internal/workspace"
)

func TestFeatureDeliveryScenarios(t *testing.T) {
	t.Run("verified-no-op", func(t *testing.T) {
		decision := (featuredelivery.Policy{}).EvaluateCompletion(policy.CompletionEvidence{
			ObjectiveAddressed: true, VerifiedNoOp: true, ActionsTerminal: true,
			Checks: []policy.CheckEvidence{{Argv: []string{"git", "diff", "--check"}, ExitCode: 0}},
		})
		if !decision.Complete {
			t.Fatalf("no-op rejected: %#v", decision)
		}
	})
	t.Run("focused-edit", func(t *testing.T) {
		h := newHarness(t)
		patch := "diff --git a/README.md b/README.md\nindex ce01362..94954ab 100644\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-hello\n+hello midgard\n"
		h.run(t, "patch", "patch.apply", map[string]any{"patch": patch}, true, nil)
		check := h.run(t, "check", "check.run", map[string]any{"argv": []string{"git", "diff", "--check"}}, true, [][]string{{"git", "diff", "--check"}})
		var output workspace.Output
		if err := json.Unmarshal(check, &output); err != nil {
			t.Fatal(err)
		}
		decision := (featuredelivery.Policy{}).EvaluateCompletion(policy.CompletionEvidence{
			ObjectiveAddressed: true, GitDiffObserved: true, ActionsTerminal: true,
			Checks: []policy.CheckEvidence{{Argv: []string{"git", "diff", "--check"}, ExitCode: output.ExitCode}},
		})
		if !decision.Complete {
			t.Fatalf("focused edit rejected: %#v", decision)
		}
		if countChanged(t, h.binding.WorktreeRoot) != 1 {
			t.Fatal("focused edit did not change one file")
		}
	})
	t.Run("multi-file-edit", func(t *testing.T) {
		h := newHarness(t)
		patch := "diff --git a/README.md b/README.md\nindex ce01362..94954ab 100644\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-hello\n+hello midgard\n" +
			"diff --git a/NEW.md b/NEW.md\nnew file mode 100644\nindex 0000000..3e75765\n--- /dev/null\n+++ b/NEW.md\n@@ -0,0 +1 @@\n+new\n"
		h.run(t, "patch", "patch.apply", map[string]any{"patch": patch}, true, nil)
		if countChanged(t, h.binding.WorktreeRoot) != 2 {
			t.Fatal("multi-file edit did not change two files")
		}
	})
	t.Run("failed-check", func(t *testing.T) {
		h := newHarness(t)
		patch := "diff --git a/README.md b/README.md\nindex ce01362..94954ab 100644\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-hello\n+changed\n"
		h.run(t, "patch", "patch.apply", map[string]any{"patch": patch}, true, nil)
		result := h.run(t, "check", "check.run", map[string]any{"argv": []string{"git", "diff", "--exit-code"}}, false, [][]string{{"git", "diff", "--exit-code"}})
		var output workspace.Output
		if err := json.Unmarshal(result, &output); err != nil {
			t.Fatal(err)
		}
		decision := (featuredelivery.Policy{}).EvaluateCompletion(policy.CompletionEvidence{
			ObjectiveAddressed: true, GitDiffObserved: true, ActionsTerminal: true,
			Checks: []policy.CheckEvidence{{Argv: []string{"git", "diff", "--exit-code"}, ExitCode: output.ExitCode}},
		})
		if decision.Complete {
			t.Fatal("failed check produced completion")
		}
	})
}

type harness struct {
	ctx     context.Context
	store   *eventlog.Store
	actions action.Service
	binding workspace.Binding
	cleanup func()
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	runCommand(t, repo, "git", "init", "-b", "main")
	runCommand(t, repo, "git", "config", "user.email", "test@example.com")
	runCommand(t, repo, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCommand(t, repo, "git", "add", "README.md")
	runCommand(t, repo, "git", "commit", "-m", "initial")
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{}, observe.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (session.Service{Log: store}).Create(ctx, "session-1", "scenario"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(t.TempDir(), "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-1", repo)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{ctx: ctx, store: store, binding: binding,
		actions: action.Service{Log: store, Validator: action.CapabilitySet{"patch.apply": nil, "check.run": nil}}}
	h.cleanup = func() { _ = workspaces.Cleanup(context.Background(), "session-1"); _ = store.Close() }
	t.Cleanup(h.cleanup)
	return h
}

func (h *harness) run(t *testing.T, id, capability string, arguments any, success bool, allowed [][]string) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(arguments)
	if _, err := h.actions.Intent(h.ctx, "session-1", id, capability, raw, false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.actions.Validate(h.ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.actions.Commit(h.ctx, id, id); err != nil {
		t.Fatal(err)
	}
	claim, err := h.actions.Dispatch(h.ctx, id, "worker")
	if err != nil {
		t.Fatal(err)
	}
	result, err := (workspace.Runner{Actions: &h.actions, Binding: h.binding, AllowedChecks: allowed, Sandbox: testSandbox{}}).Execute(h.ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.actions.Result(h.ctx, claim, success, result); err != nil {
		t.Fatal(err)
	}
	return result
}

func countChanged(t *testing.T, root string) int {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain=v1")
	cmd.Dir = root
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, b := range raw {
		if b == '\n' {
			count++
		}
	}
	return count
}

func runCommand(t *testing.T, dir string, argv ...string) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v: %s", argv, err, output)
	}
}

// testSandbox is process isolation test plumbing only. Production composition
// must supply an implementation that actually contains filesystem/process IO.
type testSandbox struct{}

func (testSandbox) RunShell(context.Context, string, string, map[string]string, int) (workspace.Output, error) {
	return workspace.Output{}, os.ErrPermission
}

func (testSandbox) RunArgv(ctx context.Context, dir string, argv []string, _ map[string]string, limit int) (workspace.Output, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		if exit, ok := err.(*exec.ExitError); ok {
			exitCode = exit.ExitCode()
		}
	}
	return workspace.Output{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, err
}
