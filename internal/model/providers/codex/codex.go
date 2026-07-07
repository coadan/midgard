package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"midgard/internal/model"
)

const (
	DefaultBaseURL = "https://chatgpt.com/backend-api/codex"
	DefaultModel   = "gpt-5.4"
)

type Client struct {
	BaseURL    string
	AuthToken  string
	HTTPClient *http.Client
}

func New(authToken string) *Client {
	return &Client{BaseURL: DefaultBaseURL, AuthToken: authToken}
}

func NewFromLocalAuth() (*Client, error) {
	auth, err := LoadLocalAuth()
	if err != nil {
		return nil, err
	}
	return New(auth.Token), nil
}

func (c *Client) ID() string {
	return "codex"
}

func (c *Client) Stream(ctx context.Context, packet model.Packet, emit func(model.Delta) error) (model.Usage, error) {
	if strings.TrimSpace(c.AuthToken) == "" {
		return model.Usage{}, fmt.Errorf("%s or Codex auth.json access token is required", AccessTokenEnv)
	}
	modelID := packet.ModelID
	if modelID == "" {
		modelID = DefaultModel
	}
	body := responsesRequest{
		Model:             modelID,
		Instructions:      packet.System,
		Input:             []responseItem{userMessage(packet.UserContent())},
		Tools:             []json.RawMessage{},
		ToolChoice:        "auto",
		ParallelToolCalls: false,
		Store:             false,
		Stream:            true,
		Include:           []string{},
		ClientMetadata: map[string]string{
			"harness": "midgard",
			"task_id": packet.TaskID,
			"role":    packet.Role.String(),
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return model.Usage{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.responsesURL(), bytes.NewReader(payload))
	if err != nil {
		return model.Usage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.AuthToken)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("User-Agent", "midgard/codex-direct")

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
		return model.Usage{}, responseStatusError(resp)
	}
	usage, err := readResponsesSSE(resp.Body, emit)
	if err != nil {
		return model.Usage{}, err
	}
	usage.ProviderID = c.ID()
	usage.ModelID = modelID
	return usage, nil
}

func (c *Client) responsesURL() string {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return baseURL + "/responses"
}

func responseStatusError(resp *http.Response) error {
	var text string
	if data, err := io.ReadAll(io.LimitReader(resp.Body, 4096)); err == nil {
		text = strings.TrimSpace(string(data))
	}
	if text == "" {
		return fmt.Errorf("codex responses status %d", resp.StatusCode)
	}
	return fmt.Errorf("codex responses status %d: %s", resp.StatusCode, text)
}

type responsesRequest struct {
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions,omitempty"`
	Input             []responseItem    `json:"input"`
	Tools             []json.RawMessage `json:"tools"`
	ToolChoice        string            `json:"tool_choice"`
	ParallelToolCalls bool              `json:"parallel_tool_calls"`
	Reasoning         any               `json:"reasoning"`
	Store             bool              `json:"store"`
	Stream            bool              `json:"stream"`
	Include           []string          `json:"include"`
	ClientMetadata    map[string]string `json:"client_metadata,omitempty"`
}

type responseItem struct {
	Type    string        `json:"type"`
	Role    string        `json:"role"`
	Content []contentItem `json:"content"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func userMessage(text string) responseItem {
	return responseItem{
		Type: "message",
		Role: "user",
		Content: []contentItem{{
			Type: "input_text",
			Text: text,
		}},
	}
}
