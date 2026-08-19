CREATE TABLE workspace_projection_v4 (
  session_id TEXT NOT NULL,
  project_id TEXT NOT NULL DEFAULT '',
  repository_name TEXT NOT NULL,
  repository_root TEXT NOT NULL,
  worktree_root TEXT NOT NULL,
  start_commit TEXT NOT NULL,
  default_branch TEXT NOT NULL DEFAULT '',
  landing_strategy TEXT NOT NULL DEFAULT 'direct',
  cleanup_when_landed INTEGER NOT NULL DEFAULT 1,
  cleanup_state TEXT NOT NULL DEFAULT 'retained',
  last_sequence INTEGER NOT NULL,
  PRIMARY KEY (session_id, repository_name),
  FOREIGN KEY (session_id) REFERENCES session_heads(session_id)
);

INSERT INTO workspace_projection_v4(
  session_id,project_id,repository_name,repository_root,worktree_root,start_commit,
  default_branch,landing_strategy,cleanup_when_landed,cleanup_state,last_sequence
)
SELECT session_id,project_id,
  CASE WHEN repository_name='' THEN 'repository' ELSE repository_name END,
  repository_root,worktree_root,start_commit,default_branch,landing_strategy,
  cleanup_when_landed,cleanup_state,last_sequence
FROM workspace_projection;

DROP TABLE workspace_projection;
ALTER TABLE workspace_projection_v4 RENAME TO workspace_projection;

CREATE INDEX workspace_projection_repository
  ON workspace_projection(repository_root, session_id);
