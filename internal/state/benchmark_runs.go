package state

import (
	"context"
	"database/sql"
	"fmt"
)

type BenchmarkRun struct {
	ID                string
	ManifestID        string
	ManifestChecksum  string
	ExecutionChecksum string
	ExecutionJSON     string
	Status            string
	StartedAt         string
	UpdatedAt         string
	FinishedAt        string
}

type BenchmarkRunRepo struct {
	RunID       string
	RepoID      string
	CheckoutRef string
	StartCommit string
}

type BenchmarkRunItem struct {
	RunID      string
	ItemID     string
	Ordinal    int
	TaskID     string
	Phase      string
	Status     string
	ErrorClass string
	Error      string
	Score      string
	StartedAt  string
	UpdatedAt  string
	FinishedAt string
}

func (db *DB) InsertBenchmarkRun(ctx context.Context, run BenchmarkRun, repos []BenchmarkRunRepo, items []BenchmarkRunItem) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := assertExecutionFences(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO benchmark_runs (
  id, manifest_id, manifest_checksum, execution_checksum, execution_json,
  status, started_at, updated_at, finished_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.ManifestID, run.ManifestChecksum, run.ExecutionChecksum, run.ExecutionJSON,
		run.Status, run.StartedAt, run.UpdatedAt, run.FinishedAt,
	); err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, repo := range repos {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO benchmark_run_repos (run_id, repo_id, checkout_ref, start_commit)
VALUES (?, ?, ?, ?)`, run.ID, repo.RepoID, repo.CheckoutRef, repo.StartCommit); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO benchmark_run_items (
  run_id, item_id, ordinal, task_id, phase, status, error_class, error,
  score, started_at, updated_at, finished_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			run.ID, item.ItemID, item.Ordinal, item.TaskID, item.Phase, item.Status,
			item.ErrorClass, item.Error, item.Score, item.StartedAt, item.UpdatedAt, item.FinishedAt,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) BenchmarkRunByManifest(ctx context.Context, manifestID string) (BenchmarkRun, error) {
	var run BenchmarkRun
	err := db.conn.QueryRowContext(ctx, `
SELECT id, manifest_id, manifest_checksum, execution_checksum, execution_json,
       status, started_at, updated_at, finished_at
FROM benchmark_runs
WHERE manifest_id = ?`, manifestID).Scan(
		&run.ID, &run.ManifestID, &run.ManifestChecksum, &run.ExecutionChecksum, &run.ExecutionJSON,
		&run.Status, &run.StartedAt, &run.UpdatedAt, &run.FinishedAt,
	)
	return run, err
}

func (db *DB) BenchmarkRunRepos(ctx context.Context, runID string) ([]BenchmarkRunRepo, error) {
	rows, err := db.conn.QueryContext(ctx, `
SELECT run_id, repo_id, checkout_ref, start_commit
FROM benchmark_run_repos
WHERE run_id = ?
ORDER BY repo_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repos []BenchmarkRunRepo
	for rows.Next() {
		var repo BenchmarkRunRepo
		if err := rows.Scan(&repo.RunID, &repo.RepoID, &repo.CheckoutRef, &repo.StartCommit); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}

func (db *DB) BenchmarkRunItems(ctx context.Context, runID string) ([]BenchmarkRunItem, error) {
	rows, err := db.conn.QueryContext(ctx, `
SELECT run_id, item_id, ordinal, task_id, phase, status, error_class, error,
       score, started_at, updated_at, finished_at
FROM benchmark_run_items
WHERE run_id = ?
ORDER BY ordinal`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []BenchmarkRunItem
	for rows.Next() {
		var item BenchmarkRunItem
		if err := rows.Scan(
			&item.RunID, &item.ItemID, &item.Ordinal, &item.TaskID, &item.Phase, &item.Status,
			&item.ErrorClass, &item.Error, &item.Score, &item.StartedAt, &item.UpdatedAt, &item.FinishedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (db *DB) UpdateBenchmarkRun(ctx context.Context, runID, status, updatedAt, finishedAt string) error {
	result, err := db.fencedExecContext(ctx, `
UPDATE benchmark_runs
SET status = ?, updated_at = ?, finished_at = ?
WHERE id = ?`, status, updatedAt, finishedAt, runID)
	if err != nil {
		return err
	}
	return requireBenchmarkRow(result, "run", runID)
}

func (db *DB) UpdateBenchmarkRunItem(ctx context.Context, item BenchmarkRunItem) error {
	result, err := db.fencedExecContext(ctx, `
UPDATE benchmark_run_items
SET phase = ?, status = ?, error_class = ?, error = ?, score = ?,
    started_at = CASE WHEN started_at = '' THEN ? ELSE started_at END,
    updated_at = ?, finished_at = ?
WHERE run_id = ? AND item_id = ?`,
		item.Phase, item.Status, item.ErrorClass, item.Error, item.Score,
		item.StartedAt, item.UpdatedAt, item.FinishedAt, item.RunID, item.ItemID,
	)
	if err != nil {
		return err
	}
	return requireBenchmarkRow(result, "run item", item.RunID+"/"+item.ItemID)
}

func (db *DB) DeleteBenchmarkRunByManifest(ctx context.Context, manifestID string) error {
	_, err := db.fencedExecContext(ctx, `DELETE FROM benchmark_runs WHERE manifest_id = ?`, manifestID)
	return err
}

func IsNoBenchmarkRun(err error) bool {
	return err == sql.ErrNoRows
}

func requireBenchmarkRow(result sql.Result, kind, id string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("benchmark %s %q not found", kind, id)
	}
	return nil
}
