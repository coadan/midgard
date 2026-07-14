package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	LeaseResourceTask      = "task"
	LeaseResourceBenchmark = "benchmark"
)

var ErrExecutionLeaseLost = errors.New("execution lease lost")

type ExecutionLease struct {
	ResourceType string
	ResourceID   string
	OwnerID      string
	Fence        int64
	State        string
	AcquiredAt   time.Time
	RenewedAt    time.Time
	ExpiresAt    time.Time
}

type ExecutionLeaseHeldError struct {
	Lease      ExecutionLease
	ObservedAt time.Time
}

func (e ExecutionLeaseHeldError) Error() string {
	age := e.ObservedAt.Sub(e.Lease.AcquiredAt)
	if age < 0 {
		age = 0
	}
	return fmt.Sprintf(
		"%s %q is already running under owner %s (fence %d, held for %s, acquired %s, expires %s)",
		e.Lease.ResourceType,
		e.Lease.ResourceID,
		e.Lease.OwnerID,
		e.Lease.Fence,
		age.Round(time.Second),
		e.Lease.AcquiredAt.UTC().Format(time.RFC3339),
		e.Lease.ExpiresAt.UTC().Format(time.RFC3339),
	)
}

func (db *DB) AcquireExecutionLease(ctx context.Context, resourceType, resourceID, ownerID string, now time.Time, ttl time.Duration) (ExecutionLease, error) {
	if resourceType == "" || resourceID == "" || ownerID == "" {
		return ExecutionLease{}, fmt.Errorf("execution lease resource type, resource id, and owner id are required")
	}
	if ttl <= 0 {
		return ExecutionLease{}, fmt.Errorf("execution lease ttl must be positive")
	}
	now = now.UTC()
	expiresAt := now.Add(ttl)
	row := db.conn.QueryRowContext(ctx, `
INSERT INTO execution_leases (
  resource_type, resource_id, owner_id, fence, state,
  acquired_at, renewed_at, expires_at
) VALUES (?, ?, ?, 1, 'active', ?, ?, ?)
ON CONFLICT(resource_type, resource_id) DO UPDATE SET
  owner_id = excluded.owner_id,
  fence = execution_leases.fence + 1,
  state = 'active',
  acquired_at = excluded.acquired_at,
  renewed_at = excluded.renewed_at,
  expires_at = excluded.expires_at
WHERE execution_leases.state != 'active' OR execution_leases.expires_at <= excluded.acquired_at
RETURNING resource_type, resource_id, owner_id, fence, state,
          acquired_at, renewed_at, expires_at`,
		resourceType, resourceID, ownerID, unixNanos(now), unixNanos(now), unixNanos(expiresAt),
	)
	lease, err := scanExecutionLease(row)
	if err == nil {
		return lease, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ExecutionLease{}, err
	}
	held, lookupErr := db.ExecutionLease(ctx, resourceType, resourceID)
	if lookupErr != nil {
		return ExecutionLease{}, errors.Join(err, lookupErr)
	}
	return ExecutionLease{}, ExecutionLeaseHeldError{Lease: held, ObservedAt: now}
}

func (db *DB) ExecutionLease(ctx context.Context, resourceType, resourceID string) (ExecutionLease, error) {
	return scanExecutionLease(db.conn.QueryRowContext(ctx, `
SELECT resource_type, resource_id, owner_id, fence, state,
       acquired_at, renewed_at, expires_at
FROM execution_leases
WHERE resource_type = ? AND resource_id = ?`, resourceType, resourceID))
}

func (db *DB) RenewExecutionLease(ctx context.Context, lease ExecutionLease, now time.Time, ttl time.Duration) (ExecutionLease, error) {
	if ttl <= 0 {
		return ExecutionLease{}, fmt.Errorf("execution lease ttl must be positive")
	}
	now = now.UTC()
	row := db.conn.QueryRowContext(ctx, `
UPDATE execution_leases
SET renewed_at = ?, expires_at = ?
WHERE resource_type = ? AND resource_id = ?
  AND owner_id = ? AND fence = ? AND state = 'active' AND expires_at > ?
RETURNING resource_type, resource_id, owner_id, fence, state,
          acquired_at, renewed_at, expires_at`,
		unixNanos(now), unixNanos(now.Add(ttl)),
		lease.ResourceType, lease.ResourceID, lease.OwnerID, lease.Fence, unixNanos(now),
	)
	renewed, err := scanExecutionLease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutionLease{}, executionLeaseLost(lease)
	}
	return renewed, err
}

func (db *DB) AssertExecutionLease(ctx context.Context, lease ExecutionLease, now time.Time) error {
	var valid int
	err := db.conn.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM execution_leases
  WHERE resource_type = ? AND resource_id = ?
    AND owner_id = ? AND fence = ? AND state = 'active' AND expires_at > ?
)`, lease.ResourceType, lease.ResourceID, lease.OwnerID, lease.Fence, unixNanos(now.UTC())).Scan(&valid)
	if err != nil {
		return err
	}
	if valid != 1 {
		return executionLeaseLost(lease)
	}
	return nil
}

func (db *DB) ReleaseExecutionLease(ctx context.Context, lease ExecutionLease, now time.Time) error {
	result, err := db.conn.ExecContext(ctx, `
UPDATE execution_leases
SET state = 'released', renewed_at = ?, expires_at = ?
WHERE resource_type = ? AND resource_id = ?
  AND owner_id = ? AND fence = ? AND state = 'active'`,
		unixNanos(now.UTC()), unixNanos(now.UTC()),
		lease.ResourceType, lease.ResourceID, lease.OwnerID, lease.Fence,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return executionLeaseLost(lease)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanExecutionLease(row rowScanner) (ExecutionLease, error) {
	var lease ExecutionLease
	var acquiredAt, renewedAt, expiresAt int64
	err := row.Scan(
		&lease.ResourceType, &lease.ResourceID, &lease.OwnerID, &lease.Fence, &lease.State,
		&acquiredAt, &renewedAt, &expiresAt,
	)
	if err != nil {
		return ExecutionLease{}, err
	}
	lease.AcquiredAt = time.Unix(0, acquiredAt).UTC()
	lease.RenewedAt = time.Unix(0, renewedAt).UTC()
	lease.ExpiresAt = time.Unix(0, expiresAt).UTC()
	return lease, nil
}

func executionLeaseLost(lease ExecutionLease) error {
	return fmt.Errorf(
		"%w: %s %q owner %s fence %d is no longer active",
		ErrExecutionLeaseLost, lease.ResourceType, lease.ResourceID, lease.OwnerID, lease.Fence,
	)
}

func unixNanos(value time.Time) int64 {
	return value.UTC().UnixNano()
}
