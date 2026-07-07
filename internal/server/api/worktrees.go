package api

import (
	"fmt"

	midgardtask "midgard/internal/task"
)

func selectWorktree(worktrees []midgardtask.WorktreeStatus, repoID string) (midgardtask.WorktreeStatus, error) {
	if repoID == "" {
		if len(worktrees) == 1 {
			return worktrees[0], nil
		}
		return midgardtask.WorktreeStatus{}, fmt.Errorf("repo id is required when task has %d worktrees", len(worktrees))
	}
	for _, wt := range worktrees {
		if wt.RepoID == repoID {
			return wt, nil
		}
	}
	return midgardtask.WorktreeStatus{}, fmt.Errorf("repo %q not found for task", repoID)
}
