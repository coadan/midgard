// Package config loads non-secret Midgard settings. Provider credentials are
// intentionally referenced by profile and remain in the OS keyring.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const repoConfigPath = ".midgard/config.json"

type File struct {
	Provider         *string `json:"provider,omitempty"`
	Profile          *string `json:"profile,omitempty"`
	Model            *string `json:"model,omitempty"`
	BaseURL          *string `json:"base_url,omitempty"`
	DefaultBranch    *string `json:"default_branch,omitempty"`
	LandingStrategy  *string `json:"landing_strategy,omitempty"`
	CleanupLanded    *bool   `json:"cleanup_when_landed,omitempty"`
	Thinking         *bool   `json:"thinking,omitempty"`
	MaxTokens        *int    `json:"max_tokens,omitempty"`
	MaxProviderCalls *int    `json:"max_provider_calls,omitempty"`
}

type Values struct {
	Provider         string `json:"provider"`
	Profile          string `json:"profile"`
	Model            string `json:"model"`
	BaseURL          string `json:"base_url"`
	DefaultBranch    string `json:"default_branch"`
	LandingStrategy  string `json:"landing_strategy"`
	CleanupLanded    bool   `json:"cleanup_when_landed"`
	Thinking         bool   `json:"thinking"`
	MaxTokens        int    `json:"max_tokens"`
	MaxProviderCalls int    `json:"max_provider_calls"`
}

type Result struct {
	Values
	UserFile               string            `json:"user_file"`
	RepositoryFile         string            `json:"repository_file"`
	UserProfileFile        string            `json:"user_profile_file"`
	RepositoryProfileFile  string            `json:"repository_profile_file"`
	Loaded                 []string          `json:"loaded"`
	GitFile                string            `json:"git_file"`
	Sources                map[string]string `json:"sources"`
	LandingStrategyFromGit bool              `json:"-"`
	ProviderFromGit        bool              `json:"-"`
}

var valueFields = []string{
	"provider", "profile", "model", "base_url", "default_branch",
	"landing_strategy", "cleanup_when_landed", "thinking", "max_tokens",
	"max_provider_calls",
}

func Defaults() Values {
	return Values{
		Provider: "deepseek", Profile: "default", Model: "deepseek-v4-pro",
		BaseURL: "https://api.deepseek.com", DefaultBranch: "main", LandingStrategy: "direct", CleanupLanded: true, Thinking: true,
		MaxTokens: 16_384, MaxProviderCalls: 24,
	}
}

func UserPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return filepath.Join(base, "midgard", "config.json"), nil
}

func RepositoryPath(repository string) string {
	return filepath.Join(repository, filepath.FromSlash(repoConfigPath))
}

func UserProfilePath(profile string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return filepath.Join(base, "midgard", "profiles", profile+".json"), nil
}

func RepositoryProfilePath(repository, profile string) string {
	return filepath.Join(repository, ".midgard", "profiles", profile+".json")
}

// Load applies user settings first and repository settings second. CLI flags
// are applied by the caller after this result becomes their default value.
func Load(repository string, selectedProfile ...string) (Result, error) {
	userPath, err := UserPath()
	if err != nil {
		return Result{}, err
	}
	sources := make(map[string]string, len(valueFields))
	for _, field := range valueFields {
		sources[field] = "built-in default"
	}
	result := Result{Values: Defaults(), UserFile: userPath, RepositoryFile: RepositoryPath(repository), Loaded: []string{}, Sources: sources}
	userFile, userFound, err := read(result.UserFile)
	if err != nil {
		return Result{}, err
	}
	repoFile, repoFound, err := read(result.RepositoryFile)
	if err != nil {
		return Result{}, err
	}
	profile := result.Profile
	if userFound && userFile.Profile != nil {
		profile = *userFile.Profile
	}
	if repoFound && repoFile.Profile != nil {
		profile = *repoFile.Profile
	}
	if len(selectedProfile) > 0 && strings.TrimSpace(selectedProfile[0]) != "" {
		profile = selectedProfile[0]
	}
	profile, err = validateName("profile", profile)
	if err != nil {
		return Result{}, err
	}
	result.UserProfileFile, err = UserProfilePath(profile)
	if err != nil {
		return Result{}, err
	}
	result.RepositoryProfileFile = RepositoryProfilePath(repository, profile)
	for _, layer := range []struct {
		path         string
		file         File
		found        bool
		profileLayer bool
	}{
		{result.UserFile, userFile, userFound, false},
		{result.UserProfileFile, File{}, false, true},
		{result.RepositoryFile, repoFile, repoFound, false},
		{result.RepositoryProfileFile, File{}, false, true},
	} {
		file, found := layer.file, layer.found
		if layer.profileLayer {
			file, found, err = read(layer.path)
		}
		if err != nil {
			return Result{}, err
		}
		if !found {
			continue
		}
		apply(&result.Values, result.Sources, file, layer.path, !layer.profileLayer)
		result.Loaded = append(result.Loaded, layer.path)
	}
	result.Profile = profile
	if len(selectedProfile) > 0 && strings.TrimSpace(selectedProfile[0]) != "" {
		result.Sources["profile"] = "command line"
	}
	result.GitFile = filepath.Join(repository, ".git", "config")
	gitLoaded := false
	if localProvider, found := gitLocalValue(repository, "midgard.provider"); found {
		result.Provider = strings.ToLower(localProvider)
		result.Sources["provider"] = result.GitFile
		result.ProviderFromGit = true
		gitLoaded = true
	}
	if localStrategy, found := gitLocalValue(repository, "midgard.landingStrategy"); found && (localStrategy == "direct" || localStrategy == "pull-request") {
		result.LandingStrategy = localStrategy
		result.Sources["landing_strategy"] = result.GitFile
		result.LandingStrategyFromGit = true
		gitLoaded = true
	}
	if gitLoaded {
		result.Loaded = append(result.Loaded, result.GitFile)
	}
	if result.MaxTokens <= 0 || result.MaxProviderCalls <= 0 {
		return Result{}, errors.New("max_tokens and max_provider_calls must be positive")
	}
	if result.LandingStrategy != "pull-request" && result.LandingStrategy != "direct" {
		return Result{}, fmt.Errorf("landing_strategy must be %q or %q", "pull-request", "direct")
	}
	return result, nil
}

func gitLocalValue(repository, key string) (string, bool) {
	command := exec.Command("git", "config", "--local", "--get", key)
	command.Dir = repository
	output, err := command.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(output)), true
}

func SetRepositoryLandingStrategy(repository, strategy string) error {
	if strategy != "direct" && strategy != "pull-request" {
		return fmt.Errorf("landing strategy must be %q or %q", "direct", "pull-request")
	}
	command := exec.Command("git", "config", "--local", "midgard.landingStrategy", strategy)
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("store repository landing strategy: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func SetRepositoryProvider(repository, provider string) error {
	provider, err := validateName("provider", provider)
	if err != nil {
		return err
	}
	command := exec.Command("git", "config", "--local", "midgard.provider", provider)
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("store repository provider: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func validateName(kind, value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", fmt.Errorf("%s name is required", kind)
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || (index > 0 && (char == '-' || char == '_' || char == '.')) {
			continue
		}
		return "", fmt.Errorf("invalid %s name %q", kind, value)
	}
	return value, nil
}

func read(path string) (File, bool, error) {
	handle, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, false, nil
	}
	if err != nil {
		return File{}, false, fmt.Errorf("open config %s: %w", path, err)
	}
	defer handle.Close()
	decoder := json.NewDecoder(handle)
	decoder.DisallowUnknownFields()
	var file File
	if err := decoder.Decode(&file); err != nil {
		return File{}, false, fmt.Errorf("decode config %s: %w", path, err)
	}
	if err := ensureEOF(decoder); err != nil {
		return File{}, false, fmt.Errorf("decode config %s: %w", path, err)
	}
	return file, true, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values")
}

func apply(values *Values, sources map[string]string, file File, source string, includeProfile bool) {
	if file.Provider != nil {
		values.Provider = *file.Provider
		sources["provider"] = source
	}
	if includeProfile && file.Profile != nil {
		values.Profile = *file.Profile
		sources["profile"] = source
	}
	if file.Model != nil {
		values.Model = *file.Model
		sources["model"] = source
	}
	if file.BaseURL != nil {
		values.BaseURL = *file.BaseURL
		sources["base_url"] = source
	}
	if file.DefaultBranch != nil {
		values.DefaultBranch = *file.DefaultBranch
		sources["default_branch"] = source
	}
	if file.LandingStrategy != nil {
		values.LandingStrategy = *file.LandingStrategy
		sources["landing_strategy"] = source
	}
	if file.CleanupLanded != nil {
		values.CleanupLanded = *file.CleanupLanded
		sources["cleanup_when_landed"] = source
	}
	if file.Thinking != nil {
		values.Thinking = *file.Thinking
		sources["thinking"] = source
	}
	if file.MaxTokens != nil {
		values.MaxTokens = *file.MaxTokens
		sources["max_tokens"] = source
	}
	if file.MaxProviderCalls != nil {
		values.MaxProviderCalls = *file.MaxProviderCalls
		sources["max_provider_calls"] = source
	}
}
