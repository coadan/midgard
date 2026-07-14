package state

import "context"

type Event struct {
	ID      int64
	TaskID  string
	Type    string
	Payload string
}

func (db *DB) InsertEvent(ctx context.Context, event Event) (int64, error) {
	result, err := db.fencedExecContext(ctx, `
INSERT INTO events (task_id, type, payload)
VALUES (?, ?, ?)`,
		nullableString(event.TaskID),
		event.Type,
		event.Payload,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (db *DB) Event(ctx context.Context, id int64) (Event, error) {
	var event Event
	err := db.conn.QueryRowContext(ctx, `
SELECT id, COALESCE(task_id, ''), type, payload
FROM events
WHERE id = ?`, id).Scan(&event.ID, &event.TaskID, &event.Type, &event.Payload)
	return event, err
}

func (db *DB) EventsForTask(ctx context.Context, taskID string) ([]Event, error) {
	rows, err := db.conn.QueryContext(ctx, `
SELECT id, COALESCE(task_id, ''), type, payload
FROM events
WHERE task_id = ?
ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.TaskID, &event.Type, &event.Payload); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
