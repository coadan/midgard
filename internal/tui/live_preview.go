package tui

import (
	"encoding/json"
	"fmt"
	"strings"
)

type bragiEntityView struct {
	ID       string                    `json:"id"`
	Type     string                    `json:"type"`
	Revision int                       `json:"revision"`
	Status   string                    `json:"status"`
	Fields   map[string]bragiFieldView `json:"fields"`
}

type bragiFieldView struct {
	Scalar  *bragiScalarView `json:"scalar"`
	Literal string           `json:"literal"`
	Open    bool             `json:"open"`
}

type bragiScalarView struct {
	Kind   string `json:"kind"`
	String string `json:"string"`
	Number string `json:"number"`
	Bool   bool   `json:"bool"`
}

func renderBragiCard(card BragiCard) string {
	var entity bragiEntityView
	_ = json.Unmarshal([]byte(card.Entity), &entity)
	entityType := valueOr(card.Type, entity.Type)
	var output strings.Builder
	if strings.Contains(card.State, "rejected") {
		fmt.Fprintf(&output, "%s  %s\n", colors.Failure.Render("× RESPONSE DRAFT NEEDS REPAIR"), valueOr(card.Message, "The model is correcting it."))
		return output.String()
	}
	if card.Entity == "" {
		return ""
	}

	switch entityType {
	case "tool":
		name := bragiFieldText(entity, "name")
		fmt.Fprintf(&output, "%s  %s\n", colors.Warning.Render("PREPARING"), colors.Accent.Render(valueOr(name, "an action")))
		renderBragiTool(&output, name, entity)
	case "message":
		return ""
	case "plan_step":
		fmt.Fprintf(&output, "%s  %s\n", colors.Section.Render("UPDATING PLAN"), bragiFieldText(entity, "intent"))
	case "check":
		fmt.Fprintf(&output, "%s  %s  %s\n", colors.Section.Render("RECORDING CHECK"), bragiFieldText(entity, "name"), bragiFieldText(entity, "status"))
	case "completion":
		return ""
	default:
		for _, field := range []string{"change", "claim", "question", "intent", "kind", "path"} {
			if value := bragiFieldText(entity, field); value != "" {
				fmt.Fprintf(&output, "%s  %s\n", colors.Section.Render(strings.ToUpper(field)), value)
				break
			}
		}
	}
	return output.String()
}

func renderBragiTool(output *strings.Builder, name string, entity bragiEntityView) {
	argument := func(path string) string { return bragiFieldText(entity, "arguments."+path) }
	switch name {
	case "skill_read":
		skill := valueOr(argument("name"), "waiting for skill…")
		if query := argument("query"); query != "" {
			fmt.Fprintf(output, "  %s  %s\n", colors.Accent.Render(skill), colors.Muted.Render("searching references for "+bounded(query, 100)))
		} else if resource := argument("resource"); resource != "" {
			fmt.Fprintf(output, "  %s  %s\n", colors.Accent.Render(skill), colors.Location.Render(resource))
		} else {
			fmt.Fprintf(output, "  %s\n", colors.Accent.Render(skill))
		}
	case "file_replace":
		fmt.Fprintf(output, "  %s\n", colors.Location.Render(valueOr(argument("path"), "waiting for path…")))
		if content := argument("content"); content != "" {
			fmt.Fprintf(output, "  %s\n", colors.Muted.Render(fmt.Sprintf("%s drafted", pluralLineCount(content))))
		}
	case "patch_apply":
		renderPatchDraft(output, argument("patch"))
	case "file_inspect":
		fmt.Fprintf(output, "  %s\n", colors.Location.Render(valueOr(argument("path"), "waiting for path…")))
	case "repo_search":
		query := valueOr(argument("query"), "waiting for query…")
		if path := argument("path"); path != "" {
			fmt.Fprintf(output, "  %s  %s\n", colors.Accent.Render(bounded(query, 140)), colors.Location.Render(path))
		} else {
			fmt.Fprintf(output, "  %s\n", colors.Accent.Render(bounded(query, 180)))
		}
	case "browser_run":
		fmt.Fprintf(output, "  %s\n", colors.Command.Render("heimdal")+" "+colors.Location.Render(bounded(argument("command"), 140)))
	case "shell":
		fmt.Fprintf(output, "  %s\n", shellCommandSummary(valueOr(argument("command"), "waiting for command…")))
	case "shell_poll":
		fmt.Fprintf(output, "  %s  %s\n", colors.Accent.Render("checking"), colors.Location.Render(valueOr(argument("job_id"), "waiting for job…")))
	case "shell_stop":
		fmt.Fprintf(output, "  %s  %s\n", colors.Warning.Render("stopping"), colors.Location.Render(valueOr(argument("job_id"), "waiting for job…")))
	default:
		for path := range entity.Fields {
			if strings.HasPrefix(path, "arguments.") {
				fmt.Fprintf(output, "%s  %s\n", colors.Muted.Render(strings.TrimPrefix(path, "arguments.")), bounded(bragiFieldText(entity, path), 180))
			}
		}
	}
	if reason := bragiFieldText(entity, "reason"); reason != "" {
		fmt.Fprintf(output, "  %s\n", colors.Muted.Render(bounded(reason, 180)))
	}
}

func shellCommandSummary(command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "waiting for command…"
	}
	lines := strings.Split(trimmed, "\n")
	first := bounded(strings.TrimSpace(lines[0]), 160)
	if len(lines) > 1 {
		return fmt.Sprintf("%s  … %d-line script", first, len(lines))
	}
	return bounded(first, 220)
}

func pluralLineCount(content string) string {
	trimmed := strings.TrimSuffix(content, "\n")
	if trimmed == "" {
		return "0 lines"
	}
	count := strings.Count(trimmed, "\n") + 1
	if count == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", count)
}

func renderPatchDraft(output *strings.Builder, patch string) {
	files, additions, deletions := patchDraftStats(patch)
	if len(files) == 0 {
		fmt.Fprintf(output, "  %s\n", colors.Muted.Render("waiting for patch headers…"))
		return
	}
	fileLabel := fmt.Sprintf("%d files", len(files))
	if len(files) == 1 {
		fileLabel = "1 file"
	}
	fmt.Fprintf(output, "  %s  %s\n", colors.Muted.Render(fileLabel+" drafted"), coloredDiffCount(additions, deletions))

	const visibleFiles = 6
	shown := min(len(files), visibleFiles)
	for index, file := range files[:shown] {
		branch := "├"
		if index == shown-1 && len(files) == shown {
			branch = "└"
		}
		fmt.Fprintf(output, "  %s %s %s\n", colors.Muted.Render(branch), colors.Location.Render(file.path), coloredDiffCount(file.additions, file.deletions))
	}
	if len(files) > shown {
		fmt.Fprintf(output, "  %s %s\n", colors.Muted.Render("└"), colors.Muted.Render(fmt.Sprintf("… %d more files", len(files)-shown)))
	}
}

// patchDraftStats accepts both complete Git patches and a partially streamed
// unified patch. It deliberately retains only filenames and line counts so a
// live card communicates progress without exposing patch contents.
func patchDraftStats(patch string) ([]diffFile, int, int) {
	var files []diffFile
	current := -1
	oldPath := ""
	pendingDiffHeader := false
	inHunk := false
	ensureCurrent := func(path string) int {
		if current >= 0 {
			if files[current].path == "" && path != "" {
				files[current].path = path
			}
			return current
		}
		files = append(files, diffFile{path: path})
		current = len(files) - 1
		return current
	}
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			parts := strings.Fields(line)
			path := ""
			if len(parts) >= 4 {
				path = patchPath(parts[3])
			}
			files = append(files, diffFile{path: path})
			current = len(files) - 1
			oldPath = ""
			pendingDiffHeader = true
			inHunk = false
		case isPatchOldHeader(line):
			inHunk = false
			oldPath = patchPath(strings.TrimSpace(strings.TrimPrefix(line, "--- ")))
			if !pendingDiffHeader {
				files = append(files, diffFile{path: oldPath})
				current = len(files) - 1
			} else {
				ensureCurrent(oldPath)
			}
		case !inHunk && strings.HasPrefix(line, "+++ "):
			path := patchPath(strings.TrimSpace(strings.TrimPrefix(line, "+++ ")))
			if path == "" {
				path = oldPath
			}
			index := ensureCurrent(path)
			if path != "" {
				files[index].path = path
			}
			pendingDiffHeader = false
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case current >= 0 && inHunk && strings.HasPrefix(line, "+"):
			files[current].additions++
		case current >= 0 && inHunk && strings.HasPrefix(line, "-"):
			files[current].deletions++
		}
	}

	additions, deletions := 0, 0
	kept := files[:0]
	for _, file := range files {
		if file.path == "" {
			file.path = "changed file"
		}
		additions += file.additions
		deletions += file.deletions
		kept = append(kept, file)
	}
	return kept, additions, deletions
}

func patchPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(strings.TrimPrefix(path, "a/"), "b/")
}

func isPatchOldHeader(line string) bool {
	if !strings.HasPrefix(line, "--- ") {
		return false
	}
	path := strings.TrimSpace(strings.TrimPrefix(line, "--- "))
	return path == "/dev/null" || strings.HasPrefix(path, "a/")
}

func bragiFieldText(entity bragiEntityView, path string) string {
	field, ok := entity.Fields[path]
	if !ok {
		return ""
	}
	if field.Scalar == nil {
		return field.Literal
	}
	switch field.Scalar.Kind {
	case "string", "ref":
		return field.Scalar.String
	case "number":
		return field.Scalar.Number
	case "bool":
		if field.Scalar.Bool {
			return "true"
		}
		return "false"
	case "null":
		return "null"
	default:
		return ""
	}
}

func renderActionRail(state string) string {
	stages := []string{"INTENT", "VALIDATED", "COMMITTED", "DISPATCHED", "RESULT"}
	current := map[string]int{"queued": 0, "invalid": 1, "validated": 1, "committed": 2, "running": 3, "succeeded": 4, "failed": 4}[state]
	parts := make([]string, 0, len(stages))
	for index, stage := range stages {
		label := "○ " + stage
		style := colors.Muted
		switch {
		case index < current:
			label, style = "✓ "+stage, colors.Success
		case index == current && state == "succeeded":
			label, style = "✓ "+stage, colors.Success
		case index == current && state == "failed":
			label, style = "× "+stage, colors.Failure
		case index == current && state == "invalid":
			label, style = "× "+stage, colors.Failure
		case index == current:
			label, style = "● "+stage, colors.Warning
		}
		parts = append(parts, style.Render(label))
	}
	return strings.Join(parts, colors.Muted.Render(" ─ "))
}

func renderDiffLines(diff string, limit int) string {
	lines := strings.Split(diff, "\n")
	start := max(0, len(lines)-limit)
	var output strings.Builder
	if start > 0 {
		fmt.Fprintf(&output, "%s\n", colors.Muted.Render(fmt.Sprintf("  … %d earlier diff lines", start)))
	}
	for _, line := range lines[start:] {
		style := colors.Muted
		switch {
		case strings.HasPrefix(line, "@@"):
			style = colors.Accent
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			style = colors.DiffAdd
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			style = colors.DiffDelete
		case strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			style = colors.Section
		}
		fmt.Fprintf(&output, "%s\n", style.Render(line))
	}
	return output.String()
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
