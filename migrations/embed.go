package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

// Initial is the complete schema for a fresh local kernel database.
//
//go:embed 001_initial.sql
var Initial string

//go:embed 002_workspace_default_branch.sql
var workspaceDefaultBranch string

//go:embed 003_project_identity.sql
var projectIdentity string

//go:embed 004_multi_repository_workspaces.sql
var multiRepositoryWorkspaces string

//go:embed 005_session_model_selection.sql
var sessionModelSelection string

type migration struct {
	version int
	sql     string
}

var incremental = []migration{{version: 2, sql: workspaceDefaultBranch}, {version: 3, sql: projectIdentity}, {version: 4, sql: multiRepositoryWorkspaces}, {version: 5, sql: sessionModelSelection}}

// Apply upgrades databases created by earlier Midgard builds. Initial remains
// the version-one schema so existing databases and fresh databases follow the
// same tested migration path.
func Apply(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
version INTEGER PRIMARY KEY,
applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	for _, item := range incremental {
		var present int
		err := db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE version=?`, item.version).Scan(&present)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("inspect migration %d: %w", item.version, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", item.version, err)
		}
		if _, err := tx.ExecContext(ctx, item.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", item.version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (?)`, item.version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", item.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", item.version, err)
		}
	}
	return nil
}
