package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"midgard/internal/eventlog"
)

type Service struct {
	Log               *eventlog.Store
	WorktreeBase      string
	ProjectID         string
	RepositoryName    string
	DefaultBranch     string
	LandingStrategy   string
	CleanupWhenLanded bool
}

func (s Service) Bind(ctx context.Context, sessionID, repository string) (Binding, error) {
	info, err := InspectRepository(ctx, repository, s.DefaultBranch)
	if err != nil {
		return Binding{}, err
	}
	projectID, repositoryName := s.ProjectID, s.RepositoryName
	if projectID == "" {
		digest := sha256.Sum256([]byte(info.Root))
		projectID = "project_" + hex.EncodeToString(digest[:8])
	}
	if repositoryName == "" {
		repositoryName = safeName(filepath.Base(info.Root))
	}
	base, err := filepath.Abs(s.WorktreeBase)
	if err != nil {
		return Binding{}, err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return Binding{}, err
	}
	target := filepath.Join(base, safeName(sessionID), safeName(repositoryName))
	if !within(base, target) {
		return Binding{}, errors.New("worktree target escapes base")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		return Binding{}, fmt.Errorf("worktree target already exists")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return Binding{}, err
	}
	if _, err := gitOutput(ctx, info.Root, "worktree", "add", "--detach", target, info.Head); err != nil {
		return Binding{}, fmt.Errorf("create disposable worktree: %w", err)
	}
	strategy := strings.TrimSpace(s.LandingStrategy)
	if strategy == "" {
		strategy = "direct"
	}
	binding := Binding{SessionID: sessionID, ProjectID: projectID, RepositoryName: repositoryName, RepositoryRoot: info.Root, WorktreeRoot: target, StartCommit: info.Head,
		DefaultBranch: info.DefaultBranch, LandingStrategy: strategy, CleanupWhenLanded: s.CleanupWhenLanded, CleanupState: "retained"}
	raw, _ := json.Marshal(binding)
	head, err := s.Log.Head(ctx, sessionID)
	if err != nil {
		s.cleanupWorktree(context.Background(), binding)
		return Binding{}, err
	}
	event, err := s.Log.Append(ctx, eventlog.Draft{EventID: randomID("evt"), SessionID: sessionID,
		Actor: eventlog.ActorServer, Kind: "workspace.bound", SchemaVersion: 3,
		Visibility: eventlog.VisibilityInternal, Payload: raw}, head)
	if err != nil {
		s.cleanupWorktree(context.Background(), binding)
		return Binding{}, err
	}
	binding.LastSequence = event.Sequence
	return binding, nil
}

func (s Service) Get(ctx context.Context, sessionID string) (Binding, error) {
	return s.GetRepository(ctx, sessionID, s.RepositoryName)
}

func (s Service) GetRepository(ctx context.Context, sessionID, repositoryName string) (Binding, error) {
	var binding Binding
	query := `SELECT session_id,project_id,repository_name,repository_root,worktree_root,start_commit,default_branch,landing_strategy,cleanup_when_landed,cleanup_state,last_sequence FROM workspace_projection WHERE session_id=?`
	arguments := []any{sessionID}
	if repositoryName != "" {
		query += ` AND repository_name=?`
		arguments = append(arguments, repositoryName)
	}
	query += ` ORDER BY repository_name LIMIT 1`
	err := s.Log.DB().QueryRowContext(ctx, query, arguments...).Scan(&binding.SessionID, &binding.ProjectID, &binding.RepositoryName, &binding.RepositoryRoot, &binding.WorktreeRoot, &binding.StartCommit, &binding.DefaultBranch, &binding.LandingStrategy, &binding.CleanupWhenLanded, &binding.CleanupState, &binding.LastSequence)
	if errors.Is(err, sql.ErrNoRows) {
		return Binding{}, fmt.Errorf("session %s has no workspace", sessionID)
	}
	return binding, err
}

func (s Service) List(ctx context.Context, sessionID string) ([]Binding, error) {
	rows, err := s.Log.DB().QueryContext(ctx, `SELECT session_id,project_id,repository_name,repository_root,worktree_root,start_commit,default_branch,landing_strategy,cleanup_when_landed,cleanup_state,last_sequence
FROM workspace_projection WHERE session_id=? ORDER BY repository_name`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bindings []Binding
	for rows.Next() {
		var binding Binding
		if err := rows.Scan(&binding.SessionID, &binding.ProjectID, &binding.RepositoryName, &binding.RepositoryRoot, &binding.WorktreeRoot, &binding.StartCommit, &binding.DefaultBranch, &binding.LandingStrategy, &binding.CleanupWhenLanded, &binding.CleanupState, &binding.LastSequence); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

// CleanupIfLanded removes a terminal session worktree only when Git proves a
// clean, advanced worktree HEAD is contained in the configured default branch.
// The committed/cleaned events make an interrupted removal recoverable.
func (s Service) CleanupIfLanded(ctx context.Context, sessionID string) (bool, error) {
	binding, err := s.Get(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if !binding.CleanupWhenLanded {
		return false, nil
	}
	if binding.CleanupState == "cleaned" {
		return true, nil
	}
	if binding.CleanupState == "retained" {
		status, err := gitOutput(ctx, binding.WorktreeRoot, "status", "--porcelain=v1", "--untracked-files=all")
		if err != nil {
			return false, fmt.Errorf("inspect retained worktree before cleanup: %w", err)
		}
		if strings.TrimSpace(status) != "" {
			return false, nil
		}
		head, err := gitOutput(ctx, binding.WorktreeRoot, "rev-parse", "--verify", "HEAD")
		if err != nil {
			return false, fmt.Errorf("inspect retained worktree HEAD: %w", err)
		}
		head = strings.TrimSpace(head)
		if head == binding.StartCommit {
			return false, nil
		}
		landed, err := gitAncestor(ctx, binding.RepositoryRoot, head, "refs/heads/"+binding.DefaultBranch)
		if err != nil {
			return false, err
		}
		if !landed {
			return false, nil
		}
		payload, _ := json.Marshal(map[string]string{"repository_name": binding.RepositoryName, "head": head, "default_branch": binding.DefaultBranch, "evidence": "git_ancestor"})
		if _, err := s.Log.AppendCurrent(ctx, eventlog.Draft{EventID: randomID("evt"), SessionID: sessionID,
			Actor: eventlog.ActorServer, Kind: "workspace.cleanup_committed", SchemaVersion: 1,
			Visibility: eventlog.VisibilityInternal, Payload: payload}); err != nil {
			return false, err
		}
		binding.CleanupState = "committed"
	}
	if binding.CleanupState != "committed" {
		return false, fmt.Errorf("unknown workspace cleanup state %q", binding.CleanupState)
	}
	if _, err := os.Stat(binding.WorktreeRoot); err == nil {
		if _, err := gitOutput(ctx, binding.RepositoryRoot, "worktree", "remove", "--force", binding.WorktreeRoot); err != nil {
			return false, fmt.Errorf("remove landed worktree: %w", err)
		}
		_ = os.Remove(filepath.Dir(binding.WorktreeRoot))
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	payload, _ := json.Marshal(map[string]string{"repository_name": binding.RepositoryName, "default_branch": binding.DefaultBranch})
	if _, err := s.Log.AppendCurrent(ctx, eventlog.Draft{EventID: randomID("evt"), SessionID: sessionID,
		Actor: eventlog.ActorServer, Kind: "workspace.cleaned", SchemaVersion: 1,
		Visibility: eventlog.VisibilityInternal, Payload: payload}); err != nil {
		return false, err
	}
	return true, nil
}

func gitAncestor(ctx context.Context, repository, ancestor, descendant string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	command.Dir = repository
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("verify landed worktree: %w", err)
	}
	return true, nil
}

type RepositoryInfo struct {
	Root          string
	Head          string
	DefaultBranch string
}

// InspectRepository verifies the Git properties required by disposable
// worktrees and returns errors with commands or config changes that resolve the
// specific problem.
func InspectRepository(ctx context.Context, repository, defaultBranch string) (RepositoryInfo, error) {
	repo, err := filepath.Abs(repository)
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("resolve repository path: %w", err)
	}
	repo = filepath.Clean(repo)
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RepositoryInfo{}, fmt.Errorf("repository directory %s does not exist; create it or pass -repo PATH", repo)
		}
		return RepositoryInfo{}, fmt.Errorf("resolve repository directory %s: %w", repo, err)
	}
	root, err := gitOutput(ctx, resolvedRepo, "rev-parse", "--show-toplevel")
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("%s is not a Git repository; initialize it with `git init -b main`, add the intended files, and create an initial commit", resolvedRepo)
	}
	resolvedRoot, err := filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("resolve Git top level: %w", err)
	}
	if resolvedRoot != resolvedRepo {
		return RepositoryInfo{}, fmt.Errorf("Midgard must start at the Git top level; run `cd %s` or pass `-repo %s`", resolvedRoot, resolvedRoot)
	}
	head, err := gitOutput(ctx, resolvedRoot, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return RepositoryInfo{}, fmt.Errorf("Git repository %s has no initial commit; add the intended files and run `git commit -m \"Initial commit\"`", resolvedRoot)
	}
	branch := strings.TrimSpace(defaultBranch)
	if branch == "" {
		branch = "main"
	}
	if _, err := gitOutput(ctx, resolvedRoot, "check-ref-format", "--branch", branch); err != nil {
		return RepositoryInfo{}, fmt.Errorf("configured default branch %q is not a valid Git branch name; change default_branch in Midgard config or pass -default-branch NAME", branch)
	}
	if _, err := gitOutput(ctx, resolvedRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return RepositoryInfo{}, fmt.Errorf("configured default branch %q does not exist locally; set default_branch in .midgard/config.json or pass -default-branch NAME", branch)
	}
	return RepositoryInfo{Root: resolvedRoot, Head: strings.TrimSpace(head), DefaultBranch: branch}, nil
}

func (s Service) Cleanup(ctx context.Context, sessionID string) error {
	binding, err := s.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	return s.cleanupWorktree(ctx, binding)
}

func (s Service) cleanupWorktree(ctx context.Context, binding Binding) error {
	_, err := gitOutput(ctx, binding.RepositoryRoot, "worktree", "remove", "--force", binding.WorktreeRoot)
	return err
}

func safeName(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func randomID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
