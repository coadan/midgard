package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"midgard/internal/model"
)

func TestClientStreamsAnthropicCompatibleMessages(t *testing.T) {
	var request messagesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anthropic/v1/messages" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization header = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Fatal("anthropic-version header missing")
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"@report plan.mdx\\n\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"@result status:ready artifact:plan.mdx\\n\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL + "/anthropic", APIKey: "test-key", ReasoningEffort: MaxReasoningEffort, HTTPClient: server.Client()}
	packet := model.Packet{
		Role:            model.RolePlanner,
		ModelID:         "deepseek-v4-flash",
		System:          "system instructions",
		Context:         "task context",
		MaxOutputTokens: 123,
	}
	var raw strings.Builder
	usage, err := client.Stream(context.Background(), packet, func(delta model.Delta) error {
		raw.WriteString(delta.Text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if raw.String() != "@report plan.mdx\n@result status:ready artifact:plan.mdx\n" {
		t.Fatalf("raw stream = %q", raw.String())
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 7 {
		t.Fatalf("usage = %#v", usage)
	}
	if request.Model != "deepseek-v4-flash" || request.MaxTokens != 123 || !request.Stream {
		t.Fatalf("request = %#v", request)
	}
	if request.OutputConfig == nil || request.OutputConfig.Effort != MaxReasoningEffort {
		t.Fatalf("output_config = %#v", request.OutputConfig)
	}
	if request.System != "system instructions" {
		t.Fatalf("system field = %q", request.System)
	}
	if len(request.Messages) != 1 || request.Messages[0].Role != "user" {
		t.Fatalf("messages = %#v", request.Messages)
	}
}

func TestClientRequiresAPIKey(t *testing.T) {
	client := &Client{BaseURL: "http://127.0.0.1"}
	_, err := client.Stream(context.Background(), model.Packet{}, func(model.Delta) error { return nil })
	if err == nil {
		t.Fatal("missing API key accepted")
	}
}

func TestClientRejectsInvalidReasoningEffort(t *testing.T) {
	client := &Client{BaseURL: "http://127.0.0.1", APIKey: "test-key", ReasoningEffort: "extreme"}
	_, err := client.Stream(context.Background(), model.Packet{}, func(model.Delta) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "reasoning effort") {
		t.Fatalf("error = %v, want reasoning effort validation", err)
	}
}
