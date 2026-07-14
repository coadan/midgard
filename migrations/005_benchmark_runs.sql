CREATE TABLE IF NOT EXISTS benchmark_runs (
  id TEXT PRIMARY KEY,
  manifest_id TEXT NOT NULL UNIQUE,
  manifest_checksum TEXT NOT NULL,
  execution_checksum TEXT NOT NULL,
  execution_json TEXT NOT NULL,
  status TEXT NOT NULL,
  started_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  finished_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS benchmark_run_repos (
  run_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  checkout_ref TEXT NOT NULL,
  start_commit TEXT NOT NULL,
  PRIMARY KEY (run_id, repo_id),
  FOREIGN KEY (run_id) REFERENCES benchmark_runs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS benchmark_run_items (
  run_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  task_id TEXT NOT NULL,
  phase TEXT NOT NULL,
  status TEXT NOT NULL,
  error_class TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  score TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  finished_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (run_id, item_id),
  UNIQUE (run_id, ordinal),
  FOREIGN KEY (run_id) REFERENCES benchmark_runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_benchmark_run_items_status
  ON benchmark_run_items(run_id, status, ordinal);
