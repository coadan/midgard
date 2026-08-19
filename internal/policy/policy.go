package policy

import "time"

type Tool struct {
	Capability       string
	ApprovalRequired bool
	Description      string
}

type Budget struct {
	MaxTurns       int
	MaxActions     int
	MaxWallTime    time.Duration
	MaxOutputBytes int
	Context        ContextBudget
}

type ContextBudget struct {
	LimitTokens     int64
	CompactAtTokens int64
	TargetTokens    int64
}

type Configuration struct {
	PolicyID       string
	AgentProfile   string
	Tools          []Tool
	RequiredChecks [][]string
	Budget         Budget
}

type CheckEvidence struct {
	Argv     []string
	ExitCode int
}

type CompletionEvidence struct {
	ObjectiveAddressed bool
	GitDiffObserved    bool
	VerifiedNoOp       bool
	// ResearchResponse is true only when the server observed successful
	// repository or skill research and no source-changing action succeeded in
	// this turn. It permits an evidence-backed answer without treating a
	// question as a source-change task, even if the worktree was already dirty.
	ResearchResponse bool
	// AdvisoryResponse is true only when policy classified the objective as a
	// direct recommendation or prioritization request and no source-changing
	// action succeeded in this turn. It permits an answer based on the existing
	// chat and repository context, including a worktree that was already dirty
	// before this turn began.
	AdvisoryResponse bool
	// InformationalResponse is true only when policy classified the objective as
	// a direct non-mutating question and no source-changing action succeeded in
	// this turn. It avoids treating an explanation as an implementation task.
	InformationalResponse bool
	SourceChangedThisTurn bool
	ActionsTerminal       bool
	Cancelled             bool
	Checks                []CheckEvidence
}

type CompletionDecision struct {
	Complete bool     `json:"complete"`
	Reasons  []string `json:"reasons"`
}

type Policy interface {
	Configure(objective, repositoryRoot string) (Configuration, error)
	EvaluateCompletion(CompletionEvidence) CompletionDecision
}
