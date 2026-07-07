package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

func LoadDotEnv(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		key, value, ok, err := parseEnvLine(scanner.Text())
		if err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func parseEnvLine(line string) (string, string, bool, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}
	line = strings.TrimPrefix(line, "export ")

	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false, fmt.Errorf("expected KEY=VALUE")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false, fmt.Errorf("empty key")
	}
	if !validEnvKey(key) {
		return "", "", false, fmt.Errorf("invalid key %q", key)
	}

	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		quote := value[0]
		if (quote == '\'' || quote == '"') && value[len(value)-1] == quote {
			value = value[1 : len(value)-1]
		}
	}
	return key, value, true, nil
}

func validEnvKey(key string) bool {
	for i, r := range key {
		if r == '_' || ('A' <= r && r <= 'Z') || ('a' <= r && r <= 'z') {
			continue
		}
		if i > 0 && '0' <= r && r <= '9' {
			continue
		}
		return false
	}
	return true
}
