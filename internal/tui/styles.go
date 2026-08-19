package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

var colors = struct {
	Brand      lipgloss.Style
	Accent     lipgloss.Style
	Location   lipgloss.Style
	Muted      lipgloss.Style
	Subtle     lipgloss.Style
	Success    lipgloss.Style
	Warning    lipgloss.Style
	Failure    lipgloss.Style
	User       lipgloss.Style
	Agent      lipgloss.Style
	Section    lipgloss.Style
	Heading    lipgloss.Style
	InlineCode lipgloss.Style
	CodeBlock  lipgloss.Style
	Bullet     lipgloss.Style
	Command    lipgloss.Style
	ShellText  lipgloss.Style
	DiffAdd    lipgloss.Style
	DiffDelete lipgloss.Style
	Selected   lipgloss.Style
	Divider    lipgloss.Style
}{
	Brand:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#C4A7E7")),
	Accent:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#9CCFD8")),
	Location:   lipgloss.NewStyle().Foreground(lipgloss.Color("#9CCFD8")),
	Muted:      lipgloss.NewStyle().Foreground(lipgloss.Color("#6E6A86")),
	Subtle:     lipgloss.NewStyle().Foreground(lipgloss.Color("#4F4B63")),
	Success:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#9CCB7A")),
	Warning:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F6C177")),
	Failure:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#EB6F92")),
	User:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F6C177")),
	Agent:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#C4A7E7")),
	Section:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#908CAA")),
	Heading:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#C4A7E7")),
	InlineCode: lipgloss.NewStyle().Foreground(lipgloss.Color("#9CCFD8")).Background(lipgloss.Color("#26233A")),
	CodeBlock:  lipgloss.NewStyle().Foreground(lipgloss.Color("#E0DEF4")).Background(lipgloss.Color("#26233A")),
	Bullet:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#31748F")),
	Command:    lipgloss.NewStyle().Foreground(lipgloss.Color("#31748F")),
	ShellText:  lipgloss.NewStyle().Foreground(lipgloss.Color("#9CCB7A")),
	DiffAdd:    lipgloss.NewStyle().Foreground(lipgloss.Color("#E0DEF4")).Background(lipgloss.Color("#1F3A2D")),
	DiffDelete: lipgloss.NewStyle().Foreground(lipgloss.Color("#E0DEF4")).Background(lipgloss.Color("#3A202D")),
	Selected:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E0DEF4")),
	Divider:    lipgloss.NewStyle().Foreground(lipgloss.Color("#393552")),
}

func styledStatus(status string) string {
	lower := strings.ToLower(status)
	style := colors.Muted
	switch {
	case strings.Contains(lower, "failed"), strings.Contains(lower, "stopped"), strings.Contains(lower, "not added"), strings.Contains(lower, "not changed"):
		style = colors.Failure
	case strings.Contains(lower, "green"), strings.Contains(lower, "ready"), strings.Contains(lower, "using environment"), lower == "clean", lower == "completed":
		style = colors.Success
	case strings.Contains(lower, "working"), strings.Contains(lower, "queued"), strings.Contains(lower, "starting"), strings.Contains(lower, "creating"), strings.Contains(lower, "preparing"), strings.Contains(lower, "waiting"), strings.Contains(lower, "running"), strings.Contains(lower, "thinking"), strings.Contains(lower, "responding"), strings.Contains(lower, "received"), strings.Contains(lower, "checking"), strings.Contains(lower, "after the current turn"), lower == "active", lower == "dirty", lower == "new session":
		style = colors.Warning
	}
	return style.Render(status)
}

func styledToolState(state string) string {
	switch state {
	case "succeeded":
		return colors.Success.Render("✓ " + state)
	case "failed", "invalid":
		return colors.Failure.Render("× " + state)
	case "running", "committed", "validated", "queued":
		return colors.Warning.Render("● " + state)
	default:
		return colors.Muted.Render("○ " + state)
	}
}

func divider(width int) string {
	if width <= 0 {
		width = 40
	}
	return colors.Divider.Render(strings.Repeat("─", width))
}
