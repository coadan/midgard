package eventlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"midgard/migrations"

	_ "modernc.org/sqlite"
)

var (
	ErrStaleSequence  = errors.New("stale session sequence")
	ErrDuplicateEvent = errors.New("duplicate event id")
)

type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Projector interface {
	Name() string
	Reset(context.Context, DBTX) error
	Apply(context.Context, DBTX, Event) error
}

type Store struct {
	db         *sql.DB
	projectors []Projector
	now        func() time.Time
}

func Open(ctx context.Context, path string, projectors ...Projector) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;"+migrations.Initial); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize event store: %w", err)
	}
	if err := migrations.Apply(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate event store: %w", err)
	}
	return &Store{db: db, projectors: projectors, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

// Append assigns the next per-session sequence and applies all registered
// projections in the same transaction. expectedHead provides the optimistic
// ownership fence; pass zero only for the first event in a new session.
func (s *Store) Append(ctx context.Context, draft Draft, expectedHead int64) (Event, error) {
	if err := draft.Validate(); err != nil {
		return Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO session_heads(session_id, sequence) VALUES (?, 0) ON CONFLICT(session_id) DO NOTHING`, draft.SessionID); err != nil {
		return Event{}, err
	}
	var head int64
	if err := tx.QueryRowContext(ctx, `SELECT sequence FROM session_heads WHERE session_id = ?`, draft.SessionID).Scan(&head); err != nil {
		return Event{}, err
	}
	if head != expectedHead {
		return Event{}, fmt.Errorf("%w: expected %d, current %d", ErrStaleSequence, expectedHead, head)
	}
	sequence := head + 1
	createdAt := s.now().Truncate(time.Microsecond)
	_, err = tx.ExecContext(ctx, `INSERT INTO events(
event_id, session_id, sequence, turn_id, actor, kind, schema_version,
causation_id, correlation_id, visibility, payload_json, artifact_ref, created_at
) VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), ?)`,
		draft.EventID, draft.SessionID, sequence, draft.TurnID, draft.Actor, draft.Kind,
		draft.SchemaVersion, draft.CausationID, draft.CorrelationID, draft.Visibility,
		[]byte(draft.Payload), draft.ArtifactRef, createdAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueConstraint(err) {
			return Event{}, fmt.Errorf("%w: %s", ErrDuplicateEvent, draft.EventID)
		}
		return Event{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE session_heads SET sequence = ? WHERE session_id = ? AND sequence = ?`, sequence, draft.SessionID, head); err != nil {
		return Event{}, err
	}
	event := Event{
		EventID: draft.EventID, SessionID: draft.SessionID, Sequence: sequence,
		TurnID: draft.TurnID, Actor: draft.Actor, Kind: draft.Kind,
		SchemaVersion: draft.SchemaVersion, CausationID: draft.CausationID,
		CorrelationID: draft.CorrelationID, Visibility: draft.Visibility,
		Payload: append([]byte(nil), draft.Payload...), ArtifactRef: draft.ArtifactRef,
		CreatedAt: createdAt,
	}
	for _, projector := range s.projectors {
		if err := projector.Apply(ctx, tx, event); err != nil {
			return Event{}, fmt.Errorf("project %s: %w", projector.Name(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Event{}, err
	}
	return event, nil
}

// AppendCurrent appends against the latest session head and retries when a
// concurrent local producer wins the optimistic sequence race. The draft's
// event ID remains stable across retries.
func (s *Store) AppendCurrent(ctx context.Context, draft Draft) (Event, error) {
	const attempts = 32
	for range attempts {
		head, err := s.Head(ctx, draft.SessionID)
		if err != nil {
			return Event{}, err
		}
		event, err := s.Append(ctx, draft, head)
		if !errors.Is(err, ErrStaleSequence) {
			return event, err
		}
	}
	return Event{}, fmt.Errorf("append %s: concurrent session writers did not converge", draft.EventID)
}

func (s *Store) Events(ctx context.Context, sessionID string, after int64) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event_id, session_id, sequence, COALESCE(turn_id,''), actor, kind,
schema_version, COALESCE(causation_id,''), COALESCE(correlation_id,''), visibility,
COALESCE(payload_json,''), COALESCE(artifact_ref,''), created_at
FROM events WHERE session_id = ? AND sequence > ? ORDER BY sequence`, sessionID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var event Event
		var actor, visibility, created string
		var payload []byte
		if err := rows.Scan(&event.EventID, &event.SessionID, &event.Sequence, &event.TurnID,
			&actor, &event.Kind, &event.SchemaVersion, &event.CausationID,
			&event.CorrelationID, &visibility, &payload, &event.ArtifactRef, &created); err != nil {
			return nil, err
		}
		event.Actor, event.Visibility, event.Payload = Actor(actor), Visibility(visibility), payload
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) Head(ctx context.Context, sessionID string) (int64, error) {
	var head int64
	err := s.db.QueryRowContext(ctx, `SELECT sequence FROM session_heads WHERE session_id = ?`, sessionID).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return head, err
}

// Rebuild resets domain projections and replays every canonical event without
// changing the event log or session sequence heads.
func (s *Store) Rebuild(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range s.projectors {
		if err := p.Reset(ctx, tx); err != nil {
			return fmt.Errorf("reset %s: %w", p.Name(), err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_id, session_id, sequence, COALESCE(turn_id,''), actor, kind,
schema_version, COALESCE(causation_id,''), COALESCE(correlation_id,''), visibility,
COALESCE(payload_json,''), COALESCE(artifact_ref,''), created_at FROM events ORDER BY session_id, sequence`)
	if err != nil {
		return err
	}
	var events []Event
	for rows.Next() {
		var e Event
		var actor, visibility, created string
		var payload []byte
		if err := rows.Scan(&e.EventID, &e.SessionID, &e.Sequence, &e.TurnID, &actor,
			&e.Kind, &e.SchemaVersion, &e.CausationID, &e.CorrelationID, &visibility,
			&payload, &e.ArtifactRef, &created); err != nil {
			rows.Close()
			return err
		}
		e.Actor, e.Visibility = Actor(actor), Visibility(visibility)
		e.Payload = payload
		e.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, e := range events {
		for _, p := range s.projectors {
			if err := p.Apply(ctx, tx, e); err != nil {
				return fmt.Errorf("replay %s at %s/%d: %w", p.Name(), e.SessionID, e.Sequence, err)
			}
		}
	}
	return tx.Commit()
}

func isUniqueConstraint(err error) bool {
	return err != nil && (contains(err.Error(), "UNIQUE constraint failed") || contains(err.Error(), "constraint failed"))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
