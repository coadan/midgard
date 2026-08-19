package provider_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"midgard/internal/provider"
)

type captureSink struct {
	events  []provider.Event
	updates []provider.LiveUpdate
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func (s *captureSink) Emit(event provider.Event) error {
	s.events = append(s.events, event)
	return nil
}

func (s *captureSink) EmitLive(update provider.LiveUpdate) {
	s.updates = append(s.updates, update)
}

func TestDeepSeekPreservesNativeResponseAndThinkingToolContext(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["model"] != "deepseek-v4-pro" {
			t.Errorf("model = %#v", request["model"])
		}
		if request["stream"] != true || request["stream_options"].(map[string]any)["include_usage"] != true {
			t.Errorf("stream settings = %#v, %#v", request["stream"], request["stream_options"])
		}
		messages := request["messages"].([]any)
		assistant := messages[1].(map[string]any)
		if assistant["reasoning_content"] != "need the tool" {
			t.Errorf("reasoning content = %#v", assistant["reasoning_content"])
		}
		stream := `data: {"id":"response-1","model":"deepseek-v4-pro","choices":[{"index":0,"finish_reason":null,"delta":{"role":"assistant","reasoning_content":"I should inspect "}}]}

data: {"id":"response-1","model":"deepseek-v4-pro","choices":[{"index":0,"finish_reason":null,"delta":{"reasoning_content":"the file."}}]}

data: {"id":"response-1","model":"deepseek-v4-pro","choices":[{"index":0,"finish_reason":null,"delta":{"content":"Inspecting."}}]}

data: {"id":"response-1","model":"deepseek-v4-pro","choices":[{"index":0,"finish_reason":null,"delta":{"tool_calls":[{"index":0,"id":"call-2","type":"function","function":{"name":"file_inspect","arguments":"{\"path\":"}}]}}]}

data: {"id":"response-1","model":"deepseek-v4-pro","choices":[{"index":0,"finish_reason":null,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"\"README.md\"}"}}]}}]}

data: {"id":"response-1","model":"deepseek-v4-pro","choices":[{"index":0,"finish_reason":"tool_calls","delta":{}}]}

data: {"id":"response-1","model":"deepseek-v4-pro","choices":[],"usage":{"prompt_tokens":12,"prompt_cache_hit_tokens":7,"prompt_cache_miss_tokens":5,"completion_tokens":8,"completion_tokens_details":{"reasoning_tokens":3}}}

data: [DONE]

`
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}

	sink := &captureSink{}
	client := provider.DeepSeek{APIKey: "test-key", BaseURL: "https://example.invalid", HTTPClient: httpClient, ThinkingEnabled: true}
	prepared, err := client.Prepare(provider.Request{
		Messages: []provider.Message{
			{Role: "user", Content: "fix it"},
			{Role: "assistant", Content: "+ @t1 tool\n+ @t1.name \"shell\"\n+ @t1.arguments.command \"pwd\"\n+ @t1.reason \"inspect\"\n! @t1\n", ReplayState: &provider.ReplayState{Adapter: provider.DeepSeekReplayAdapter, Payload: json.RawMessage(`{"reasoning_content":"need the tool"}`)}},
			{Role: "user", Content: "Midgard host result for committed tool @t1: exit 0"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestEvent := prepared.RequestEvent()
	if strings.Contains(string(requestEvent.Payload), "test-key") {
		t.Fatal("request trace contains the API key")
	}
	if strings.Contains(string(requestEvent.Payload), `"tools"`) {
		t.Fatalf("provider-native tools were advertised: %s", requestEvent.Payload)
	}
	stop, err := prepared.Execute(context.Background(), sink)
	if err != nil {
		t.Fatal(err)
	}
	if stop.Reason != "tool_calls" || stop.Model != "deepseek-v4-pro" || stop.InputTokens != 12 || stop.CacheHitInputTokens != 7 || stop.CacheMissInputTokens != 5 || stop.OutputTokens != 8 || stop.ThinkingTokens != 3 || len(stop.Message.ToolCalls) != 1 || stop.Message.ToolCalls[0].Name != "file_inspect" {
		t.Fatalf("stop = %#v", stop)
	}
	if stop.Message.ReplayState == nil || stop.Message.ReplayState.Adapter != provider.DeepSeekReplayAdapter || !strings.Contains(string(stop.Message.ReplayState.Payload), "I should inspect the file.") {
		t.Fatalf("replay state = %#v", stop.Message.ReplayState)
	}
	if requestEvent.NativeKind != "chat.completion.request" || requestEvent.Sequence != 1 || len(sink.events) != 8 || sink.events[0].NativeKind != "chat.completion.chunk" || sink.events[0].NativeID != "response-1" || sink.events[0].Sequence != 2 || sink.events[7].NativeKind != "chat.completion.stream.completed" || sink.events[7].Sequence != 9 {
		t.Fatalf("events = %#v", sink.events)
	}
	if len(sink.updates) != 3 || sink.updates[0].Kind != provider.LiveThinking || sink.updates[2].Kind != provider.LiveOutput || sink.updates[0].Delta+sink.updates[1].Delta != "I should inspect the file." || sink.updates[2].Delta != "Inspecting." {
		t.Fatalf("live updates = %#v", sink.updates)
	}
	digest := sha256.Sum256(requestEvent.Payload)
	if requestEvent.NativeID != "sha256:"+hex.EncodeToString(digest[:]) {
		t.Fatalf("request id = %q", requestEvent.NativeID)
	}
}

func TestDeepSeekRejectsStreamWithoutCompletionMarker(t *testing.T) {
	client := provider.DeepSeek{APIKey: "test-key", BaseURL: "https://example.invalid", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"id\":\"response-1\",\"choices\":[{\"index\":0,\"finish_reason\":null,\"delta\":{\"content\":\"partial\"}}]}\n\n"))}, nil
	})}}
	prepared, err := client.Prepare(provider.Request{Messages: []provider.Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepared.Execute(context.Background(), &captureSink{}); err == nil || !strings.Contains(err.Error(), "completion marker") {
		t.Fatalf("stream error = %v", err)
	}
}

func TestDeepSeekRequiresKey(t *testing.T) {
	_, err := (provider.DeepSeek{}).Prepare(provider.Request{})
	if err == nil {
		t.Fatal("missing key accepted")
	}
}
