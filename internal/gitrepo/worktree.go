package gitrepo

import (
	"context"
	"os"
	"path/filepath"
)

func AddWorktree(ctx context.Context, repoPath, worktreePath, branch, startRef string) error {
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return err
	}
	_, err := Run(ctx, repoPath, "worktree", "add", "-b", branch, worktreePath, startRef)
	return err
}
