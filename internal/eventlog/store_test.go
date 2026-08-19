package eventlog_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"midgard/internal/eventlog"
	"midgard/internal/session"
	"midgard/migrations"
)

func TestOpenMigratesExistingWorkspaceProjection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, migrations.Initial); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := eventlog.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.DB().QueryContext(ctx, `PRAGMA table_info(workspace_projection)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&index, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	for _, name := range []string{"default_branch", "landing_strategy", "cleanup_state", "project_id", "repository_name"} {
		if !found[name] {
			t.Fatalf("migration column %s was not applied", name)
		}
	}
}

func TestAppendReplayReopenAndRebuild(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := eventlog.Open(ctx, path, session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	created := draft("event-1", "session-1", "session.created")
	if _, err := store.Append(ctx, created, 0); err != nil {
		t.Fatal(err)
	}
	unknown := draft("event-2", "session-1", "provider.unknown")
	unknown.Payload = json.RawMessage(`{"native_kind":"future_event"}`)
	unknown.ArtifactRef = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err := store.Append(ctx, unknown, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, draft("event-3", "session-1", "server.completed"), 0); !errors.Is(err, eventlog.ErrStaleSequence) {
		t.Fatalf("expected stale sequence, got %v", err)
	}
	if _, err := store.Append(ctx, draft("event-1", "session-1", "server.completed"), 2); !errors.Is(err, eventlog.ErrDuplicateEvent) {
		t.Fatalf("expected duplicate event, got %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = eventlog.Open(ctx, path, session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.Events(ctx, "session-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Sequence != 2 || events[1].ArtifactRef != unknown.ArtifactRef {
		t.Fatalf("unexpected replay: %#v", events)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE session_projection SET status='corrupt' WHERE session_id='session-1'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.DB().QueryRowContext(ctx, `SELECT status FROM session_projection WHERE session_id='session-1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("rebuild status = %q", status)
	}
}

type rejectingProjector struct{}

func (rejectingProjector) Name() string                               { return "rejecting" }
func (rejectingProjector) Reset(context.Context, eventlog.DBTX) error { return nil }
func (rejectingProjector) Apply(context.Context, eventlog.DBTX, eventlog.Event) error {
	return errors.New("injected projection failure")
}

func TestProjectionFailureRollsBackEventAndHead(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), rejectingProjector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Append(ctx, draft("event-1", "session-1", "session.created"), 0); err == nil {
		t.Fatal("expected injected failure")
	}
	if head, err := store.Head(ctx, "session-1"); err != nil || head != 0 {
		t.Fatalf("head after rollback = %d, %v", head, err)
	}
	events, err := store.Events(ctx, "session-1", 0)
	if err != nil || len(events) != 0 {
		t.Fatalf("events after rollback = %#v, %v", events, err)
	}
}

func TestAppendCurrentSerializesConcurrentLocalProducers(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.AppendCurrent(ctx, draft("created", "session-1", "session.created")); err != nil {
		t.Fatal(err)
	}
	const producers = 16
	var wait sync.WaitGroup
	errorsCh := make(chan error, producers)
	for index := range producers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := store.AppendCurrent(ctx, draft(fmt.Sprintf("event-%d", index), "session-1", "provider.unknown"))
			errorsCh <- err
		}(index)
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.Events(ctx, "session-1", 0)
	if err != nil || len(events) != producers+1 {
		t.Fatalf("events = %d, %v", len(events), err)
	}
	for index, event := range events {
		if event.Sequence != int64(index+1) {
			t.Fatalf("event %d sequence = %d", index, event.Sequence)
		}
	}
}

func draft(id, sessionID, kind string) eventlog.Draft {
	return eventlog.Draft{EventID: id, SessionID: sessionID, Actor: eventlog.ActorServer,
		Kind: kind, SchemaVersion: 1, Visibility: eventlog.VisibilityInternal}
}
