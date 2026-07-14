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
	_, err := db.fencedExecContext(ctx, `
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

func (db *DB) UpdateArtifact(ctx context.Context, artifact Artifact) error {
	result, err := db.fencedExecContext(ctx, `
UPDATE artifacts
SET task_id = ?, type = ?, path = ?, checksum = ?, producer_role = ?, state = ?
WHERE id = ?`,
		nullableString(artifact.TaskID),
		artifact.Type,
		artifact.Path,
		nullableString(artifact.Checksum),
		nullableString(artifact.ProducerRole),
		artifact.State,
		artifact.ID,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return db.InsertArtifact(ctx, artifact)
	}
	return nil
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
