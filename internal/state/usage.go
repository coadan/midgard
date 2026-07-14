package state

import "context"

type UsageRecord struct {
	ID           string
	TaskID       string
	ProviderID   string
	ModelID      string
	Role         string
	InputTokens  int64
	OutputTokens int64
	RawPayload   string
}

func (db *DB) InsertUsageRecord(ctx context.Context, usage UsageRecord) error {
	if usage.RawPayload == "" {
		usage.RawPayload = "{}"
	}
	_, err := db.fencedExecContext(ctx, `
INSERT INTO usage_records (id, task_id, provider_id, model_id, role, input_tokens, output_tokens, raw_payload)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		usage.ID,
		nullableString(usage.TaskID),
		usage.ProviderID,
		usage.ModelID,
		usage.Role,
		usage.InputTokens,
		usage.OutputTokens,
		usage.RawPayload,
	)
	return err
}

func (db *DB) UsageRecord(ctx context.Context, id string) (UsageRecord, error) {
	var usage UsageRecord
	err := db.conn.QueryRowContext(ctx, `
SELECT id, COALESCE(task_id, ''), provider_id, model_id, role, input_tokens, output_tokens, raw_payload
FROM usage_records
WHERE id = ?`, id).Scan(
		&usage.ID,
		&usage.TaskID,
		&usage.ProviderID,
		&usage.ModelID,
		&usage.Role,
		&usage.InputTokens,
		&usage.OutputTokens,
		&usage.RawPayload,
	)
	return usage, err
}

func (db *DB) UsageRecordsForTask(ctx context.Context, taskID string) ([]UsageRecord, error) {
	rows, err := db.conn.QueryContext(ctx, `
SELECT id, COALESCE(task_id, ''), provider_id, model_id, role, input_tokens, output_tokens, raw_payload
FROM usage_records
WHERE task_id = ?
ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []UsageRecord
	for rows.Next() {
		var usage UsageRecord
		if err := rows.Scan(
			&usage.ID,
			&usage.TaskID,
			&usage.ProviderID,
			&usage.ModelID,
			&usage.Role,
			&usage.InputTokens,
			&usage.OutputTokens,
			&usage.RawPayload,
		); err != nil {
			return nil, err
		}
		records = append(records, usage)
	}
	return records, rows.Err()
}
