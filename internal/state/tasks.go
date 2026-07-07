package state

import (
	"context"
	"database/sql"
)

type Repo struct {
	ID          string
	WorkbenchID string
	Path        string
	MainRef     string
}

type Task struct {
	ID          string
	WorkbenchID string
	State       string
	Objective   string
}

type Worktree struct {
	ID          string
	TaskID      string
	RepoID      string
	Path        string
	StartRef    string
	StartCommit string
}

func nullableString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func (db *DB) UpsertRepo(ctx context.Context, repo Repo) error {
	_, err := db.conn.ExecContext(ctx, `
INSERT INTO repos (id, workbench_id, path, main_ref)
VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  workbench_id = excluded.workbench_id,
  path = excluded.path,
  main_ref = excluded.main_ref`,
		repo.ID, repo.WorkbenchID, repo.Path, repo.MainRef)
	return err
}

func (db *DB) Repo(ctx context.Context, id string) (Repo, error) {
	var repo Repo
	err := db.conn.QueryRowContext(ctx, `
SELECT id, workbench_id, path, main_ref
FROM repos
WHERE id = ?`, id).Scan(&repo.ID, &repo.WorkbenchID, &repo.Path, &repo.MainRef)
	return repo, err
}

func (db *DB) InsertTask(ctx context.Context, task Task) error {
	_, err := db.conn.ExecContext(ctx, `
INSERT INTO tasks (id, workbench_id, state, objective)
VALUES (?, ?, ?, ?)`,
		task.ID, task.WorkbenchID, task.State, task.Objective)
	return err
}

func (db *DB) Task(ctx context.Context, id string) (Task, error) {
	var task Task
	err := db.conn.QueryRowContext(ctx, `
SELECT id, workbench_id, state, objective
FROM tasks
WHERE id = ?`, id).Scan(&task.ID, &task.WorkbenchID, &task.State, &task.Objective)
	return task, err
}

func (db *DB) UpdateTaskState(ctx context.Context, id, taskState string) error {
	_, err := db.conn.ExecContext(ctx, `
UPDATE tasks
SET state = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?`, taskState, id)
	return err
}

func (db *DB) DeleteTaskCascade(ctx context.Context, taskID string) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	statements := []string{
		"DELETE FROM repair_attempts WHERE stream_id IN (SELECT id FROM streams WHERE task_id = ?)",
		"DELETE FROM parser_errors WHERE stream_id IN (SELECT id FROM streams WHERE task_id = ?)",
		"DELETE FROM streams WHERE task_id = ?",
		"DELETE FROM leases WHERE task_id = ?",
		"DELETE FROM events WHERE task_id = ?",
		"DELETE FROM artifacts WHERE task_id = ?",
		"DELETE FROM usage_records WHERE task_id = ?",
		"DELETE FROM cost_rollups WHERE task_id = ?",
		"DELETE FROM worktrees WHERE task_id = ?",
		"DELETE FROM task_repos WHERE task_id = ?",
		"DELETE FROM tasks WHERE id = ?",
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, taskID); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) LinkTaskRepo(ctx context.Context, taskID, repoID string) error {
	_, err := db.conn.ExecContext(ctx, `
INSERT INTO task_repos (task_id, repo_id)
VALUES (?, ?)
ON CONFLICT(task_id, repo_id) DO NOTHING`,
		taskID, repoID)
	return err
}

func (db *DB) InsertWorktree(ctx context.Context, wt Worktree) error {
	_, err := db.conn.ExecContext(ctx, `
INSERT INTO worktrees (id, task_id, repo_id, path, start_ref, start_commit)
VALUES (?, ?, ?, ?, ?, ?)`,
		wt.ID, wt.TaskID, wt.RepoID, wt.Path, wt.StartRef, wt.StartCommit)
	return err
}

func (db *DB) WorktreesForTask(ctx context.Context, taskID string) ([]Worktree, error) {
	rows, err := db.conn.QueryContext(ctx, `
SELECT id, task_id, repo_id, path, start_ref, start_commit
FROM worktrees
WHERE task_id = ?
ORDER BY repo_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var worktrees []Worktree
	for rows.Next() {
		var wt Worktree
		if err := rows.Scan(&wt.ID, &wt.TaskID, &wt.RepoID, &wt.Path, &wt.StartRef, &wt.StartCommit); err != nil {
			return nil, err
		}
		worktrees = append(worktrees, wt)
	}
	return worktrees, rows.Err()
}

func (db *DB) InsertLease(ctx context.Context, id, taskID, role, leaseState string) error {
	_, err := db.conn.ExecContext(ctx, `
INSERT INTO leases (id, task_id, role, state)
VALUES (?, ?, ?, ?)`,
		id, taskID, role, leaseState)
	return err
}
