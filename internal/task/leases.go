package task

import (
	"context"
	"errors"

	"midgard/internal/lease"
	"midgard/internal/state"
	"midgard/internal/workbench"
)

type Execution struct {
	Context context.Context
	db      *state.DB
	scope   *lease.Scope
}

func AcquireExecution(ctx context.Context, root, taskID string) (*Execution, error) {
	return AcquireExecutionWithOptions(ctx, root, taskID, lease.Options{})
}

func AcquireExecutionWithOptions(ctx context.Context, root, taskID string, opts lease.Options) (*Execution, error) {
	if err := lease.Check(ctx); err != nil {
		return nil, err
	}
	status, err := workbench.Status(root)
	if err != nil {
		return nil, err
	}
	db, err := state.Open(ctx, workbench.NewLayout(status.Root).State)
	if err != nil {
		return nil, err
	}
	execution, err := acquireExecutionWithDBAndOptions(ctx, db, taskID, opts)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	execution.db = db
	return execution, nil
}

func acquireExecutionWithDB(ctx context.Context, db *state.DB, taskID string) (*Execution, error) {
	return acquireExecutionWithDBAndOptions(ctx, db, taskID, lease.Options{})
}

func acquireExecutionWithDBAndOptions(ctx context.Context, db *state.DB, taskID string, opts lease.Options) (*Execution, error) {
	scope, err := lease.Ensure(ctx, db, state.LeaseResourceTask, taskID, opts)
	if err != nil {
		return nil, err
	}
	return &Execution{Context: scope.Context, scope: scope}, nil
}

func (e *Execution) Close() error {
	if e == nil {
		return nil
	}
	var err error
	if e.scope != nil {
		err = e.scope.Close()
	}
	if e.db != nil {
		err = errors.Join(err, e.db.Close())
	}
	return err
}

func CheckExecution(ctx context.Context) error {
	return lease.Check(ctx)
}
