package command

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"midgard/internal/gitrepo"
	"midgard/internal/policy"
)

func TestExecutorRunsAllowedCommandAndWritesArtifacts(t *testing.T) {
	ctx := context.Background()
	repo := initCommandRepo(t)
	artifactDir := filepath.Join(t.TempDir(), "artifacts")
	executor := NewExecutor(policy.DefaultCommandPolicy(repo, artifactDir))

	result, err := executor.Run(ctx, Request{
		ID:          "cmd_test",
		TaskID:      "task_1",
		RepoID:      "repo1",
		Command:     "printf stdout && printf stderr >&2",
		CWD:         repo,
		ArtifactDir: artifactDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.TimedOut {
		t.Fatalf("result = %#v", result)
	}
	stdout := readArtifact(t, artifactDir, result.StdoutPath)
	stderr := readArtifact(t, artifactDir, result.StderrPath)
	if stdout != "stdout" || stderr != "stderr" {
		t.Fatalf("stdout/stderr = %q/%q", stdout, stderr)
	}
	if result.ResultPath == "" {
		t.Fatal("result artifact path missing")
	}
}

func TestExecutorRejectsOutsideCWD(t *testing.T) {
	ctx := context.Background()
	repo := initCommandRepo(t)
	executor := NewExecutor(policy.DefaultCommandPolicy(repo))
	_, err := executor.Run(ctx, Request{
		Command:     "true",
		CWD:         t.TempDir(),
		ArtifactDir: filepath.Join(t.TempDir(), "artifacts"),
	})
	if err == nil {
		t.Fatal("outside cwd accepted")
	}
}

func TestExecutorTimeoutAndOutputCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep command shape is Unix-specific")
	}
	ctx := context.Background()
	repo := initCommandRepo(t)
	artifactDir := filepath.Join(t.TempDir(), "artifacts")
	commandPolicy := policy.DefaultCommandPolicy(repo, artifactDir)
	commandPolicy.Limits.Timeout = 50 * time.Millisecond
	commandPolicy.Limits.MaxStdoutBytes = 4
	executor := NewExecutor(commandPolicy)

	result, err := executor.Run(ctx, Request{
		ID:          "cmd_timeout",
		Command:     "printf 1234567890; sleep 1",
		CWD:         repo,
		ArtifactDir: artifactDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut {
		t.Fatalf("result = %#v, want timeout", result)
	}
	if !result.StdoutTruncated {
		t.Fatal("stdout should be truncated")
	}
	stdout := readArtifact(t, artifactDir, result.StdoutPath)
	if stdout != "1234" {
		t.Fatalf("stdout = %q, want capped output", stdout)
	}
}

func TestExecutorFiltersEnvironment(t *testing.T) {
	t.Setenv("MIDGARD_ALLOWED_ENV", "from-process")
	t.Setenv("MIDGARD_SECRET_ENV", "secret")
	ctx := context.Background()
	repo := initCommandRepo(t)
	artifactDir := filepath.Join(t.TempDir(), "artifacts")
	commandPolicy := policy.DefaultCommandPolicy(repo, artifactDir)
	commandPolicy.EnvAllowlist = append(commandPolicy.EnvAllowlist, "MIDGARD_ALLOWED_ENV")
	executor := NewExecutor(commandPolicy)

	result, err := executor.Run(ctx, Request{
		ID:          "cmd_env",
		Command:     `printf "%s:%s" "$MIDGARD_ALLOWED_ENV" "$MIDGARD_SECRET_ENV"`,
		CWD:         repo,
		ArtifactDir: artifactDir,
		Env:         map[string]string{"MIDGARD_ALLOWED_ENV": "from-request", "MIDGARD_SECRET_ENV": "blocked"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stdout := readArtifact(t, artifactDir, result.StdoutPath)
	if stdout != "from-request:" {
		t.Fatalf("stdout = %q, want only allowlisted env", stdout)
	}
}

func TestExecutorCapturesTouchedFiles(t *testing.T) {
	ctx := context.Background()
	repo := initCommandRepo(t)
	artifactDir := filepath.Join(t.TempDir(), "artifacts")
	executor := NewExecutor(policy.DefaultCommandPolicy(repo, artifactDir))

	result, err := executor.Run(ctx, Request{
		ID:          "cmd_touch",
		Command:     `printf "\nchange\n" >> README.md`,
		CWD:         repo,
		ArtifactDir: artifactDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(result.TouchedFiles, "README.md") {
		t.Fatalf("touched files = %#v, want README.md", result.TouchedFiles)
	}
}

func TestTouchedFilesIncludesRenames(t *testing.T) {
	got := touchedFiles("", "R  old.txt -> new.txt\n")
	if !slices.Contains(got, "new.txt") {
		t.Fatalf("touched files = %#v", got)
	}
}

func readArtifact(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func initCommandRepo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	if _, err := gitrepo.Run(ctx, repo, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "config", "user.email", "midgard@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "config", "user.name", "Midgard Test"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# command fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "commit", "-m", "initial"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(repo) == "" {
		t.Fatal("empty repo path")
	}
	return repo
}
