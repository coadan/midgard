package provider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	DefaultDeepSeekBaseURL = "https://api.deepseek.com"
	DefaultDeepSeekModel   = "deepseek-v4-pro"
	maxDeepSeekResponse    = 32 << 20
)

// DeepSeek implements the OpenAI-compatible DeepSeek Chat Completions API. It
// preserves every native SSE chunk while projecting ephemeral text and
// reasoning deltas to an optional live client.
type DeepSeek struct {
	APIKey          string
	BaseURL         string
	Model           string
	HTTPClient      *http.Client
	ThinkingEnabled bool
	MaxTokens       int
}

type deepSeekRequest struct {
	Model           string            `json:"model"`
	Messages        []deepSeekMessage `json:"messages"`
	Thinking        *thinking         `json:"thinking,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	Stream          bool              `json:"stream"`
	StreamOptions   *streamOptions    `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type thinking struct {
	Type string `json:"type"`
}

type deepSeekMessage struct {
	Role             string             `json:"role"`
	Content          *string            `json:"content"`
	ReasoningContent string             `json:"reasoning_content,omitempty"`
	ToolCalls        []deepSeekToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string             `json:"tool_call_id,omitempty"`
}

type deepSeekToolCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type deepSeekResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string          `json:"finish_reason"`
		Message      deepSeekMessage `json:"message"`
	} `json:"choices"`
	Usage deepSeekUsage `json:"usage"`
}

type deepSeekUsage struct {
	PromptTokens          int64 `json:"prompt_tokens"`
	PromptCacheHitTokens  int64 `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int64 `json:"prompt_cache_miss_tokens"`
	CompletionTokens      int64 `json:"completion_tokens"`
	CompletionDetails     struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type deepSeekChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int             `json:"index"`
		FinishReason string          `json:"finish_reason"`
		Delta        deepSeekMessage `json:"delta"`
	} `json:"choices"`
	Usage *deepSeekUsage `json:"usage"`
}

const DeepSeekReplayAdapter = "deepseek.chat-completions.v1"

type deepSeekReplayState struct {
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type deepSeekPreparedCall struct {
	client  DeepSeek
	model   string
	request Event
	body    []byte
}

func (d DeepSeek) Prepare(request Request) (PreparedCall, error) {
	if strings.TrimSpace(d.APIKey) == "" {
		return nil, errors.New("DeepSeek API key is required")
	}
	model := d.Model
	if model == "" {
		model = DefaultDeepSeekModel
	}
	body := deepSeekRequest{Model: model, MaxTokens: d.MaxTokens, Stream: true, StreamOptions: &streamOptions{IncludeUsage: true}}
	if body.MaxTokens <= 0 {
		body.MaxTokens = 16_384
	}
	if d.ThinkingEnabled {
		body.Thinking = &thinking{Type: "enabled"}
		body.ReasoningEffort = "high"
	} else {
		body.Thinking = &thinking{Type: "disabled"}
	}
	for _, message := range request.Messages {
		mapped, err := mapDeepSeekMessage(message)
		if err != nil {
			return nil, err
		}
		body.Messages = append(body.Messages, mapped)
	}
	rawRequest, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(rawRequest)
	return &deepSeekPreparedCall{client: d, model: model, body: rawRequest, request: Event{
		NativeKind: "chat.completion.request", NativeID: "sha256:" + hex.EncodeToString(digest[:]), Sequence: 1, Payload: rawRequest,
	}}, nil
}

func (c *deepSeekPreparedCall) RequestEvent() Event {
	return Event{NativeKind: c.request.NativeKind, NativeID: c.request.NativeID, Sequence: c.request.Sequence, Payload: append(json.RawMessage(nil), c.request.Payload...)}
}

func (c *deepSeekPreparedCall) Execute(ctx context.Context, sink EventSink) (Stop, error) {
	baseURL := strings.TrimRight(c.client.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultDeepSeekBaseURL
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(c.body))
	if err != nil {
		return Stop{}, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.client.APIKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	client := c.client.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return Stop{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		rawResponse, err := io.ReadAll(io.LimitReader(response.Body, 4097))
		if err != nil {
			return Stop{}, err
		}
		return Stop{}, fmt.Errorf("DeepSeek API returned %s: %s", response.Status, boundedError(rawResponse))
	}
	stop, err := readDeepSeekStream(response.Body, sink)
	if stop.Model == "" {
		stop.Model = c.model
	}
	return stop, err
}

func readDeepSeekStream(body io.Reader, sink EventSink) (Stop, error) {
	limited := &io.LimitedReader{R: body, N: maxDeepSeekResponse + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64<<10), maxDeepSeekResponse)
	sequence := int64(1)
	completed := false
	responseID, model, finishReason := "", "", ""
	content, reasoning := "", ""
	usage := deepSeekUsage{}
	toolCalls := map[int]*deepSeekToolCall{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		data, found := strings.CutPrefix(line, "data:")
		if !found {
			return Stop{}, errors.New("DeepSeek stream returned an invalid SSE event")
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			completed = true
			break
		}
		raw := json.RawMessage(data)
		var chunk deepSeekChunk
		if !json.Valid(raw) || json.Unmarshal(raw, &chunk) != nil {
			return Stop{}, errors.New("DeepSeek stream returned an invalid JSON chunk")
		}
		sequence++
		if sink != nil {
			if err := sink.Emit(Event{NativeKind: "chat.completion.chunk", NativeID: chunk.ID, Sequence: sequence, Payload: append(json.RawMessage(nil), raw...)}); err != nil {
				return Stop{}, err
			}
		}
		if chunk.ID != "" {
			responseID = chunk.ID
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		if len(chunk.Choices) > 1 {
			return Stop{}, fmt.Errorf("DeepSeek stream chunk has %d choices", len(chunk.Choices))
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.Index != 0 {
			return Stop{}, fmt.Errorf("DeepSeek stream returned choice index %d", choice.Index)
		}
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
		if choice.Delta.ReasoningContent != "" {
			reasoning += choice.Delta.ReasoningContent
			emitDeepSeekLive(sink, LiveThinking, choice.Delta.ReasoningContent)
		}
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			content += *choice.Delta.Content
			emitDeepSeekLive(sink, LiveOutput, *choice.Delta.Content)
		}
		for _, delta := range choice.Delta.ToolCalls {
			call := toolCalls[delta.Index]
			if call == nil {
				call = &deepSeekToolCall{Index: delta.Index}
				toolCalls[delta.Index] = call
			}
			if delta.ID != "" {
				call.ID = delta.ID
			}
			if delta.Type != "" {
				call.Type = delta.Type
			}
			call.Function.Name += delta.Function.Name
			call.Function.Arguments += delta.Function.Arguments
		}
	}
	if err := scanner.Err(); err != nil {
		return Stop{}, fmt.Errorf("read DeepSeek stream: %w", err)
	}
	if limited.N <= 0 {
		return Stop{}, fmt.Errorf("DeepSeek response exceeds %d bytes", maxDeepSeekResponse)
	}
	if !completed {
		return Stop{}, errors.New("DeepSeek stream ended before its completion marker")
	}
	if finishReason == "" {
		return Stop{}, errors.New("DeepSeek stream completed without a finish reason")
	}
	assembled := deepSeekResponse{ID: responseID, Model: model, Usage: usage}
	assembled.Choices = append(assembled.Choices, struct {
		FinishReason string          `json:"finish_reason"`
		Message      deepSeekMessage `json:"message"`
	}{FinishReason: finishReason, Message: deepSeekMessage{Role: "assistant", Content: &content, ReasoningContent: reasoning}})
	indices := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		assembled.Choices[0].Message.ToolCalls = append(assembled.Choices[0].Message.ToolCalls, *toolCalls[index])
	}
	rawCompleted, err := json.Marshal(assembled)
	if err != nil {
		return Stop{}, err
	}
	sequence++
	if sink != nil {
		if err := sink.Emit(Event{NativeKind: "chat.completion.stream.completed", NativeID: responseID, Sequence: sequence, Payload: rawCompleted}); err != nil {
			return Stop{}, err
		}
	}
	message, err := unmapDeepSeekMessage(assembled.Choices[0].Message)
	if err != nil {
		return Stop{}, err
	}
	cacheMiss := usage.PromptCacheMissTokens
	if usage.PromptCacheHitTokens+cacheMiss == 0 {
		cacheMiss = usage.PromptTokens
	}
	return Stop{
		Reason: finishReason, Model: assembled.Model,
		InputTokens: usage.PromptTokens, CacheHitInputTokens: usage.PromptCacheHitTokens,
		CacheMissInputTokens: cacheMiss, OutputTokens: usage.CompletionTokens,
		ThinkingTokens: usage.CompletionDetails.ReasoningTokens, Message: message,
	}, nil
}

func emitDeepSeekLive(sink EventSink, kind LiveKind, delta string) {
	if delta == "" {
		return
	}
	if live, ok := sink.(LiveSink); ok {
		live.EmitLive(LiveUpdate{Kind: kind, Delta: delta})
	}
}

func mapDeepSeekMessage(message Message) (deepSeekMessage, error) {
	if message.Role != "system" && message.Role != "user" && message.Role != "assistant" {
		return deepSeekMessage{}, fmt.Errorf("invalid provider message role %q", message.Role)
	}
	if len(message.ToolCalls) > 0 {
		return deepSeekMessage{}, errors.New("provider-native tool calls cannot be sent as model context")
	}
	mapped := deepSeekMessage{Role: message.Role}
	if message.ReplayState != nil {
		if message.ReplayState.Adapter != DeepSeekReplayAdapter {
			return deepSeekMessage{}, fmt.Errorf("DeepSeek cannot read replay state owned by %q", message.ReplayState.Adapter)
		}
		if !json.Valid(message.ReplayState.Payload) {
			return deepSeekMessage{}, errors.New("DeepSeek replay state is unavailable or invalid")
		}
		var replay deepSeekReplayState
		if err := json.Unmarshal(message.ReplayState.Payload, &replay); err != nil {
			return deepSeekMessage{}, fmt.Errorf("decode DeepSeek replay state: %w", err)
		}
		mapped.ReasoningContent = replay.ReasoningContent
	}
	content := message.Content
	if message.Role != "assistant" || content != "" {
		mapped.Content = &content
	}
	return mapped, nil
}

func unmapDeepSeekMessage(message deepSeekMessage) (Message, error) {
	result := Message{Role: message.Role}
	if message.Content != nil {
		result.Content = *message.Content
	}
	if message.ReasoningContent != "" {
		payload, err := json.Marshal(deepSeekReplayState{ReasoningContent: message.ReasoningContent})
		if err != nil {
			return Message{}, err
		}
		result.ReplayState = &ReplayState{Adapter: DeepSeekReplayAdapter, Payload: payload}
	}
	for _, call := range message.ToolCalls {
		arguments := json.RawMessage(call.Function.Arguments)
		if call.ID == "" || call.Function.Name == "" || !json.Valid(arguments) {
			return Message{}, errors.New("DeepSeek returned an invalid tool call")
		}
		result.ToolCalls = append(result.ToolCalls, ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: arguments})
	}
	return result, nil
}

func boundedError(raw []byte) string {
	const limit = 4096
	if len(raw) > limit {
		raw = raw[:limit]
	}
	return strings.TrimSpace(string(raw))
}
