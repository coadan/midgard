CREATE TABLE IF NOT EXISTS forge_accounts (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  base_url TEXT NOT NULL,
  auth_profile TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS forge_repos (
  repo_id TEXT NOT NULL,
  forge_id TEXT NOT NULL,
  owner TEXT NOT NULL,
  name TEXT NOT NULL,
  default_branch TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (repo_id, forge_id),
  FOREIGN KEY (repo_id) REFERENCES repos(id),
  FOREIGN KEY (forge_id) REFERENCES forge_accounts(id)
);

CREATE TABLE IF NOT EXISTS task_pr_groups (
  task_id TEXT NOT NULL,
  group_id TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  purpose TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (task_id, group_id),
  FOREIGN KEY (task_id) REFERENCES tasks(id)
);

CREATE TABLE IF NOT EXISTS task_pr_links (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  group_id TEXT NOT NULL DEFAULT '',
  forge_id TEXT NOT NULL,
  pr_number INTEGER NOT NULL,
  url TEXT NOT NULL,
  base_branch TEXT NOT NULL DEFAULT '',
  head_branch TEXT NOT NULL DEFAULT '',
  head_sha TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'manual',
  linked_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (task_id, repo_id, forge_id, pr_number),
  FOREIGN KEY (task_id) REFERENCES tasks(id),
  FOREIGN KEY (repo_id) REFERENCES repos(id),
  FOREIGN KEY (forge_id) REFERENCES forge_accounts(id)
);

CREATE TABLE IF NOT EXISTS forge_pr_snapshots (
  id TEXT PRIMARY KEY,
  link_id TEXT NOT NULL,
  fetched_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  state TEXT NOT NULL DEFAULT 'unknown',
  draft INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL DEFAULT '',
  author TEXT NOT NULL DEFAULT '',
  labels TEXT NOT NULL DEFAULT '',
  mergeable_state TEXT NOT NULL DEFAULT 'unknown',
  review_decision TEXT NOT NULL DEFAULT 'unknown',
  check_conclusion TEXT NOT NULL DEFAULT 'unknown',
  unresolved_thread_count INTEGER NOT NULL DEFAULT 0,
  review_threads_complete INTEGER NOT NULL DEFAULT 0,
  artifact_ref TEXT NOT NULL DEFAULT '',
  checks_artifact_ref TEXT NOT NULL DEFAULT '',
  threads_artifact_ref TEXT NOT NULL DEFAULT '',
  reviews_artifact_ref TEXT NOT NULL DEFAULT '',
  merged_at TEXT NOT NULL DEFAULT '',
  closed_at TEXT NOT NULL DEFAULT '',
  FOREIGN KEY (link_id) REFERENCES task_pr_links(id)
);

CREATE TABLE IF NOT EXISTS forge_check_runs (
  id TEXT PRIMARY KEY,
  link_id TEXT NOT NULL,
  name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'unknown',
  conclusion TEXT NOT NULL DEFAULT 'unknown',
  url TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL DEFAULT '',
  completed_at TEXT NOT NULL DEFAULT '',
  FOREIGN KEY (link_id) REFERENCES task_pr_links(id)
);

CREATE TABLE IF NOT EXISTS forge_review_threads (
  id TEXT PRIMARY KEY,
  link_id TEXT NOT NULL,
  thread_id TEXT NOT NULL,
  path TEXT NOT NULL DEFAULT '',
  line INTEGER NOT NULL DEFAULT 0,
  resolved INTEGER NOT NULL DEFAULT 0,
  last_author TEXT NOT NULL DEFAULT '',
  last_updated_at TEXT NOT NULL DEFAULT '',
  artifact_ref TEXT NOT NULL DEFAULT '',
  FOREIGN KEY (link_id) REFERENCES task_pr_links(id)
);

CREATE INDEX IF NOT EXISTS idx_task_pr_links_task ON task_pr_links(task_id);
CREATE INDEX IF NOT EXISTS idx_forge_pr_snapshots_link ON forge_pr_snapshots(link_id, fetched_at);
CREATE INDEX IF NOT EXISTS idx_forge_check_runs_link ON forge_check_runs(link_id);
CREATE INDEX IF NOT EXISTS idx_forge_review_threads_link ON forge_review_threads(link_id);
