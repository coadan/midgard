package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const maxFencedWriteAttempts = 50

type executionFenceKey struct{}

type executionFences struct {
	lease  ExecutionLease
	parent *executionFences
}

type executionFenceQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func ContextWithExecutionFence(ctx context.Context, lease ExecutionLease) context.Context {
	parent, _ := ctx.Value(executionFenceKey{}).(*executionFences)
	return context.WithValue(ctx, executionFenceKey{}, &executionFences{lease: lease, parent: parent})
}

func (db *DB) fencedExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if executionFencesFromContext(ctx) == nil {
		return db.conn.ExecContext(ctx, query, args...)
	}
	for attempt := 0; attempt < maxFencedWriteAttempts; attempt++ {
		tx, err := db.conn.BeginTx(ctx, nil)
		if err != nil {
			if isSQLiteBusy(err) {
				if waitErr := waitFencedWriteRetry(ctx, attempt); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, err
		}
		if err := assertExecutionFences(ctx, tx); err != nil {
			_ = tx.Rollback()
			if isSQLiteBusy(err) {
				if waitErr := waitFencedWriteRetry(ctx, attempt); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, err
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			_ = tx.Rollback()
			if isSQLiteBusy(err) {
				if waitErr := waitFencedWriteRetry(ctx, attempt); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			if isSQLiteBusy(err) {
				if waitErr := waitFencedWriteRetry(ctx, attempt); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, err
		}
		return result, nil
	}
	return nil, fmt.Errorf("fenced SQLite write remained busy after %d attempts", maxFencedWriteAttempts)
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database is busy")
}

func waitFencedWriteRetry(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt+1) * 2 * time.Millisecond
	if delay > 50*time.Millisecond {
		delay = 50 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

func assertExecutionFences(ctx context.Context, queryer executionFenceQueryer) error {
	for current := executionFencesFromContext(ctx); current != nil; current = current.parent {
		lease := current.lease
		var valid int
		err := queryer.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM execution_leases
  WHERE resource_type = ? AND resource_id = ?
    AND owner_id = ? AND fence = ? AND state = 'active' AND expires_at > ?
)`, lease.ResourceType, lease.ResourceID, lease.OwnerID, lease.Fence, time.Now().UTC().UnixNano()).Scan(&valid)
		if err != nil {
			return err
		}
		if valid != 1 {
			return executionLeaseLost(lease)
		}
	}
	return nil
}

func executionFencesFromContext(ctx context.Context) *executionFences {
	fences, _ := ctx.Value(executionFenceKey{}).(*executionFences)
	return fences
}
