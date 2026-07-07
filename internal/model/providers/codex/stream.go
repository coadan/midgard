package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"midgard/internal/model"
)

func readResponsesSSE(r io.Reader, emit func(model.Delta) error) (model.Usage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var usage model.Usage
	var completed bool
	var emittedText bool
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		raw := data.String()
		data.Reset()
		if strings.TrimSpace(raw) == "[DONE]" {
			completed = true
			return nil
		}
		var event responsesStreamEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return err
		}
		if event.Usage != nil {
			usage.InputTokens = event.Usage.InputTokens
			usage.OutputTokens = event.Usage.OutputTokens
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				emittedText = true
				if err := emit(model.Delta{Text: event.Delta}); err != nil {
					return err
				}
			}
		case "agent_message_content_delta", "agent.message.content_delta":
			if text := firstNonEmpty(event.Delta, event.Text); text != "" {
				emittedText = true
				if err := emit(model.Delta{Text: text}); err != nil {
					return err
				}
			}
		case "agent_message", "agent.message":
			if !emittedText && event.Text != "" {
				emittedText = true
				if err := emit(model.Delta{Text: event.Text}); err != nil {
					return err
				}
			}
		case "item.completed", "item_completed":
			if !emittedText && event.Item != nil && event.Item.Type == "agent_message" && event.Item.Text != "" {
				emittedText = true
				if err := emit(model.Delta{Text: event.Item.Text}); err != nil {
					return err
				}
			}
		case "turn.completed", "turn_completed", "task_complete":
			completed = true
		case "response.completed":
			completed = true
			if event.Response != nil && event.Response.Usage != nil {
				usage.InputTokens = event.Response.Usage.InputTokens
				usage.OutputTokens = event.Response.Usage.OutputTokens
			}
		case "error", "turn.failed", "turn_failed", "stream_error", "response.failed":
			return fmt.Errorf("codex response failed: %s", event.failureMessage())
		case "response.incomplete":
			return fmt.Errorf("codex response incomplete: %s", event.incompleteReason())
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return usage, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, err
	}
	if err := flush(); err != nil {
		return usage, err
	}
	if !completed {
		return usage, fmt.Errorf("codex response stream closed before response.completed")
	}
	return usage, nil
}

type responsesStreamEvent struct {
	Type     string            `json:"type"`
	Delta    string            `json:"delta"`
	Text     string            `json:"text"`
	Message  string            `json:"message"`
	Item     *codexStreamItem  `json:"item"`
	Usage    *responsesUsage   `json:"usage"`
	Error    *responsesError   `json:"error"`
	Response *responsesPayload `json:"response"`
}

type codexStreamItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesPayload struct {
	ID               string                 `json:"id"`
	Usage            *responsesUsage        `json:"usage"`
	Error            *responsesError        `json:"error"`
	IncompleteDetail map[string]interface{} `json:"incomplete_details"`
}

type responsesUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type responsesError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e responsesStreamEvent) failureMessage() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Error != nil {
		if e.Error.Message != "" {
			return e.Error.Message
		}
		if e.Error.Code != "" {
			return e.Error.Code
		}
		if e.Error.Type != "" {
			return e.Error.Type
		}
	}
	if e.Response != nil && e.Response.Error != nil {
		if e.Response.Error.Message != "" {
			return e.Response.Error.Message
		}
		if e.Response.Error.Code != "" {
			return e.Response.Error.Code
		}
		if e.Response.Error.Type != "" {
			return e.Response.Error.Type
		}
	}
	return "response.failed event received"
}

func (e responsesStreamEvent) incompleteReason() string {
	if e.Response != nil {
		if reason, ok := e.Response.IncompleteDetail["reason"].(string); ok && reason != "" {
			return reason
		}
	}
	return "unknown"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
