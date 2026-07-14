package task

func nextAction(taskState string, worktrees []WorktreeStatus) string {
	switch taskState {
	case "completed":
		return "done"
	case "failed":
		return "inspect-failure"
	case "blocked":
		for _, wt := range worktrees {
			if wt.Dirty {
				return "review-diff"
			}
		}
		return "resolve-blocker"
	}
	for _, wt := range worktrees {
		if wt.Dirty {
			return "review-diff"
		}
	}
	return "implement"
}
