package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// UnsafeHostExecutor runs commands directly on the host. It provides output
// bounds but no filesystem, process, credential, or network containment.
// Production composition must not use it.
type UnsafeHostExecutor struct{}

func (UnsafeHostExecutor) RunShell(ctx context.Context, dir, command string, environment map[string]string, limit int) (Output, error) {
	if strings.TrimSpace(command) == "" {
		return Output{}, errors.New("command is required")
	}
	cmd := exec.Command("/bin/sh", "-lc", command)
	cmd.Dir = dir
	cmd.Env = commandEnvironment(environment)
	return collectProcessGroup(ctx, cmd, normalizedLimit(limit))
}

func collectProcessGroup(ctx context.Context, cmd *exec.Cmd, limit int) (Output, error) {
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = limit, limit
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return Output{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return Output{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode(err), Truncated: stdout.truncated || stderr.truncated}, err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		err := <-done
		code := "cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = "timeout"
		}
		message := bytes.TrimSpace(stderr.Bytes())
		if len(message) > 0 {
			message = append(message, '\n')
		}
		message = append(message, ctx.Err().Error()...)
		return Output{Stdout: stdout.String(), Stderr: string(message), ExitCode: exitCode(err), Truncated: stdout.truncated || stderr.truncated, ErrorCode: code}, ctx.Err()
	}
}

func (UnsafeHostExecutor) RunArgv(ctx context.Context, dir string, argv []string, environment map[string]string, limit int) (Output, error) {
	if len(argv) == 0 || strings.ContainsRune(argv[0], filepath.Separator) {
		return Output{}, errors.New("executable must be a bare name")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = commandEnvironment(environment)
	return collect(cmd, normalizedLimit(limit))
}

func commandEnvironment(injected map[string]string) []string {
	environment := withoutEnvironmentKey(os.Environ(), "DEEPSEEK_API_KEY")
	keys := make([]string, 0, len(injected))
	for key := range injected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environment = withoutEnvironmentKey(environment, key)
		environment = append(environment, key+"="+injected[key])
	}
	return environment
}

func withoutEnvironmentKey(environment []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(environment))
	for _, value := range environment {
		if !strings.HasPrefix(value, prefix) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func normalizedLimit(limit int) int {
	if limit <= 0 {
		return DefaultMaxOutput
	}
	return limit
}
