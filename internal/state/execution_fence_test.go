package state

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestFencedWriteRejectsStaleOwnerAfterReclaim(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	first, err := db.AcquireExecutionLease(ctx, LeaseResourceTask, "task-1", "owner-1", now, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	firstCtx := ContextWithExecutionFence(ctx, first)
	if _, err := db.InsertEvent(firstCtx, Event{Type: "before-reclaim", Payload: `{}`}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	second, err := db.AcquireExecutionLease(ctx, LeaseResourceTask, "task-1", "owner-2", time.Now(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertEvent(firstCtx, Event{Type: "stale-write", Payload: `{}`}); !errors.Is(err, ErrExecutionLeaseLost) {
		t.Fatalf("stale write error = %v", err)
	}
	secondCtx := ContextWithExecutionFence(ctx, second)
	if _, err := db.InsertEvent(secondCtx, Event{Type: "new-owner-write", Payload: `{}`}); err != nil {
		t.Fatal(err)
	}
	var staleWrites int
	if err := db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE type = 'stale-write'`).Scan(&staleWrites); err != nil {
		t.Fatal(err)
	}
	if staleWrites != 0 {
		t.Fatalf("stale writes = %d, want 0", staleWrites)
	}
}

func TestFencedWriteRequiresEveryNestedLease(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	benchmarkLease, err := db.AcquireExecutionLease(ctx, LeaseResourceBenchmark, "suite-1", "suite-owner", now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	taskLease, err := db.AcquireExecutionLease(ctx, LeaseResourceTask, "task-1", "task-owner", now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	nestedCtx := ContextWithExecutionFence(ContextWithExecutionFence(ctx, benchmarkLease), taskLease)
	if _, err := db.InsertEvent(nestedCtx, Event{Type: "nested-valid", Payload: `{}`}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseExecutionLease(ctx, benchmarkLease, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertEvent(nestedCtx, Event{Type: "nested-stale", Payload: `{}`}); !errors.Is(err, ErrExecutionLeaseLost) {
		t.Fatalf("nested stale write error = %v", err)
	}
}
