package agentloop_test

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
	"sort"
	"strings"
	"testing"

	"midgard/internal/action"
	"midgard/internal/agentloop"
	"midgard/internal/artifact"
	contextview "midgard/internal/context"
	"midgard/internal/eventlog"
	"midgard/internal/observe"
	"midgard/internal/policy/featuredelivery"
	"midgard/internal/provider"
	"midgard/internal/session"
	"midgard/internal/workspace"
)

type scriptedProvider struct {
	stops []provider.Stop
	next  int
}

type steeringProvider struct {
	calls  int
	second chan provider.Request
}

type preparedTestCall struct {
	request provider.Event
	execute func(context.Context, provider.EventSink) (provider.Stop, error)
}

func (c preparedTestCall) RequestEvent() provider.Event { return c.request }
func (c preparedTestCall) Execute(ctx context.Context, sink provider.EventSink) (provider.Stop, error) {
	return c.execute(ctx, sink)
}

func testRequestEvent(request provider.Request) provider.Event {
	raw, _ := json.Marshal(request)
	return provider.Event{NativeKind: "chat.completion.request", NativeID: "test-request", Sequence: 1, Payload: raw}
}

func (p *steeringProvider) Prepare(request provider.Request) (provider.PreparedCall, error) {
	p.calls++
	callNumber := p.calls
	return preparedTestCall{request: testRequestEvent(request), execute: func(_ context.Context, sink provider.EventSink) (provider.Stop, error) {
		if callNumber == 2 {
			p.second <- request
			return provider.Stop{}, context.Canceled
		}
		raw, _ := json.Marshal(map[string]any{"step": callNumber})
		if err := sink.Emit(provider.Event{NativeKind: "chat.completion", NativeID: "response", Sequence: 2, Payload: raw}); err != nil {
			return provider.Stop{}, err
		}
		return testBragiStop(provider.Stop{Reason: "tool_calls", Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "call-1", Name: "shell", Arguments: json.RawMessage(`{"command":"first"}`)},
			{ID: "call-2", Name: "shell", Arguments: json.RawMessage(`{"command":"second"}`)},
		}}}), nil
	}}, nil
}

type blockingShell struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingShell) RunShell(ctx context.Context, _ string, _ string, _ map[string]string, _ int) (workspace.Output, error) {
	close(b.started)
	select {
	case <-b.release:
		return workspace.Output{Stdout: "first complete", ExitCode: 0}, nil
	case <-ctx.Done():
		return workspace.Output{ExitCode: -1}, ctx.Err()
	}
}

func (*blockingShell) RunArgv(context.Context, string, []string, map[string]string, int) (workspace.Output, error) {
	return workspace.Output{ExitCode: 0}, nil
}

func (p *scriptedProvider) Prepare(request provider.Request) (provider.PreparedCall, error) {
	stop := p.stops[p.next]
	p.next++
	raw, _ := json.Marshal(map[string]any{"step": p.next})
	return preparedTestCall{request: testRequestEvent(request), execute: func(_ context.Context, sink provider.EventSink) (provider.Stop, error) {
		if err := sink.Emit(provider.Event{NativeKind: "chat.completion", NativeID: "response", Sequence: 2, Payload: raw}); err != nil {
			return provider.Stop{}, err
		}
		return testBragiStop(stop), nil
	}}, nil
}

func testBragiStop(stop provider.Stop) provider.Stop {
	var source strings.Builder
	if len(stop.Message.ToolCalls) > 0 {
		for _, call := range stop.Message.ToolCalls {
			id := "@" + strings.NewReplacer("-", "_", ":", "_").Replace(call.ID)
			fmt.Fprintf(&source, "+ %s tool\n+ %s.name %q\n", id, id, call.Name)
			var arguments map[string]any
			_ = json.Unmarshal(call.Arguments, &arguments)
			keys := make([]string, 0, len(arguments))
			for key := range arguments {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				raw, _ := json.Marshal(arguments[key])
				fmt.Fprintf(&source, "+ %s.arguments.%s %s\n", id, key, raw)
			}
			fmt.Fprintf(&source, "+ %s.reason %q\n! %s\n", id, "test action", id)
		}
	} else {
		content, _ := json.Marshal(stop.Message.Content)
		fmt.Fprintf(&source, "+ @answer message\n+ @answer.speaker \"assistant\"\n+ @answer.audience \"user\"\n+ @answer.channel \"final\"\n+ @answer.content %s\n! @answer\n+ @done completion\n+ @done.requested_outcome \"test complete\"\n! @done\n", content)
	}
	stop.Message = provider.Message{Role: "assistant", Content: source.String(), ReplayState: stop.Message.ReplayState}
	return stop
}

func TestCoordinatorCompletesCommittedWorktreeEdit(t *testing.T) {
	ctx := context.Background()
	repo := createRepo(t)
	state := t.TempDir()
	store, err := eventlog.Open(ctx, filepath.Join(state, "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{}, observe.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-1", "change Name to return midgard"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(state, "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-1", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-1") })
	artifacts, err := artifact.Open(filepath.Join(state, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := (featuredelivery.Policy{}).Configure("change Name to return midgard", repo)
	if err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: agentloop.Capabilities()}
	view, err := (contextview.Assembler{Log: store}).Build(ctx, "change Name to return midgard", binding)
	if err != nil {
		t.Fatal(err)
	}
	originalContent := "package todo\n\nfunc Name() string { return \"todo\" }\n"
	digest := sha256.Sum256([]byte(originalContent))
	originalHash := "sha256:" + hex.EncodeToString(digest[:])
	providerScript := &scriptedProvider{stops: []provider.Stop{
		{Reason: "tool_calls", Model: "deepseek-v4-pro", InputTokens: 100, CacheHitInputTokens: 20, CacheMissInputTokens: 80, OutputTokens: 10, Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "file_inspect", Arguments: mustJSON(map[string]any{"path": "todo.go"})}}}},
		{Reason: "tool_calls", Model: "deepseek-v4-pro", InputTokens: 120, CacheHitInputTokens: 70, CacheMissInputTokens: 50, OutputTokens: 12, Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call-2", Name: "file_replace", Arguments: mustJSON(map[string]any{"path": "todo.go", "expected_sha256": originalHash, "content": "package todo\n\nfunc Name() string { return \"midgard\" }\n"})}}}},
		{Reason: "stop", Model: "deepseek-v4-pro", InputTokens: 140, CacheHitInputTokens: 90, CacheMissInputTokens: 50, OutputTokens: 20, Message: provider.Message{Role: "assistant", Content: "Changed Name and verified the Go tests."}},
	}}
	coordinator := agentloop.Coordinator{
		Provider: providerScript, Artifacts: artifacts, Sessions: sessions,
		Actions: actions, Observe: observe.Service{Log: store}, Context: view,
		Runner:        workspace.Runner{Actions: &actions, Binding: binding, Unsafe: workspace.UnsafeHostExecutor{}},
		Configuration: configuration, Policy: featuredelivery.Policy{},
		MaxProviderCalls: 3,
	}
	result, err := coordinator.Run(ctx, "session-1", "change Name to return midgard")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Decision.Complete || result.Actions != 4 || result.ProviderCalls != 3 || result.Diff == "" {
		t.Fatalf("result = %#v", result)
	}
	var protocolCommit, actionIntent int64
	if err := store.DB().QueryRowContext(ctx, `SELECT MIN(sequence) FROM events WHERE session_id='session-1' AND kind='bragi.commit_accepted'`).Scan(&protocolCommit); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT MIN(sequence) FROM events WHERE session_id='session-1' AND kind='action.intent'`).Scan(&actionIntent); err != nil {
		t.Fatal(err)
	}
	if protocolCommit == 0 || protocolCommit >= actionIntent {
		t.Fatalf("protocol commit %d must be durable before action intent %d", protocolCommit, actionIntent)
	}
	projection, err := sessions.Get(ctx, "session-1")
	if err != nil || projection.Status != "completed" {
		t.Fatalf("session = %#v, %v", projection, err)
	}
	messages, err := sessions.Messages(ctx, "session-1")
	if err != nil || len(messages) != 2 || messages[1].Role != "assistant" {
		t.Fatalf("messages = %#v, %v", messages, err)
	}
	usages, err := sessions.TurnUsages(ctx, "session-1")
	if err != nil || len(usages) != 1 || usages[0].InputTokens != 360 || usages[0].CacheHitInputTokens != 180 || usages[0].CacheMissInputTokens != 180 || usages[0].OutputTokens != 42 {
		t.Fatalf("turn usages = %#v, %v", usages, err)
	}
	original, err := os.ReadFile(filepath.Join(repo, "todo.go"))
	if err != nil || string(original) != "package todo\n\nfunc Name() string { return \"todo\" }\n" {
		t.Fatalf("source repository changed: %q, %v", original, err)
	}
}

func TestCoordinatorCompletesResearchedResponseWithoutSourceEdit(t *testing.T) {
	ctx := context.Background()
	repo := createRepo(t)
	state := t.TempDir()
	store, err := eventlog.Open(ctx, filepath.Join(state, "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{}, observe.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-research", "explain what this project does"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(state, "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-research", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-research") })
	artifacts, err := artifact.Open(filepath.Join(state, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := (featuredelivery.Policy{}).Configure("explain what this project does", repo)
	if err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: agentloop.Capabilities()}
	view, err := (contextview.Assembler{Log: store}).Build(ctx, "explain what this project does", binding)
	if err != nil {
		t.Fatal(err)
	}
	providerScript := &scriptedProvider{stops: []provider.Stop{
		{Reason: "tool_calls", Model: "deepseek-v4-pro", Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "read-overview", Name: "file_inspect", Arguments: mustJSON(map[string]any{"path": "todo.go"})}}}},
		{Reason: "stop", Model: "deepseek-v4-pro", Message: provider.Message{Role: "assistant", Content: "This project exposes a small Go package named todo."}},
	}}
	coordinator := agentloop.Coordinator{
		Provider: providerScript, Artifacts: artifacts, Sessions: sessions,
		Actions: actions, Observe: observe.Service{Log: store}, Context: view,
		Runner:        workspace.Runner{Actions: &actions, Binding: binding, Unsafe: workspace.UnsafeHostExecutor{}},
		Configuration: configuration, Policy: featuredelivery.Policy{}, MaxProviderCalls: 2,
	}
	result, err := coordinator.RunTurn(ctx, "session-research", "explain what this project does")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Decision.Complete || result.Diff != "" || result.Actions != 1 || result.ProviderCalls != 2 {
		t.Fatalf("result = %#v", result)
	}
	var checks int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM action_projection WHERE session_id='session-research' AND capability='check.run'`).Scan(&checks); err != nil {
		t.Fatal(err)
	}
	if checks != 0 {
		t.Fatalf("research response ran %d implementation checks", checks)
	}
	messages, err := sessions.Messages(ctx, "session-research")
	if err != nil || len(messages) != 2 || messages[1].Content != "This project exposes a small Go package named todo." {
		t.Fatalf("messages = %#v, %v", messages, err)
	}
}

func TestCoordinatorCompletesDirectAdviceWithoutResearchOrChecks(t *testing.T) {
	ctx := context.Background()
	repo := createRepo(t)
	state := t.TempDir()
	store, err := eventlog.Open(ctx, filepath.Join(state, "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{}, observe.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	objective := "tell me one high impact thing we can do next"
	if _, err := sessions.Create(ctx, "session-advice", objective); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(state, "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-advice", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-advice") })
	if err := os.WriteFile(filepath.Join(binding.WorktreeRoot, "todo.go"), []byte("package todo\n\nfunc Name() string { return \"already dirty\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifact.Open(filepath.Join(state, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := (featuredelivery.Policy{}).Configure(objective, repo)
	if err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: agentloop.Capabilities()}
	view, err := (contextview.Assembler{Log: store}).Build(ctx, objective, binding)
	if err != nil {
		t.Fatal(err)
	}
	providerScript := &scriptedProvider{stops: []provider.Stop{{
		Reason: "stop", Model: "deepseek-v4-pro",
		Message: provider.Message{Role: "assistant", Content: "Prioritize a focused regression test for the request-to-response path."},
	}}}
	coordinator := agentloop.Coordinator{
		Provider: providerScript, Artifacts: artifacts, Sessions: sessions,
		Actions: actions, Observe: observe.Service{Log: store}, Context: view,
		Runner:           workspace.Runner{Actions: &actions, Binding: binding, Unsafe: workspace.UnsafeHostExecutor{}},
		Configuration:    configuration,
		Policy:           featuredelivery.Policy{},
		MaxProviderCalls: 1,
	}
	result, err := coordinator.RunTurn(ctx, "session-advice", objective)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Decision.Complete || result.ProviderCalls != 1 || result.Actions != 0 || result.Diff != "" {
		t.Fatalf("result = %#v", result)
	}
	var checks int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM action_projection WHERE session_id='session-advice' AND capability='check.run'`).Scan(&checks); err != nil {
		t.Fatal(err)
	}
	if checks != 0 {
		t.Fatalf("direct advice ran %d implementation checks", checks)
	}
	messages, err := sessions.Messages(ctx, "session-advice")
	if err != nil || len(messages) != 2 || messages[1].Content != "Prioritize a focused regression test for the request-to-response path." {
		t.Fatalf("messages = %#v, %v", messages, err)
	}
}

func TestCoordinatorRunsTwoTurnsInSameActiveSessionAndWorktree(t *testing.T) {
	ctx := context.Background()
	repo := createRepo(t)
	state := t.TempDir()
	store, err := eventlog.Open(ctx, filepath.Join(state, "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{}, observe.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-multi", "change the name"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(state, "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-multi", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-multi") })
	artifacts, err := artifact.Open(filepath.Join(state, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := (featuredelivery.Policy{}).Configure("change the name", repo)
	if err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: agentloop.Capabilities()}
	view, err := (contextview.Assembler{Log: store}).Build(ctx, "change the name", binding)
	if err != nil {
		t.Fatal(err)
	}
	original := "package todo\n\nfunc Name() string { return \"todo\" }\n"
	midgard := "package todo\n\nfunc Name() string { return \"midgard\" }\n"
	odin := "package todo\n\nfunc Name() string { return \"odin\" }\n"
	hash := func(content string) string {
		digest := sha256.Sum256([]byte(content))
		return "sha256:" + hex.EncodeToString(digest[:])
	}
	providerScript := &scriptedProvider{stops: []provider.Stop{
		{Reason: "tool_calls", Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "multi-1", Name: "file_replace", Arguments: mustJSON(map[string]any{"path": "todo.go", "expected_sha256": hash(original), "content": midgard})}}}},
		{Reason: "stop", Message: provider.Message{Role: "assistant", Content: "Changed the name to midgard."}},
		{Reason: "tool_calls", Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "multi-2", Name: "file_replace", Arguments: mustJSON(map[string]any{"path": "todo.go", "expected_sha256": hash(midgard), "content": odin})}}}},
		{Reason: "stop", Message: provider.Message{Role: "assistant", Content: "Changed the name to odin."}},
	}}
	coordinator := agentloop.Coordinator{
		Provider: providerScript, Artifacts: artifacts, Sessions: sessions,
		Actions: actions, Observe: observe.Service{Log: store}, Context: view,
		Runner:        workspace.Runner{Actions: &actions, Binding: binding, Unsafe: workspace.UnsafeHostExecutor{}},
		Configuration: configuration, Policy: featuredelivery.Policy{}, MaxProviderCalls: 4,
	}
	first, err := coordinator.RunTurn(ctx, "session-multi", "change the name to midgard")
	if err != nil || !first.Decision.Complete {
		t.Fatalf("first turn = %#v, %v", first, err)
	}
	projection, err := sessions.Get(ctx, "session-multi")
	if err != nil || projection.Status != "active" {
		t.Fatalf("session after first turn = %#v, %v", projection, err)
	}
	second, err := coordinator.RunTurn(ctx, "session-multi", "now change it to odin")
	if err != nil || !second.Decision.Complete || second.Worktree != first.Worktree {
		t.Fatalf("second turn = %#v, %v", second, err)
	}
	messages, err := sessions.Messages(ctx, "session-multi")
	if err != nil || len(messages) != 4 || messages[2].Content != "now change it to odin" {
		t.Fatalf("messages = %#v, %v", messages, err)
	}
	content, err := os.ReadFile(filepath.Join(binding.WorktreeRoot, "todo.go"))
	if err != nil || string(content) != odin {
		t.Fatalf("final worktree content = %q, %v", content, err)
	}
}

func TestCoordinatorAppliesSteerBeforeAnotherActionCommits(t *testing.T) {
	ctx := context.Background()
	repo := createRepo(t)
	state := t.TempDir()
	store, err := eventlog.Open(ctx, filepath.Join(state, "state.sqlite"), session.Projector{}, action.Projector{}, workspace.Projector{}, observe.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-steer", "initial objective"); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.Service{Log: store, WorktreeBase: filepath.Join(state, "worktrees")}
	binding, err := workspaces.Bind(ctx, "session-steer", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaces.Cleanup(context.Background(), "session-steer") })
	artifacts, err := artifact.Open(filepath.Join(state, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := (featuredelivery.Policy{}).Configure("initial objective", repo)
	if err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: agentloop.Capabilities()}
	view, err := (contextview.Assembler{Log: store}).Build(ctx, "initial objective", binding)
	if err != nil {
		t.Fatal(err)
	}
	providerScript := &steeringProvider{second: make(chan provider.Request, 1)}
	executor := &blockingShell{started: make(chan struct{}), release: make(chan struct{})}
	coordinator := agentloop.Coordinator{
		Provider: providerScript, Artifacts: artifacts, Sessions: sessions,
		Actions: actions, Observe: observe.Service{Log: store}, Context: view,
		Runner:        workspace.Runner{Actions: &actions, Binding: binding, Unsafe: executor},
		Configuration: configuration, Policy: featuredelivery.Policy{}, MaxProviderCalls: 3,
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := coordinator.RunTurn(ctx, "session-steer", "initial objective")
		errCh <- err
	}()
	<-executor.started
	control, err := sessions.Steer(ctx, "session-steer", "do not run the second command")
	if err != nil {
		t.Fatal(err)
	}
	close(executor.release)
	request := <-providerScript.second
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("turn error = %v", err)
	}
	var sawSteer, sawSuperseded bool
	for _, message := range request.Messages {
		if message.Role == "user" && message.Content == "do not run the second command" {
			sawSteer = true
		}
		if message.Role == "user" && strings.Contains(message.Content, "@call_2") && strings.Contains(message.Content, "superseded_by_steer") {
			sawSuperseded = true
		}
	}
	if !sawSteer || !sawSuperseded {
		t.Fatalf("steered request missing messages: %#v", request.Messages)
	}
	var actionsCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM action_projection WHERE session_id='session-steer'`).Scan(&actionsCount); err != nil {
		t.Fatal(err)
	}
	if actionsCount != 1 {
		t.Fatalf("actions = %d, want only the dispatched command", actionsCount)
	}
	var acknowledged int
	if err := store.DB().QueryRowContext(ctx, `SELECT acknowledged FROM control_projection WHERE control_id=?`, control.ControlID).Scan(&acknowledged); err != nil || acknowledged != 1 {
		t.Fatalf("acknowledged = %d, %v", acknowledged, err)
	}
	var commitsAfterSteer int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE session_id='session-steer' AND kind='action.committed' AND sequence>?`, control.Sequence).Scan(&commitsAfterSteer); err != nil {
		t.Fatal(err)
	}
	if commitsAfterSteer != 0 {
		t.Fatalf("%d actions committed after steering was queued", commitsAfterSteer)
	}
}

func createRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run(t, repo, "git", "init", "-b", "main")
	run(t, repo, "git", "config", "user.email", "test@example.com")
	run(t, repo, "git", "config", "user.name", "Test")
	files := map[string]string{
		"go.mod":       "module todo\n\ngo 1.25.0\n",
		"todo.go":      "package todo\n\nfunc Name() string { return \"todo\" }\n",
		"todo_test.go": "package todo\n\nimport \"testing\"\n\nfunc TestName(t *testing.T) { if Name() == \"\" { t.Fatal(\"empty\") } }\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-m", "initial")
	return repo
}

func run(t *testing.T, dir string, argv ...string) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v: %s", argv, err, output)
	}
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
