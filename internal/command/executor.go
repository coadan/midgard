package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"midgard/internal/artifact"
	"midgard/internal/gitrepo"
	"midgard/internal/policy"
)

var commandCounter atomic.Uint64

type Executor struct {
	Policy policy.CommandPolicy
}

func NewExecutor(commandPolicy policy.CommandPolicy) Executor {
	if commandPolicy.Limits == (policy.OutputLimits{}) {
		commandPolicy.Limits = policy.DefaultOutputLimits()
	}
	return Executor{Policy: commandPolicy}
}

func (e Executor) Run(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.Command) == "" {
		return Result{}, fmt.Errorf("command is required")
	}
	if req.ArtifactDir == "" {
		return Result{}, fmt.Errorf("artifact dir is required")
	}
	if err := e.Policy.ValidateCWD(req.CWD); err != nil {
		return Result{}, err
	}
	if err := e.Policy.ValidateCommand(req.Command); err != nil {
		return Result{}, err
	}
	if req.Fence != nil {
		if err := req.Fence(ctx); err != nil {
			return Result{}, err
		}
	}
	if err := os.MkdirAll(req.ArtifactDir, 0o755); err != nil {
		return Result{}, err
	}
	stdoutFile, err := os.CreateTemp(req.ArtifactDir, ".midgard-stdout-*")
	if err != nil {
		return Result{}, err
	}
	stdoutTemp := stdoutFile.Name()
	defer os.Remove(stdoutTemp)
	stderrFile, err := os.CreateTemp(req.ArtifactDir, ".midgard-stderr-*")
	if err != nil {
		_ = stdoutFile.Close()
		return Result{}, err
	}
	stderrTemp := stderrFile.Name()
	defer os.Remove(stderrTemp)
	id := req.ID
	if id == "" {
		id = newCommandID()
	}
	prefix := req.ArtifactPrefix
	if prefix == "" {
		prefix = filepath.ToSlash(filepath.Join("commands", id))
	}
	if err := artifact.ValidatePath(prefix); err != nil {
		return Result{}, fmt.Errorf("artifact prefix: %w", err)
	}
	result := Result{
		ID:        id,
		TaskID:    req.TaskID,
		RepoID:    req.RepoID,
		Command:   req.Command,
		CWD:       req.CWD,
		StartedAt: time.Now().UTC(),
	}

	beforeStatus, _ := gitrepo.Run(ctx, req.CWD, "status", "--porcelain")

	runCtx := ctx
	cancel := func() {}
	if e.Policy.Limits.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, e.Policy.Limits.Timeout)
	}
	defer cancel()

	shell, shellArgs := shellCommand(req.Command)
	cmd := exec.CommandContext(runCtx, shell, shellArgs...)
	cmd.Dir = req.CWD
	cmd.Env = e.Policy.Environment(req.Env)
	stdout := &limitedBuffer{limit: e.Policy.Limits.MaxStdoutBytes}
	stderr := &limitedBuffer{limit: e.Policy.Limits.MaxStderrBytes}
	cmd.Stdout = io.MultiWriter(stdoutFile, stdout)
	cmd.Stderr = io.MultiWriter(stderrFile, stderr)
	err = cmd.Run()
	stdoutCloseErr := stdoutFile.Close()
	stderrCloseErr := stderrFile.Close()
	if stdoutCloseErr != nil {
		return Result{}, stdoutCloseErr
	}
	if stderrCloseErr != nil {
		return Result{}, stderrCloseErr
	}
	result.FinishedAt = time.Now().UTC()
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
	}
	result.StdoutTruncated = stdout.truncated
	result.StderrTruncated = stderr.truncated
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else if result.TimedOut {
			result.ExitCode = -1
		} else {
			result.ExitCode = -1
		}
	}
	if req.Fence != nil {
		if err := req.Fence(ctx); err != nil {
			return Result{}, err
		}
	}

	afterStatus, _ := gitrepo.Run(ctx, req.CWD, "status", "--porcelain")
	result.TouchedFiles = touchedFiles(beforeStatus, afterStatus)

	store := artifact.NewStore(req.ArtifactDir)
	stdoutPath := filepath.ToSlash(filepath.Join(prefix, "stdout.txt"))
	stderrPath := filepath.ToSlash(filepath.Join(prefix, "stderr.txt"))
	resultPath := filepath.ToSlash(filepath.Join(prefix, "result.json"))
	outputRecord := func(path, temp string, preview []byte) (artifact.Record, error) {
		record := artifact.Record{Path: path, Type: artifact.TypePayload, State: artifact.StateSealed, PayloadType: "text"}
		if req.PreserveFullOutput {
			return store.PutFile(record, temp)
		}
		return store.Put(record, preview)
	}
	stdoutRec, err := outputRecord(stdoutPath, stdoutTemp, stdout.bytes())
	if err != nil {
		return Result{}, err
	}
	stderrRec, err := outputRecord(stderrPath, stderrTemp, stderr.bytes())
	if err != nil {
		return Result{}, err
	}
	result.StdoutPath = stdoutPath
	result.StderrPath = stderrPath
	result.ResultPath = resultPath
	result.StdoutChecksum = stdoutRec.Checksum
	result.StderrChecksum = stderrRec.Checksum
	result.StdoutBytes = stdoutRec.Size
	result.StderrBytes = stderrRec.Size
	resultJSON, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return Result{}, err
	}
	resultRec, err := store.Put(artifact.Record{Path: resultPath, Type: artifact.TypePayload, State: artifact.StateSealed, PayloadType: "json"}, append(resultJSON, '\n'))
	if err != nil {
		return Result{}, err
	}
	result.ResultChecksum = resultRec.Checksum
	return result, nil
}

func shellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", command}
	}
	return "/bin/sh", []string{"-lc", command}
}

func newCommandID() string {
	return fmt.Sprintf("cmd_%d_%d", time.Now().UTC().UnixNano(), commandCounter.Add(1))
}

type limitedBuffer struct {
	limit     int64
	data      []byte
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = true
		return len(p), nil
	}
	remaining := int(b.limit) - len(b.data)
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.data = append(b.data, p[:remaining]...)
		b.truncated = true
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *limitedBuffer) bytes() []byte {
	return append([]byte(nil), b.data...)
}
