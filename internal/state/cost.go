package state

import "context"

type CostRollup struct {
	ID        string
	TaskID    string
	AmountUSD string
	Caveats   string
}

func (db *DB) InsertCostRollup(ctx context.Context, rollup CostRollup) error {
	_, err := db.conn.ExecContext(ctx, `
INSERT INTO cost_rollups (id, task_id, amount_usd, caveats)
VALUES (?, ?, ?, ?)`,
		rollup.ID,
		rollup.TaskID,
		rollup.AmountUSD,
		rollup.Caveats,
	)
	return err
}

func (db *DB) CostRollup(ctx context.Context, id string) (CostRollup, error) {
	var rollup CostRollup
	err := db.conn.QueryRowContext(ctx, `
SELECT id, task_id, amount_usd, caveats
FROM cost_rollups
WHERE id = ?`, id).Scan(
		&rollup.ID,
		&rollup.TaskID,
		&rollup.AmountUSD,
		&rollup.Caveats,
	)
	return rollup, err
}

func (db *DB) CostRollupsForTask(ctx context.Context, taskID string) ([]CostRollup, error) {
	rows, err := db.conn.QueryContext(ctx, `
SELECT id, task_id, amount_usd, caveats
FROM cost_rollups
WHERE task_id = ?
ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rollups []CostRollup
	for rows.Next() {
		var rollup CostRollup
		if err := rows.Scan(&rollup.ID, &rollup.TaskID, &rollup.AmountUSD, &rollup.Caveats); err != nil {
			return nil, err
		}
		rollups = append(rollups, rollup)
	}
	return rollups, rows.Err()
}
