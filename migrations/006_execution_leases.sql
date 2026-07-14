CREATE TABLE IF NOT EXISTS execution_leases (
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  owner_id TEXT NOT NULL,
  fence INTEGER NOT NULL,
  state TEXT NOT NULL,
  acquired_at INTEGER NOT NULL,
  renewed_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  PRIMARY KEY (resource_type, resource_id),
  CHECK (fence > 0),
  CHECK (state IN ('active', 'released'))
);

CREATE INDEX IF NOT EXISTS idx_execution_leases_active_expiry
  ON execution_leases(state, expires_at);
