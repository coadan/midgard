package lease

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"midgard/internal/state"
)

func TestGuardRenewsAndReleasesLease(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db1, err := state.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := state.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	scope, err := Ensure(ctx, db1, state.LeaseResourceTask, "task-1", Options{
		OwnerID: "owner-1", TTL: 120 * time.Millisecond, RenewInterval: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(180 * time.Millisecond)
	if err := Check(scope.Context); err != nil {
		t.Fatalf("renewed guard check: %v", err)
	}
	if _, err := db2.AcquireExecutionLease(ctx, state.LeaseResourceTask, "task-1", "owner-2", time.Now(), time.Second); err == nil {
		t.Fatal("renewed lease was acquired by a competitor")
	}
	firstFence := scope.Lease().Fence
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := db2.AcquireExecutionLease(ctx, state.LeaseResourceTask, "task-1", "owner-2", time.Now(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Fence != firstFence+1 {
		t.Fatalf("reclaimed fence = %d, want %d", reclaimed.Fence, firstFence+1)
	}
}

func TestGuardCancelsContextWhenFenceIsLost(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db1, err := state.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := state.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	scope, err := Ensure(ctx, db1, state.LeaseResourceBenchmark, "suite-1", Options{
		OwnerID: "owner-1", TTL: 40 * time.Millisecond, RenewInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := db2.AcquireExecutionLease(ctx, state.LeaseResourceBenchmark, "suite-1", "owner-2", time.Now(), time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-scope.Context.Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("guard context was not canceled after fence loss")
	}
	if err := Check(scope.Context); !errors.Is(err, state.ErrExecutionLeaseLost) {
		t.Fatalf("guard check error = %v", err)
	}
	if err := scope.Close(); !errors.Is(err, state.ErrExecutionLeaseLost) {
		t.Fatalf("stale close error = %v", err)
	}
}

func TestEnsureReusesNestedResourceGuard(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	outer, err := Ensure(ctx, db, state.LeaseResourceTask, "task-1", Options{OwnerID: "owner-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	inner, err := Ensure(outer.Context, db, state.LeaseResourceTask, "task-1", Options{OwnerID: "owner-2"})
	if err != nil {
		t.Fatal(err)
	}
	if inner.Lease().Fence != outer.Lease().Fence {
		t.Fatalf("inner fence = %d, outer fence = %d", inner.Lease().Fence, outer.Lease().Fence)
	}
	if err := inner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Check(outer.Context); err != nil {
		t.Fatalf("inner close released outer guard: %v", err)
	}
}

func TestScopeCloseIsIdempotentAndConcurrentSafe(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	scope, err := Ensure(ctx, db, state.LeaseResourceTask, "task-1", Options{OwnerID: "owner-1"})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 8)
	for range 8 {
		go func() {
			<-start
			errs <- scope.Close()
		}()
	}
	close(start)
	for range 8 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent close: %v", err)
		}
	}
}
