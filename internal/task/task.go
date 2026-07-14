package task

import "midgard/internal/state"

const StateOpen = "open"

type CreateOptions struct {
	ID        string
	Objective string
	RepoIDs   []string
}

type CreateResult struct {
	Task       state.Task
	Worktrees  []WorktreeStatus
	ReportPath string
}

type StatusResult struct {
	Task          state.Task
	Worktrees     []WorktreeStatus
	NextAction    string
	ForgeGates    bool
	ForgeReady    bool
	ForgeBlockers []string
	ForgeWarnings []string
}

type WorktreeStatus struct {
	RepoID      string
	Path        string
	StartRef    string
	StartCommit string
	Dirty       bool
	Porcelain   string
}
