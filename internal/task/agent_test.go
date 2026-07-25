package task

import (
	"context"
	"os"
	"strings"
	"testing"

	"midgard/internal/cost"
	"midgard/internal/gitrepo"
	"midgard/internal/model"
	"midgard/internal/model/providers/fake"
	"midgard/internal/workbench"
)

func TestAgentFinalState(t *testing.T) {
	tests := []struct {
		name     string
		run      RoleRun
		hasPatch bool
		want     string
	}{
		{name: "ready patch", run: RoleRun{Status: "ready"}, hasPatch: true, want: "completed"},
		{name: "ready empty", run: RoleRun{Status: "ready"}, want: "blocked"},
		{name: "no op", run: RoleRun{Status: "no-op"}, want: "completed"},
		{name: "blocked", run: RoleRun{Status: "blocked"}, want: "blocked"},
		{name: "failed", run: RoleRun{Status: "failed"}, want: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentFinalState(tt.run, tt.hasPatch, nil); got != tt.want {
				t.Fatalf("state = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunAgentCompletesTaskAndLoadsRepositoryGuidance(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "agent-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initLifecycleRepo(t)
	if err := os.WriteFile(repo+"/AGENTS.md", []byte("# Repository Rules\n\nKeep the fixture focused.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "add", "AGENTS.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "commit", "-m", "add guidance"); err != nil {
		t.Fatal(err)
	}
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, root, CreateOptions{ID: "task_agent", Objective: "Update the fixture heading"}); err != nil {
		t.Fatal(err)
	}

	provider := fake.New(fake.Response{
		Text: strings.Join([]string{
			"@report implementation.mdx",
			"# Implementation",
			"",
			"Updated the fixture heading.",
			"@payload begin type:patch path:patches/readme.diff",
			"diff --git a/README.md b/README.md",
			"--- a/README.md",
			"+++ b/README.md",
			"@@ -1 +1 @@",
			"-# fixture",
			"+# updated fixture",
			"@payload end",
			"@edit file:README.md action:modify mode:patch content:artifact:patches/readme.diff reason:update-heading repo:repo1",
			"@result status:ready artifact:implementation.mdx checks:none",
			"",
		}, "\n"),
		Usage: model.Usage{InputTokens: 100, OutputTokens: 50},
	})
	result, err := RunAgent(ctx, root, "task_agent", RunnerOptions{
		ModelID: "fake-model",
		Providers: RoleProviders{
			model.RoleImplementer: provider,
		},
		Pricing: cost.Pricing{ID: "fake", ProviderID: "fake", ModelID: "fake-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || result.PatchPath != "patch.diff" {
		t.Fatalf("result = %#v", result)
	}
	packets := provider.Packets()
	if len(packets) != 1 {
		t.Fatalf("packets = %d, want 1", len(packets))
	}
	content := packets[0].UserContent()
	for _, want := range []string{"repository_guidance:", "# Repository Rules", "tool:heimdal"} {
		if !strings.Contains(content, want) {
			t.Fatalf("packet missing %q:\n%s", want, content)
		}
	}
}
