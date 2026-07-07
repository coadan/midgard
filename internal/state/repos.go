package state

import "context"

type Workbench struct {
	ID         string
	Root       string
	ConfigPath string
}

func (db *DB) UpsertWorkbench(ctx context.Context, wb Workbench) error {
	_, err := db.conn.ExecContext(ctx, `
INSERT INTO workbenches (id, root, config_path)
VALUES (?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  root = excluded.root,
  config_path = excluded.config_path`,
		wb.ID, wb.Root, wb.ConfigPath)
	return err
}

func (db *DB) Workbench(ctx context.Context, id string) (Workbench, error) {
	var wb Workbench
	err := db.conn.QueryRowContext(ctx, `
SELECT id, root, config_path
FROM workbenches
WHERE id = ?`, id).Scan(&wb.ID, &wb.Root, &wb.ConfigPath)
	return wb, err
}
