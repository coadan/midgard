package state

import (
	"context"
	"database/sql"
)

type BenchmarkAcceptanceRun struct {
	ID               string
	TaskID           string
	ItemID           string
	Status           string
	StartedAt        string
	FinishedAt       string
	PatchChecksum    string
	ArtifactRef      string
	ArtifactChecksum string
}

type BenchmarkAcceptanceCheck struct {
	ID                string
	RunID             string
	CheckID           string
	RepoID            string
	Command           string
	CWD               string
	Status            string
	ExpectedExitCode  int
	ExitCode          int
	TimedOut          bool
	StdoutTruncated   bool
	StderrTruncated   bool
	Error             string
	ResultArtifactRef string
	StdoutArtifactRef string
	StderrArtifactRef string
	StartedAt         string
	FinishedAt        string
}

func (db *DB) InsertBenchmarkAcceptanceRun(ctx context.Context, run BenchmarkAcceptanceRun, checks []BenchmarkAcceptanceCheck) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := assertExecutionFences(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO benchmark_acceptance_runs (
  id, task_id, item_id, status, started_at, finished_at, patch_checksum, artifact_ref, artifact_checksum
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.TaskID, run.ItemID, run.Status, run.StartedAt, run.FinishedAt,
		run.PatchChecksum, run.ArtifactRef, run.ArtifactChecksum,
	); err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, check := range checks {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO benchmark_acceptance_checks (
  id, run_id, check_id, repo_id, command, cwd, status, expected_exit_code, exit_code, timed_out, stdout_truncated, stderr_truncated, error,
  result_artifact_ref, stdout_artifact_ref, stderr_artifact_ref, started_at, finished_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			check.ID, run.ID, check.CheckID, check.RepoID, check.Command, check.CWD,
			check.Status, check.ExpectedExitCode, check.ExitCode, boolInt(check.TimedOut), boolInt(check.StdoutTruncated), boolInt(check.StderrTruncated), check.Error,
			check.ResultArtifactRef, check.StdoutArtifactRef, check.StderrArtifactRef,
			check.StartedAt, check.FinishedAt,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) LatestBenchmarkAcceptanceRun(ctx context.Context, taskID, itemID string) (BenchmarkAcceptanceRun, error) {
	var run BenchmarkAcceptanceRun
	err := db.conn.QueryRowContext(ctx, `
SELECT id, task_id, item_id, status, started_at, finished_at, patch_checksum, artifact_ref, artifact_checksum
FROM benchmark_acceptance_runs
WHERE task_id = ? AND item_id = ?
ORDER BY finished_at DESC, id DESC
LIMIT 1`, taskID, itemID).Scan(
		&run.ID, &run.TaskID, &run.ItemID, &run.Status, &run.StartedAt, &run.FinishedAt,
		&run.PatchChecksum, &run.ArtifactRef, &run.ArtifactChecksum,
	)
	return run, err
}

func (db *DB) BenchmarkAcceptanceChecks(ctx context.Context, runID string) ([]BenchmarkAcceptanceCheck, error) {
	rows, err := db.conn.QueryContext(ctx, `
SELECT id, run_id, check_id, repo_id, command, cwd, status, expected_exit_code, exit_code, timed_out, stdout_truncated, stderr_truncated, error,
       result_artifact_ref, stdout_artifact_ref, stderr_artifact_ref, started_at, finished_at
FROM benchmark_acceptance_checks
WHERE run_id = ?
ORDER BY check_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var checks []BenchmarkAcceptanceCheck
	for rows.Next() {
		var check BenchmarkAcceptanceCheck
		var timedOut, stdoutTruncated, stderrTruncated int
		if err := rows.Scan(
			&check.ID, &check.RunID, &check.CheckID, &check.RepoID, &check.Command, &check.CWD,
			&check.Status, &check.ExpectedExitCode, &check.ExitCode, &timedOut, &stdoutTruncated, &stderrTruncated, &check.Error,
			&check.ResultArtifactRef, &check.StdoutArtifactRef, &check.StderrArtifactRef,
			&check.StartedAt, &check.FinishedAt,
		); err != nil {
			return nil, err
		}
		check.TimedOut = timedOut != 0
		check.StdoutTruncated = stdoutTruncated != 0
		check.StderrTruncated = stderrTruncated != 0
		checks = append(checks, check)
	}
	return checks, rows.Err()
}

func IsNoBenchmarkAcceptanceRun(err error) bool {
	return err == sql.ErrNoRows
}
