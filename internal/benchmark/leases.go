package benchmark

import (
	"context"
	"errors"

	"midgard/internal/lease"
	"midgard/internal/state"
	"midgard/internal/workbench"
)

type benchmarkExecution struct {
	Context context.Context
	db      *state.DB
	scope   *lease.Scope
}

func acquireBenchmarkExecution(ctx context.Context, root, manifestID string) (*benchmarkExecution, error) {
	if err := checkBenchmarkExecution(ctx); err != nil {
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
	execution, err := acquireBenchmarkExecutionWithDB(ctx, db, manifestID)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	execution.db = db
	return execution, nil
}

func acquireBenchmarkExecutionWithDB(ctx context.Context, db *state.DB, manifestID string) (*benchmarkExecution, error) {
	scope, err := lease.Ensure(ctx, db, state.LeaseResourceBenchmark, manifestID, lease.Options{})
	if err != nil {
		return nil, err
	}
	return &benchmarkExecution{Context: scope.Context, scope: scope}, nil
}

func (e *benchmarkExecution) Close() error {
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

func checkBenchmarkExecution(ctx context.Context) error {
	return lease.Check(ctx)
}
