package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"midgard/internal/agentloop"
	runtimeenv "midgard/internal/environment"
	"midgard/internal/local"
	"midgard/internal/session"
)

func TestHomeRendersRepositorySessionsAtSmallTerminal(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.summaries = []session.Summary{{SessionID: "session_one", Objective: "fix tests", Status: "active", WorktreeRoot: "/tmp/worktree"}}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	view := updated.(Model).View().Content
	for _, expected := range []string{"MIDGARD", "repository", "/repo", "CHATS", "fix tests", "enter open", "\x1b["} {
		if !strings.Contains(view, expected) {
			t.Fatalf("view missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "worktree") || strings.Contains(view, "/tmp/worktree") {
		t.Fatalf("internal worktree location leaked into home:\n%s", view)
	}
}

func TestTranscriptRefreshPreservesTurnFailureAndShowsRecoveryContext(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID, model.running = ModeChat, "session-1", true
	failure := errors.New("provider connection closed")
	updated, _ := model.Update(turnDone{err: failure})
	model = updated.(Model)
	updated, _ = model.Update(loadedMsg{id: "session-1", gitStatus: "clean", preserveStatus: true})
	model = updated.(Model)
	if model.err == nil || !strings.Contains(model.err.Error(), "provider connection closed") || !strings.Contains(model.status, "turn stopped") {
		t.Fatalf("refresh hid turn failure: status=%q err=%v", model.status, model.err)
	}
	view := model.View().Content
	if !strings.Contains(view, "Midgard needs attention") || !strings.Contains(view, "provider connection closed") {
		t.Fatalf("failure is not actionable in the TUI:\n%s", view)
	}
}

func TestRunningTurnExplainsItsCurrentPhase(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID, model.running = ModeChat, "session-1", true
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	model.applyActivity(agentloop.Activity{Kind: "provider", State: "running", ProviderCalls: 1})
	if !strings.Contains(model.status, "waiting for the model") || !strings.Contains(model.status, "1/24") {
		t.Fatalf("provider phase = %q", model.status)
	}
	view := model.View().Content
	if !strings.Contains(view, "waiting for the model") || !strings.Contains(view, model.progress.View()) {
		t.Fatalf("running phase is not visible:\n%s", view)
	}
	model.applyActivity(agentloop.Activity{Kind: "provider", State: "completed", ProviderCalls: 1, InputTokens: 12_400, CacheHitInputTokens: 8_100, CacheMissInputTokens: 4_300, OutputTokens: 2_300})
	if !strings.Contains(model.status, "↻ 65% cache") || !strings.Contains(model.status, "↑ 12.4k input") || !strings.Contains(model.status, "↓ 2.3k output") {
		t.Fatalf("live usage status = %q", model.status)
	}
	view = model.View().Content
	for _, expected := range []string{"●", "thinking", "1/24", "↻ 65%", "↑ 12.4k", "↓ 2.3k"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("visual status missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "cached input") || strings.Contains(view, "uncached input") {
		t.Fatalf("live status kept verbose labels:\n%s", view)
	}
	model.applyActivity(agentloop.Activity{Kind: "tool", Name: "check_run", State: "running", At: time.Now()})
	if model.status != "running check_run" {
		t.Fatalf("tool phase = %q", model.status)
	}
}

func TestThinkingShowsOnlyAsTransientChatStatus(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID, model.running = ModeChat, "session-1", true
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	model.applyActivity(agentloop.Activity{Kind: "stream", State: "thinking", Message: "I should inspect\n the repository first."})
	thinkingView := model.View().Content
	if !strings.Contains(thinkingView, "thinking") || strings.Contains(thinkingView, "I should inspect") || strings.Contains(thinkingView, "THINKING") {
		t.Fatalf("thinking details leaked or status is missing:\n%s", thinkingView)
	}
	model.applyActivity(agentloop.Activity{Kind: "stream", State: "output", Message: "This project "})
	model.applyActivity(agentloop.Activity{Kind: "stream", State: "output", Message: "is a todo service."})
	view := model.View().Content
	for _, expected := range []string{"MIDGARD", "receiving model updates"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("streaming view missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "I should inspect") || model.status != "receiving model updates" || strings.Contains(view, "This project is a todo service.") {
		t.Fatalf("thinking or raw model output leaked, or status was stale: status=%q\n%s", model.status, view)
	}
}

func TestControlCClearsOnlyIdleComposer(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID, model.status = ModeChat, "session-1", "ready"
	model.messages = []session.Message{{Role: "user", Content: "old task", TurnID: "turn-1", Sequence: 2}}
	model.interruptions = []session.Interruption{{TurnID: "turn-1", Sequence: 3}}
	model.turnUsages = []session.TurnUsage{{TurnID: "turn-1", Model: "deepseek-v4-pro", InputTokens: 100, CacheMissInputTokens: 100}}
	model.tools["action-1"] = &ToolCard{ActionID: "action-1", TurnID: "turn-1", Name: "shell"}
	model.toolOrder = []string{"action-1"}
	model.composer.SetValue("draft text")
	updated, command := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(Model)
	if command != nil {
		t.Fatal("idle ctrl+c returned a quit command")
	}
	if model.sessionID != "session-1" || len(model.messages) != 1 || len(model.interruptions) != 1 || len(model.turnUsages) != 1 || len(model.tools) != 1 || model.composer.Value() != "" {
		t.Fatalf("composer or chat state is wrong: session=%q messages=%d interruptions=%d usage=%d tools=%d input=%q", model.sessionID, len(model.messages), len(model.interruptions), len(model.turnUsages), len(model.tools), model.composer.Value())
	}
	if model.status != "ready" || !strings.Contains(model.chatContent(), "old task") {
		t.Fatalf("chat changed while clearing input: status=%q content=%q", model.status, model.chatContent())
	}
}

func TestControlCClearsComposerWithoutStoppingRunningTurn(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	model.mode, model.sessionID, model.running, model.cancel = ModeChat, "session-1", true, cancel
	model.composer.SetValue("draft steering text")
	updated, command := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(Model)
	if command != nil || model.sessionID != "session-1" || !model.running || model.composer.Value() != "" {
		t.Fatalf("running ctrl+c changed task instead of input: running=%v status=%q session=%q input=%q", model.running, model.status, model.sessionID, model.composer.Value())
	}
	select {
	case <-ctx.Done():
		t.Fatal("running turn was cancelled")
	default:
	}
}

func TestDraftResponseShowsStableStatusWithoutPartialText(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID, model.running = ModeChat, "session-1", true
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	entity := `{"id":"@message1","type":"message","revision":1,"status":"draft","fields":{"content":{"scalar":{"kind":"string","string":"This partial response should never flash."}}}}`
	model.applyActivity(agentloop.Activity{Kind: "model_state", State: "op.accepted", EntityID: "@message1", Name: "message", Revision: 1, Arguments: entity})
	view := model.View().Content
	if !strings.Contains(view, "responding") || strings.Contains(view, "partial response") || strings.Contains(view, "DRAFT RESPONSE") {
		t.Fatalf("draft response flickered or stable status is missing:\n%s", view)
	}
	if model.activeModelState != nil {
		t.Fatalf("message draft remained as a transient preview: %#v", model.activeModelState)
	}
}

func TestFileEditDraftShowsSummaryWithoutCodeDump(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID, model.running = ModeChat, "session-1", true
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	entity := `{"id":"@edit1","type":"tool","revision":1,"status":"draft","fields":{"name":{"scalar":{"kind":"string","string":"file_replace"}},"arguments.path":{"scalar":{"kind":"string","string":"README.md"}},"arguments.content":{"scalar":{"kind":"string","string":"package main\nfunc main() {}\n"}},"reason":{"scalar":{"kind":"string","string":"update the file"}}}}`
	model.applyActivity(agentloop.Activity{Kind: "model_state", State: "op.accepted", EntityID: "@edit1", Name: "tool", Revision: 1, Arguments: entity})
	view := model.View().Content
	for _, expected := range []string{"PREPARING", "file_replace", "README.md", "2 lines drafted", "\x1b["} {
		if !strings.Contains(view, expected) {
			t.Fatalf("live edit preview missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "package main") || strings.Contains(view, "func main") {
		t.Fatalf("live edit preview dumped code:\n%s", view)
	}
}

func TestTransientModelStateReplacesTheSameChatSlot(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID, model.running = ModeChat, "session-1", true
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 90, Height: 28})
	model = updated.(Model)
	first := `{"id":"@read1","type":"tool","revision":1,"status":"draft","fields":{"name":{"scalar":{"kind":"string","string":"file_inspect"}},"arguments.path":{"scalar":{"kind":"string","string":"README.md"}},"reason":{"scalar":{"kind":"string","string":"read overview"}}}}`
	second := `{"id":"@read2","type":"tool","revision":1,"status":"draft","fields":{"name":{"scalar":{"kind":"string","string":"file_inspect"}},"arguments.path":{"scalar":{"kind":"string","string":"TASKS.md"}},"reason":{"scalar":{"kind":"string","string":"read task notes"}}}}`
	model.applyActivity(agentloop.Activity{Kind: "model_state", State: "op.accepted", EntityID: "@read1", Name: "tool", Arguments: first})
	model.applyActivity(agentloop.Activity{Kind: "model_state", State: "op.accepted", EntityID: "@read2", Name: "tool", Arguments: second})
	view := model.View().Content
	if strings.Contains(view, "README.md") || !strings.Contains(view, "TASKS.md") {
		t.Fatalf("transient state accumulated instead of replacing in place:\n%s", view)
	}
}

func TestToolsPrecedeFinalResponseAndChatTextWraps(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID = ModeChat, "session-1"
	longResponse := "This response contains enough ordinary words that it must wrap across several readable terminal lines."
	model.messages = []session.Message{
		{Role: "user", Content: "What is this project?", TurnID: "turn-1"},
		{Role: "assistant", Content: longResponse, TurnID: "turn-1"},
	}
	now := time.Now()
	model.applyActivity(agentloop.Activity{Kind: "tool", TurnID: "turn-1", ActionID: "action-1", Name: "file_inspect", State: "succeeded", At: now})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 48, Height: 28})
	view := updated.(Model).View().Content
	userAt, toolAt, responseAt := strings.Index(view, "What is this project?"), strings.Index(view, "Explored"), strings.Index(view, "This response contains")
	if userAt < 0 || toolAt <= userAt || responseAt <= toolAt {
		t.Fatalf("chat order is not user, tools, response:\n%s", view)
	}
	if strings.Contains(view, longResponse) || strings.Contains(view, "INTENT") {
		t.Fatalf("response did not wrap or completed action stayed expanded:\n%s", view)
	}
}

func TestReloadedActionTimelineUsesDurableOrder(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID = ModeChat, "session-1"
	loaded, _ := model.Update(loadedMsg{
		id: "session-1", timelineLoaded: true, gitStatus: "clean",
		messages: []session.Message{
			{Role: "user", Content: "Check the repository", TurnID: "turn-1", Sequence: 2},
			{Role: "assistant", Content: "Everything is ready.", TurnID: "turn-1", Sequence: 9},
		},
		actions: []local.ActionTimeline{{
			ActionID: "action-1", TurnID: "turn-1", Capability: "file.inspect", State: "succeeded",
			Arguments: []byte(`{"path":"README.md"}`), StartedSequence: 5,
		}},
	})
	model = loaded.(Model)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 90, Height: 28})
	view := updated.(Model).View().Content
	requestAt, actionAt, responseAt := strings.Index(view, "Check the repository"), strings.Index(view, "Explored"), strings.Index(view, "Everything is ready.")
	if requestAt < 0 || actionAt <= requestAt || responseAt <= actionAt {
		t.Fatalf("reloaded timeline order is not user, action, response:\n%s", view)
	}
	if !strings.Contains(view, "README.md") {
		t.Fatalf("reloaded action card was not rendered:\n%s", view)
	}
}

func TestLiveMessageSequenceSettlesTheOptimisticChatRow(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID = ModeChat, "session-1"
	// The TUI creates this row immediately, before RunTurn generates the durable
	// turn ID. The activity must settle this row rather than append a duplicate.
	model.messages = []session.Message{{Role: "user", Content: "Inspect README"}}
	model.applyActivity(agentloop.Activity{Kind: "message", SessionID: "session-1", TurnID: "turn-1", Sequence: 2, Role: "user", Message: "Inspect README"})
	model.applyActivity(agentloop.Activity{Kind: "tool", TurnID: "turn-1", Sequence: 5, ActionID: "action-1", Name: "file_inspect", State: "succeeded", Arguments: `{"path":"README.md"}`, At: time.Now()})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 90, Height: 28})
	view := updated.(Model).View().Content
	requestAt, actionAt := strings.Index(view, "Inspect README"), strings.Index(view, "Explored")
	if requestAt < 0 || actionAt <= requestAt || len(model.messages) != 1 || model.messages[0].TurnID != "turn-1" || model.messages[0].Sequence != 2 {
		t.Fatalf("optimistic row did not settle into the durable timeline: messages=%#v\n%s", model.messages, view)
	}
}

func TestLongChatProjectionShowsOmissionMarkerAndBoundsToolCards(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID, model.omittedMessages = ModeChat, "session-1", 42
	for index := 0; index < tuiToolCardLimit+12; index++ {
		id := fmt.Sprintf("action-%03d", index)
		model.applyActivity(agentloop.Activity{Kind: "tool", TurnID: "turn-1", ActionID: id, Name: "file_inspect", State: "succeeded", At: time.Now()})
	}
	if len(model.toolOrder) != tuiToolCardLimit || len(model.tools) != tuiToolCardLimit || model.omittedActivities != 12 {
		t.Fatalf("tool window = order %d map %d omitted %d", len(model.toolOrder), len(model.tools), model.omittedActivities)
	}
	content := model.chatContent()
	if !strings.Contains(content, "42 older messages") || !strings.Contains(content, "12 older activities") || !strings.Contains(content, "complete history remains saved") {
		t.Fatalf("omission marker = %q", content)
	}
}

func TestRenderedChatWindowKeepsNewestLinesWithinHardLimit(t *testing.T) {
	var content strings.Builder
	for line := 1; line <= tuiTranscriptRenderedLines+50; line++ {
		fmt.Fprintf(&content, "line %d\n", line)
	}
	rendered := limitRenderedChatLines(content.String(), tuiTranscriptRenderedLines)
	if got := len(strings.Split(rendered, "\n")); got != tuiTranscriptRenderedLines {
		t.Fatalf("rendered line count = %d, want %d", got, tuiTranscriptRenderedLines)
	}
	for _, expected := range []string{"older rendered lines are hidden", fmt.Sprintf("line %d", tuiTranscriptRenderedLines+50)} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("bounded transcript missing %q", expected)
		}
	}
	if strings.Contains(rendered, "line 1\n") {
		t.Fatalf("oldest rendered line remained in bounded transcript")
	}
}

func TestFailedToolKeepsConciseRecoveryDetail(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID = ModeChat, "session-1"
	now := time.Now()
	model.applyActivity(agentloop.Activity{Kind: "tool", TurnID: "turn-1", ActionID: "failed-action", Name: "file_replace", State: "failed", Output: `{"error":"stale file"}`, At: now})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	collapsed := model.View().Content
	if !strings.Contains(collapsed, "Could not edit file") || !strings.Contains(collapsed, "stale file") || strings.Contains(collapsed, "INTENT") {
		t.Fatalf("failed tool did not collapse:\n%s", collapsed)
	}
}

func TestGitDiffRendersNoEditDetails(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID = ModeChat, "session-1"
	diff := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1,2 @@\n-old\n+new\n+more\ndiff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-package old\n+package main\n"
	output := `{"stdout":` + strconv.Quote(diff) + `,"exit_code":0}`
	model.applyActivity(agentloop.Activity{Kind: "tool", TurnID: "turn-1", ActionID: "diff-action", Name: "git_diff", State: "succeeded", Output: output, At: time.Now()})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	view := updated.(Model).View().Content
	for _, expected := range []string{"Inspected the worktree", "Existing uncommitted changes"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("worktree inspection missing %q:\n%s", expected, view)
		}
	}
	for _, unexpected := range []string{"README.md", "main.go", "@@ -1 +1,2 @@", "Edited"} {
		if strings.Contains(view, unexpected) {
			t.Fatalf("worktree inspection exposed edit detail %q:\n%s", unexpected, view)
		}
	}
}

func TestChatToolCardRendersCompactlyAtWideTerminal(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID = ModeChat, "session_123456789"
	model.messages = []session.Message{{Role: "user", Content: "fix it"}, {Role: "assistant", Content: "working"}}
	now := time.Now()
	model.applyActivity(agentloop.Activity{Kind: "tool", ActionID: "action-1", Name: "go_test", State: "running", Arguments: `{"argv":["go","test","./..."]}`, At: now})
	model.applyActivity(agentloop.Activity{Kind: "tool", ActionID: "action-1", Name: "go_test", State: "succeeded", Output: `{"exit_code":0}`, At: now.Add(time.Second)})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	chat := updated.(Model).View().Content
	for _, expected := range []string{"›", "│", "fix it", "repository", "/repo", "\x1b["} {
		if !strings.Contains(chat, expected) {
			t.Fatalf("chat missing %q:\n%s", expected, chat)
		}
	}
	if strings.Contains(chat, "worktree") || strings.Contains(chat, "INTENT") || strings.Contains(chat, "exit_code") {
		t.Fatalf("internal worktree location leaked into chat:\n%s", chat)
	}
}

func TestChatFooterHasNoPersistentMetadataOrRemovedCommands(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID, model.gitStatus = ModeChat, "session-1", "modified"
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	compact := model.View().Content
	for _, hidden := range []string{"calls 0/24", "actions 0", "model deepseek-v4-pro", "ctrl+r review", "/status", "/help", "/review", "/expand"} {
		if strings.Contains(compact, hidden) {
			t.Fatalf("persistent metadata %q remained in compact footer:\n%s", hidden, compact)
		}
	}
}

func TestSlashMenuHasNoLineNumberAndFiltersByPrefix(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID = ModeChat, "session-1"
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	if model.composer.ShowLineNumbers {
		t.Fatal("composer still shows line numbers")
	}
	model.composer.SetValue("/env")
	model.updateSlashMenu()
	menu := model.slashMenuView()
	if !strings.Contains(menu, "/env") || strings.Contains(menu, "/env status") || strings.Contains(menu, "/env use") {
		t.Fatalf("environment menu is not a single entry:\n%s", menu)
	}
	for _, hidden := range []string{"/review", "/repo add", "/quit"} {
		if strings.Contains(menu, hidden) {
			t.Fatalf("prefix-filtered menu retained %q:\n%s", hidden, menu)
		}
	}

	model.composer.SetValue("/repo")
	model.updateSlashMenu()
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if model.composer.Value() != "/repo add " || model.slashMenuOpen {
		t.Fatalf("argument command selection = %q, menu open=%v", model.composer.Value(), model.slashMenuOpen)
	}
}

func TestSlashMenuSelectionOpensSkillPicker(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID = ModeChat, "session-1"
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	model.composer.SetValue("/skills")
	model.updateSlashMenu()
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if model.mode != ModeSkills || command == nil {
		t.Fatalf("/skills did not open picker: mode=%v command=%v", model.mode, command)
	}
}

func TestModelPickerGroupsProvidersAndQueuesSafeBoundarySwitch(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", ProviderName: "deepseek", Model: "deepseek-v4-pro", Effort: "standard"}, "")
	model.mode, model.sessionID, model.running = ModeModels, "session-1", true
	model.modelOptions = []local.ModelOption{
		{Provider: "deepseek", ProviderName: "DeepSeek", Model: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", Efforts: []string{"standard", "high"}, Effort: "standard", Selected: true},
		{Provider: "codex", ProviderName: "Codex", Model: "gpt-5.6-codex", Name: "GPT-5.6 Codex", Efforts: []string{"low", "medium", "high"}, Effort: "medium"},
	}
	model.modelSelected = 1
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	model = updated.(Model)
	if command != nil || model.modelOptions[1].Effort != "high" {
		t.Fatalf("picker effort = %q, command=%v", model.modelOptions[1].Effort, command)
	}
	updated, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if command != nil || model.pendingModel == nil || model.pendingModel.Model != "gpt-5.6-codex" || model.pendingModel.Effort != "high" || model.mode != ModeChat || !strings.Contains(model.status, "after the current turn") {
		t.Fatalf("queued switch = %#v mode=%v status=%q command=%v", model.pendingModel, model.mode, model.status, command)
	}
	view := model.modelsView()
	for _, expected := range []string{"DeepSeek", "Codex", "GPT-5.6 Codex"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("model view missing %q:\n%s", expected, view)
		}
	}
}

func TestSkillPickerDistinguishesTechnologyGroups(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo"}, "")
	model.mode = ModeSkills
	model.skillStatuses = []local.SkillStatus{
		{Name: "xtdb", Group: "xtdb", Enabled: false, IsGroup: true, Members: 1},
		{Name: "xtdb-query-and-transact", Group: "xtdb", Description: "XTDB persistence", Enabled: false},
		{Name: "evidence-first-planning", Description: "General planning", Enabled: true},
	}
	view := model.skillsView()
	for _, expected := range []string{"xtdb", "1 skills", "xtdb-query-and-transact", "evidence-first-planning", "toggle skill/group"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("skill groups missing %q:\n%s", expected, view)
		}
	}
}

func TestIncomingActivityDoesNotStealScrollPosition(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID, model.running = ModeChat, "session-1", true
	for index := 0; index < 30; index++ {
		model.messages = append(model.messages, session.Message{Role: "assistant", Content: fmt.Sprintf("Earlier response line %d", index)})
	}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(Model)
	model.viewport.GotoBottom()
	model.viewport.ScrollUp(6)
	offset := model.viewport.YOffset()
	model.applyActivity(agentloop.Activity{Kind: "tool", TurnID: "turn-1", ActionID: "action-new", Name: "file_inspect", State: "queued", Arguments: `{"path":"README.md"}`, At: time.Now()})
	if model.viewport.YOffset() != offset {
		t.Fatalf("activity stole scroll position: before=%d after=%d", offset, model.viewport.YOffset())
	}

	model.viewport.GotoBottom()
	model.applyActivity(agentloop.Activity{Kind: "tool", TurnID: "turn-1", ActionID: "action-new", Name: "file_inspect", State: "validated", Arguments: `{"path":"README.md"}`, At: time.Now().Add(time.Millisecond)})
	if !model.viewport.AtBottom() {
		t.Fatalf("activity stopped following after the user returned to bottom: offset=%d", model.viewport.YOffset())
	}
}

func TestInterruptedTurnRendersInTranscriptWithoutStaleSteering(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID = ModeChat, "session-1"
	model.messages = []session.Message{{Role: "user", Content: "Show me the rendered output", TurnID: "turn-1", Sequence: 2}}
	model.interruptions = []session.Interruption{{TurnID: "turn-1", Sequence: 5, UnknownOutcome: true}}
	model.controls["old-control"] = "applied"
	model.controlContent["old-control"] = "/exit"
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	updated, _ = model.Update(loadedMsg{id: "session-1", messages: model.messages, interruptions: model.interruptions, preserveStatus: true})
	view := updated.(Model).View().Content
	requestAt := strings.Index(view, "Show me the rendered output")
	interruptedAt := strings.Index(view, "Turn interrupted")
	if requestAt < 0 || interruptedAt <= requestAt || !strings.Contains(view, "outcome is unknown") {
		t.Fatalf("interruption notice is absent or out of order:\n%s", view)
	}
	if strings.Contains(view, "[steer applied]") || strings.Contains(view, "/exit") {
		t.Fatalf("stale steering leaked into reopened chat:\n%s", view)
	}
}

func TestLatestCompletedTurnCostRendersBelowComposer(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode, model.sessionID = ModeChat, "session-1"
	model.messages = []session.Message{
		{Role: "user", Content: "Implement the filter", TurnID: "turn-1", Sequence: 2},
		{Role: "assistant", Content: "Implemented and tested it.", TurnID: "turn-1", Sequence: 6},
	}
	model.turnUsages = []session.TurnUsage{{TurnID: "turn-1", Model: "deepseek-v4-pro", InputTokens: 12_000, CacheHitInputTokens: 8_000, CacheMissInputTokens: 4_000, OutputTokens: 2_000}}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	view := model.View().Content
	requestAt := strings.Index(view, "Implement the filter")
	costAt := strings.Index(view, "≈ $0.003509")
	answerAt := strings.Index(view, "Implemented and tested it")
	composerAt := strings.Index(view, model.composer.View())
	if requestAt < 0 || answerAt <= requestAt || composerAt <= answerAt || costAt <= composerAt ||
		!strings.Contains(view, "↻ 67% cache") || !strings.Contains(view, "↑ 12.0k input · ↓ 2.0k output") {
		t.Fatalf("cost metadata is absent or misplaced:\n%s", view)
	}
	if strings.Contains(model.chatContent(), "≈ $0.003509") {
		t.Fatalf("cost metadata remained in transcript:\n%s", model.chatContent())
	}
	model.running = true
	if strings.Contains(model.chatView(), "≈ $0.003509") {
		t.Fatalf("completed cost remained visible during a new task:\n%s", model.chatView())
	}
}

func TestRepoAddQueuesWhileAgentWorksAndAsksForProjectName(t *testing.T) {
	runtime := &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}
	runtime.Project.Implicit = true
	model := New(context.Background(), runtime, "")
	model.mode, model.sessionID, model.running = ModeChat, "session-1", true
	model.composer.SetValue("/repo add /repos/bragi")
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if model.pendingRepoPath != "/repos/bragi" || !strings.Contains(model.status, "after the current turn finishes") {
		t.Fatalf("queued repository = %q, status = %q", model.pendingRepoPath, model.status)
	}
	model.running = false
	updated, _ = model.Update(repoPreparedMsg{path: "/repos/bragi", name: "bragi"})
	model = updated.(Model)
	if !model.awaitingProjectName || !strings.Contains(model.status, "Name this project") || !strings.Contains(model.composer.Placeholder, "Project name") {
		t.Fatalf("project prompt = awaiting %v, status %q, placeholder %q", model.awaitingProjectName, model.status, model.composer.Placeholder)
	}
}

func TestRepoAddCopyExplainsHowToRecoverFromIncompleteCommand(t *testing.T) {
	_, recognized, err := parseRepoAdd("/repo add")
	if !recognized || err == nil || !strings.Contains(err.Error(), "/repo add PATH") {
		t.Fatalf("parse result = %v, %v", recognized, err)
	}
}

func TestEnvironmentPickerQueuesAtTurnBoundaryAndShowsOnlySafeMetadata(t *testing.T) {
	runtime := &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}
	model := New(context.Background(), runtime, "")
	model.mode, model.sessionID, model.running = ModeEnvironments, "session-1", true
	model.environmentOptions = []local.EnvironmentOption{{Name: "production", Variables: []runtimeenv.VariableInfo{
		{Name: "PUBLIC_URL", Description: "service URL", Kind: "plain"},
		{Name: "API_TOKEN", Kind: "secret"},
	}}}
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if model.pendingEnvironment != "production" || !strings.Contains(model.status, "after the current turn finishes") {
		t.Fatalf("queued environment = %q, status = %q", model.pendingEnvironment, model.status)
	}
	model.mode = ModeEnvironments
	view := model.environmentsView()
	if !strings.Contains(view, "production") || !strings.Contains(view, "API_TOKEN") || !strings.Contains(view, "secret") {
		t.Fatalf("environment picker = %s", view)
	}
}

func TestEnvironmentHasOneSlashCommand(t *testing.T) {
	command, _, recognized, err := parseEnvironmentCommand("/env")
	if err != nil || !recognized || command != "open" {
		t.Fatalf("parse /env = %q %v %v", command, recognized, err)
	}
	_, _, recognized, err = parseEnvironmentCommand("/env use production")
	if !recognized || err == nil || !strings.Contains(err.Error(), "Enter") {
		t.Fatalf("parse result = %v, %v", recognized, err)
	}
}

func TestSkillPickerFiltersAsUserTypesAndSpaceToggles(t *testing.T) {
	model := New(context.Background(), &local.Runtime{Repository: "/repo", Model: "deepseek-v4-pro", MaxProviderCalls: 24}, "")
	model.mode = ModeSkills
	model.skillStatuses = []local.SkillStatus{
		{Name: "heimdal", Description: "browser evidence", Enabled: true},
		{Name: "midgard", Description: "runtime operations", Enabled: false},
		{Name: "migration", Description: "database changes", Enabled: true},
	}
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'm', Text: "mi"})
	model = updated.(Model)
	visible := model.filteredSkillStatuses()
	if len(visible) != 2 || model.skillFilter != "mi" {
		t.Fatalf("filtered skills = %#v, filter=%q", visible, model.skillFilter)
	}
	updated, command := model.Update(tea.KeyPressMsg{Code: ' '})
	model = updated.(Model)
	if command == nil || model.mode != ModeSkills {
		t.Fatalf("space did not request a toggle: mode=%v command=%v", model.mode, command)
	}
	if view := model.skillsView(); !strings.Contains(view, "filter") || !strings.Contains(view, "mi") || strings.Contains(view, "heimdal") {
		t.Fatalf("filtered picker = %q", view)
	}
}

func TestSkillsHasOneSlashCommand(t *testing.T) {
	command, _, recognized, err := parseSkillCommand("/skills")
	if err != nil || !recognized || command != "status" {
		t.Fatalf("parse /skills = %q %v %v", command, recognized, err)
	}
	_, _, recognized, err = parseSkillCommand("/skills disable midgard")
	if !recognized || err == nil || !strings.Contains(err.Error(), "Space") {
		t.Fatalf("legacy form was not redirected to picker: %v %v", recognized, err)
	}
}
