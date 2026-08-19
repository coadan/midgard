ALTER TABLE workspace_projection
  ADD COLUMN default_branch TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_projection
  ADD COLUMN landing_strategy TEXT NOT NULL DEFAULT 'direct';

ALTER TABLE workspace_projection
  ADD COLUMN cleanup_when_landed INTEGER NOT NULL DEFAULT 1;

ALTER TABLE workspace_projection
  ADD COLUMN cleanup_state TEXT NOT NULL DEFAULT 'retained';
