package task

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func writeTaskReport(path, id, objective string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf(`# Task %s

## Objective
%s

## State
Created at %s.
`, id, objective, time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(path, []byte(content), 0o644)
}
