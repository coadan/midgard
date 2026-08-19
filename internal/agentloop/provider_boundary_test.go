package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"midgard/internal/action"
	"midgard/internal/artifact"
	"midgard/internal/eventlog"
	"midgard/internal/observe"
	"midgard/internal/policy/featuredelivery"
	modelprotocol "midgard/internal/protocol"
	"midgard/internal/provider"
	"midgard/internal/session"
	"midgard/internal/workspace"
)

type boundaryProbeProvider struct {
	log           *eventlog.Store
	artifacts     *artifact.Store
	executed      bool
	replayPayload json.RawMessage
}

type boundaryProbeCall struct {
	owner   *boundaryProbeProvider
	request provider.Event
}

type nativeToolProvider struct{}

type protocolTextProvider struct {
	content string
}

type protocolTextCall struct {
	request provider.Event
	content string
}

func (c protocolTextCall) RequestEvent() provider.Event { return c.request }

func (c protocolTextCall) Execute(_ context.Context, sink provider.EventSink) (provider.Stop, error) {
	if err := sink.Emit(provider.Event{NativeKind: "chat.completion", NativeID: "protocol-text", Sequence: 2, Payload: json.RawMessage(`{"ok":true}`)}); err != nil {
		return provider.Stop{}, err
	}
	return provider.Stop{Reason: "stop", Message: provider.Message{Role: "assistant", Content: c.content}}, nil
}

type feedbackCaptureProvider struct {
	calls  int
	second provider.Request
	source string
}

type lengthRecoveryProvider struct {
	calls  int
	second provider.Request
}

type lengthRecoveryCall struct{ request provider.Event }

func (p *feedbackCaptureProvider) Prepare(request provider.Request) (provider.PreparedCall, error) {
	p.calls++
	if p.calls == 2 {
		p.second = request
		return nil, errors.New("captured repair request")
	}
	return protocolTextProvider{content: p.source}.Prepare(request)
}

func (p *lengthRecoveryProvider) Prepare(request provider.Request) (provider.PreparedCall, error) {
	p.calls++
	if p.calls == 2 {
		p.second = request
		return nil, errors.New("captured length recovery request")
	}
	raw, _ := json.Marshal(request)
	return lengthRecoveryCall{request: provider.Event{NativeKind: "chat.completion.request", NativeID: "length-request", Sequence: 1, Payload: raw}}, nil
}

func (c lengthRecoveryCall) RequestEvent() provider.Event { return c.request }

func (c lengthRecoveryCall) Execute(_ context.Context, sink provider.EventSink) (provider.Stop, error) {
	if err := sink.Emit(provider.Event{NativeKind: "chat.completion", NativeID: "length-response", Sequence: 2, Payload: json.RawMessage(`{"ok":true}`)}); err != nil {
		return provider.Stop{}, err
	}
	return provider.Stop{Reason: "length", Message: provider.Message{Role: "assistant", ReplayState: &provider.ReplayState{Adapter: "test.v1", Payload: json.RawMessage(`{"private_reasoning":"must not be replayed"}`)}}}, nil
}

func (p protocolTextProvider) Prepare(request provider.Request) (provider.PreparedCall, error) {
	raw, _ := json.Marshal(request)
	return protocolTextCall{request: provider.Event{NativeKind: "chat.completion.request", NativeID: "protocol-text-request", Sequence: 1, Payload: raw}, content: p.content}, nil
}

func (nativeToolProvider) Prepare(provider.Request) (provider.PreparedCall, error) {
	return preparedNativeToolCall{}, nil
}

type preparedNativeToolCall struct{}

func (preparedNativeToolCall) RequestEvent() provider.Event {
	return provider.Event{NativeKind: "chat.completion.request", NativeID: "native-tool-request", Sequence: 1, Payload: json.RawMessage(`{"tools":[]}`)}
}

func (preparedNativeToolCall) Execute(_ context.Context, sink provider.EventSink) (provider.Stop, error) {
	if err := sink.Emit(provider.Event{NativeKind: "chat.completion", NativeID: "native-tool-response", Sequence: 2, Payload: json.RawMessage(`{"tool_call":true}`)}); err != nil {
		return provider.Stop{}, err
	}
	return provider.Stop{Reason: "tool_calls", Message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "native-1", Name: "shell", Arguments: json.RawMessage(`{"command":"pwd"}`)}}}}, nil
}

func (p *boundaryProbeProvider) Prepare(provider.Request) (provider.PreparedCall, error) {
	return boundaryProbeCall{owner: p, request: provider.Event{
		NativeKind: "chat.completion.request", NativeID: "native-request-1", Sequence: 1,
		Payload: json.RawMessage(`{"messages":[{"role":"user","content":"exact request"}]}`),
	}}, nil
}

func (c boundaryProbeCall) RequestEvent() provider.Event { return c.request }

func (c boundaryProbeCall) Execute(ctx context.Context, sink provider.EventSink) (provider.Stop, error) {
	var ref string
	if err := c.owner.log.DB().QueryRowContext(ctx, `SELECT artifact_ref FROM events WHERE kind='provider.requested'`).Scan(&ref); err != nil {
		return provider.Stop{}, err
	}
	if err := c.owner.artifacts.Verify(ref); err != nil {
		return provider.Stop{}, err
	}
	handle, err := c.owner.artifacts.Open(ref)
	if err != nil {
		return provider.Stop{}, err
	}
	raw, err := io.ReadAll(handle)
	_ = handle.Close()
	if err != nil {
		return provider.Stop{}, err
	}
	var recorded provider.Event
	if err := json.Unmarshal(raw, &recorded); err != nil {
		return provider.Stop{}, err
	}
	if string(recorded.Payload) != string(c.request.Payload) {
		return provider.Stop{}, &provider.ValidationError{Message: "durable request artifact changed the native request"}
	}
	c.owner.executed = true
	content := "+ @m1 message\n+ @m1.speaker \"assistant\"\n+ @m1.audience \"user\"\n+ @m1.channel \"final\"\n+ @m1.content \"done\"\n! @m1\n+ @done completion\n+ @done.requested_outcome \"boundary verified\"\n! @done\n"
	if live, ok := sink.(provider.LiveSink); ok {
		live.EmitLive(provider.LiveUpdate{Kind: provider.LiveThinking, Delta: "checking context"})
		live.EmitLive(provider.LiveUpdate{Kind: provider.LiveThinking, Delta: "checking files"})
		live.EmitLive(provider.LiveUpdate{Kind: provider.LiveOutput, Delta: content[:len(content)/2]})
		live.EmitLive(provider.LiveUpdate{Kind: provider.LiveOutput, Delta: content[len(content)/2:]})
	}
	if err := sink.Emit(provider.Event{NativeKind: "chat.completion", NativeID: "native-response-1", Sequence: 2, Payload: json.RawMessage(`{"ok":true}`)}); err != nil {
		return provider.Stop{}, err
	}
	replayPayload := c.owner.replayPayload
	if replayPayload == nil {
		replayPayload = json.RawMessage(`{"opaque":"continuation"}`)
	}
	return provider.Stop{Reason: "stop", Message: provider.Message{
		Role: "assistant", Content: content,
		ReplayState: &provider.ReplayState{Adapter: "probe.v1", Payload: replayPayload},
	}}, nil
}

func TestInvalidReplayStateStopsBeforeItsDurableTransition(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	sessions := session.Service{Log: log}
	if _, err := sessions.Create(ctx, "session-1", "test invalid replay state"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.StartTurn(ctx, "session-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifact.Open(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	probe := &boundaryProbeProvider{log: log, artifacts: artifacts, replayPayload: json.RawMessage(`not-json`)}
	coordinator := Coordinator{Provider: probe, Artifacts: artifacts, Sessions: sessions}
	protocolTurn, err := modelprotocol.NewTurn()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.generate(ctx, "session-1", "turn-1", []provider.Message{{Role: "user", Content: "exact request"}}, protocolTurn); err == nil {
		t.Fatal("expected invalid replay state to stop the provider call")
	}
	var replayEvents int
	if err := log.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE kind='provider.replay_state'`).Scan(&replayEvents); err != nil {
		t.Fatal(err)
	}
	if replayEvents != 0 {
		t.Fatalf("replay events = %d", replayEvents)
	}
}

func TestProviderRequestIsDurableBeforeExecutionAndReplayStateIsArtifactOwned(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	sessions := session.Service{Log: log}
	if _, err := sessions.Create(ctx, "session-1", "test provider boundary"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.StartTurn(ctx, "session-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifact.Open(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	probe := &boundaryProbeProvider{log: log, artifacts: artifacts}
	var activities []Activity
	coordinator := Coordinator{Provider: probe, Artifacts: artifacts, Sessions: sessions, Activity: ActivityFunc(func(activity Activity) {
		activities = append(activities, activity)
	})}
	protocolTurn, err := modelprotocol.NewTurn()
	if err != nil {
		t.Fatal(err)
	}
	generated, err := coordinator.generate(ctx, "session-1", "turn-1", []provider.Message{{Role: "user", Content: "exact request"}}, protocolTurn)
	if err != nil {
		t.Fatal(err)
	}
	if !probe.executed {
		t.Fatal("provider did not execute after the durable request boundary")
	}
	if generated.Stop.Message.ReplayState == nil || generated.Stop.Message.ReplayState.ArtifactRef == "" {
		t.Fatalf("replay state = %#v", generated.Stop.Message.ReplayState)
	}
	if err := artifacts.Verify(generated.Stop.Message.ReplayState.ArtifactRef); err != nil {
		t.Fatal(err)
	}
	var replayRef string
	if err := log.DB().QueryRowContext(ctx, `SELECT artifact_ref FROM events WHERE kind='provider.replay_state'`).Scan(&replayRef); err != nil {
		t.Fatal(err)
	}
	if replayRef != generated.Stop.Message.ReplayState.ArtifactRef {
		t.Fatalf("replay event ref = %q, message ref = %q", replayRef, generated.Stop.Message.ReplayState.ArtifactRef)
	}
	if len(activities) < 3 || activities[0].Kind != "stream" || activities[0].State != "thinking" || activities[0].Message != "" || activities[1].State != "output" || activities[1].Message != "" || activities[2].Kind != "model_state" {
		t.Fatalf("live activities = %#v", activities)
	}
	streamStates := make([]string, 0, 2)
	for _, activity := range activities {
		if activity.Kind == "stream" {
			streamStates = append(streamStates, activity.State)
		}
	}
	if got := strings.Join(streamStates, ","); got != "thinking,output" {
		t.Fatalf("stream phases = %q, want only coalesced phase changes", got)
	}
}

func TestProviderNativeToolCallCannotBecomeActionIntent(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	sessions := session.Service{Log: log}
	if _, err := sessions.Create(ctx, "session-native", "reject native tools"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.StartTurn(ctx, "session-native", "turn-native"); err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifact.Open(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	protocolTurn, err := modelprotocol.NewTurn()
	if err != nil {
		t.Fatal(err)
	}
	coordinator := Coordinator{Provider: nativeToolProvider{}, Artifacts: artifacts, Sessions: sessions}
	_, err = coordinator.generate(ctx, "session-native", "turn-native", []provider.Message{{Role: "user", Content: "do it"}}, protocolTurn)
	if err == nil || !strings.Contains(err.Error(), "model-protocol") {
		t.Fatalf("native tool boundary error = %v", err)
	}
	var intents int
	if err := log.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE kind='action.intent'`).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if intents != 0 {
		t.Fatalf("native tool created %d action intents", intents)
	}
}

func TestProtocolFeedbackCollapsesRepeatedDiagnosticsAndKeepsRepairHint(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	sessions := session.Service{Log: log}
	if _, err := sessions.Create(ctx, "session-feedback", "inspect a file"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.StartTurn(ctx, "session-feedback", "turn-feedback"); err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifact.Open(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	source := "+ @read tool\n+ @read.name \"file_inspect\"\n+ @read.arguments.path \"README.md\"\n+ @read.reason \"inspect\"\n! @read\nmalformed one\nmalformed two\nmalformed three\n"
	coordinator := Coordinator{Provider: protocolTextProvider{content: source}, Artifacts: artifacts, Sessions: sessions}
	protocolTurn, err := modelprotocol.NewTurn()
	if err != nil {
		t.Fatal(err)
	}
	generated, err := coordinator.generate(ctx, "session-feedback", "turn-feedback", []provider.Message{{Role: "user", Content: "inspect"}}, protocolTurn)
	if err != nil {
		t.Fatal(err)
	}
	if len(generated.Actions) != 1 || !strings.Contains(generated.ProtocolFeedback, "repeated 3 times") || !strings.Contains(generated.ProtocolFeedback, "XML/DSML") || !strings.Contains(generated.ProtocolFeedback, "each accepted line starts") {
		t.Fatalf("generation = actions:%d feedback:%s", len(generated.Actions), generated.ProtocolFeedback)
	}
	if strings.Count(generated.ProtocolFeedback, "unknown_operator") != 1 {
		t.Fatalf("diagnostics were not collapsed: %s", generated.ProtocolFeedback)
	}
}

func TestLengthWithoutBragiOutputRetriesWithoutReplayingPrivateReasoning(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, observe.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-length", "explain the repository"); err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifact.Open(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := (featuredelivery.Policy{}).Configure("explain the repository", root)
	if err != nil {
		t.Fatal(err)
	}
	model := &lengthRecoveryProvider{}
	actions := action.Service{Log: store, Validator: Capabilities()}
	coordinator := Coordinator{
		Provider: model, Artifacts: artifacts, Sessions: sessions, Actions: actions, Observe: observe.Service{Log: store},
		Runner:        workspace.Runner{Actions: &actions, Binding: workspace.Binding{SessionID: "session-length", WorktreeRoot: root}},
		Configuration: configuration, Policy: featuredelivery.Policy{}, MaxProviderCalls: 2,
	}
	if _, err := coordinator.RunTurn(ctx, "session-length", "explain the repository"); err == nil || !strings.Contains(err.Error(), "captured length recovery request") {
		t.Fatalf("turn error = %v", err)
	}
	if model.calls != 2 || len(model.second.Messages) == 0 {
		t.Fatalf("provider calls=%d second=%#v", model.calls, model.second)
	}
	var combined strings.Builder
	for _, message := range model.second.Messages {
		if message.ReplayState != nil {
			t.Fatalf("empty length response replayed into recovery request: %#v", message.ReplayState)
		}
		combined.WriteString(message.Content)
		combined.WriteByte('\n')
	}
	if !strings.Contains(combined.String(), lengthWithoutOutputRecovery) {
		t.Fatalf("recovery instruction missing from second request:\n%s", combined.String())
	}
	var evidenceCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM completion_evidence_projection WHERE session_id='session-length' AND kind='provider.length_without_output'`).Scan(&evidenceCount); err != nil || evidenceCount != 1 {
		t.Fatalf("length evidence=%d, err=%v", evidenceCount, err)
	}
	var code, message string
	if err := store.DB().QueryRowContext(ctx, `SELECT json_extract(payload_json,'$.code'), json_extract(payload_json,'$.message') FROM events WHERE session_id='session-length' AND kind='turn.failed'`).Scan(&code, &message); err != nil {
		t.Fatal(err)
	}
	if code != "turn_failed" || strings.Contains(message, "captured length recovery request") {
		t.Fatalf("failed-turn receipt leaked or misclassified: code=%q message=%q", code, message)
	}
}

func TestAcceptedActionDoesNotDiscardProtocolRepairFeedback(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("overview\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{}, observe.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-mixed", "inspect overview"); err != nil {
		t.Fatal(err)
	}
	actions := action.Service{Log: store, Validator: Capabilities()}
	artifacts, err := artifact.Open(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := (featuredelivery.Policy{}).Configure("inspect overview", root)
	if err != nil {
		t.Fatal(err)
	}
	source := "+ @read tool\n+ @read.name \"file_inspect\"\n+ @read.arguments.path \"README.md\"\n+ @read.reason \"inspect\"\n! @read\nnot a record\n"
	model := &feedbackCaptureProvider{source: source}
	coordinator := Coordinator{Provider: model, Artifacts: artifacts, Sessions: sessions, Actions: actions,
		Observe: observe.Service{Log: store}, Runner: workspace.Runner{Actions: &actions, Binding: workspace.Binding{SessionID: "session-mixed", WorktreeRoot: root}},
		Configuration: configuration, Policy: featuredelivery.Policy{}, MaxProviderCalls: 2}
	if _, err := coordinator.RunTurn(ctx, "session-mixed", "inspect overview"); err == nil || !strings.Contains(err.Error(), "captured repair request") {
		t.Fatalf("turn error = %v", err)
	}
	var combined strings.Builder
	for _, message := range model.second.Messages {
		combined.WriteString(message.Content)
		combined.WriteByte('\n')
	}
	if !strings.Contains(combined.String(), "Midgard rejected malformed protocol lines") || !strings.Contains(combined.String(), "host result") {
		t.Fatalf("second request lost action result or repair feedback:\n%s", combined.String())
	}
}
