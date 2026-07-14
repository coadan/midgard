package state

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestExecutionLeaseExcludesConcurrentOwner(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db1, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	now := time.Now().UTC()
	start := make(chan struct{})
	results := make(chan error, 2)
	var leasesMu sync.Mutex
	var leases []ExecutionLease
	for owner, db := range map[string]*DB{"owner-1": db1, "owner-2": db2} {
		go func(owner string, db *DB) {
			<-start
			lease, err := db.AcquireExecutionLease(ctx, LeaseResourceTask, "task-1", owner, now, time.Minute)
			if err == nil {
				leasesMu.Lock()
				leases = append(leases, lease)
				leasesMu.Unlock()
			}
			results <- err
		}(owner, db)
	}
	close(start)
	var acquired, held int
	for range 2 {
		err := <-results
		if err == nil {
			acquired++
			continue
		}
		var heldErr ExecutionLeaseHeldError
		if errors.As(err, &heldErr) {
			held++
			continue
		}
		t.Fatalf("unexpected acquire error: %v", err)
	}
	if acquired != 1 || held != 1 || len(leases) != 1 {
		t.Fatalf("acquired=%d held=%d leases=%#v", acquired, held, leases)
	}
}

func TestExecutionLeaseRenewReleaseAndFencedReclaim(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	first, err := db.AcquireExecutionLease(ctx, LeaseResourceBenchmark, "suite-1", "owner-1", now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := db.RenewExecutionLease(ctx, first, now.Add(500*time.Millisecond), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Fence != first.Fence || !renewed.ExpiresAt.Equal(now.Add(1500*time.Millisecond)) {
		t.Fatalf("renewed lease = %#v", renewed)
	}
	if _, err := db.AcquireExecutionLease(ctx, LeaseResourceBenchmark, "suite-1", "owner-2", now.Add(time.Second), time.Second); err == nil {
		t.Fatal("active renewed lease was stolen")
	}

	second, err := db.AcquireExecutionLease(ctx, LeaseResourceBenchmark, "suite-1", "owner-2", now.Add(2*time.Second), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if second.Fence != first.Fence+1 {
		t.Fatalf("reclaimed fence = %d, want %d", second.Fence, first.Fence+1)
	}
	if err := db.AssertExecutionLease(ctx, first, now.Add(2*time.Second)); !errors.Is(err, ErrExecutionLeaseLost) {
		t.Fatalf("stale assert error = %v", err)
	}
	if _, err := db.RenewExecutionLease(ctx, first, now.Add(2*time.Second), time.Second); !errors.Is(err, ErrExecutionLeaseLost) {
		t.Fatalf("stale renew error = %v", err)
	}
	if err := db.ReleaseExecutionLease(ctx, first, now.Add(2*time.Second)); !errors.Is(err, ErrExecutionLeaseLost) {
		t.Fatalf("stale release error = %v", err)
	}
	if err := db.ReleaseExecutionLease(ctx, second, now.Add(2500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	third, err := db.AcquireExecutionLease(ctx, LeaseResourceBenchmark, "suite-1", "owner-3", now.Add(2500*time.Millisecond), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if third.Fence != second.Fence+1 {
		t.Fatalf("released reacquire fence = %d, want %d", third.Fence, second.Fence+1)
	}
}
