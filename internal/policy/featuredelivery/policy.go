package featuredelivery

import (
	"errors"
	"time"

	"midgard/internal/policy"
)

type Policy struct {
	Checks [][]string
}

func (p Policy) Configure(objective, repositoryRoot string) (policy.Configuration, error) {
	if objective == "" {
		return policy.Configuration{}, errors.New("objective is required")
	}
	if repositoryRoot == "" {
		return policy.Configuration{}, errors.New("repository root is required")
	}
	checks := p.Checks
	if len(checks) == 0 {
		checks = [][]string{{"go", "test", "./..."}}
	}
	return policy.Configuration{
		PolicyID:     "feature-delivery/v1",
		AgentProfile: "coding",
		Tools: []policy.Tool{
			{Capability: "environment.describe", Description: "Describe the selected runtime environment without exposing values."},
			{Capability: "skill.search", Description: "Find matching available skill names and short descriptions."},
			{Capability: "skill.read", Description: "Read an installed skill or one of its referenced resources."},
			{Capability: "repo.search", Description: "Search the repository with bundled local retrieval and return bounded citations."},
			{Capability: "browser.run", Description: "Run bundled Heimdal browser QA inside the session worktree."},
			{Capability: "file.inspect", Description: "Read a bounded file inside the session worktree."},
			{Capability: "file.replace", Description: "Atomically replace an inspected file when its expected SHA-256 still matches."},
			{Capability: "git.diff", Description: "Inspect the authoritative Git diff."},
			{Capability: "patch.apply", Description: "Apply a Git-checked patch inside the worktree."},
			{Capability: "check.run", Description: "Run a deterministic argv-based check."},
			{Capability: "shell", ApprovalRequired: true, Description: "Run through an explicitly configured containment sandbox."},
			{Capability: "shell.poll", Description: "Read new bounded output and status from a session-owned background shell job."},
			{Capability: "shell.stop", Description: "Stop a session-owned background shell job."},
		},
		RequiredChecks: checks,
		Budget: policy.Budget{MaxTurns: 24, MaxActions: 200, MaxWallTime: 2 * time.Hour, MaxOutputBytes: 1 << 20,
			Context: policy.ContextBudget{LimitTokens: 128_000, CompactAtTokens: 96_000, TargetTokens: 64_000}},
	}, nil
}

func (Policy) EvaluateCompletion(e policy.CompletionEvidence) policy.CompletionDecision {
	var reasons []string
	nonImplementationResponse := e.ResearchResponse || e.AdvisoryResponse || e.InformationalResponse
	if e.Cancelled {
		reasons = append(reasons, "session is cancelled")
	}
	if !e.ObjectiveAddressed {
		reasons = append(reasons, "objective has not been established by server evidence")
	}
	if !e.GitDiffObserved && !e.VerifiedNoOp && !nonImplementationResponse {
		reasons = append(reasons, "neither a Git diff, verified no-op, researched response, nor direct response is recorded")
	}
	if nonImplementationResponse && e.SourceChangedThisTurn {
		reasons = append(reasons, "a non-edit response cannot include source changes from this turn")
	}
	if !e.ActionsTerminal {
		reasons = append(reasons, "one or more actions are non-terminal")
	}
	if !nonImplementationResponse && len(e.Checks) == 0 {
		reasons = append(reasons, "no deterministic check evidence is recorded")
	}
	if !nonImplementationResponse {
		for _, check := range e.Checks {
			if check.ExitCode != 0 {
				reasons = append(reasons, "a required check failed")
				break
			}
		}
	}
	return policy.CompletionDecision{Complete: len(reasons) == 0, Reasons: reasons}
}
