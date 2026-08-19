package provider_test

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"midgard/internal/artifact"
	"midgard/internal/eventlog"
	"midgard/internal/provider"
)

func TestTracePreservesUnknownNativeEvents(t *testing.T) {
	store, err := artifact.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := provider.NewTraceRecorder(store)
	if err != nil {
		t.Fatal(err)
	}
	unknown := provider.Event{NativeKind: "future.provider.event", NativeID: "n1", Sequence: 1, Payload: json.RawMessage(`{"opaque":true}`)}
	if err := recorder.Emit(unknown); err != nil {
		t.Fatal(err)
	}
	sealed, err := recorder.Seal()
	if err != nil {
		t.Fatal(err)
	}
	file, err := store.Open(sealed.Ref)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	var replayed provider.Event
	if err := json.Unmarshal(raw, &replayed); err != nil {
		t.Fatal(err)
	}
	if replayed.NativeKind != unknown.NativeKind || string(replayed.Payload) != string(unknown.Payload) {
		t.Fatalf("trace changed unknown event: %#v", replayed)
	}
}

func TestTraceRejectsSequenceGaps(t *testing.T) {
	store, _ := artifact.Open(t.TempDir())
	recorder, _ := provider.NewTraceRecorder(store)
	defer recorder.Abort()
	err := recorder.Emit(provider.Event{NativeKind: "usage", Sequence: 2, Payload: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("sequence gap accepted")
	}
}

func TestNormalizerSkipsTokensAndPreservesUnknownBoundary(t *testing.T) {
	traceRef := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, ok := provider.Normalize("e1", "s1", "t1", traceRef, provider.Event{NativeKind: "content_block_delta", Sequence: 1, Payload: json.RawMessage(`{}`)}); ok {
		t.Fatal("token delta became a canonical event")
	}
	draft, ok := provider.Normalize("e2", "s1", "t1", traceRef, provider.Event{NativeKind: "future_event", Sequence: 2, Payload: json.RawMessage(`{"x":1}`)})
	if !ok || draft.Kind != "provider.unknown" || draft.ArtifactRef != traceRef || string(draft.Payload) == "" {
		t.Fatalf("unknown boundary not preserved: %#v", draft)
	}
}

func TestNormalizerRecordsRequestBoundaryWithoutDuplicatingBody(t *testing.T) {
	traceRef := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, fixture := range []struct {
		name       string
		nativeKind string
		payload    json.RawMessage
	}{
		{name: "DeepSeek", nativeKind: "chat.completion.request", payload: json.RawMessage(`{"messages":[{"role":"user","content":"private"}]}`)},
		{name: "Codex", nativeKind: "codex.turn.request", payload: json.RawMessage(`{"model":"gpt-5","input":"private"}`)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			draft, ok := provider.Normalize("e1", "s1", "t1", traceRef, provider.Event{NativeKind: fixture.nativeKind, NativeID: traceRef, Sequence: 1, Payload: fixture.payload})
			if !ok || draft.Kind != "provider.requested" || draft.ArtifactRef != traceRef {
				t.Fatalf("request boundary = %#v", draft)
			}
			if string(draft.Payload) == "" || containsJSONText(draft.Payload, "private") {
				t.Fatalf("canonical request duplicated trace body: %s", draft.Payload)
			}
		})
	}
}

func TestNormalizerKeepsLargeProviderBodiesInTheTraceArtifact(t *testing.T) {
	traceRef := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	body := strings.Repeat("x", eventlog.MaxPayloadBytes*2)
	draft, ok := provider.Normalize("e1", "s1", "t1", traceRef, provider.Event{
		NativeKind: "item/completed", Sequence: 1, Payload: json.RawMessage(`{"text":"` + body + `"}`),
	})
	if !ok || draft.ArtifactRef != traceRef {
		t.Fatalf("large provider boundary = %#v", draft)
	}
	if len(draft.Payload) >= eventlog.MaxPayloadBytes || containsJSONText(draft.Payload, body[:100]) {
		t.Fatalf("canonical event retained large provider body: %d bytes", len(draft.Payload))
	}
	if err := draft.Validate(); err != nil {
		t.Fatalf("large provider boundary invalid: %v", err)
	}
}

func TestNormalizerProjectsCodexSemanticBoundariesWithoutTokenDeltas(t *testing.T) {
	traceRef := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, fixture := range []struct {
		nativeKind string
		canonical  string
		kept       bool
	}{
		{nativeKind: "item/started", canonical: "provider.item_started", kept: true},
		{nativeKind: "item/completed", canonical: "provider.item_finished", kept: true},
		{nativeKind: "thread/tokenUsage/updated", canonical: "provider.usage", kept: true},
		{nativeKind: "turn/completed", canonical: "provider.completed", kept: true},
		{nativeKind: "item/agentMessage/delta", kept: false},
		{nativeKind: "item/reasoning/summaryTextDelta", kept: false},
		{nativeKind: "item/reasoning/textDelta", kept: false},
	} {
		draft, ok := provider.Normalize("event", "session", "turn", traceRef, provider.Event{NativeKind: fixture.nativeKind, Sequence: 2, Payload: json.RawMessage(`{"opaque":true}`)})
		if ok != fixture.kept {
			t.Fatalf("%s kept=%v, want %v", fixture.nativeKind, ok, fixture.kept)
		}
		if ok && draft.Kind != fixture.canonical {
			t.Fatalf("%s normalized to %q, want %q", fixture.nativeKind, draft.Kind, fixture.canonical)
		}
	}
}

func containsJSONText(raw json.RawMessage, text string) bool {
	return strings.Contains(string(raw), text)
}
