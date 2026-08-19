package provider

import (
	"context"
	"fmt"
	"strings"
)

type Definition struct {
	Name               string
	DisplayName        string
	RequiredCredential string
	AuthDescription    string
}

type ModelDefinition struct {
	ID            string
	DisplayName   string
	Description   string
	Efforts       []string
	DefaultEffort string
	Default       bool
}

var installedProviders = []Definition{
	{Name: "deepseek", DisplayName: "DeepSeek", RequiredCredential: "api-key", AuthDescription: "API key in the OS keyring"},
	{Name: "codex", DisplayName: "Codex", AuthDescription: "local Codex ChatGPT or API-key login"},
}

func Installed() []Definition {
	return append([]Definition(nil), installedProviders...)
}

func Models(ctx context.Context, providerName string) ([]ModelDefinition, error) {
	switch strings.ToLower(strings.TrimSpace(providerName)) {
	case "deepseek":
		return []ModelDefinition{
			{ID: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro", Description: "DeepSeek coding model", Efforts: []string{"standard", "high"}, DefaultEffort: "high", Default: true},
			{ID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash", Description: "Faster DeepSeek coding model", Efforts: []string{"standard", "high"}, DefaultEffort: "standard"},
		}, nil
	case "codex":
		return CodexModels(ctx)
	default:
		return nil, fmt.Errorf("provider adapter %q is not installed", providerName)
	}
}

func Lookup(name string) (Definition, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, definition := range installedProviders {
		if definition.Name == name {
			return definition, nil
		}
	}
	return Definition{}, fmt.Errorf("provider adapter %q is not installed", name)
}
