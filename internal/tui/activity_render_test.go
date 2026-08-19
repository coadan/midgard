package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestShellCommandHighlightsExecutableAndQuotedText(t *testing.T) {
	command := `ruby -e 'require "yaml"; puts "ok"'`
	rendered := renderShellCommand(command)
	if !strings.Contains(rendered, colors.Command.Render("ruby")) || !strings.Contains(rendered, colors.ShellText.Render(`'require "yaml"; puts "ok"'`)) {
		t.Fatalf("command syntax was not styled by role: %q", rendered)
	}
}

func TestCompletedShellCommandSummarizesHeredocBody(t *testing.T) {
	var output strings.Builder
	renderRanCommand(&output, colors.Success.Render("•"), "cat > main.go <<'EOF'\npackage main\nfunc main() {}\nEOF\n", "2ms", 100)
	got := output.String()
	if !strings.Contains(got, "main.go") || !strings.Contains(got, "4-line script") {
		t.Fatalf("completed shell summary missing: %s", got)
	}
	if strings.Contains(got, "package main") || strings.Contains(got, "func main") {
		t.Fatalf("completed shell dumped heredoc body: %s", got)
	}
}

func TestSuccessfulCommandCollapsesMiddleAndKeepsTail(t *testing.T) {
	lines := make([]string, 10)
	for index := range lines {
		lines[index] = fmt.Sprintf("line %d", index+1)
	}
	var output strings.Builder
	renderCommandOutput(&output, activityOutput{Stdout: strings.Join(lines, "\n")}, 100)
	rendered := output.String()
	for _, expected := range []string{"… +8 lines", "line 9", "line 10", "├", "└"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("collapsed output missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "line 1\n") || strings.Contains(rendered, "line 8\n") {
		t.Fatalf("routine middle output was not collapsed:\n%s", rendered)
	}
}

func TestCompletedSkillSearchRendersAsCompactExploration(t *testing.T) {
	var output strings.Builder
	card := &ToolCard{Name: "skill_read", State: "succeeded", Arguments: `{"name":"heimdal","query":"visual evidence"}`}
	if !renderCompletedActivity(&output, card, 100) {
		t.Fatal("skill activity was not rendered")
	}
	for _, expected := range []string{"Explored", "Searched", "heimdal references"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("skill activity missing %q: %s", expected, output.String())
		}
	}
}

func TestCompletedRepositorySearchRendersBoundedMatches(t *testing.T) {
	var matches strings.Builder
	for index := 1; index <= 10; index++ {
		fmt.Fprintf(&matches, "internal/example.go:%d:needle %d\n", index, index)
	}
	card := &ToolCard{Name: "repo_search", State: "succeeded", Arguments: `{"query":"needle","path":"internal"}`,
		Output: fmt.Sprintf(`{"stdout":%q,"exit_code":0,"truncated":true}`, matches.String())}
	var output strings.Builder
	if !renderCompletedActivity(&output, card, 100) {
		t.Fatal("repository search was not rendered")
	}
	got := output.String()
	for _, expected := range []string{"Explored", "Searched", "needle", "internal/example.go:1", "… +2 matches", "Results capped"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("repository search missing %q: %s", expected, got)
		}
	}
	if strings.Contains(got, "internal/example.go:9") {
		t.Fatalf("repository search rendered too many matches: %s", got)
	}
}

func TestGitDiffRendersAsCompactWorktreeInspection(t *testing.T) {
	card := &ToolCard{Name: "git_diff", State: "succeeded", Output: `{"stdout":"diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n+new line\n"}`}
	var output strings.Builder
	if !renderCompletedActivity(&output, card, 100) {
		t.Fatal("git diff activity was not rendered")
	}
	got := output.String()
	for _, unexpected := range []string{"Edited", "README.md", "+new line"} {
		if strings.Contains(got, unexpected) {
			t.Fatalf("git diff exposed edit visuals %q: %s", unexpected, got)
		}
	}
	for _, expected := range []string{"Inspected the worktree", "Existing uncommitted changes"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("git diff inspection missing %q: %s", expected, got)
		}
	}
}

func TestUnavailableRepositorySearchShowsReinstallRecovery(t *testing.T) {
	card := &ToolCard{Name: "repo_search", State: "failed", Arguments: `{"query":"needle"}`,
		Output: `{"exit_code":-1,"error_code":"search_unavailable"}`}
	var output strings.Builder
	if !renderCompletedActivity(&output, card, 100) {
		t.Fatal("repository search was not rendered")
	}
	got := output.String()
	if !strings.Contains(got, "Could not search") || !strings.Contains(got, "Reinstall Midgard") {
		t.Fatalf("repository search failure = %s", got)
	}
}

func TestInvalidSkillReadShowsNotRunRecovery(t *testing.T) {
	card := &ToolCard{Name: "skill_read", State: "invalid", Arguments: `{}`, Output: `{"error":"skill_read needs arguments.name. Nothing was run"}`}
	var output strings.Builder
	if !renderCompletedActivity(&output, card, 100) {
		t.Fatal("invalid skill activity was not rendered")
	}
	got := output.String()
	if !strings.Contains(got, "Not run") || !strings.Contains(got, "arguments.name") {
		t.Fatalf("invalid skill activity = %q", got)
	}
}
