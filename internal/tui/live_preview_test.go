package tui

import (
	"strings"
	"testing"
)

func TestBragiToolCardSummarizesDraftWithoutDumpingCode(t *testing.T) {
	card := BragiCard{EntityID: "@edit1", Type: "tool", State: "op.accepted", Revision: 1,
		Entity: `{"id":"@edit1","type":"tool","revision":1,"status":"draft","fields":{"name":{"scalar":{"kind":"string","string":"file_replace"}},"arguments.path":{"scalar":{"kind":"string","string":"main.go"}},"arguments.content":{"scalar":{"kind":"string","string":"package main\n"}},"reason":{"scalar":{"kind":"string","string":"apply the change"}}}}`}
	preview := renderBragiCard(card)
	for _, expected := range []string{"PREPARING", "file_replace", "main.go", "1 line drafted", "apply the change"} {
		if !strings.Contains(preview, expected) {
			t.Fatalf("preview missing %q: %s", expected, preview)
		}
	}
	if strings.Contains(preview, "package main") || strings.Contains(preview, "+   1") {
		t.Fatalf("raw code leaked into transient preview: %s", preview)
	}
	if strings.Contains(strings.ToLower(preview), "bragi") {
		t.Fatalf("protocol name leaked into product copy: %s", preview)
	}
}

func TestPatchDraftShowsLivePerFileTallyWithoutPatchContents(t *testing.T) {
	card := BragiCard{EntityID: "@patch1", Type: "tool", State: "op.accepted", Revision: 1,
		Entity: `{"id":"@patch1","type":"tool","revision":1,"status":"draft","fields":{"name":{"scalar":{"kind":"string","string":"patch_apply"}},"arguments.patch":{"literal":"diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old secret-looking body\n+new secret-looking body\ndiff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n+document the change\n"}}}`}
	preview := renderBragiCard(card)
	for _, expected := range []string{"2 files drafted", "main.go", "README.md", "+2", "-1"} {
		if !strings.Contains(preview, expected) {
			t.Fatalf("patch draft preview missing %q: %s", expected, preview)
		}
	}
	if strings.Contains(preview, "secret-looking body") || strings.Contains(preview, "document the change") {
		t.Fatalf("patch draft preview = %s", preview)
	}
}

func TestPatchDraftTallyWorksBeforeThePatchIsComplete(t *testing.T) {
	files, additions, deletions := patchDraftStats("--- a/internal/todo/store.go\n+++ b/internal/todo/store.go\n@@ -1 +1 @@\n-old\n+new\n+another")
	if len(files) != 1 || files[0].path != "internal/todo/store.go" || files[0].additions != 2 || files[0].deletions != 1 || additions != 2 || deletions != 1 {
		t.Fatalf("partial patch stats = %#v, +%d -%d", files, additions, deletions)
	}
}

func TestPatchDraftTallySeparatesUnifiedPatchFilesWithoutGitHeaders(t *testing.T) {
	files, additions, deletions := patchDraftStats("--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-old docs\n+new docs")
	if len(files) != 2 || files[0].path != "main.go" || files[1].path != "README.md" || additions != 2 || deletions != 2 {
		t.Fatalf("multi-file unified patch stats = %#v, +%d -%d", files, additions, deletions)
	}
}

func TestShellDraftSummarizesHeredocWithoutDumpingBody(t *testing.T) {
	card := BragiCard{EntityID: "@shell1", Type: "tool", State: "op.accepted", Revision: 1,
		Entity: `{"id":"@shell1","type":"tool","revision":1,"status":"draft","fields":{"name":{"scalar":{"kind":"string","string":"shell"}},"arguments.command":{"literal":"cat > main.go <<'EOF'\npackage main\nfunc main() {}\nEOF\n"}}}`}
	preview := renderBragiCard(card)
	if !strings.Contains(preview, "cat > main.go <<'EOF'") || !strings.Contains(preview, "4-line script") {
		t.Fatalf("shell summary missing: %s", preview)
	}
	if strings.Contains(preview, "package main") || strings.Contains(preview, "func main") {
		t.Fatalf("heredoc body leaked into shell preview: %s", preview)
	}
}

func TestSkillDraftShowsBoundedRetrievalIntent(t *testing.T) {
	card := BragiCard{EntityID: "@skill1", Type: "tool", State: "op.accepted", Revision: 1,
		Entity: `{"id":"@skill1","type":"tool","revision":1,"status":"draft","fields":{"name":{"scalar":{"kind":"string","string":"skill_read"}},"arguments.name":{"scalar":{"kind":"string","string":"heimdal"}},"arguments.query":{"scalar":{"kind":"string","string":"visual evidence"}}}}`}
	preview := renderBragiCard(card)
	for _, expected := range []string{"PREPARING", "skill_read", "heimdal", "searching references", "visual evidence"} {
		if !strings.Contains(preview, expected) {
			t.Fatalf("skill preview missing %q: %s", expected, preview)
		}
	}
}

func TestDiffPreviewUsesDistinctSemanticColors(t *testing.T) {
	preview := renderDiffLines("@@ -1 +1 @@\n-old\n+new", 10)
	for _, expected := range []string{"@@ -1 +1 @@", "-old", "+new", "\x1b["} {
		if !strings.Contains(preview, expected) {
			t.Fatalf("diff preview missing %q: %s", expected, preview)
		}
	}
	if !strings.Contains(preview, colors.DiffAdd.Render("+new")) || !strings.Contains(preview, colors.DiffDelete.Render("-old")) {
		t.Fatalf("diff additions and deletions do not use readable backgrounds: %q", preview)
	}
}

func TestActionRailMakesCommitAndDispatchBoundaryVisible(t *testing.T) {
	committed := renderActionRail("committed")
	for _, expected := range []string{"✓ INTENT", "✓ VALIDATED", "● COMMITTED", "○ DISPATCHED", "○ RESULT"} {
		if !strings.Contains(committed, expected) {
			t.Fatalf("committed rail missing %q: %s", expected, committed)
		}
	}
	failed := renderActionRail("failed")
	if !strings.Contains(failed, "✓ DISPATCHED") || !strings.Contains(failed, "× RESULT") {
		t.Fatalf("failed rail = %s", failed)
	}
	invalid := renderActionRail("invalid")
	if !strings.Contains(invalid, "✓ INTENT") || !strings.Contains(invalid, "× VALIDATED") || !strings.Contains(invalid, "○ COMMITTED") {
		t.Fatalf("invalid rail = %s", invalid)
	}
}

func TestRejectedDraftIsOneConciseRepairStatus(t *testing.T) {
	preview := renderBragiCard(BragiCard{State: "commit.rejected", Message: "A final response field is missing."})
	if !strings.Contains(preview, "RESPONSE DRAFT NEEDS REPAIR") || !strings.Contains(preview, "final response field is missing") || strings.Contains(preview, "PROPOSED") {
		t.Fatalf("rejected draft preview = %s", preview)
	}
}
