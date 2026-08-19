ALTER TABLE session_projection
  ADD COLUMN project_id TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_projection
  ADD COLUMN project_id TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_projection
  ADD COLUMN repository_name TEXT NOT NULL DEFAULT '';
