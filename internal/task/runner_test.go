package task

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"midgard/internal/cost"
	"midgard/internal/model"
	"midgard/internal/model/providers/fake"
	"midgard/internal/state"
	"midgard/internal/workbench"
)

func TestRunLoopFakeProviderCompletesTask(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "loop-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, root, CreateOptions{ID: "task_e2e", Objective: "change readme"}); err != nil {
		t.Fatal(err)
	}

	artifactDir := filepath.Join(root, ".midgard", "artifacts", "task_e2e")
	scriptPath := filepath.Join(artifactDir, "scripts", "edit_readme.py")
	implementerStream := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"",
		"Write a small Python helper and run a focused diff check.",
		"",
		"@payload begin type:script path:scripts/edit_readme.py lang:python",
		"from pathlib import Path",
		"Path('README.md').write_text('# changed by midgard\\n')",
		"@payload end",
		"@edit file:README.md action:modify mode:script content:artifact:scripts/edit_readme.py reason:test-change",
		"@cmd repo:repo1 -- python3 " + scriptPath,
		"@cmd repo:repo1 -- git diff --check",
		"@result status:ready artifact:implementation.mdx checks:diff-check",
		"",
	}, "\n")

	result, err := RunLoop(ctx, root, "task_e2e", RunnerOptions{
		ModelID: "fake-model",
		Providers: RoleProviders{
			model.RolePlanner: fake.New(fake.Response{
				Text:  "@report plan.mdx\n# Plan\n\nModify README and verify the diff.\n@result status:ready artifact:plan.mdx checks:diff-check\n",
				Usage: model.Usage{InputTokens: 100, OutputTokens: 40},
			}),
			model.RoleImplementer: fake.New(fake.Response{
				Text:  implementerStream,
				Usage: model.Usage{InputTokens: 120, OutputTokens: 80},
			}),
			model.RoleReviewer: fake.New(fake.Response{
				Text:  "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n",
				Usage: model.Usage{InputTokens: 90, OutputTokens: 30},
			}),
		},
		Pricing: cost.Pricing{ID: "fake-pricing", InputUSDPerMillion: 1, OutputUSDPerMillion: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Fatalf("state = %s", result.State)
	}
	if result.PatchPath != "patch.diff" || result.CostUSD <= 0 {
		t.Fatalf("loop result = %#v", result)
	}
	for _, path := range []string{"plan.mdx", "implementation.mdx", "review.mdx", "patch.diff", "streams/planner.stream"} {
		if _, err := os.Stat(filepath.Join(artifactDir, path)); err != nil {
			t.Fatalf("%s missing: %v", path, err)
		}
	}
	plan, err := os.ReadFile(filepath.Join(artifactDir, "plan.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan), "## Midgard Provenance") ||
		!strings.Contains(string(plan), "- provider: fake") ||
		!strings.Contains(string(plan), "- model: fake-model") ||
		!strings.Contains(string(plan), "- protocol: midgard-agent-stream-v1") {
		t.Fatalf("plan provenance missing:\n%s", plan)
	}
	patch, err := os.ReadFile(filepath.Join(artifactDir, "patch.diff"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "changed by midgard") {
		t.Fatalf("patch.diff:\n%s", patch)
	}
	status, err := Status(ctx, root, "task_e2e")
	if err != nil {
		t.Fatal(err)
	}
	if status.Task.State != "completed" {
		t.Fatalf("db task state = %s", status.Task.State)
	}
	taskReport, err := os.ReadFile(filepath.Join(root, ".midgard", "tasks", "task_e2e.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(taskReport), "## Midgard Run Summary") ||
		!strings.Contains(string(taskReport), "provider_fingerprint:sha256:") ||
		!strings.Contains(string(taskReport), "model:fake-model") ||
		!strings.Contains(string(taskReport), "cost: $") {
		t.Fatalf("task report missing run summary:\n%s", taskReport)
	}

	db, err := state.Open(ctx, filepath.Join(root, ".midgard", "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.UsageRecord(ctx, "usage_task_e2e_planner_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CostRollup(ctx, "cost_task_e2e_reviewer_1"); err != nil {
		t.Fatal(err)
	}
	events, err := db.EventsForTask(ctx, "task_e2e")
	if err != nil {
		t.Fatal(err)
	}
	var sawCommand bool
	var sawRole bool
	for _, event := range events {
		if event.Type == "command.finished" {
			sawCommand = true
		}
		if event.Type == "role.completed" {
			sawRole = true
		}
	}
	if !sawCommand || !sawRole {
		t.Fatalf("events = %#v", events)
	}
}

func TestRunLoopContinuesImplementerAfterInspectionCommand(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "command-continuation-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, root, CreateOptions{ID: "task_command_continue", Objective: "inspect then change readme"}); err != nil {
		t.Fatal(err)
	}

	implementer := fake.New(
		fake.Response{
			Text: strings.Join([]string{
				"@report implementation.mdx",
				"Need to inspect README before editing.",
				"@cmd repo:repo1 -- sed -n '1,5p' README.md",
				"",
			}, "\n"),
			Usage: model.Usage{InputTokens: 20, OutputTokens: 10},
		},
		fake.Response{
			Text: strings.Join([]string{
				"@report implementation.mdx",
				"Inspected README and prepared a patch.",
				"@payload begin type:patch path:patches/readme.diff",
				"--- a/README.md",
				"+++ b/README.md",
				"@@ -1 +1 @@",
				"-# fixture",
				"+# changed after inspection",
				"@payload end",
				"@edit file:README.md action:modify mode:patch content:artifact:patches/readme.diff reason:readme-change repo:repo1",
				"@result status:ready artifact:implementation.mdx checks:none",
				"",
			}, "\n"),
			Usage: model.Usage{InputTokens: 30, OutputTokens: 20},
		},
	)
	result, err := RunLoop(ctx, root, "task_command_continue", RunnerOptions{
		ModelID: "fake-model",
		Providers: RoleProviders{
			model.RolePlanner: fake.New(fake.Response{
				Text:  "@report plan.mdx\nInspect README, then edit it.\n@result status:ready artifact:plan.mdx checks:none\n",
				Usage: model.Usage{InputTokens: 10, OutputTokens: 5},
			}),
			model.RoleImplementer: implementer,
			model.RoleReviewer: fake.New(fake.Response{
				Text:  "@report review.mdx\nApproved.\n@result status:approved artifact:review.mdx findings:none\n",
				Usage: model.Usage{InputTokens: 10, OutputTokens: 5},
			}),
		},
		Pricing: cost.Pricing{ID: "fake-pricing", InputUSDPerMillion: 1, OutputUSDPerMillion: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Fatalf("state = %s", result.State)
	}
	if implementer.Calls() != 2 {
		t.Fatalf("implementer calls = %d, want 2", implementer.Calls())
	}
	packets := implementer.Packets()
	if len(packets) != 2 || !strings.Contains(packets[1].UserContent(), "stdout_preview") ||
		!strings.Contains(packets[1].UserContent(), "# fixture") {
		t.Fatalf("continuation packet missing command output")
	}
	patch, err := os.ReadFile(filepath.Join(root, ".midgard", "artifacts", "task_command_continue", "patch.diff"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "changed after inspection") {
		t.Fatalf("patch.diff:\n%s", patch)
	}
	db, err := state.Open(ctx, filepath.Join(root, ".midgard", "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.EventsForTask(ctx, "task_command_continue")
	if err != nil {
		t.Fatal(err)
	}
	var sawCommand bool
	for _, event := range events {
		if event.Type == "command.finished" {
			sawCommand = true
		}
	}
	if !sawCommand {
		t.Fatalf("events = %#v", events)
	}
}

func TestRunLoopAppliesPatchEditPayload(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "patch-edit-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, root, CreateOptions{ID: "task_patch_edit", Objective: "change readme"}); err != nil {
		t.Fatal(err)
	}

	implementerStream := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"",
		"Applied a README patch.",
		"",
		"@payload begin type:patch path:patches/readme.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-# fixture",
		"+# changed by patch edit",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme.diff reason:readme-change repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")

	result, err := RunLoop(ctx, root, "task_patch_edit", RunnerOptions{
		ModelID: "fake-model",
		Providers: RoleProviders{
			model.RolePlanner:     fake.New(fake.Response{Text: "@report plan.mdx\n# Plan\n\nModify README.\n@result status:ready artifact:plan.mdx checks:none\n"}),
			model.RoleImplementer: fake.New(fake.Response{Text: implementerStream}),
			model.RoleReviewer:    fake.New(fake.Response{Text: "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"}),
		},
		Pricing: cost.Pricing{ID: "fake-pricing", InputUSDPerMillion: 1, OutputUSDPerMillion: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || result.PatchPath != "patch.diff" {
		t.Fatalf("result = %#v", result)
	}
	artifactDir := filepath.Join(root, ".midgard", "artifacts", "task_patch_edit")
	patch, err := os.ReadFile(filepath.Join(artifactDir, "patch.diff"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "changed by patch edit") {
		t.Fatalf("patch.diff:\n%s", patch)
	}
	status, err := Status(ctx, root, "task_patch_edit")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Worktrees[0].Dirty {
		t.Fatalf("worktree should be dirty after patch edit: %#v", status.Worktrees[0])
	}
	db, err := state.Open(ctx, filepath.Join(root, ".midgard", "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.EventsForTask(ctx, "task_patch_edit")
	if err != nil {
		t.Fatal(err)
	}
	var sawEdit bool
	for _, event := range events {
		if event.Type == "source_edit.applied" && strings.Contains(event.Payload, "README.md") {
			sawEdit = true
		}
	}
	if !sawEdit {
		t.Fatalf("events = %#v, want source_edit.applied", events)
	}
}

func TestRunLoopAppliesSharedPatchArtifactOnce(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "shared-patch-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, root, CreateOptions{ID: "task_shared_patch", Objective: "change README"}); err != nil {
		t.Fatal(err)
	}
	patch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"@payload begin type:patch path:patches/shared.diff",
		"diff --git a/README.md b/README.md",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-# fixture",
		"+# shared patch",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/shared.diff reason:shared repo:repo1",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/shared.diff reason:shared-again repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	result, err := RunLoop(ctx, root, "task_shared_patch", RunnerOptions{
		ModelID: "fake-model",
		Providers: RoleProviders{
			model.RolePlanner:     fake.New(fake.Response{Text: "@report plan.mdx\n# Plan\n\nModify README.\n@result status:ready artifact:plan.mdx checks:none\n"}),
			model.RoleImplementer: fake.New(fake.Response{Text: patch}),
			model.RoleReviewer:    fake.New(fake.Response{Text: "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Fatalf("state = %s, want completed", result.State)
	}
	artifactDir := filepath.Join(root, ".midgard", "artifacts", "task_shared_patch")
	diff, err := os.ReadFile(filepath.Join(artifactDir, "patch.diff"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(diff), "+# shared patch") {
		t.Fatalf("patch.diff:\n%s", diff)
	}
	db, err := state.Open(ctx, filepath.Join(root, ".midgard", "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.EventsForTask(ctx, "task_shared_patch")
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	for _, event := range events {
		if event.Type == "source_edit.applied" {
			applied++
		}
		if event.Type == "source_edit.apply_failed" {
			t.Fatalf("unexpected apply failure: %#v", event)
		}
	}
	if applied != 1 {
		t.Fatalf("source_edit.applied events = %d, want 1: %#v", applied, events)
	}
}

func TestRunLoopRepairsPatchApplyFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "patch-repair-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, root, CreateOptions{ID: "task_patch_repair", Objective: "change README heading"}); err != nil {
		t.Fatal(err)
	}

	badPatch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"@payload begin type:patch path:patches/readme-bad.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-# missing",
		"+# repaired after apply failure",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-bad.diff reason:first-pass repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	fixedPatch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"Repaired the patch against the current worktree.",
		"@payload begin type:patch path:patches/readme-fixed.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-# fixture",
		"+# repaired after apply failure",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-fixed.diff reason:repair repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	implementer := fake.New(
		fake.Response{Text: badPatch, Usage: model.Usage{InputTokens: 10, OutputTokens: 20}},
		fake.Response{Text: fixedPatch, Usage: model.Usage{InputTokens: 30, OutputTokens: 40}},
	)
	result, err := RunLoop(ctx, root, "task_patch_repair", RunnerOptions{
		ModelID: "fake-model",
		Providers: RoleProviders{
			model.RolePlanner:     fake.New(fake.Response{Text: "@report plan.mdx\n# Plan\n\nModify README.\n@result status:ready artifact:plan.mdx checks:none\n"}),
			model.RoleImplementer: implementer,
			model.RoleReviewer:    fake.New(fake.Response{Text: "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"}),
		},
		MaxSourceEditRepairs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || len(result.RoleRuns) != 3 {
		t.Fatalf("result = %#v", result)
	}
	if implementer.Calls() != 2 {
		t.Fatalf("implementer calls = %d, want 2", implementer.Calls())
	}
	if result.RoleRuns[1].Attempts != 2 || result.RoleRuns[1].InputTokens != 40 || result.RoleRuns[1].OutputTokens != 60 {
		t.Fatalf("implementer run = %#v", result.RoleRuns[1])
	}
	packets := implementer.Packets()
	if len(packets) != 2 || !packets[1].Repair ||
		!strings.Contains(packets[1].RepairInstructions, "git could not apply the patch") ||
		!strings.Contains(packets[1].RepairInstructions, "artifact:source-edits/apply-failures/1/stderr.txt") {
		t.Fatalf("repair packet = %#v", packets)
	}

	artifactDir := filepath.Join(root, ".midgard", "artifacts", "task_patch_repair")
	for _, path := range []string{
		"source-edits/apply-failures/1/patch.diff",
		"source-edits/apply-failures/1/stderr.txt",
		"source-edits/apply-failures/1/source-context.txt",
	} {
		if _, err := os.Stat(filepath.Join(artifactDir, path)); err != nil {
			t.Fatalf("missing failure artifact %s: %v", path, err)
		}
	}
	patch, err := os.ReadFile(filepath.Join(artifactDir, "patch.diff"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "repaired after apply failure") {
		t.Fatalf("patch.diff:\n%s", patch)
	}
	taskReport, err := os.ReadFile(filepath.Join(root, ".midgard", "tasks", "task_patch_repair.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Source Edit Summary", "apply_failed attempt:1 file:README.md", "repair_requested after_attempt:1", "applied file:README.md"} {
		if !strings.Contains(string(taskReport), want) {
			t.Fatalf("task report missing %q:\n%s", want, taskReport)
		}
	}
	db, err := state.Open(ctx, filepath.Join(root, ".midgard", "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.EventsForTask(ctx, "task_patch_repair")
	if err != nil {
		t.Fatal(err)
	}
	var sawFailure, sawRepair, sawApplied bool
	for _, event := range events {
		switch event.Type {
		case "source_edit.apply_failed":
			sawFailure = strings.Contains(event.Payload, "source-edits/apply-failures/1/stderr.txt")
		case "source_edit.repair_requested":
			sawRepair = true
		case "source_edit.applied":
			sawApplied = true
		}
	}
	if !sawFailure || !sawRepair || !sawApplied {
		t.Fatalf("events = %#v", events)
	}
}

func TestRunLoopRepairsPartiallyAppliedPatch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "partial-patch-repair-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, root, CreateOptions{ID: "task_partial_patch_repair", Objective: "change README heading and add detail"}); err != nil {
		t.Fatal(err)
	}

	partialPatch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"@payload begin type:patch path:patches/readme-partial.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-# fixture",
		"+# partial heading",
		"@@ -2 +2 @@",
		"-missing",
		"+detail line",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-partial.diff reason:first-pass repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	repairPatch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"Completed the remaining hunk on top of the partial diff.",
		"@payload begin type:patch path:patches/readme-partial-repair.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1,2 @@",
		" # partial heading",
		"+detail line",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-partial-repair.diff reason:repair repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	implementer := fake.New(fake.Response{Text: partialPatch}, fake.Response{Text: repairPatch})
	result, err := RunLoop(ctx, root, "task_partial_patch_repair", RunnerOptions{
		ModelID: "fake-model",
		Providers: RoleProviders{
			model.RolePlanner:     fake.New(fake.Response{Text: "@report plan.mdx\n# Plan\n\nModify README.\n@result status:ready artifact:plan.mdx checks:none\n"}),
			model.RoleImplementer: implementer,
			model.RoleReviewer:    fake.New(fake.Response{Text: "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"}),
		},
		MaxSourceEditRepairs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || implementer.Calls() != 2 {
		t.Fatalf("result = %#v implementer calls = %d", result, implementer.Calls())
	}
	packets := implementer.Packets()
	if len(packets) != 2 ||
		!strings.Contains(packets[1].RepairInstructions, "partial_applied:true") ||
		!strings.Contains(packets[1].RepairInstructions, "current_diff:") ||
		!strings.Contains(packets[1].RepairInstructions, "+# partial heading") {
		t.Fatalf("repair packet = %#v", packets)
	}
	artifactDir := filepath.Join(root, ".midgard", "artifacts", "task_partial_patch_repair")
	patch, err := os.ReadFile(filepath.Join(artifactDir, "patch.diff"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+# partial heading", "+detail line"} {
		if !strings.Contains(string(patch), want) {
			t.Fatalf("patch.diff missing %q:\n%s", want, patch)
		}
	}
	status, err := Status(ctx, root, "task_partial_patch_repair")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(status.Worktrees[0].Path, "README.md.rej")); !os.IsNotExist(err) {
		t.Fatalf("README.md.rej err = %v, want not exist", err)
	}
	taskReport, err := os.ReadFile(filepath.Join(root, ".midgard", "tasks", "task_partial_patch_repair.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(taskReport), "partial_applied:true") {
		t.Fatalf("task report missing partial flag:\n%s", taskReport)
	}
}

func TestRunLoopRepairsReadyImplementationWithEmptyDiff(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "empty-diff-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, root, CreateOptions{ID: "task_empty_diff", Objective: "change readme"}); err != nil {
		t.Fatal(err)
	}
	implementer := fake.New(
		fake.Response{Text: "@report implementation.mdx\n# Implementation\n\nClaimed done.\n@result status:ready artifact:implementation.mdx checks:none\n"},
		fake.Response{Text: strings.Join([]string{
			"@report implementation.mdx",
			"# Implementation",
			"",
			"Applied the requested README patch.",
			"@payload begin type:patch path:patches/readme-empty-repair.diff",
			"--- a/README.md",
			"+++ b/README.md",
			"@@ -1 +1 @@",
			"-# fixture",
			"+# changed after empty-ready repair",
			"@payload end",
			"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-empty-repair.diff reason:empty-ready-repair repo:repo1",
			"@result status:ready artifact:implementation.mdx checks:none",
			"",
		}, "\n")},
	)
	result, err := RunLoop(ctx, root, "task_empty_diff", RunnerOptions{
		ModelID: "fake-model",
		Providers: RoleProviders{
			model.RolePlanner:     fake.New(fake.Response{Text: "@report plan.mdx\n# Plan\n\nModify README.\n@result status:ready artifact:plan.mdx checks:none\n"}),
			model.RoleImplementer: implementer,
			model.RoleReviewer:    fake.New(fake.Response{Text: "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Fatalf("state = %s, want completed", result.State)
	}
	if implementer.Calls() != 2 {
		t.Fatalf("implementer calls = %d, want 2", implementer.Calls())
	}
	packets := implementer.Packets()
	if len(packets) != 2 || !strings.Contains(packets[1].UserContent(), "no worktree diff") {
		t.Fatalf("empty-ready repair packet missing:\n%v", packets)
	}
	patch, err := os.ReadFile(filepath.Join(root, ".midgard", "artifacts", "task_empty_diff", "patch.diff"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "changed after empty-ready repair") {
		t.Fatalf("patch.diff:\n%s", patch)
	}
	db, err := state.Open(ctx, filepath.Join(root, ".midgard", "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.EventsForTask(ctx, "task_empty_diff")
	if err != nil {
		t.Fatal(err)
	}
	var sawRepair bool
	for _, event := range events {
		if event.Type == "implementation.empty_ready_repair_requested" {
			sawRepair = true
		}
	}
	if !sawRepair {
		t.Fatalf("events = %#v", events)
	}
}

func TestRunLoopReworksAfterChangesRequested(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "rework-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, root, CreateOptions{ID: "task_rework", Objective: "replace README heading"}); err != nil {
		t.Fatal(err)
	}
	firstPatch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"@payload begin type:patch path:patches/readme-first.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1,2 @@",
		"-# fixture",
		"+# bad",
		"+# final",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-first.diff reason:first-pass repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	secondPatch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"Removed the extra line called out in review.",
		"@payload begin type:patch path:patches/readme-second.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1,2 +1 @@",
		"-# bad",
		" # final",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-second.diff reason:review-fix repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	result, err := RunLoop(ctx, root, "task_rework", RunnerOptions{
		ModelID: "fake-model",
		Providers: RoleProviders{
			model.RolePlanner: fake.New(fake.Response{Text: "@report plan.mdx\n# Plan\n\nModify README.\n@result status:ready artifact:plan.mdx checks:none\n"}),
			model.RoleImplementer: fake.New(
				fake.Response{Text: firstPatch, Usage: model.Usage{InputTokens: 10, OutputTokens: 10}},
				fake.Response{Text: secondPatch, Usage: model.Usage{InputTokens: 11, OutputTokens: 9}},
			),
			model.RoleReviewer: fake.New(
				fake.Response{Text: "@report review.mdx\n# Review\n\nThe diff leaves an extra bad line.\n@result status:changes-requested artifact:review.mdx findings:extra-line\n"},
				fake.Response{Text: "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"},
			),
		},
		MaxReviewCycles: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Fatalf("state = %s, want completed", result.State)
	}
	if len(result.RoleRuns) != 5 {
		t.Fatalf("role runs = %#v, want planner + two implementer/reviewer cycles", result.RoleRuns)
	}
	artifactDir := filepath.Join(root, ".midgard", "artifacts", "task_rework")
	patch, err := os.ReadFile(filepath.Join(artifactDir, "patch.diff"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "+# final") || strings.Contains(string(patch), "+# bad") {
		t.Fatalf("patch.diff:\n%s", patch)
	}
	for _, path := range []string{
		"attempts/implementer/1/implementation.mdx",
		"attempts/implementer/2/implementation.mdx",
		"attempts/reviewer/1/review.mdx",
		"attempts/reviewer/2/review.mdx",
	} {
		if _, err := os.Stat(filepath.Join(artifactDir, path)); err != nil {
			t.Fatalf("missing attempt snapshot %s: %v", path, err)
		}
	}
	db, err := state.Open(ctx, filepath.Join(root, ".midgard", "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	usageRecords, err := db.UsageRecordsForTask(ctx, "task_rework")
	if err != nil {
		t.Fatal(err)
	}
	var implementerUsage int
	for _, record := range usageRecords {
		if record.Role == "implementer" {
			implementerUsage++
		}
	}
	if implementerUsage != 2 {
		t.Fatalf("implementer usage records = %d, want 2: %#v", implementerUsage, usageRecords)
	}
	events, err := db.EventsForTask(ctx, "task_rework")
	if err != nil {
		t.Fatal(err)
	}
	var sawRework bool
	var editEvents int
	for _, event := range events {
		if event.Type == "rework.requested" {
			sawRework = true
		}
		if event.Type == "source_edit.applied" {
			editEvents++
		}
	}
	if !sawRework || editEvents != 2 {
		t.Fatalf("events = %#v, want rework.requested and two source edits", events)
	}
}

func TestRunLoopResumesFromChangesRequestedReview(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "resume-review-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, root, CreateOptions{ID: "task_resume_review", Objective: "replace README heading"}); err != nil {
		t.Fatal(err)
	}
	badPatch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"@payload begin type:patch path:patches/readme-bad.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-# fixture",
		"+# bad",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-bad.diff reason:first-pass repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	firstResult, err := RunLoop(ctx, root, "task_resume_review", RunnerOptions{
		ModelID: "fake-model",
		Providers: RoleProviders{
			model.RolePlanner:     fake.New(fake.Response{Text: "@report plan.mdx\n# Plan\n\nModify README.\n@result status:ready artifact:plan.mdx checks:none\n"}),
			model.RoleImplementer: fake.New(fake.Response{Text: badPatch}),
			model.RoleReviewer:    fake.New(fake.Response{Text: "@report review.mdx\n# Review\n\nThe diff leaves the wrong heading.\n@result status:changes-requested artifact:review.mdx findings:extra-line\n"}),
		},
		MaxReviewCycles: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.State != "blocked" {
		t.Fatalf("first state = %s, want blocked", firstResult.State)
	}
	fixPatch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"Fixed the review finding against the current worktree diff.",
		"@payload begin type:patch path:patches/readme-review-fix.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-# bad",
		"+# final",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-review-fix.diff reason:review-fix repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	implementer := fake.New(fake.Response{Text: fixPatch})
	secondResult, err := RunLoop(ctx, root, "task_resume_review", RunnerOptions{
		ModelID: "fake-model",
		Providers: RoleProviders{
			model.RoleImplementer: implementer,
			model.RoleReviewer:    fake.New(fake.Response{Text: "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"}),
		},
		MaxReviewCycles: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.State != "completed" {
		t.Fatalf("second state = %s, want completed", secondResult.State)
	}
	if len(secondResult.RoleRuns) != 2 ||
		secondResult.RoleRuns[0].Role != model.RoleImplementer ||
		secondResult.RoleRuns[1].Role != model.RoleReviewer {
		t.Fatalf("second role runs = %#v, want implementer then reviewer", secondResult.RoleRuns)
	}
	packets := implementer.Packets()
	if len(packets) != 1 {
		t.Fatalf("implementer packets = %d, want 1", len(packets))
	}
	if !strings.Contains(packets[0].Context, "latest_role_reports") ||
		!strings.Contains(packets[0].Context, "role:reviewer status:changes-requested artifact:review.mdx") ||
		!strings.Contains(packets[0].Context, "wrong heading") ||
		!strings.Contains(packets[0].Context, "worktree_diff") ||
		!strings.Contains(packets[0].Context, "+# bad") {
		t.Fatalf("resume implementer context missing review or diff:\n%s", packets[0].Context)
	}
	artifactDir := filepath.Join(root, ".midgard", "artifacts", "task_resume_review")
	for _, path := range []string{
		"attempts/implementer/1/implementation.mdx",
		"attempts/implementer/2/implementation.mdx",
		"attempts/reviewer/1/review.mdx",
		"attempts/reviewer/2/review.mdx",
	} {
		if _, err := os.Stat(filepath.Join(artifactDir, path)); err != nil {
			t.Fatalf("missing attempt snapshot %s: %v", path, err)
		}
	}
	patch, err := os.ReadFile(filepath.Join(artifactDir, "patch.diff"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "+# final") || strings.Contains(string(patch), "+# bad") {
		t.Fatalf("patch.diff:\n%s", patch)
	}
	db, err := state.Open(ctx, filepath.Join(root, ".midgard", "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.EventsForTask(ctx, "task_resume_review")
	if err != nil {
		t.Fatal(err)
	}
	var sawResume bool
	for _, event := range events {
		if event.Type == "rework.resumed" {
			sawResume = true
		}
	}
	if !sawResume {
		t.Fatalf("events = %#v, want rework.resumed", events)
	}
}

func TestRunLoopAutoReviewGuardReworksApprovedBadPatch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "auto-review-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, root, CreateOptions{ID: "task_auto_review", Objective: "change the phrase # fixture to # final."}); err != nil {
		t.Fatal(err)
	}
	badPatch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"@payload begin type:patch path:patches/readme-bad.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-# fixture",
		"+# wrong",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-bad.diff reason:first-pass repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	fixPatch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"@payload begin type:patch path:patches/readme-fix.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-# wrong",
		"+# final",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-fix.diff reason:auto-review-fix repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	result, err := RunLoop(ctx, root, "task_auto_review", RunnerOptions{
		ModelID: "fake-model",
		Providers: RoleProviders{
			model.RolePlanner: fake.New(fake.Response{Text: "@report plan.mdx\n# Plan\n\nModify README.\n@result status:ready artifact:plan.mdx checks:none\n"}),
			model.RoleImplementer: fake.New(
				fake.Response{Text: badPatch},
				fake.Response{Text: fixPatch},
			),
			model.RoleReviewer: fake.New(
				fake.Response{Text: "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"},
				fake.Response{Text: "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"},
			),
		},
		MaxReviewCycles: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || len(result.RoleRuns) != 5 {
		t.Fatalf("result = %#v", result)
	}
	if result.RoleRuns[2].Status != "changes-requested" {
		t.Fatalf("first reviewer status = %s, want auto changes-requested", result.RoleRuns[2].Status)
	}
	db, err := state.Open(ctx, filepath.Join(root, ".midgard", "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.EventsForTask(ctx, "task_auto_review")
	if err != nil {
		t.Fatal(err)
	}
	var sawAutoReview bool
	for _, event := range events {
		if event.Type == "review.auto_changes_requested" {
			sawAutoReview = true
		}
	}
	if !sawAutoReview {
		t.Fatalf("events = %#v, want review.auto_changes_requested", events)
	}
}
