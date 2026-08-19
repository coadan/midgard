package provider

import (
	"encoding/json"

	"midgard/internal/eventlog"
)

// Normalize maps only semantic provider boundaries to canonical drafts. Native
// provider bodies (including unknown observations) remain solely in the raw
// trace artifact, which prevents an arbitrarily large model item from becoming
// an oversized event-log payload.
func Normalize(eventID, sessionID, turnID, traceRef string, native Event) (eventlog.Draft, bool) {
	kind := ""
	switch native.NativeKind {
	case "chat.completion.request", "codex.turn.request":
		kind = "provider.requested"
	case "chat.completion.chunk", "item/agentMessage/delta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		return eventlog.Draft{}, false
	case "chat.completion.stream.completed":
		kind = "provider.completed"
	case "chat.completion":
		kind = "provider.completed"
	case "response.output_item.added", "content_block_start", "item/started":
		kind = "provider.item_started"
	case "response.output_item.done", "content_block_stop", "item/completed":
		kind = "provider.item_finished"
	case "response.function_call", "tool_use":
		kind = "provider.tool_intent"
	case "response.completed", "message_stop", "turn/completed":
		kind = "provider.completed"
	case "response.failed", "error":
		kind = "provider.error"
	case "response.usage", "message_delta", "thread/tokenUsage/updated":
		kind = "provider.usage"
	case "response.output_text.delta", "content_block_delta":
		return eventlog.Draft{}, false
	default:
		kind = "provider.unknown"
	}
	payload, _ := json.Marshal(struct {
		NativeKind     string          `json:"native_kind"`
		NativeID       string          `json:"native_id,omitempty"`
		NativeSequence int64           `json:"native_sequence"`
		Payload        json.RawMessage `json:"payload"`
	}{native.NativeKind, native.NativeID, native.Sequence, nil})
	return eventlog.Draft{EventID: eventID, SessionID: sessionID, TurnID: turnID,
		Actor: eventlog.ActorModel, Kind: kind, SchemaVersion: 1,
		Visibility: eventlog.VisibilityInternal, Payload: payload, ArtifactRef: traceRef}, true
}
