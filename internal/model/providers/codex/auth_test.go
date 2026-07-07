package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLocalAuthPrefersAccessTokenEnv(t *testing.T) {
	t.Setenv(AccessTokenEnv, "env-token")
	t.Setenv(CodexHomeEnv, filepath.Join(t.TempDir(), "missing"))

	auth, err := LoadLocalAuth()
	if err != nil {
		t.Fatal(err)
	}
	if auth.Token != "env-token" || auth.Source != AccessTokenEnv {
		t.Fatalf("auth = %#v", auth)
	}
}

func TestLoadLocalAuthReadsChatGPTAuthJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(AccessTokenEnv, "")
	t.Setenv(CodexHomeEnv, dir)
	writeAuth(t, dir, `{
  "auth_mode": "chatgpt",
  "OPENAI_API_KEY": null,
  "tokens": {
    "access_token": "file-token",
    "account_id": "acct-1",
    "id_token": "id",
    "refresh_token": "refresh"
  }
}`)

	auth, err := LoadLocalAuth()
	if err != nil {
		t.Fatal(err)
	}
	if auth.Token != "file-token" || auth.AccountID != "acct-1" || auth.Mode != "chatgpt" {
		t.Fatalf("auth = %#v", auth)
	}
}

func TestLoadLocalAuthReadsPersonalAccessToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(AccessTokenEnv, "")
	t.Setenv(CodexHomeEnv, dir)
	writeAuth(t, dir, `{
  "auth_mode": "personal_access_token",
  "personal_access_token": "at-test"
}`)

	auth, err := LoadLocalAuth()
	if err != nil {
		t.Fatal(err)
	}
	if auth.Token != "at-test" || auth.Mode != "personal_access_token" {
		t.Fatalf("auth = %#v", auth)
	}
}

func TestLoadLocalAuthRejectsAPIKeyAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(AccessTokenEnv, "")
	t.Setenv(CodexHomeEnv, dir)
	writeAuth(t, dir, `{
  "auth_mode": "apikey",
  "OPENAI_API_KEY": "sk-test"
}`)

	_, err := LoadLocalAuth()
	if err == nil {
		t.Fatal("API key auth accepted")
	}
}

func writeAuth(t *testing.T, dir, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
