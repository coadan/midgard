package model_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"midgard/internal/artifact"
	"midgard/internal/model"
	"midgard/internal/model/providers/fake"
	"midgard/internal/stream"
)

func TestFakeProviderCompletesCoreRoles(t *testing.T) {
	cases := []struct {
		role   model.Role
		text   string
		status string
	}{
		{model.RolePlanner, "@report plan.mdx\n# Plan\n\nReady.\n@result status:ready artifact:plan.mdx checks:none\n", "ready"},
		{model.RoleImplementer, "@report implementation.mdx\n# Implementation\n\nNo changes.\n@result status:no-op artifact:implementation.mdx checks:none\n", "no-op"},
		{model.RoleReviewer, "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n", "approved"},
	}
	for _, tc := range cases {
		t.Run(tc.role.String(), func(t *testing.T) {
			packet, err := model.BuildPacket(model.PacketInput{
				TaskID:  "task_1",
				Role:    tc.role,
				ModelID: "fake-model",
				Context: "task context",
				Budget:  stream.DefaultBudget(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if packet.ProtocolVersion != model.ProtocolVersion || packet.ProtocolFingerprint == "" {
				t.Fatalf("protocol metadata missing: %#v", packet)
			}
			provider := fake.New(fake.Response{Text: tc.text, Usage: model.Usage{InputTokens: 10, OutputTokens: 5}})
			result, err := model.Runner{
				Provider: provider,
				Store:    artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
				Budget:   stream.DefaultBudget(),
			}.Run(context.Background(), packet)
			if err != nil {
				t.Fatal(err)
			}
			if result.Parsed.Result == nil || result.Parsed.Result.Status != tc.status {
				t.Fatalf("parsed result = %#v", result.Parsed.Result)
			}
			if result.Usage[0].ProviderID != "fake" || result.Usage[0].Role != tc.role {
				t.Fatalf("usage = %#v", result.Usage[0])
			}
		})
	}
}

func TestRepairPacketUsesRoleReportPath(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	repaired := model.RepairPacket(packet, &stream.RepairPacket{ErrorCodes: []string{"invalid_artifact_path"}})
	if !strings.Contains(repaired.RepairInstructions, "implementation.mdx") ||
		!strings.Contains(repaired.RepairInstructions, "Do not use repair.mdx") {
		t.Fatalf("repair instructions:\n%s", repaired.RepairInstructions)
	}
	for _, want := range []string{
		"parsed independently; it must be a complete valid stream",
		"Start the repair response with exactly @report implementation.mdx",
		"Do not emit @result until every required @payload, @edit, and @cmd frame has already been emitted",
		"emit nothing after it",
	} {
		if !strings.Contains(repaired.RepairInstructions, want) {
			t.Fatalf("repair instructions missing %q:\n%s", want, repaired.RepairInstructions)
		}
	}
}

func TestSourceEditRepairPacketReferencesDiagnostics(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	repaired := model.SourceEditRepairPacket(packet, model.SourceEditApplyFailure{
		Attempt:               1,
		RemainingAttempts:     1,
		File:                  "README.md",
		Repo:                  "repo1",
		Action:                "modify",
		Reason:                "test",
		ContentArtifact:       "artifact:patches/readme.diff",
		FailedPatchArtifact:   "artifact:source-edits/apply-failures/1/patch.diff",
		StderrArtifact:        "artifact:source-edits/apply-failures/1/stderr.txt",
		SourceContextArtifact: "artifact:source-edits/apply-failures/1/source-context.txt",
		Error:                 "git apply failed",
	})
	for _, want := range []string{
		"git could not apply the patch",
		"implementation.mdx",
		"artifact:source-edits/apply-failures/1/stderr.txt",
		"Patch the current worktree state",
	} {
		if !strings.Contains(repaired.RepairInstructions, want) {
			t.Fatalf("repair instructions missing %q:\n%s", want, repaired.RepairInstructions)
		}
	}
}

func TestRunnerRepairsMissingResult(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RolePlanner,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := fake.New(
		fake.Response{Text: "@report plan.mdx\n# Plan\n\nMissing result.\n", Usage: model.Usage{InputTokens: 10, OutputTokens: 5}},
		fake.Response{Text: "@report plan.mdx\n# Plan\n\nRepaired.\n@result status:ready artifact:plan.mdx checks:none\n", Usage: model.Usage{InputTokens: 4, OutputTokens: 3}},
	)
	result, err := model.Runner{
		Provider:   provider,
		Store:      artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:     stream.DefaultBudget(),
		MaxRepairs: 1,
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", result.Attempts)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "ready" {
		t.Fatalf("parsed result = %#v", result.Parsed.Result)
	}
	if !result.Packet.Repair {
		t.Fatal("final packet should be repair packet")
	}
}

func TestRunnerContinuesAfterCommandOnlyTurn(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := fake.New(
		fake.Response{Text: "@report implementation.mdx\nNeed to inspect files.\n@cmd repo:repo1 -- rg -n TODO .\n", Usage: model.Usage{InputTokens: 10, OutputTokens: 5}},
		fake.Response{Text: "@report implementation.mdx\nInspected command output.\n@result status:no-op artifact:implementation.mdx checks:none\n", Usage: model.Usage{InputTokens: 4, OutputTokens: 3}},
	)
	result, err := model.Runner{
		Provider: provider,
		Store:    artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:   stream.DefaultBudget(),
		CommandHandler: func(ctx context.Context, commands []stream.CommandProposal) (string, error) {
			if len(commands) != 1 || commands[0].Repo != "repo1" || commands[0].Command != "rg -n TODO ." {
				t.Fatalf("commands = %#v", commands)
			}
			return "command id:cmd_1 repo:repo1 exit:0 stdout:artifact:commands/cmd_1/stdout.txt stderr:artifact:commands/cmd_1/stderr.txt result:artifact:commands/cmd_1/result.json\nstdout_preview:\nREADME.md:1:TODO\n", nil
		},
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", result.Attempts)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "no-op" {
		t.Fatalf("parsed result = %#v", result.Parsed.Result)
	}
	packets := provider.Packets()
	if len(packets) != 2 {
		t.Fatalf("packets = %d, want 2", len(packets))
	}
	if !strings.Contains(packets[1].UserContent(), "stdout_preview") ||
		!strings.Contains(packets[1].UserContent(), "README.md:1:TODO") ||
		packets[1].Repair {
		t.Fatalf("continuation packet:\n%s", packets[1].UserContent())
	}
	if !strings.Contains(result.Raw, "midgard-provider-turn:1") ||
		!strings.Contains(result.Raw, "midgard-provider-turn:2") {
		t.Fatalf("raw transcript = %q", result.Raw)
	}
}

func TestRunnerContinuesReadyImplementerCommandTurnWithoutEdits(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := fake.New(
		fake.Response{Text: "@report implementation.mdx\nNeed command output.\n@cmd repo:repo1 -- sed -n '1,20p' README.md\n@result status:ready artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nInspected command output.\n@result status:no-op artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
	)
	var commandCalls int
	result, err := model.Runner{
		Provider: provider,
		Store:    artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:   stream.DefaultBudget(),
		CommandHandler: func(ctx context.Context, commands []stream.CommandProposal) (string, error) {
			commandCalls++
			if len(commands) != 1 || commands[0].Command != "sed -n '1,20p' README.md" {
				t.Fatalf("commands = %#v", commands)
			}
			return "command id:cmd_1 repo:repo1 exit:0\nstdout_preview:\n# fixture\n", nil
		},
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if commandCalls != 1 {
		t.Fatalf("command calls = %d, want 1", commandCalls)
	}
	if result.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", result.Attempts)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "no-op" {
		t.Fatalf("parsed result = %#v", result.Parsed.Result)
	}
}

func TestRunnerDoesNotContinueCommandTurnWithEdits(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := fake.New(
		fake.Response{Text: strings.Join([]string{
			"@report implementation.mdx",
			"Change plus check.",
			"@payload begin type:patch path:patches/readme.diff",
			"--- a/README.md",
			"+++ b/README.md",
			"@@ -1 +1 @@",
			"-old",
			"+new",
			"@payload end",
			"@edit file:README.md action:modify mode:patch content:artifact:patches/readme.diff reason:readme-change repo:repo1",
			"@cmd repo:repo1 -- sed -n '1,20p' README.md",
			"@result status:ready artifact:implementation.mdx checks:none",
			"",
		}, "\n"), Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nThis turn should not be requested.\n@result status:no-op artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
	)
	var commandCalls int
	result, err := model.Runner{
		Provider: provider,
		Store:    artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:   stream.DefaultBudget(),
		CommandHandler: func(ctx context.Context, commands []stream.CommandProposal) (string, error) {
			commandCalls++
			t.Fatalf("command handler should not run before source edits are returned: %#v", commands)
			return "", nil
		},
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if commandCalls != 0 {
		t.Fatalf("command calls = %d, want 0", commandCalls)
	}
	if result.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", result.Attempts)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "ready" {
		t.Fatalf("parsed result = %#v", result.Parsed.Result)
	}
	if len(result.Parsed.Edits) != 1 {
		t.Fatalf("edits = %#v, want 1", result.Parsed.Edits)
	}
	if len(result.Parsed.Commands) != 1 {
		t.Fatalf("commands = %#v, want 1", result.Parsed.Commands)
	}
	if len(provider.Packets()) != 1 {
		t.Fatalf("packets = %d, want 1", len(provider.Packets()))
	}
}

func TestRunnerContinuesBlockedImplementerCommandTurn(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := fake.New(
		fake.Response{Text: "@report implementation.mdx\nNeed command output.\n@cmd repo:repo1 -- sed -n '1,20p' README.md\n@result status:blocked artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nInspected command output.\n@result status:no-op artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
	)
	var commandCalls int
	result, err := model.Runner{
		Provider: provider,
		Store:    artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:   stream.DefaultBudget(),
		CommandHandler: func(ctx context.Context, commands []stream.CommandProposal) (string, error) {
			commandCalls++
			if len(commands) != 1 || commands[0].Command != "sed -n '1,20p' README.md" {
				t.Fatalf("commands = %#v", commands)
			}
			return "command id:cmd_1 repo:repo1 exit:0\nstdout_preview:\n# fixture\n", nil
		},
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if commandCalls != 1 {
		t.Fatalf("command calls = %d, want 1", commandCalls)
	}
	if result.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", result.Attempts)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "no-op" {
		t.Fatalf("parsed result = %#v", result.Parsed.Result)
	}
}

func TestRunnerContinuesMalformedBlockedCommandTurn(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := fake.New(
		fake.Response{Text: "Need command output.\n@result status:blocked artifact:implementation.mdx checks:none\n@cmd repo:repo1 -- sed -n '1,20p' README.md\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nInspected command output.\n@result status:no-op artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
	)
	var commandCalls int
	result, err := model.Runner{
		Provider: provider,
		Store:    artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:   stream.DefaultBudget(),
		CommandHandler: func(ctx context.Context, commands []stream.CommandProposal) (string, error) {
			commandCalls++
			if len(commands) != 1 || commands[0].Command != "sed -n '1,20p' README.md" {
				t.Fatalf("commands = %#v", commands)
			}
			return "command id:cmd_1 repo:repo1 exit:0\nstdout_preview:\n# fixture\n", nil
		},
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if commandCalls != 1 {
		t.Fatalf("command calls = %d, want 1", commandCalls)
	}
	if result.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", result.Attempts)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "no-op" {
		t.Fatalf("parsed result = %#v", result.Parsed.Result)
	}
}

func TestRunnerRepairAfterCommandContinuationKeepsCommandEvidence(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := fake.New(
		fake.Response{Text: "@report implementation.mdx\nNeed to inspect.\n@cmd repo:repo1 -- rg -n TODO .\n", Usage: model.Usage{}},
		fake.Response{Text: "@result status:no-op artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nUsed command output after repair.\n@result status:no-op artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
	)
	result, err := model.Runner{
		Provider:   provider,
		Store:      artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:     stream.DefaultBudget(),
		MaxRepairs: 1,
		CommandHandler: func(ctx context.Context, commands []stream.CommandProposal) (string, error) {
			if len(commands) != 1 || commands[0].Command != "rg -n TODO ." {
				t.Fatalf("commands = %#v", commands)
			}
			return "command id:cmd_1 repo:repo1 exit:0\nstdout_preview:\nREADME.md:1:TODO\n", nil
		},
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", result.Attempts)
	}
	packets := provider.Packets()
	if len(packets) != 3 {
		t.Fatalf("packets = %d, want 3", len(packets))
	}
	repairPrompt := packets[2].UserContent()
	if !strings.Contains(repairPrompt, "stdout_preview") ||
		!strings.Contains(repairPrompt, "README.md:1:TODO") ||
		!strings.Contains(repairPrompt, "missing_report") {
		t.Fatalf("repair prompt lost command evidence:\n%s", repairPrompt)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "no-op" {
		t.Fatalf("parsed result = %#v", result.Parsed.Result)
	}
}

func TestRunnerRetriesBlockedSourceEditRepairWithoutProgress(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	packet = model.SourceEditRepairPacket(packet, model.SourceEditApplyFailure{
		Attempt:               1,
		RemainingAttempts:     1,
		File:                  "README.md",
		Repo:                  "repo1",
		Action:                "modify",
		Reason:                "test",
		ContentArtifact:       "artifact:patches/readme.diff",
		FailedPatchArtifact:   "artifact:source-edits/apply-failures/1/patch.diff",
		StderrArtifact:        "artifact:source-edits/apply-failures/1/stderr.txt",
		SourceContextArtifact: "artifact:source-edits/apply-failures/1/source-context.txt",
		PartialApplied:        true,
		Error:                 "git apply failed",
	})
	provider := fake.New(
		fake.Response{Text: "@report implementation.mdx\nRepairing the partial application.\n@result status:blocked artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nNo further source change is appropriate.\n@result status:no-op artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
	)
	result, err := model.Runner{
		Provider: provider,
		Store:    artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:   stream.DefaultBudget(),
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", result.Attempts)
	}
	packets := provider.Packets()
	if len(packets) != 2 || !strings.Contains(packets[1].UserContent(), "previous repair response returned a terminal status") {
		t.Fatalf("retry packet missing repair guidance:\n%v", packets)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "no-op" {
		t.Fatalf("parsed result = %#v", result.Parsed.Result)
	}
}

func TestRunnerRetriesEmptyBlockedAfterCommandResults(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := fake.New(
		fake.Response{Text: "@report implementation.mdx\nNeed to inspect.\n@cmd repo:repo1 -- rg -n TODO .\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\n@result status:blocked artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nNeed a narrower slice.\n@cmd repo:repo1 -- sed -n '1,20p' README.md\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nInspected enough.\n@result status:no-op artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
	)
	var commandCalls int
	result, err := model.Runner{
		Provider: provider,
		Store:    artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:   stream.DefaultBudget(),
		CommandHandler: func(ctx context.Context, commands []stream.CommandProposal) (string, error) {
			commandCalls++
			return "command id:cmd_1 repo:repo1 exit:0\nstdout_preview:\nREADME.md:1:TODO\n", nil
		},
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if commandCalls != 2 {
		t.Fatalf("command calls = %d, want 2", commandCalls)
	}
	if result.Attempts != 4 {
		t.Fatalf("attempts = %d, want 4", result.Attempts)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "no-op" {
		t.Fatalf("parsed result = %#v", result.Parsed.Result)
	}
	packets := provider.Packets()
	if len(packets) != 4 || !strings.Contains(packets[2].UserContent(), "Midgard did not accept it") {
		t.Fatalf("corrective packet missing:\n%v", packets)
	}
}

func TestRunnerAcceptsConcreteBlockedAfterCommandResults(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := fake.New(
		fake.Response{Text: "@report implementation.mdx\nNeed to inspect.\n@cmd repo:repo1 -- rg -n TODO .\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nBlocked: the requested path is outside the registered repository, so no bounded command can change it.\n@result status:blocked artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
	)
	result, err := model.Runner{
		Provider: provider,
		Store:    artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:   stream.DefaultBudget(),
		CommandHandler: func(context.Context, []stream.CommandProposal) (string, error) {
			return "command id:cmd_1 repo:repo1 exit:0\nstdout_preview:\nREADME.md:1:TODO\n", nil
		},
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", result.Attempts)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "blocked" {
		t.Fatalf("parsed result = %#v", result.Parsed.Result)
	}
}

func TestRunnerRetriesContextOnlyFailedAfterCommandResults(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := fake.New(
		fake.Response{Text: "@report implementation.mdx\nNeed to inspect.\n@cmd repo:repo1 -- rg -n TODO .\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nPrevious implementation stream stopped after requesting repository inspection, before any source edits could be completed.\n@result status:failed artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nInspected enough.\n@result status:no-op artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
	)
	result, err := model.Runner{
		Provider: provider,
		Store:    artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:   stream.DefaultBudget(),
		CommandHandler: func(context.Context, []stream.CommandProposal) (string, error) {
			return "command id:cmd_1 repo:repo1 exit:0\nstdout_preview:\nREADME.md:1:TODO\n", nil
		},
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", result.Attempts)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "no-op" {
		t.Fatalf("parsed result = %#v", result.Parsed.Result)
	}
}

func TestRunnerRetriesContextOnlyFailedWithoutCommand(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := fake.New(
		fake.Response{Text: "@report implementation.mdx\nNeed inspect.\n@result status:failed artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nNo source change is appropriate.\n@result status:no-op artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
	)
	result, err := model.Runner{
		Provider: provider,
		Store:    artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:   stream.DefaultBudget(),
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", result.Attempts)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "no-op" {
		t.Fatalf("parsed result = %#v", result.Parsed.Result)
	}
	packets := provider.Packets()
	if len(packets) != 2 || !strings.Contains(packets[1].UserContent(), "did not emit @cmd") {
		t.Fatalf("terminal retry packet missing:\n%v", packets)
	}
}

func TestRunnerRetriesContextOnlyBlockedSourceEditRepair(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	packet = model.SourceEditRepairPacket(packet, model.SourceEditApplyFailure{
		Attempt:             1,
		RemainingAttempts:   1,
		File:                "README.md",
		Repo:                "repo1",
		Action:              "modify",
		Reason:              "test",
		ContentArtifact:     "artifact:patches/readme.diff",
		FailedPatchArtifact: "artifact:source-edits/apply-failures/1/patch.diff",
		StderrArtifact:      "artifact:source-edits/apply-failures/1/stderr.txt",
		Error:               "No valid patches in input",
	})
	provider := fake.New(
		fake.Response{Text: "@report implementation.mdx\nRepairing the rejected source edit by inspecting the current worktree and emitting a valid unified diff against the current file.\n@result status:blocked artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nNo source change is appropriate.\n@result status:no-op artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
	)
	result, err := model.Runner{
		Provider: provider,
		Store:    artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:   stream.DefaultBudget(),
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", result.Attempts)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "no-op" {
		t.Fatalf("parsed result = %#v", result.Parsed.Result)
	}
	packets := provider.Packets()
	if len(packets) != 2 ||
		!strings.Contains(packets[1].UserContent(), "previous repair response returned a terminal status") ||
		!strings.Contains(packets[1].UserContent(), "No valid patches in input") {
		t.Fatalf("repair continuation packet missing context:\n%v", packets)
	}
}

func TestRunnerCommandContinuationLimitGivesFinalNoCommandTurn(t *testing.T) {
	packet, err := model.BuildPacket(model.PacketInput{
		TaskID:  "task_1",
		Role:    model.RoleImplementer,
		ModelID: "fake-model",
		Context: "task context",
		Budget:  stream.DefaultBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := fake.New(
		fake.Response{Text: "@report implementation.mdx\n@cmd repo:repo1 -- pwd\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\n@cmd repo:repo1 -- pwd\n", Usage: model.Usage{}},
		fake.Response{Text: "@report implementation.mdx\nNo source change is appropriate.\n@result status:no-op artifact:implementation.mdx checks:none\n", Usage: model.Usage{}},
	)
	result, err := model.Runner{
		Provider:        provider,
		Store:           artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")),
		Budget:          stream.DefaultBudget(),
		MaxCommandTurns: 1,
		CommandHandler: func(context.Context, []stream.CommandProposal) (string, error) {
			return "command id:cmd_1 repo:repo1 exit:0\n", nil
		},
	}.Run(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", result.Attempts)
	}
	if result.Parsed.Result == nil || result.Parsed.Result.Status != "no-op" {
		t.Fatalf("result = %#v", result.Parsed.Result)
	}
	packets := provider.Packets()
	if len(packets) != 3 ||
		!strings.Contains(packets[2].UserContent(), "Command inspection budget is exhausted") ||
		!strings.Contains(packets[2].UserContent(), "Midgard will not execute more @cmd") {
		t.Fatalf("limit packet missing no-command instruction:\n%#v", packets)
	}
}
