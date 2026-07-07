package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"midgard/internal/model"
)

func TestClientStreamsCodexResponses(t *testing.T) {
	var request responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization header = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("accept header = %q", got)
		}
		if got := r.Header.Get("Originator"); got == "" {
			t.Fatal("originator header missing")
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"@report plan.mdx\\n\"}\n\n"))
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"@result status:ready artifact:plan.mdx checks:none\\n\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":12,\"output_tokens\":8,\"total_tokens\":20}}}\n\n"))
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL + "/backend-api/codex", AuthToken: "test-token", HTTPClient: server.Client()}
	packet := model.Packet{
		TaskID:  "task_1",
		Role:    model.RolePlanner,
		ModelID: "gpt-test",
		System:  "system instructions",
		Context: "task context",
	}
	var raw strings.Builder
	usage, err := client.Stream(context.Background(), packet, func(delta model.Delta) error {
		raw.WriteString(delta.Text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if raw.String() != "@report plan.mdx\n@result status:ready artifact:plan.mdx checks:none\n" {
		t.Fatalf("raw stream = %q", raw.String())
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 8 {
		t.Fatalf("usage = %#v", usage)
	}
	if request.Model != "gpt-test" || request.Instructions != "system instructions" || !request.Stream || request.Store {
		t.Fatalf("request = %#v", request)
	}
	if len(request.Input) != 1 || request.Input[0].Role != "user" || request.Input[0].Content[0].Text != "task context" {
		t.Fatalf("input = %#v", request.Input)
	}
	if request.ClientMetadata["harness"] != "midgard" || request.ClientMetadata["role"] != "planner" {
		t.Fatalf("client metadata = %#v", request.ClientMetadata)
	}
}

func TestClientDefaultsModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != DefaultModel {
			t.Fatalf("model = %q, want %q", request.Model, DefaultModel)
		}
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"))
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL, AuthToken: "test-token", HTTPClient: server.Client()}
	_, err := client.Stream(context.Background(), model.Packet{}, func(model.Delta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
}

func TestClientReturnsResponseFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"usage limit\"}}}\n\n"))
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL, AuthToken: "test-token", HTTPClient: server.Client()}
	_, err := client.Stream(context.Background(), model.Packet{}, func(model.Delta) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadResponsesSSEAcceptsCodexItemCompletedEvents(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"thread.started","thread_id":"thread_1"}`,
		"",
		`data: {"type":"turn.started"}`,
		"",
		`data: {"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"@report plan.mdx\n@result status:ready artifact:plan.mdx checks:none\n"}}`,
		"",
		`data: {"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":6,"reasoning_output_tokens":2}}`,
		"",
	}, "\n")
	var out strings.Builder
	usage, err := readResponsesSSE(strings.NewReader(raw), func(delta model.Delta) error {
		out.WriteString(delta.Text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "@report plan.mdx\n@result status:ready artifact:plan.mdx checks:none\n" {
		t.Fatalf("out = %q", out.String())
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 6 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestReadResponsesSSEDoesNotDuplicateCodexFinalItemAfterDeltas(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"agent_message_content_delta","delta":"@report plan.mdx\n"}`,
		"",
		`data: {"type":"agent_message_content_delta","delta":"@result status:ready artifact:plan.mdx checks:none\n"}`,
		"",
		`data: {"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"@report plan.mdx\n@result status:ready artifact:plan.mdx checks:none\n"}}`,
		"",
		`data: {"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}`,
		"",
	}, "\n")
	var out strings.Builder
	usage, err := readResponsesSSE(strings.NewReader(raw), func(delta model.Delta) error {
		out.WriteString(delta.Text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "@report plan.mdx\n@result status:ready artifact:plan.mdx checks:none\n" {
		t.Fatalf("out = %q", out.String())
	}
	if usage.InputTokens != 1 || usage.OutputTokens != 2 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestReadResponsesSSEReturnsCodexTurnFailure(t *testing.T) {
	raw := "data: {\"type\":\"turn.failed\",\"error\":{\"message\":\"bad request\"}}\n\n"
	_, err := readResponsesSSE(strings.NewReader(raw), func(model.Delta) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("err = %v", err)
	}
}
