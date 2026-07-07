package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"midgard/internal/model"
)

const (
	DefaultBaseURL         = "https://api.deepseek.com/anthropic"
	DefaultModel           = "deepseek-v4-flash"
	MaxReasoningEffort     = "max"
	HighReasoningEffort    = "high"
	ReasoningEffortEnvName = "MIDGARD_DEEPSEEK_REASONING_EFFORT"
)

type Client struct {
	BaseURL         string
	APIKey          string
	ReasoningEffort string
	HTTPClient      *http.Client
}

func New(apiKey string) *Client {
	return &Client{BaseURL: DefaultBaseURL, APIKey: apiKey}
}

func (c *Client) ID() string {
	return "deepseek"
}

func (c *Client) Stream(ctx context.Context, packet model.Packet, emit func(model.Delta) error) (model.Usage, error) {
	if c.APIKey == "" {
		return model.Usage{}, fmt.Errorf("DEEPSEEK_API_KEY is required")
	}
	modelID := packet.ModelID
	if modelID == "" {
		modelID = DefaultModel
	}
	body := messagesRequest{
		Model:     modelID,
		MaxTokens: packet.MaxOutputTokens,
		Stream:    true,
		System:    packet.System,
		Messages: []message{{
			Role:    "user",
			Content: packet.UserContent(),
		}},
	}
	if c.ReasoningEffort != "" {
		if c.ReasoningEffort != HighReasoningEffort && c.ReasoningEffort != MaxReasoningEffort {
			return model.Usage{}, fmt.Errorf("deepseek reasoning effort must be %q or %q", HighReasoningEffort, MaxReasoningEffort)
		}
		body.OutputConfig = &outputConfig{Effort: c.ReasoningEffort}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return model.Usage{}, err
	}
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return model.Usage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return model.Usage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return model.Usage{}, fmt.Errorf("deepseek messages status %d", resp.StatusCode)
	}
	usage, err := readSSE(resp.Body, emit)
	if err != nil {
		return model.Usage{}, err
	}
	usage.ProviderID = c.ID()
	usage.ModelID = modelID
	return usage, nil
}

type messagesRequest struct {
	Model        string        `json:"model"`
	MaxTokens    int           `json:"max_tokens"`
	Stream       bool          `json:"stream"`
	System       string        `json:"system,omitempty"`
	Messages     []message     `json:"messages"`
	OutputConfig *outputConfig `json:"output_config,omitempty"`
}

type outputConfig struct {
	Effort string `json:"effort"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
