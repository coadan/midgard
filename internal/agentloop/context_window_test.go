package agentloop

import (
	"encoding/json"
	"strings"
	"testing"

	"midgard/internal/policy"
	modelprotocol "midgard/internal/protocol"
	"midgard/internal/provider"
)

func TestContextWindowUsesExactLastRequestAndConservativeDelta(t *testing.T) {
	messages := []provider.Message{{Role: "system", Content: "required instructions"}}
	window := newContextWindow(messages, "finish the change")
	window.recordRequest(1, 1_000)
	messages = append(messages, provider.Message{Role: "user", Content: strings.Repeat("x", 200)})
	want := int64(1_000) + estimateProviderMessages(messages[1:])
	if got := window.estimate(messages); got != want {
		t.Fatalf("estimate = %d, want %d", got, want)
	}
}

func TestContextWindowCompactsRawHistoryIntoActionCheckpoint(t *testing.T) {
	base := []provider.Message{{Role: "system", Content: "required instructions"}, {Role: "user", Content: "implement it"}}
	window := newContextWindow(base, "implement bounded context")
	window.recordAction(modelprotocol.HostAction{EntityID: "@read", Name: "file_inspect", Reason: "inspect source", Arguments: json.RawMessage(`{"path":"large.go"}`)}, json.RawMessage(`{"exit_code":0,"sha256":"abc"}`))
	messages := append(append([]provider.Message{}, base...),
		provider.Message{Role: "assistant", Content: strings.Repeat("protocol draft ", 600)},
		provider.Message{Role: "user", Content: strings.Repeat("raw file content ", 600)},
		provider.Message{Role: "assistant", Content: "latest exact message"})
	compacted, result, err := window.compact(messages, policy.ContextBudget{LimitTokens: 20_000, CompactAtTokens: 100, TargetTokens: 3_000})
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, message := range compacted {
		joined += message.Content
	}
	if result.Removed == 0 || !strings.Contains(joined, "MIDGARD CONTEXT CHECKPOINT") || !strings.Contains(joined, "read large.go at abc") || strings.Contains(joined, strings.Repeat("raw file content ", 50)) {
		t.Fatalf("compaction = %#v, messages=%q", result, joined)
	}
	if compacted[0].Role != base[0].Role || compacted[0].Content != base[0].Content || !strings.Contains(joined, "implement it") {
		t.Fatalf("pinned instructions or conversation excerpt was not preserved: %#v", compacted)
	}
}

func TestContextWindowRejectsRequiredBaseAboveQualityLimit(t *testing.T) {
	base := []provider.Message{{Role: "system", Content: strings.Repeat("required ", 500)}}
	window := newContextWindow(base, "continue")
	messages := append(base, provider.Message{Role: "user", Content: "new observation"})
	_, _, err := window.compact(messages, policy.ContextBudget{LimitTokens: 100, CompactAtTokens: 1, TargetTokens: 50})
	if err == nil || !strings.Contains(err.Error(), "quality limit") {
		t.Fatalf("expected quality limit error, got %v", err)
	}
}
