PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS session_heads (
  session_id TEXT PRIMARY KEY,
  sequence INTEGER NOT NULL DEFAULT 0 CHECK (sequence >= 0)
);

CREATE TABLE IF NOT EXISTS events (
  event_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  sequence INTEGER NOT NULL CHECK (sequence > 0),
  turn_id TEXT,
  actor TEXT NOT NULL CHECK (actor IN ('user', 'model', 'server', 'tool', 'policy')),
  kind TEXT NOT NULL,
  schema_version INTEGER NOT NULL CHECK (schema_version > 0),
  causation_id TEXT,
  correlation_id TEXT,
  visibility TEXT NOT NULL CHECK (visibility IN ('public', 'internal', 'secret')),
  payload_json BLOB,
  artifact_ref TEXT,
  created_at TEXT NOT NULL,
  UNIQUE (session_id, sequence),
  FOREIGN KEY (session_id) REFERENCES session_heads(session_id)
);

CREATE INDEX IF NOT EXISTS events_session_kind
  ON events(session_id, kind, sequence);

CREATE TABLE IF NOT EXISTS session_projection (
  session_id TEXT PRIMARY KEY,
  objective TEXT NOT NULL,
  status TEXT NOT NULL,
  last_sequence INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS message_projection (
  message_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  turn_id TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
  content TEXT NOT NULL,
  artifact_ref TEXT,
  sequence INTEGER NOT NULL,
  UNIQUE (session_id, sequence),
  FOREIGN KEY (session_id) REFERENCES session_heads(session_id),
  FOREIGN KEY (turn_id) REFERENCES turn_projection(turn_id)
);

CREATE TABLE IF NOT EXISTS turn_projection (
  turn_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  status TEXT NOT NULL,
  started_sequence INTEGER NOT NULL,
  ended_sequence INTEGER,
  FOREIGN KEY (session_id) REFERENCES session_heads(session_id)
);

CREATE TABLE IF NOT EXISTS control_projection (
  control_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  acknowledged INTEGER NOT NULL DEFAULT 0,
  sequence INTEGER NOT NULL,
  FOREIGN KEY (session_id) REFERENCES session_heads(session_id)
);

CREATE TABLE IF NOT EXISTS action_projection (
  action_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  state TEXT NOT NULL,
  version INTEGER NOT NULL,
  capability TEXT NOT NULL,
  arguments_json BLOB NOT NULL,
  approval_required INTEGER NOT NULL DEFAULT 0,
  approved INTEGER NOT NULL DEFAULT 0,
  commit_id TEXT UNIQUE,
  idempotency_key TEXT,
  dispatch_owner TEXT,
  dispatch_fence INTEGER,
  compensation_action_id TEXT,
  result_json BLOB,
  last_sequence INTEGER NOT NULL,
  UNIQUE (session_id, idempotency_key),
  FOREIGN KEY (session_id) REFERENCES session_heads(session_id)
);

CREATE TABLE IF NOT EXISTS approval_projection (
  action_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  state TEXT NOT NULL,
  decided_by TEXT,
  decision_sequence INTEGER,
  FOREIGN KEY (action_id) REFERENCES action_projection(action_id)
);

CREATE TABLE IF NOT EXISTS workspace_projection (
  session_id TEXT PRIMARY KEY,
  repository_root TEXT NOT NULL,
  worktree_root TEXT NOT NULL,
  start_commit TEXT NOT NULL,
  last_sequence INTEGER NOT NULL,
  FOREIGN KEY (session_id) REFERENCES session_heads(session_id)
);

CREATE TABLE IF NOT EXISTS completion_evidence_projection (
  session_id TEXT NOT NULL,
  evidence_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  artifact_ref TEXT,
  payload_json BLOB,
  sequence INTEGER NOT NULL,
  PRIMARY KEY (session_id, evidence_id),
  FOREIGN KEY (session_id) REFERENCES session_heads(session_id)
);
