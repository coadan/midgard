package tui

import (
	"strings"
	"testing"
)

func TestChatTextHighlightsInlineCodeAndWraps(t *testing.T) {
	plain := "Run `gofmt` on the changed files and then use `go test ./...` to verify the complete repository."
	rendered := renderChatText(plain, 38)
	for _, expected := range []string{"gofmt", "go test ./...", "\x1b[", "\n"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered text missing %q: %s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "`") || strings.Contains(rendered, plain) {
		t.Fatalf("inline Markdown was not rendered or text did not wrap: %s", rendered)
	}
}

func TestChatTextRendersHierarchyWithoutChangingParagraphs(t *testing.T) {
	rendered := renderChatText("## Checks\n\n1. `go test ./...` passed\n- **No whitespace errors**", 60)
	for _, expected := range []string{"Checks", "1.", "•", "go test ./...", "No whitespace errors", "\n\n"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered text missing %q: %s", expected, rendered)
		}
	}
}

func TestChatMessageUsesLightweightTurnMarkers(t *testing.T) {
	user := renderChatMessage("user", "Please fix this", 60)
	agent := renderChatMessage("assistant", "Done", 60)
	if !strings.Contains(user, "›") || !strings.Contains(agent, "│") {
		t.Fatalf("markers = %q, %q", user, agent)
	}
	if strings.Contains(user+agent, "YOU") || strings.Contains(user+agent, "MIDGARD") {
		t.Fatalf("heavy role labels remain: %q %q", user, agent)
	}
}
