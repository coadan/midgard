package agentloop

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type Activity struct {
	Kind                 string
	SessionID            string
	TurnID               string
	Sequence             int64
	Role                 string
	ActionID             string
	ControlID            string
	EntityID             string
	Revision             int
	Name                 string
	State                string
	Message              string
	Arguments            string
	Output               string
	ProviderCalls        int
	Actions              int
	InputTokens          int64
	CacheHitInputTokens  int64
	CacheMissInputTokens int64
	OutputTokens         int64
	ThinkingTokens       int64
	ProviderDuration     time.Duration
	ContextTokens        int64
	ContextLimitTokens   int64
	ContextEstimated     bool
	Compactions          int
	StreamIndex          int
	At                   time.Time
}

type ActivitySink interface {
	EmitActivity(Activity)
}

type ActivityFunc func(Activity)

func (f ActivityFunc) EmitActivity(activity Activity) { f(activity) }

type TextActivitySink struct {
	Writer io.Writer
	mu     sync.Mutex
}

func (s *TextActivitySink) EmitActivity(activity Activity) {
	if s == nil || s.Writer == nil {
		return
	}
	// Session messages are already emitted by the headless conversation
	// renderer. They are sent on the activity channel only so an interactive
	// client can attach a durable sequence to its optimistic chat row.
	if activity.Kind == "message" {
		return
	}
	message := activity.Message
	label := activity.Kind
	if activity.Kind == "model_state" {
		label = "model"
		state := map[string]string{"op.accepted": "draft updated", "commit.proposed": "commit proposed", "commit.accepted": "commit accepted",
			"source.rejected": "record rejected", "op.rejected": "change rejected", "commit.rejected": "commit rejected"}[activity.State]
		message = strings.TrimSpace(strings.Join([]string{state, activity.Name, activity.EntityID}, " "))
	}
	if message == "" {
		message = activity.Name
	}
	if activity.Kind == "tool" && activity.Arguments != "" {
		message += " " + activity.Arguments
	}
	if message == "" {
		return
	}
	const limit = 4096
	if len(message) > limit {
		message = message[:limit] + "… [truncated]"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = fmt.Fprintf(s.Writer, "[%s] %s\n", label, message)
}
