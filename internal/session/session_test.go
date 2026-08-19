package session_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"midgard/internal/eventlog"
	"midgard/internal/session"
)

func TestSessionProjectIdentitySurvivesRebuild(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := session.Service{Log: store}
	if _, err := service.CreateInProject(ctx, "session-project", "project-1", "change two repositories"); err != nil {
		t.Fatal(err)
	}
	if err := store.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	projection, err := service.Get(ctx, "session-project")
	if err != nil || projection.ProjectID != "project-1" {
		t.Fatalf("projection = %#v, %v", projection, err)
	}
}

func TestModelSelectionWaitsForSafeBoundaryAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := eventlog.Open(ctx, path, session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	service := session.Service{Log: store}
	if _, err := service.Create(ctx, "session-model", "write code"); err != nil {
		t.Fatal(err)
	}
	wanted := session.ModelSelection{Provider: "codex", Profile: "default", Model: "gpt-5.6-codex", Effort: "high"}
	if _, err := service.SelectModel(ctx, "session-model", wanted); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTurn(ctx, "session-model", "turn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SelectModel(ctx, "session-model", session.ModelSelection{Provider: "deepseek", Model: "deepseek-v4-pro", Effort: "standard"}); err == nil || !strings.Contains(err.Error(), "safe turn boundary") {
		t.Fatalf("active-turn selection error = %v", err)
	}
	if _, err := service.EndTurn(ctx, "session-model", "turn-1", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = eventlog.Open(ctx, path, session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service.Log = store
	got, err := service.ModelSelection(ctx, "session-model")
	if err != nil || got != wanted {
		t.Fatalf("selection = %#v, %v", got, err)
	}
	if err := store.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = service.ModelSelection(ctx, "session-model")
	if err != nil || got != wanted {
		t.Fatalf("rebuilt selection = %#v, %v", got, err)
	}
}

func TestInterruptedTurnRecoversIntoNewTurn(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := session.Service{Log: store}
	if _, err := service.Create(ctx, "session-1", "objective"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTurn(ctx, "session-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EndTurn(ctx, "session-1", "turn-1", "interrupted"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTurn(ctx, "session-1", "turn-2"); err != nil {
		t.Fatal(err)
	}
	var first, second string
	if err := store.DB().QueryRowContext(ctx, `SELECT status FROM turn_projection WHERE turn_id='turn-1'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT status FROM turn_projection WHERE turn_id='turn-2'`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != "interrupted" || second != "active" {
		t.Fatalf("turn states = %q, %q", first, second)
	}
}

func TestFailedTurnStoresSanitizedRecoveryReceipt(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := session.Service{Log: store}
	if _, err := service.Create(ctx, "session-failure", "objective"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTurn(ctx, "session-failure", "turn-failure"); err != nil {
		t.Fatal(err)
	}
	want := session.TurnFailure{Code: "provider_response_unavailable", Message: "The model provider did not return a complete response. Continue."}
	if _, err := service.FailTurn(ctx, "session-failure", "turn-failure", want); err != nil {
		t.Fatal(err)
	}
	var code, message string
	if err := store.DB().QueryRowContext(ctx, `SELECT json_extract(payload_json,'$.code'), json_extract(payload_json,'$.message') FROM events WHERE session_id='session-failure' AND kind='turn.failed'`).Scan(&code, &message); err != nil {
		t.Fatal(err)
	}
	if code != want.Code || message != want.Message {
		t.Fatalf("failure receipt = %q, %q", code, message)
	}
	if _, err := service.FailTurn(ctx, "session-failure", "turn-failure", session.TurnFailure{}); err == nil {
		t.Fatal("accepted empty failure receipt")
	}
}

func TestInterruptedTurnReportsNoticeAndDoesNotReplaySteering(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := session.Service{Log: store}
	if _, err := service.Create(ctx, "session-1", "objective"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTurn(ctx, "session-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	control, err := service.Steer(ctx, "session-1", "/exit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.EndTurn(ctx, "session-1", "turn-1", "interrupted"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTurn(ctx, "session-1", "turn-2"); err != nil {
		t.Fatal(err)
	}
	pending, err := service.PendingSteers(ctx, "session-1")
	if err != nil || len(pending) != 0 {
		t.Fatalf("stale steering reached the next turn: %#v, %v", pending, err)
	}
	if _, err := service.AcknowledgeControl(ctx, "session-1", control.ControlID); err != nil {
		t.Fatal(err)
	}
	messages, err := service.Messages(ctx, "session-1")
	if err != nil || len(messages) != 0 {
		t.Fatalf("interrupted steering leaked into transcript: %#v, %v", messages, err)
	}
	interruptions, err := service.Interruptions(ctx, "session-1")
	if err != nil || len(interruptions) != 1 || interruptions[0].TurnID != "turn-1" || interruptions[0].UnknownOutcome {
		t.Fatalf("interruptions = %#v, %v", interruptions, err)
	}
}

func TestSessionRejectsConcurrentActiveTurns(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := session.Service{Log: store}
	if _, err := service.Create(ctx, "session-1", "objective"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTurn(ctx, "session-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	head, _ := store.Head(ctx, "session-1")
	if _, err := service.StartTurn(ctx, "session-1", "turn-2"); err == nil {
		t.Fatal("second active turn started")
	}
	if after, _ := store.Head(ctx, "session-1"); after != head {
		t.Fatalf("rejected turn advanced head from %d to %d", head, after)
	}
}

func TestMessagesAreDurableAndRequireAnActiveTurn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := eventlog.Open(ctx, path, session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	service := session.Service{Log: store}
	if _, err := service.Create(ctx, "session-1", "fix the service"); err != nil {
		t.Fatal(err)
	}
	projection, err := service.Get(ctx, "session-1")
	if err != nil || projection.Objective != "fix the service" {
		t.Fatalf("projection = %#v, %v", projection, err)
	}
	if _, err := service.RecordMessage(ctx, "session-1", "turn-1", "user", "hello"); err == nil {
		t.Fatal("message recorded before turn started")
	}
	if _, err := service.StartTurn(ctx, "session-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordMessage(ctx, "session-1", "turn-1", "user", "fix it"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordMessage(ctx, "session-1", "turn-1", "assistant", "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordMessage(ctx, "session-1", "turn-1", "tool", "bad"); err == nil || !strings.Contains(err.Error(), "role") {
		t.Fatalf("invalid role error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = eventlog.Open(ctx, path, session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service.Log = store
	messages, err := service.Messages(ctx, "session-1")
	if err != nil || len(messages) != 2 || messages[0].Content != "fix it" || messages[1].Content != "done" {
		t.Fatalf("messages = %#v, %v", messages, err)
	}
	if err := store.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	messages, err = service.Messages(ctx, "session-1")
	if err != nil || len(messages) != 2 {
		t.Fatalf("rebuilt messages = %#v, %v", messages, err)
	}
}

func TestCompletedTurnUsageIsDurableAndValidated(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := eventlog.Open(ctx, path, session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	service := session.Service{Log: store}
	if _, err := service.Create(ctx, "session-usage", "measure the turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTurn(ctx, "session-usage", "turn-usage"); err != nil {
		t.Fatal(err)
	}
	invalid := session.TurnUsage{TurnID: "turn-usage", Model: "deepseek-v4-pro", InputTokens: 10, CacheHitInputTokens: 3, CacheMissInputTokens: 6}
	if _, err := service.RecordTurnUsage(ctx, "session-usage", invalid); err == nil {
		t.Fatal("inconsistent input split was accepted")
	}
	invalidThinking := session.TurnUsage{TurnID: "turn-usage", Model: "deepseek-v4-pro", InputTokens: 10, CacheHitInputTokens: 3, CacheMissInputTokens: 7, OutputTokens: 4, ThinkingTokens: 5}
	if _, err := service.RecordTurnUsage(ctx, "session-usage", invalidThinking); err == nil {
		t.Fatal("thinking tokens above completion tokens were accepted")
	}
	want := session.TurnUsage{TurnID: "turn-usage", Model: "deepseek-v4-pro", InputTokens: 10, CacheHitInputTokens: 3, CacheMissInputTokens: 7, OutputTokens: 4,
		ThinkingTokens: 2, ProviderDurationMillis: 250, PeakContextTokens: 8, ContextLimitTokens: 128_000, Compactions: 1}
	if _, err := service.RecordTurnUsage(ctx, "session-usage", want); err != nil {
		t.Fatal(err)
	}
	if usages, err := service.TurnUsages(ctx, "session-usage"); err != nil || len(usages) != 0 {
		t.Fatalf("active turn usage leaked into transcript: %#v, %v", usages, err)
	}
	if _, err := service.EndTurn(ctx, "session-usage", "turn-usage", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = eventlog.Open(ctx, path, session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service.Log = store
	if err := store.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	usages, err := service.TurnUsages(ctx, "session-usage")
	if err != nil || len(usages) != 1 {
		t.Fatalf("durable usages = %#v, %v", usages, err)
	}
	got := usages[0]
	if got.TurnID != want.TurnID || got.Model != want.Model || got.InputTokens != want.InputTokens || got.CacheHitInputTokens != want.CacheHitInputTokens || got.CacheMissInputTokens != want.CacheMissInputTokens || got.OutputTokens != want.OutputTokens || got.ThinkingTokens != want.ThinkingTokens || got.ProviderDurationMillis != want.ProviderDurationMillis || got.PeakContextTokens != want.PeakContextTokens || got.ContextLimitTokens != want.ContextLimitTokens || got.Compactions != want.Compactions {
		t.Fatalf("usage = %#v, want %#v", got, want)
	}
}

func TestRecentMessagesBoundsPresentationWithoutChangingDurableHistory(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := session.Service{Log: store}
	if _, err := service.Create(ctx, "session-window", "long conversation"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTurn(ctx, "session-window", "turn-window"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		content := fmt.Sprintf("message-%02d %s", index, strings.Repeat("x", 80))
		if _, err := service.RecordMessage(ctx, "session-window", "turn-window", "user", content); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.RecordMessage(ctx, "session-window", "turn-window", "assistant", strings.Repeat("z", 500)); err != nil {
		t.Fatal(err)
	}
	window, err := service.RecentMessages(ctx, "session-window", 500, 120)
	if err != nil {
		t.Fatal(err)
	}
	if window.OmittedMessages == 0 || window.ShortenedMessages != 1 || len(window.Messages) >= 21 {
		t.Fatalf("window = %#v", window)
	}
	if last := window.Messages[len(window.Messages)-1].Content; !strings.Contains(last, "Message shortened in the TUI") || len(last) > 250 {
		t.Fatalf("last presented message was not bounded: %d bytes, %q", len(last), last)
	}
	all, err := service.Messages(ctx, "session-window")
	if err != nil || len(all) != 21 || len(all[len(all)-1].Content) != 500 {
		t.Fatalf("durable messages changed: count=%d last=%d err=%v", len(all), len(all[len(all)-1].Content), err)
	}
}

func TestControlsAreDurableAndAcknowledged(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := session.Service{Log: store}
	if _, err := service.Create(ctx, "session-1", "objective"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTurn(ctx, "session-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	control, err := service.Steer(ctx, "session-1", "focus on tests")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcknowledgeControl(ctx, "session-1", control.ControlID); err != nil {
		t.Fatal(err)
	}
	var acknowledged int
	if err := store.DB().QueryRowContext(ctx, `SELECT acknowledged FROM control_projection WHERE control_id=?`, control.ControlID).Scan(&acknowledged); err != nil {
		t.Fatal(err)
	}
	if acknowledged != 1 {
		t.Fatalf("acknowledged = %d", acknowledged)
	}
}
