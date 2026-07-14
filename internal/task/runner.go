package task

import (
	"midgard/internal/cost"
	"midgard/internal/model"
	"midgard/internal/stream"
)

type RoleProviders map[model.Role]model.Provider

type RunnerOptions struct {
	Providers            RoleProviders
	ModelID              string
	Budget               stream.Budget
	Pricing              cost.Pricing
	MaxReviewCycles      int
	MaxSourceEditRepairs int
	MaxCommandTurns      int
	ExternalContext      string
}

type LoopResult struct {
	TaskID      string
	State       string
	PatchPath   string
	CostUSD     float64
	CostCaveats []string
	RoleRuns    []RoleRun
	Error       string
}

type RoleRun struct {
	Role                model.Role
	Status              string
	Artifact            string
	ProviderID          string
	ModelID             string
	ProviderFingerprint string
	Attempts            int
	InputTokens         int64
	OutputTokens        int64
}
