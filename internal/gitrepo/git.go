package gitrepo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func Clone(ctx context.Context, source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "clone", "--no-checkout", source, target)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

func IsRepo(ctx context.Context, path string) error {
	_, err := Run(ctx, path, "rev-parse", "--show-toplevel")
	return err
}

func CurrentCommit(ctx context.Context, path string) (string, error) {
	return Run(ctx, path, "rev-parse", "HEAD")
}

func ResolveRef(ctx context.Context, path, ref string) (string, error) {
	return Run(ctx, path, "rev-parse", ref)
}
