package featuredelivery_test

import (
	"testing"

	"midgard/internal/policy"
	"midgard/internal/policy/featuredelivery"
)

func TestCompletionIsServerEvaluatedFromEvidence(t *testing.T) {
	p := featuredelivery.Policy{}
	incomplete := p.EvaluateCompletion(policy.CompletionEvidence{ObjectiveAddressed: true, GitDiffObserved: true, ActionsTerminal: true})
	if incomplete.Complete || len(incomplete.Reasons) == 0 {
		t.Fatalf("missing checks accepted: %#v", incomplete)
	}
	complete := p.EvaluateCompletion(policy.CompletionEvidence{
		ObjectiveAddressed: true, GitDiffObserved: true, ActionsTerminal: true,
		Checks: []policy.CheckEvidence{{Argv: []string{"go", "test", "./..."}, ExitCode: 0}},
	})
	if !complete.Complete {
		t.Fatalf("valid evidence rejected: %#v", complete)
	}
}

func TestResearchResponseNeedsNoImplementationCheck(t *testing.T) {
	p := featuredelivery.Policy{}
	accepted := p.EvaluateCompletion(policy.CompletionEvidence{
		ObjectiveAddressed: true, ResearchResponse: true, GitDiffObserved: true, ActionsTerminal: true,
	})
	if !accepted.Complete {
		t.Fatalf("researched response with pre-existing diff rejected: %#v", accepted)
	}
	rejected := p.EvaluateCompletion(policy.CompletionEvidence{
		ObjectiveAddressed: true, ResearchResponse: true, SourceChangedThisTurn: true, ActionsTerminal: true,
	})
	if rejected.Complete {
		t.Fatalf("researched response with a source change accepted: %#v", rejected)
	}
}

func TestDirectAdvisoryCanCompleteWithExistingWorktreeChanges(t *testing.T) {
	p := featuredelivery.Policy{}
	accepted := p.EvaluateCompletion(policy.CompletionEvidence{
		ObjectiveAddressed: true, AdvisoryResponse: true, GitDiffObserved: true, ActionsTerminal: true,
	})
	if !accepted.Complete {
		t.Fatalf("direct advisory rejected: %#v", accepted)
	}
	rejected := p.EvaluateCompletion(policy.CompletionEvidence{
		ObjectiveAddressed: true, AdvisoryResponse: true, SourceChangedThisTurn: true, ActionsTerminal: true,
	})
	if rejected.Complete {
		t.Fatalf("direct advisory with a source change accepted: %#v", rejected)
	}
}

func TestDirectInformationalResponseNeedsNoImplementationCheck(t *testing.T) {
	p := featuredelivery.Policy{}
	accepted := p.EvaluateCompletion(policy.CompletionEvidence{
		ObjectiveAddressed: true, InformationalResponse: true, ActionsTerminal: true,
	})
	if !accepted.Complete {
		t.Fatalf("direct informational response rejected: %#v", accepted)
	}
	rejected := p.EvaluateCompletion(policy.CompletionEvidence{
		ObjectiveAddressed: true, InformationalResponse: true, SourceChangedThisTurn: true, ActionsTerminal: true,
	})
	if rejected.Complete {
		t.Fatalf("direct informational response with a source change accepted: %#v", rejected)
	}
}

func TestPolicyOwnsToolsAndChecksWithoutRoles(t *testing.T) {
	configuration, err := (featuredelivery.Policy{}).Configure("make a focused edit", "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if configuration.PolicyID == "" || configuration.AgentProfile == "" || len(configuration.Tools) < 4 || len(configuration.RequiredChecks) == 0 {
		t.Fatalf("incomplete policy: %#v", configuration)
	}
	for _, tool := range configuration.Tools {
		if tool.Capability == "shell" && !tool.ApprovalRequired {
			t.Fatal("general shell must require approval")
		}
	}
}
