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
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const CodexReplayAdapter = "codex.app-server.v1"

const (
	codexStartupTimeout    = 30 * time.Second
	codexFirstEventTimeout = 45 * time.Second
)

const codexBridgeInstructions = "Return only the requested Midgard protocol text. Native Codex tools are deliberately unavailable because Midgard executes repository work through the protocol in the base instructions. Do not decline repository work or say that tools or modifications are disabled. For every inspection, edit, check, shell, or browser operation, emit the corresponding Midgard protocol tool entity; Midgard will validate and execute it. Emit the protocol once as one complete assistant message. Prefer the final answer; if Codex uses commentary, Midgard treats that completed message as the response and ignores a mirrored final copy."

// Codex uses the installed Codex app-server as an authenticated, model-only
// transport. Midgard disables native execution and integrations when starting
// the server and fails the call if any native tool item is nevertheless seen.
type Codex struct {
	Executable string
	Model      string
	Effort     string
}

type codexPreparedCall struct {
	client  Codex
	model   string
	effort  string
	request Event
	params  codexThreadParams
}

type codexThreadParams struct {
	BaseInstructions string
	Input            string
}

func (c Codex) Prepare(request Request) (PreparedCall, error) {
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = strings.TrimSpace(c.Model)
	}
	if model == "" {
		return nil, errors.New("Codex model is required")
	}
	effort := strings.TrimSpace(request.Effort)
	if effort == "" {
		effort = strings.TrimSpace(c.Effort)
	}
	var system, transcript strings.Builder
	for _, message := range request.Messages {
		if len(message.ToolCalls) > 0 {
			return nil, errors.New("provider-native tool calls cannot be sent as model context")
		}
		if message.Role == "system" {
			if system.Len() > 0 {
				system.WriteString("\n\n")
			}
			system.WriteString(message.Content)
			continue
		}
		if message.Role != "user" && message.Role != "assistant" {
			return nil, fmt.Errorf("invalid provider message role %q", message.Role)
		}
		fmt.Fprintf(&transcript, "<%s>\n%s\n</%s>\n", message.Role, message.Content, message.Role)
	}
	params := codexThreadParams{BaseInstructions: system.String(), Input: transcript.String()}
	raw, err := json.Marshal(struct {
		Model  string `json:"model"`
		Effort string `json:"effort,omitempty"`
		Input  string `json:"input"`
	}{model, effort, params.Input})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return &codexPreparedCall{client: c, model: model, effort: effort, params: params, request: Event{
		NativeKind: "codex.turn.request", NativeID: "sha256:" + hex.EncodeToString(digest[:]), Sequence: 1, Payload: raw,
	}}, nil
}

func (c *codexPreparedCall) RequestEvent() Event {
	return Event{NativeKind: c.request.NativeKind, NativeID: c.request.NativeID, Sequence: c.request.Sequence, Payload: append(json.RawMessage(nil), c.request.Payload...)}
}

type rpcEnvelope struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func (c *codexPreparedCall) Execute(ctx context.Context, sink EventSink) (Stop, error) {
	server, err := startCodexServer(ctx, c.client.Executable)
	if err != nil {
		return Stop{}, err
	}
	defer server.close()
	if _, err := server.callWithin(ctx, codexStartupTimeout, 1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "midgard", "title": "Midgard", "version": "2"}, "capabilities": map[string]any{"experimentalApi": true}}, nil); err != nil {
		return Stop{}, fmt.Errorf("initialize local Codex bridge: %w", err)
	}
	if err := server.writeWithin(ctx, codexStartupTimeout, "initialization acknowledgement", map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return Stop{}, err
	}
	temporary, err := os.MkdirTemp("", "midgard-codex-model-")
	if err != nil {
		return Stop{}, err
	}
	defer os.RemoveAll(temporary)
	threadResult, err := server.callWithin(ctx, codexStartupTimeout, 2, "thread/start", c.threadStartParams(temporary), nil)
	if err != nil {
		return Stop{}, fmt.Errorf("start local Codex model thread: %w", err)
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(threadResult, &started) != nil || started.Thread.ID == "" {
		return Stop{}, errors.New("local Codex bridge returned no thread id")
	}
	turnParams := map[string]any{"threadId": started.Thread.ID, "input": []map[string]any{{"type": "text", "text": c.params.Input}}, "model": c.model}
	if c.effort != "" && c.effort != "standard" {
		turnParams["effort"] = c.effort
	}
	if err := server.writeWithin(ctx, codexStartupTimeout, "model turn request", map[string]any{"id": 3, "method": "turn/start", "params": turnParams}); err != nil {
		return Stop{}, err
	}
	return c.readTurn(ctx, server, sink)
}

func (c *codexPreparedCall) threadStartParams(cwd string) map[string]any {
	return map[string]any{
		"model": c.model, "baseInstructions": c.params.BaseInstructions,
		"developerInstructions": codexBridgeInstructions,
		"cwd":                   cwd, "runtimeWorkspaceRoots": []string{}, "dynamicTools": []any{}, "environments": []any{},
		"approvalPolicy": "never", "sandbox": "danger-full-access", "ephemeral": true,
		"config": map[string]any{"mcp_servers": map[string]any{}},
	}
}

func (c *codexPreparedCall) readTurn(ctx context.Context, server *codexServer, sink EventSink) (Stop, error) {
	sequence := int64(1)
	output := newCodexOutputCollector()
	stop := Stop{Model: c.model}
	firstEventContext, cancelFirstEvent := context.WithTimeout(ctx, codexFirstEventTimeout)
	defer cancelFirstEvent()
	seenEvent := false
	for {
		readContext := ctx
		if !seenEvent {
			readContext = firstEventContext
		}
		raw, err := server.read(readContext)
		if err != nil {
			if !seenEvent && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return Stop{}, fmt.Errorf("local Codex bridge did not begin the model turn within %s: %w", codexFirstEventTimeout, err)
			}
			return Stop{}, err
		}
		seenEvent = true
		var envelope rpcEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return Stop{}, fmt.Errorf("decode local Codex event: %w", err)
		}
		sequence++
		if sink != nil {
			if err := sink.Emit(Event{NativeKind: valueOrCodex(envelope.Method, "rpc.response"), Sequence: sequence, Payload: append(json.RawMessage(nil), raw...)}); err != nil {
				return Stop{}, err
			}
		}
		if len(envelope.ID) > 0 && envelope.Method != "" {
			return Stop{}, fmt.Errorf("local Codex requested %s; the model-only bridge does not permit host callbacks", envelope.Method)
		}
		switch envelope.Method {
		case "item/started":
			itemType := output.Start(envelope.Params)
			if itemType != "" && itemType != "userMessage" && itemType != "agentMessage" && itemType != "reasoning" {
				return Stop{}, fmt.Errorf("local Codex attempted native %s; Midgard stopped the provider call before accepting output", itemType)
			}
		case "item/agentMessage/delta":
			if delta, accepted := output.Delta(envelope.Params); accepted {
				emitCodexLive(sink, LiveOutput, delta)
			}
		case "item/completed":
			if output.CompletedCommentary(envelope.Params) {
				stop.Reason = "stop"
				stop.Message = Message{Role: "assistant", Content: output.Content()}
				return stop, nil
			}
		case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
			var event struct {
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal(envelope.Params, &event)
			emitCodexLive(sink, LiveThinking, event.Delta)
		case "thread/tokenUsage/updated":
			var event struct {
				TokenUsage struct {
					Last struct {
						Input     int64 `json:"inputTokens"`
						Cached    int64 `json:"cachedInputTokens"`
						Output    int64 `json:"outputTokens"`
						Reasoning int64 `json:"reasoningOutputTokens"`
					} `json:"last"`
				} `json:"tokenUsage"`
			}
			_ = json.Unmarshal(envelope.Params, &event)
			stop.InputTokens, stop.CacheHitInputTokens = event.TokenUsage.Last.Input, event.TokenUsage.Last.Cached
			stop.CacheMissInputTokens = max(0, stop.InputTokens-stop.CacheHitInputTokens)
			stop.OutputTokens, stop.ThinkingTokens = event.TokenUsage.Last.Output, event.TokenUsage.Last.Reasoning
		case "turn/completed":
			var event struct {
				Turn struct {
					Status string `json:"status"`
					Error  any    `json:"error"`
				} `json:"turn"`
			}
			_ = json.Unmarshal(envelope.Params, &event)
			if event.Turn.Status != "completed" {
				return Stop{}, fmt.Errorf("local Codex turn ended with status %s", valueOrCodex(event.Turn.Status, "unknown"))
			}
			stop.Reason = "stop"
			stop.Message = Message{Role: "assistant", Content: output.Content()}
			return stop, nil
		}
	}
}

// codexOutputCollector admits one complete assistant message into Midgard's
// protocol stream. Codex app-server can emit a protocol message in commentary,
// then wait indefinitely for a native tool result that this model-only bridge
// deliberately cannot provide. Once that commentary message completes, it is a
// safe response boundary: Midgard validates it before any host effect. If
// Codex mirrors it as a final answer, the later copy is ignored. Unphased
// messages remain a compatibility fallback for older servers.
type codexOutputCollector struct {
	phases     map[string]string
	commentary strings.Builder
	final      strings.Builder
	fallback   strings.Builder
}

func newCodexOutputCollector() *codexOutputCollector {
	return &codexOutputCollector{phases: make(map[string]string)}
}

func (c *codexOutputCollector) Start(params json.RawMessage) string {
	var event struct {
		Item struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			Phase string `json:"phase"`
		} `json:"item"`
	}
	if json.Unmarshal(params, &event) != nil {
		return ""
	}
	if event.Item.Type == "agentMessage" && event.Item.ID != "" {
		c.phases[event.Item.ID] = strings.ToLower(strings.TrimSpace(event.Item.Phase))
	}
	return event.Item.Type
}

func (c *codexOutputCollector) Delta(params json.RawMessage) (string, bool) {
	var event struct {
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if json.Unmarshal(params, &event) != nil || event.Delta == "" {
		return "", false
	}
	switch c.phases[event.ItemID] {
	case "final_answer":
		c.final.WriteString(event.Delta)
		if c.commentary.Len() > 0 {
			return "", false
		}
		return event.Delta, true
	case "commentary":
		c.commentary.WriteString(event.Delta)
		return event.Delta, true
	case "analysis":
		return "", false
	default:
		c.fallback.WriteString(event.Delta)
		return "", false
	}
}

func (c *codexOutputCollector) CompletedCommentary(params json.RawMessage) bool {
	var event struct {
		Item struct {
			Type  string `json:"type"`
			Phase string `json:"phase"`
		} `json:"item"`
	}
	if json.Unmarshal(params, &event) != nil {
		return false
	}
	return event.Item.Type == "agentMessage" && strings.EqualFold(strings.TrimSpace(event.Item.Phase), "commentary") && c.commentary.Len() > 0
}

func (c *codexOutputCollector) Content() string {
	if c.commentary.Len() > 0 {
		return c.commentary.String()
	}
	if c.final.Len() > 0 {
		return c.final.String()
	}
	return c.fallback.String()
}

func emitCodexLive(sink EventSink, kind LiveKind, delta string) {
	if delta == "" {
		return
	}
	if live, ok := sink.(LiveSink); ok {
		live.EmitLive(LiveUpdate{Kind: kind, Delta: delta})
	}
}

type codexServer struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	scan   *bufio.Scanner
	stderr bytes.Buffer
	mu     sync.Mutex
}

func startCodexServer(ctx context.Context, executable string) (*codexServer, error) {
	if executable == "" {
		executable = "codex"
	}
	args := []string{"app-server", "--stdio", "-c", "mcp_servers={}", "--disable", "apps", "--disable", "plugins",
		"--disable", "shell_tool", "--disable", "unified_exec", "--disable", "code_mode_host",
		"--disable", "web_search_request", "--disable", "browser_use", "--disable", "computer_use", "--disable", "image_generation"}
	cmd := exec.CommandContext(ctx, executable, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	server := &codexServer{cmd: cmd, stdin: stdin}
	cmd.Stderr = &limitedWriter{writer: &server.stderr, remaining: 32 << 10}
	server.scan = bufio.NewScanner(stdout)
	server.scan.Buffer(make([]byte, 64<<10), 4<<20)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start local Codex app-server: %w", err)
	}
	return server, nil
}

func (s *codexServer) write(ctx context.Context, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, err := s.stdin.Write(append(raw, '\n'))
		done <- result{err: err}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-done:
		return result.err
	}
}

func (s *codexServer) read(ctx context.Context) (json.RawMessage, error) {
	type result struct {
		raw json.RawMessage
		err error
	}
	done := make(chan result, 1)
	go func() {
		if s.scan.Scan() {
			done <- result{raw: append(json.RawMessage(nil), s.scan.Bytes()...)}
			return
		}
		err := s.scan.Err()
		if err == nil {
			err = io.EOF
		}
		done <- result{err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case item := <-done:
		if item.err != nil && s.stderr.Len() > 0 {
			return nil, fmt.Errorf("local Codex app-server stopped: %s", strings.TrimSpace(s.stderr.String()))
		}
		return item.raw, item.err
	}
}

func (s *codexServer) call(ctx context.Context, id int, method string, params any, observe func(json.RawMessage) error) (json.RawMessage, error) {
	if err := s.write(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		raw, err := s.read(ctx)
		if err != nil {
			return nil, err
		}
		var envelope rpcEnvelope
		if json.Unmarshal(raw, &envelope) != nil {
			continue
		}
		if observe != nil {
			if err := observe(raw); err != nil {
				return nil, err
			}
		}
		if string(envelope.ID) != fmt.Sprint(id) {
			continue
		}
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			return nil, fmt.Errorf("%s", envelope.Error)
		}
		return envelope.Result, nil
	}
}

func (s *codexServer) writeWithin(ctx context.Context, timeout time.Duration, operation string, value any) error {
	writeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := s.write(writeContext, value)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return fmt.Errorf("local Codex bridge did not accept the %s within %s: %w", operation, timeout, err)
	}
	return err
}

// callWithin prevents a stalled local app-server handshake from holding a TUI
// turn in its waiting state forever. Once a turn has started, its broader
// policy budget still governs the model's actual work.
func (s *codexServer) callWithin(ctx context.Context, timeout time.Duration, id int, method string, params any, observe func(json.RawMessage) error) (json.RawMessage, error) {
	callContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := s.call(callContext, id, method, params, observe)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return nil, fmt.Errorf("local Codex bridge did not respond to %s within %s: %w", method, timeout, err)
	}
	return result, err
}

func (s *codexServer) close() {
	_ = s.stdin.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.remaining <= 0 {
		return n, nil
	}
	keep := min(len(p), w.remaining)
	_, err := w.writer.Write(p[:keep])
	w.remaining -= keep
	return n, err
}

func CodexModels(ctx context.Context) ([]ModelDefinition, error) {
	server, err := startCodexServer(ctx, "")
	if err != nil {
		return nil, err
	}
	defer server.close()
	if _, err := server.callWithin(ctx, codexStartupTimeout, 1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "midgard", "title": "Midgard", "version": "2"}, "capabilities": map[string]any{"experimentalApi": true}}, nil); err != nil {
		return nil, err
	}
	if err := server.writeWithin(ctx, codexStartupTimeout, "initialization acknowledgement", map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	raw, err := server.callWithin(ctx, codexStartupTimeout, 2, "model/list", map[string]any{"includeHidden": false}, nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data []struct {
			ID            string `json:"id"`
			DisplayName   string `json:"displayName"`
			Description   string `json:"description"`
			DefaultEffort string `json:"defaultReasoningEffort"`
			IsDefault     bool   `json:"isDefault"`
			Supported     []struct {
				Effort string `json:"reasoningEffort"`
			} `json:"supportedReasoningEfforts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	models := make([]ModelDefinition, 0, len(response.Data))
	for _, item := range response.Data {
		efforts := make([]string, 0, len(item.Supported))
		for _, effort := range item.Supported {
			efforts = append(efforts, effort.Effort)
		}
		models = append(models, ModelDefinition{ID: item.ID, DisplayName: item.DisplayName, Description: item.Description, Efforts: efforts, DefaultEffort: item.DefaultEffort, Default: item.IsDefault})
	}
	return models, nil
}

func CodexAuthStatus(ctx context.Context) (bool, string, error) {
	command := exec.CommandContext(ctx, "codex", "login", "status")
	raw, err := command.CombinedOutput()
	message := strings.TrimSpace(string(raw))
	if err != nil {
		return false, message, nil
	}
	return true, message, nil
}

func valueOrCodex(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
