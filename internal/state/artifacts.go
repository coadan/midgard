package state

import "context"

type Artifact struct {
	ID           string
	TaskID       string
	Type         string
	Path         string
	Checksum     string
	ProducerRole string
	State        string
}

func (db *DB) InsertArtifact(ctx context.Context, artifact Artifact) error {
	_, err := db.conn.ExecContext(ctx, `
INSERT INTO artifacts (id, task_id, type, path, checksum, producer_role, state)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		artifact.ID,
		nullableString(artifact.TaskID),
		artifact.Type,
		artifact.Path,
		nullableString(artifact.Checksum),
		nullableString(artifact.ProducerRole),
		artifact.State,
	)
	return err
}

func (db *DB) Artifact(ctx context.Context, id string) (Artifact, error) {
	var artifact Artifact
	err := db.conn.QueryRowContext(ctx, `
SELECT id, COALESCE(task_id, ''), type, path, COALESCE(checksum, ''), COALESCE(producer_role, ''), state
FROM artifacts
WHERE id = ?`, id).Scan(
		&artifact.ID,
		&artifact.TaskID,
		&artifact.Type,
		&artifact.Path,
		&artifact.Checksum,
		&artifact.ProducerRole,
		&artifact.State,
	)
	return artifact, err
}
