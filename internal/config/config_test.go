package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMergesUserThenRepository(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	repo := t.TempDir()
	userPath, err := UserPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(`{"profile":"personal","max_provider_calls":12,"thinking":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	repoPath := RepositoryPath(repo)
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repoPath, []byte(`{"profile":"work","model":"repo-model","default_branch":"trunk"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != "work" || result.Model != "repo-model" || result.DefaultBranch != "trunk" || result.MaxProviderCalls != 12 || result.Thinking {
		t.Fatalf("merged config = %+v", result.Values)
	}
	if len(result.Loaded) != 2 || result.Loaded[0] != userPath || result.Loaded[1] != repoPath {
		t.Fatalf("loaded = %#v", result.Loaded)
	}
	if result.Sources["model"] != repoPath || result.Sources["max_provider_calls"] != userPath || result.Sources["max_tokens"] != "built-in default" {
		t.Fatalf("sources = %#v", result.Sources)
	}
	if result.Sources["profile"] != repoPath {
		t.Fatalf("profile source = %q", result.Sources["profile"])
	}
}

func TestLoadReportsProfileAndProfileLayerSources(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	repo := t.TempDir()
	profilePath := RepositoryProfilePath(repo, "review")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, []byte(`{"model":"review-model"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Load(repo, "review")
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != "review" || result.Sources["profile"] != "command line" {
		t.Fatalf("profile = %q from %q", result.Profile, result.Sources["profile"])
	}
	if result.Model != "review-model" || result.Sources["model"] != profilePath {
		t.Fatalf("model = %q from %q", result.Model, result.Sources["model"])
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	repo := t.TempDir()
	path := RepositoryPath(repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"api_key":"must-not-live-here"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(repo); err == nil {
		t.Fatal("expected unknown secret field to be rejected")
	}
}
