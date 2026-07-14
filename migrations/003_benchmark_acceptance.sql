CREATE TABLE IF NOT EXISTS benchmark_acceptance_runs (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  status TEXT NOT NULL,
  started_at TEXT NOT NULL,
  finished_at TEXT NOT NULL,
  patch_checksum TEXT NOT NULL,
  artifact_ref TEXT NOT NULL,
  artifact_checksum TEXT NOT NULL,
  FOREIGN KEY (task_id) REFERENCES tasks(id)
);

CREATE TABLE IF NOT EXISTS benchmark_acceptance_checks (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  check_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  command TEXT NOT NULL,
  cwd TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  exit_code INTEGER NOT NULL,
  timed_out INTEGER NOT NULL DEFAULT 0,
  stdout_truncated INTEGER NOT NULL DEFAULT 0,
  stderr_truncated INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  result_artifact_ref TEXT NOT NULL DEFAULT '',
  stdout_artifact_ref TEXT NOT NULL DEFAULT '',
  stderr_artifact_ref TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  finished_at TEXT NOT NULL,
  FOREIGN KEY (run_id) REFERENCES benchmark_acceptance_runs(id)
);

CREATE INDEX IF NOT EXISTS idx_benchmark_acceptance_runs_task_item
  ON benchmark_acceptance_runs(task_id, item_id, finished_at);
CREATE INDEX IF NOT EXISTS idx_benchmark_acceptance_checks_run
  ON benchmark_acceptance_checks(run_id, check_id);
