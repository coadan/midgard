package provider

import (
	"context"
	"encoding/json"
)

type Event struct {
	NativeKind string          `json:"native_kind"`
	NativeID   string          `json:"native_id,omitempty"`
	Sequence   int64           `json:"sequence"`
	Payload    json.RawMessage `json:"payload"`
}

func (e Event) Validate() error {
	if e.NativeKind == "" {
		return &ValidationError{"native kind is required"}
	}
	if e.Sequence < 1 {
		return &ValidationError{"native sequence must be positive"}
	}
	if !json.Valid(e.Payload) {
		return &ValidationError{"native payload must be valid JSON"}
	}
	return nil
}

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

type Request struct {
	Model    string
	Effort   string
	Messages []Message
}

type Message struct {
	Role        string       `json:"role"`
	Content     string       `json:"content,omitempty"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ReplayState *ReplayState `json:"-"`
}

// ReplayState is adapter-owned continuation data. The kernel preserves its
// immutable artifact reference but never interprets the opaque payload.
type ReplayState struct {
	Adapter     string          `json:"adapter"`
	ArtifactRef string          `json:"artifact_ref,omitempty"`
	Payload     json.RawMessage `json:"-"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Stop struct {
	Reason               string
	Model                string
	InputTokens          int64
	CacheHitInputTokens  int64
	CacheMissInputTokens int64
	OutputTokens         int64
	ThinkingTokens       int64
	Message              Message
}

type EventSink interface{ Emit(Event) error }

type LiveKind string

const (
	LiveOutput   LiveKind = "output"
	LiveThinking LiveKind = "thinking"
)

// LiveUpdate is an ephemeral, user-visible projection of a native streaming
// observation. The provider trace remains the durable source of truth.
type LiveUpdate struct {
	Kind  LiveKind
	Delta string
}

// LiveSink is optional. Provider adapters emit through it when the supplied
// EventSink also supports live UI updates.
type LiveSink interface{ EmitLive(LiveUpdate) }

// PreparedCall binds the exact native request observation to one provider
// execution. RequestEvent must be side-effect free: callers persist it before
// Execute is allowed to perform provider I/O.
type PreparedCall interface {
	RequestEvent() Event
	Execute(context.Context, EventSink) (Stop, error)
}

type Provider interface {
	Prepare(Request) (PreparedCall, error)
}
