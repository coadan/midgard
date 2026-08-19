package provider_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"midgard/internal/provider"
)

func TestInstalledCodexModelCatalog(t *testing.T) {
	if os.Getenv("MIDGARD_CODEX_INTEGRATION") == "" {
		t.Skip("set MIDGARD_CODEX_INTEGRATION=1 for the installed Codex smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := provider.CodexModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		t.Fatal("Codex returned no models")
	}
	for _, model := range models {
		if model.ID == "" || model.DisplayName == "" || len(model.Efforts) == 0 {
			t.Fatalf("incomplete model = %#v", model)
		}
	}
}

func TestCodexPreparedRequestIsADurableProviderBoundary(t *testing.T) {
	prepared, err := (provider.Codex{Model: "gpt-test", Effort: "medium"}).Prepare(provider.Request{Messages: []provider.Message{
		{Role: "system", Content: "protocol instructions"},
		{Role: "user", Content: "do the work"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := prepared.RequestEvent()
	if request.NativeKind != "codex.turn.request" || request.Sequence != 1 {
		t.Fatalf("request event = %#v", request)
	}
	traceRef := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	draft, ok := provider.Normalize("event", "session", "turn", traceRef, request)
	if !ok || draft.Kind != "provider.requested" || draft.ArtifactRef != traceRef {
		t.Fatalf("normalized request = %#v, kept=%v", draft, ok)
	}
}

func TestInstalledCodexStreamsModelOnlyTurn(t *testing.T) {
	if os.Getenv("MIDGARD_CODEX_TURN_INTEGRATION") == "" {
		t.Skip("set MIDGARD_CODEX_TURN_INTEGRATION=1 for a live model turn")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	models, err := provider.CodexModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selected := models[0]
	for _, model := range models {
		if model.Default {
			selected = model
			break
		}
	}
	prepared, err := (provider.Codex{Model: selected.ID, Effort: selected.DefaultEffort}).Prepare(provider.Request{Messages: []provider.Message{
		{Role: "system", Content: "You are a model-only transport. Return exactly: bridge-ready"},
		{Role: "user", Content: "Respond now."},
	}})
	if err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	stop, err := prepared.Execute(ctx, sink)
	if err != nil {
		t.Fatal(err)
	}
	if stop.Message.Content == "" {
		t.Fatal("Codex returned no streamed assistant text")
	}
	for _, event := range sink.events {
		if event.NativeKind == "item/started" && (containsJSON(event.Payload, `"commandExecution"`) || containsJSON(event.Payload, `"fileChange"`) || containsJSON(event.Payload, `"mcpToolCall"`)) {
			t.Fatalf("native tool escaped model-only bridge: %s", event.Payload)
		}
	}
	if !containsCodexFullAccessSetting(sink.events) {
		t.Fatalf("Codex did not report danger-full-access for the bridge thread: %#v", sink.events)
	}
}

func containsJSON(raw []byte, text string) bool { return strings.Contains(string(raw), text) }

func containsCodexFullAccessSetting(events []provider.Event) bool {
	for _, event := range events {
		if event.NativeKind == "thread/settings/updated" && containsJSON(event.Payload, `"type":"dangerFullAccess"`) {
			return true
		}
	}
	return false
}
