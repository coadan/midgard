package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

type activityArguments struct {
	Path       string   `json:"path"`
	Content    string   `json:"content"`
	Patch      string   `json:"patch"`
	Command    string   `json:"command"`
	Argv       []string `json:"argv"`
	Name       string   `json:"name"`
	Resource   string   `json:"resource"`
	Query      string   `json:"query"`
	JobID      string   `json:"job_id"`
	Background bool     `json:"background"`
}

type activityOutput struct {
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	ExitCode    int    `json:"exit_code"`
	Truncated   bool   `json:"truncated"`
	ErrorCode   string `json:"error_code"`
	Error       string `json:"error"`
	JobID       string `json:"job_id"`
	Status      string `json:"status"`
	JobExitCode *int   `json:"job_exit_code"`
}

type diffFile struct {
	path      string
	additions int
	deletions int
}

func renderCompletedActivity(output *strings.Builder, card *ToolCard, width int) bool {
	var args activityArguments
	var result activityOutput
	_ = json.Unmarshal([]byte(card.Arguments), &args)
	_ = json.Unmarshal([]byte(card.Output), &result)
	stateDot := colors.Success.Render("•")
	if card.State == "failed" || card.State == "invalid" {
		stateDot = colors.Failure.Render("•")
	}
	if result.Status == "failed" {
		stateDot = colors.Failure.Render("•")
	}
	elapsed := colors.Muted.Render(card.Elapsed.Round(1e6).String())

	switch card.Name {
	case "skill_read":
		if card.State == "invalid" {
			fmt.Fprintf(output, "\n%s %s  %s\n  %s %s\n", stateDot, colors.Selected.Render("Not run  skill_read"), elapsed,
				colors.Muted.Render("└"), colors.Failure.Render(valueOr(result.Error, "The skill name was missing")))
			return true
		}
		operation := "Read"
		target := valueOr(args.Name, "skill instructions")
		if args.Query != "" {
			operation = "Searched"
			target += " references"
		} else if args.Resource != "" {
			target += "/" + args.Resource
		}
		fmt.Fprintf(output, "\n%s %s  %s\n  %s %s %s\n", stateDot, colors.Selected.Render("Explored"), elapsed, colors.Muted.Render("└"), colors.Accent.Render(operation), colors.Location.Render(target))
	case "repo_search":
		if card.State == "failed" || card.State == "invalid" || result.ExitCode != 0 {
			fmt.Fprintf(output, "\n%s %s  %s\n  %s %s\n", stateDot, colors.Selected.Render("Could not search"), elapsed,
				colors.Muted.Render("└"), colors.Failure.Render(repositorySearchRecovery(result.ErrorCode)))
			return true
		}
		target := fmt.Sprintf("%q", args.Query)
		if args.Path != "" {
			target += " in " + args.Path
		}
		fmt.Fprintf(output, "\n%s %s  %s\n  %s %s %s\n", stateDot, colors.Selected.Render("Explored"), elapsed, colors.Muted.Render("└"), colors.Accent.Render("Searched"), colors.Location.Render(target))
		renderSearchMatches(output, result.Stdout, width)
		if result.Truncated {
			fmt.Fprintf(output, "    %s\n", colors.Warning.Render("Results capped; narrow the path or query"))
		}
	case "file_inspect":
		fmt.Fprintf(output, "\n%s %s  %s\n  %s %s %s\n", stateDot, colors.Selected.Render("Explored"), elapsed, colors.Muted.Render("└"), colors.Accent.Render("Read"), colors.Location.Render(args.Path))
	case "file_replace":
		lines := contentLineCount(args.Content)
		label := "Edited 1 file"
		if card.State == "failed" {
			label = "Could not edit file"
		}
		fmt.Fprintf(output, "\n%s %s  %s\n  %s %s", stateDot, colors.Selected.Render(label), elapsed, colors.Muted.Render("└"), colors.Location.Render(valueOr(args.Path, "requested file")))
		if lines > 0 {
			fmt.Fprintf(output, " %s", colors.Success.Render(fmt.Sprintf("(+%d)", lines)))
		}
		output.WriteByte('\n')
		if card.State == "failed" {
			detail := valueOr(result.Error, strings.TrimSpace(result.Stderr))
			fmt.Fprintf(output, "    %s %s\n", colors.Failure.Render("×"), colors.Failure.Render(valueOr(detail, "The edit was not applied")))
		}
	case "patch_apply":
		renderEditedDiff(output, stateDot, args.Patch, elapsed, width)
	case "git_diff":
		renderInspectedDiff(output, stateDot, result.Stdout, elapsed)
	case "shell", "check_run", "browser_run":
		command := args.Command
		if command == "" {
			command = strings.Join(args.Argv, " ")
		}
		if card.Name == "browser_run" {
			command = "heimdal " + command
		}
		if card.Name == "shell" && result.JobID != "" {
			fmt.Fprintf(output, "\n%s %s %s  %s\n  %s %s\n", stateDot, colors.Selected.Render("Started"), renderShellCommand(shellCommandSummary(command)), elapsed, colors.Muted.Render("└"), colors.Location.Render(result.JobID))
		} else {
			renderRanCommand(output, stateDot, command, elapsed, width)
			renderCommandOutput(output, result, width)
		}
	case "shell_poll", "shell_stop":
		label := "Checked background job"
		if card.Name == "shell_stop" {
			label = "Stopped background job"
		}
		fmt.Fprintf(output, "\n%s %s  %s\n  %s %s  %s\n", stateDot, colors.Selected.Render(label), elapsed, colors.Muted.Render("└"), colors.Location.Render(args.JobID), colors.Muted.Render(valueOr(result.Status, "unknown")))
		renderCommandOutput(output, result, width)
	default:
		return false
	}
	return true
}

func repositorySearchRecovery(code string) string {
	switch code {
	case "search_unavailable":
		return "Bundled repository search is unavailable. Reinstall Midgard to restore it."
	case "search_timeout":
		return "Search took too long. Try a narrower path or query."
	case "invalid_path":
		return "That path is not available in this worktree. Choose a current relative path."
	default:
		return "Search could not finish. Try a narrower path or query."
	}
}

func renderSearchMatches(output *strings.Builder, matches string, width int) {
	lines := strings.Split(strings.TrimSpace(matches), "\n")
	if len(lines) == 1 && lines[0] == "" {
		fmt.Fprintf(output, "    %s\n", colors.Muted.Render("No matches"))
		return
	}
	const visible = 8
	shown := min(len(lines), visible)
	for _, line := range lines[:shown] {
		fmt.Fprintf(output, "    %s\n", colors.Muted.Render(bounded(line, max(20, width-8))))
	}
	if len(lines) > shown {
		fmt.Fprintf(output, "    %s\n", colors.Muted.Render(fmt.Sprintf("… +%d matches", len(lines)-shown)))
	}
}

func renderRanCommand(output *strings.Builder, stateDot, command, elapsed string, width int) {
	rendered := renderShellCommand(shellCommandSummary(command))
	lines := strings.Split(lipgloss.Wrap(rendered, max(20, width-9), " "), "\n")
	for index, line := range lines {
		if index == 0 {
			fmt.Fprintf(output, "\n%s %s %s  %s\n", stateDot, colors.Selected.Render("Ran"), line, elapsed)
			continue
		}
		fmt.Fprintf(output, "  %s %s\n", colors.Muted.Render("│"), line)
	}
}

func renderShellCommand(command string) string {
	var output strings.Builder
	commandRunes := []rune(command)
	executableEnd := 0
	for executableEnd < len(commandRunes) && !isCommandSpace(commandRunes[executableEnd]) {
		executableEnd++
	}
	output.WriteString(colors.Command.Render(string(commandRunes[:executableEnd])))
	for index := executableEnd; index < len(commandRunes); {
		quote := commandRunes[index]
		if quote != '\'' && quote != '"' {
			output.WriteRune(quote)
			index++
			continue
		}
		end := index + 1
		for end < len(commandRunes) {
			if commandRunes[end] == quote && (end == index+1 || commandRunes[end-1] != '\\') {
				end++
				break
			}
			end++
		}
		output.WriteString(colors.ShellText.Render(string(commandRunes[index:end])))
		index = end
	}
	return output.String()
}

func isCommandSpace(value rune) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}

func renderEditedDiff(output *strings.Builder, stateDot, diff, elapsed string, width int) {
	renderDiff(output, stateDot, diff, elapsed, width, "Edited", "Checked changes")
}

func renderInspectedDiff(output *strings.Builder, stateDot, diff, elapsed string) {
	detail := "Existing uncommitted changes"
	if strings.TrimSpace(diff) == "" {
		detail = "No uncommitted changes"
	}
	fmt.Fprintf(output, "\n%s %s  %s\n  %s %s\n", stateDot, colors.Selected.Render("Inspected the worktree"), elapsed, colors.Muted.Render("└"), colors.Muted.Render(detail))
}

func renderDiff(output *strings.Builder, stateDot, diff, elapsed string, width int, changedLabel, emptyLabel string) {
	files, additions, deletions := diffStats(diff)
	if len(files) == 0 {
		fmt.Fprintf(output, "\n%s %s  %s\n  %s %s\n", stateDot, colors.Selected.Render(emptyLabel), elapsed, colors.Muted.Render("└"), colors.Muted.Render("No uncommitted edits"))
		return
	}
	label := fmt.Sprintf("%s %d files", changedLabel, len(files))
	if len(files) == 1 {
		label = changedLabel + " 1 file"
	}
	fmt.Fprintf(output, "\n%s %s %s  %s\n", stateDot, colors.Selected.Render(label), coloredDiffCount(additions, deletions), elapsed)
	for index, file := range files {
		branch := "├"
		if index == len(files)-1 {
			branch = "└"
		}
		fmt.Fprintf(output, "  %s %s %s\n", colors.Muted.Render(branch), colors.Location.Render(file.path), coloredDiffCount(file.additions, file.deletions))
	}
	if strings.TrimSpace(diff) != "" {
		output.WriteString(renderDiffLines(diff, min(14, max(6, width/7))))
	}
}

func coloredDiffCount(additions, deletions int) string {
	return "(" + colors.Success.Render(fmt.Sprintf("+%d", additions)) + " " + colors.Failure.Render(fmt.Sprintf("-%d", deletions)) + ")"
}

func diffStats(diff string) ([]diffFile, int, int) {
	var files []diffFile
	current := -1
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files = append(files, diffFile{})
			current = len(files) - 1
		case current >= 0 && strings.HasPrefix(line, "+++ "):
			path := strings.TrimPrefix(line, "+++ ")
			path = strings.TrimPrefix(path, "b/")
			if path != "/dev/null" {
				files[current].path = path
			}
		case current >= 0 && strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			files[current].additions++
		case current >= 0 && strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			files[current].deletions++
		}
	}
	totalAdditions, totalDeletions := 0, 0
	kept := files[:0]
	for _, file := range files {
		if file.path == "" {
			file.path = "deleted file"
		}
		totalAdditions += file.additions
		totalDeletions += file.deletions
		kept = append(kept, file)
	}
	return kept, totalAdditions, totalDeletions
}

func renderCommandOutput(output *strings.Builder, result activityOutput, width int) {
	text := strings.TrimSpace(strings.Join([]string{result.Stdout, result.Stderr, result.Error}, "\n"))
	if text == "" {
		fmt.Fprintf(output, "  %s %s\n", colors.Muted.Render("└"), colors.Muted.Render(fmt.Sprintf("exit %d", result.ExitCode)))
		return
	}
	lines := strings.Split(text, "\n")
	if cardOutputFailed(result) {
		renderFailedCommandOutput(output, lines, width)
		return
	}
	const tailLines = 2
	if len(lines) > 4 {
		fmt.Fprintf(output, "  %s %s\n", colors.Muted.Render("├"), colors.Muted.Render(fmt.Sprintf("… +%d lines", len(lines)-tailLines)))
		lines = lines[len(lines)-tailLines:]
	}
	renderOutputLines(output, lines, width, true)
}

func cardOutputFailed(result activityOutput) bool {
	return result.ExitCode != 0 || result.Error != ""
}

func renderFailedCommandOutput(output *strings.Builder, lines []string, width int) {
	if len(lines) <= 8 {
		renderOutputLines(output, lines, width, true)
		return
	}
	renderOutputLines(output, lines[:4], width, false)
	fmt.Fprintf(output, "  %s %s\n", colors.Muted.Render("│"), colors.Muted.Render(fmt.Sprintf("… +%d lines", len(lines)-7)))
	renderOutputLines(output, lines[len(lines)-3:], width, true)
}

func renderOutputLines(output *strings.Builder, lines []string, width int, closeTree bool) {
	for index, line := range lines {
		branch := "│"
		if closeTree && index == len(lines)-1 {
			branch = "└"
		}
		fmt.Fprintf(output, "  %s %s\n", colors.Muted.Render(branch), colors.Muted.Render(lipglossSafeWrap(line, max(20, width-4))))
	}
}

func lipglossSafeWrap(value string, width int) string {
	// Command output is already line-oriented. Hard clipping keeps its tree
	// gutter intact without making a single process line dominate the chat.
	if len([]rune(value)) <= width {
		return value
	}
	runes := []rune(value)
	return string(runes[:max(1, width-1)]) + "…"
}

func contentLineCount(content string) int {
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}
