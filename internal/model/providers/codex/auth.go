package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	AccessTokenEnv = "CODEX_ACCESS_TOKEN"
	CodexHomeEnv   = "CODEX_HOME"
)

type LocalAuth struct {
	Token     string
	Source    string
	Mode      string
	AccountID string
}

type authDotJSON struct {
	AuthMode            string          `json:"auth_mode"`
	OpenAIAPIKey        *string         `json:"OPENAI_API_KEY"`
	Tokens              *authTokens     `json:"tokens"`
	PersonalAccessToken string          `json:"personal_access_token"`
	AgentIdentity       json.RawMessage `json:"agent_identity"`
}

type authTokens struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
}

func LoadLocalAuth() (LocalAuth, error) {
	if token := strings.TrimSpace(os.Getenv(AccessTokenEnv)); token != "" {
		return LocalAuth{Token: token, Source: AccessTokenEnv, Mode: "access_token"}, nil
	}

	codexHome, err := CodexHome()
	if err != nil {
		return LocalAuth{}, err
	}
	path := filepath.Join(codexHome, "auth.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return LocalAuth{}, fmt.Errorf("%s is not set and %s does not exist", AccessTokenEnv, path)
	}
	if err != nil {
		return LocalAuth{}, fmt.Errorf("read codex auth: %w", err)
	}

	var auth authDotJSON
	if err := json.Unmarshal(data, &auth); err != nil {
		return LocalAuth{}, fmt.Errorf("parse codex auth: %w", err)
	}
	mode := strings.TrimSpace(auth.AuthMode)
	if mode == "" {
		mode = inferredAuthMode(auth)
	}
	if auth.PersonalAccessToken != "" {
		return LocalAuth{
			Token:  auth.PersonalAccessToken,
			Source: path,
			Mode:   mode,
		}, nil
	}
	if auth.Tokens != nil && strings.TrimSpace(auth.Tokens.AccessToken) != "" {
		return LocalAuth{
			Token:     auth.Tokens.AccessToken,
			Source:    path,
			Mode:      mode,
			AccountID: auth.Tokens.AccountID,
		}, nil
	}
	if len(auth.AgentIdentity) > 0 && string(auth.AgentIdentity) != "null" {
		return LocalAuth{}, fmt.Errorf("codex auth mode %q uses agent identity material, which Midgard direct backend auth does not support yet", mode)
	}
	if auth.OpenAIAPIKey != nil && strings.TrimSpace(*auth.OpenAIAPIKey) != "" {
		return LocalAuth{}, fmt.Errorf("codex auth mode %q is API-key based; use a ChatGPT/Codex access token for --provider codex", mode)
	}
	return LocalAuth{}, fmt.Errorf("codex auth at %s does not contain a usable access token", path)
}

func CodexHome() (string, error) {
	if home := strings.TrimSpace(os.Getenv(CodexHomeEnv)); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve home directory: empty home")
	}
	return filepath.Join(home, ".codex"), nil
}

func inferredAuthMode(auth authDotJSON) string {
	switch {
	case auth.PersonalAccessToken != "":
		return "personal_access_token"
	case auth.Tokens != nil:
		return "chatgpt"
	case len(auth.AgentIdentity) > 0 && string(auth.AgentIdentity) != "null":
		return "agent_identity"
	case auth.OpenAIAPIKey != nil && *auth.OpenAIAPIKey != "":
		return "api"
	default:
		return "unknown"
	}
}
