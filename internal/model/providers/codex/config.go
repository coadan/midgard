package codex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type codexConfig struct {
	Model string `toml:"model"`
}

func ConfiguredModel() (string, error) {
	codexHome, err := CodexHome()
	if err != nil {
		return "", err
	}
	path := filepath.Join(codexHome, "config.toml")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read codex config: %w", err)
	}
	var cfg codexConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parse codex config: %w", err)
	}
	return strings.TrimSpace(cfg.Model), nil
}
