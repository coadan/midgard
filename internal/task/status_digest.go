package task

func nextAction(worktrees []WorktreeStatus) string {
	for _, wt := range worktrees {
		if wt.Dirty {
			return "review-diff"
		}
	}
	return "implement"
}
