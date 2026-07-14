package task

import (
	"context"
	"os"
	"path/filepath"

	"midgard/internal/gitrepo"
	"midgard/internal/workbench"
)

type CleanupOptions struct {
	Worktrees bool
	Artifacts bool
}

type CleanupResult struct {
	RemovedWorktrees []string
	RemovedArtifacts string
}

func Cleanup(ctx context.Context, root, taskID string, opts CleanupOptions) (result CleanupResult, retErr error) {
	execution, err := AcquireExecution(ctx, root, taskID)
	if err != nil {
		return CleanupResult{}, err
	}
	defer func() {
		if err := execution.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	ctx = execution.Context
	status, err := Status(ctx, root, taskID)
	if err != nil {
		return CleanupResult{}, err
	}
	wbStatus, err := workbench.Status(root)
	if err != nil {
		return CleanupResult{}, err
	}
	layout := workbench.NewLayout(wbStatus.Root)
	result = CleanupResult{}
	if opts.Worktrees {
		for _, wt := range status.Worktrees {
			if err := CheckExecution(ctx); err != nil {
				return CleanupResult{}, err
			}
			if _, err := os.Stat(wt.Path); err == nil {
				if _, err := gitrepo.Run(ctx, wt.Path, "worktree", "remove", "--force", wt.Path); err != nil {
					return CleanupResult{}, err
				}
				result.RemovedWorktrees = append(result.RemovedWorktrees, wt.Path)
			}
		}
	}
	if opts.Artifacts {
		if err := CheckExecution(ctx); err != nil {
			return CleanupResult{}, err
		}
		artifactRoot := filepath.Join(layout.Artifacts, taskID)
		if err := os.RemoveAll(artifactRoot); err != nil {
			return CleanupResult{}, err
		}
		result.RemovedArtifacts = artifactRoot
	}
	return result, nil
}
