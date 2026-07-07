package deepseek

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"midgard/internal/model"
)

func readSSE(r io.Reader, emit func(model.Delta) error) (model.Usage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var usage model.Usage
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		raw := data.String()
		data.Reset()
		if strings.TrimSpace(raw) == "[DONE]" {
			return nil
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return err
		}
		if event.Message.Usage.InputTokens > 0 {
			usage.InputTokens = event.Message.Usage.InputTokens
		}
		if event.Usage.InputTokens > 0 {
			usage.InputTokens = event.Usage.InputTokens
		}
		if event.Usage.OutputTokens > 0 {
			usage.OutputTokens = event.Usage.OutputTokens
		}
		if event.Delta.Text != "" {
			if err := emit(model.Delta{Text: event.Delta.Text}); err != nil {
				return err
			}
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
	return usage, nil
}

type streamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
	Message struct {
		Usage usageFields `json:"usage"`
	} `json:"message"`
	Usage usageFields `json:"usage"`
}

type usageFields struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}
