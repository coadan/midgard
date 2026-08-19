package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// renderChatText applies a deliberately small Markdown presentation surface.
// The session retains the original text; styling is a TUI-only projection.
func renderChatText(value string, width int) string {
	width = max(20, width)
	lines := strings.Split(value, "\n")
	rendered := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			rendered = append(rendered, lipgloss.Wrap(colors.CodeBlock.Render("  "+line), width, " "))
			continue
		}

		content := line
		switch {
		case strings.HasPrefix(trimmed, "### "):
			content = colors.Heading.Render(strings.TrimPrefix(trimmed, "### "))
		case strings.HasPrefix(trimmed, "## "):
			content = colors.Heading.Render(strings.TrimPrefix(trimmed, "## "))
		case strings.HasPrefix(trimmed, "# "):
			content = colors.Heading.Render(strings.TrimPrefix(trimmed, "# "))
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			content = colors.Bullet.Render("•") + " " + renderInlineMarkdown(trimmed[2:])
		default:
			marker, remainder, ordered := orderedListItem(trimmed)
			if ordered {
				content = colors.Bullet.Render(marker) + " " + renderInlineMarkdown(remainder)
			} else {
				content = renderInlineMarkdown(content)
			}
		}
		rendered = append(rendered, lipgloss.Wrap(content, width, " "))
	}
	return strings.Join(rendered, "\n")
}

func orderedListItem(value string) (string, string, bool) {
	dot := strings.Index(value, ". ")
	if dot <= 0 {
		return "", "", false
	}
	for _, char := range value[:dot] {
		if char < '0' || char > '9' {
			return "", "", false
		}
	}
	return value[:dot+1], value[dot+2:], true
}

func renderChatMessage(role, value string, width int) string {
	width = max(20, width)
	if role == "assistant" {
		return prefixChatLines(renderChatText(value, width-2), colors.Agent.Render("│"), colors.Agent.Render("│"))
	}
	content := lipgloss.Wrap(value, max(20, width-2), " ")
	return prefixChatLines(content, colors.User.Render("›"), " ")
}

func prefixChatLines(value, firstPrefix, continuationPrefix string) string {
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		prefix := continuationPrefix
		if index == 0 {
			prefix = firstPrefix
		}
		lines[index] = prefix + " " + line
	}
	return strings.Join(lines, "\n")
}

func renderInlineMarkdown(value string) string {
	var output strings.Builder
	for {
		start := strings.IndexByte(value, '`')
		if start < 0 {
			output.WriteString(renderStrong(value))
			break
		}
		end := strings.IndexByte(value[start+1:], '`')
		if end < 0 {
			output.WriteString(renderStrong(value))
			break
		}
		end += start + 1
		output.WriteString(renderStrong(value[:start]))
		output.WriteString(colors.InlineCode.Render(value[start+1 : end]))
		value = value[end+1:]
	}
	return output.String()
}

func renderStrong(value string) string {
	var output strings.Builder
	for {
		start := strings.Index(value, "**")
		if start < 0 {
			output.WriteString(value)
			break
		}
		end := strings.Index(value[start+2:], "**")
		if end < 0 {
			output.WriteString(value)
			break
		}
		end += start + 2
		output.WriteString(value[:start])
		output.WriteString(colors.Selected.Render(value[start+2 : end]))
		value = value[end+2:]
	}
	return output.String()
}
