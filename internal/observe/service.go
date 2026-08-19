package observe

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"midgard/internal/eventlog"
)

type Service struct{ Log *eventlog.Store }

func (s Service) RecordEvidence(ctx context.Context, sessionID string, evidence Evidence) (Evidence, error) {
	if evidence.EvidenceID == "" {
		evidence.EvidenceID = randomID("evidence")
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return Evidence{}, err
	}
	event, err := s.Log.AppendCurrent(ctx, eventlog.Draft{EventID: randomID("evt"), SessionID: sessionID,
		Actor: eventlog.ActorServer, Kind: "evidence.recorded", SchemaVersion: 1,
		Visibility: eventlog.VisibilityInternal, Payload: raw, ArtifactRef: evidence.ArtifactRef})
	if err != nil {
		return Evidence{}, err
	}
	evidence.Sequence = event.Sequence
	return evidence, nil
}

func (s Service) Evidence(ctx context.Context, sessionID string) ([]Evidence, error) {
	rows, err := s.Log.DB().QueryContext(ctx, `SELECT evidence_id,kind,COALESCE(artifact_ref,''),COALESCE(payload_json,''),sequence FROM completion_evidence_projection WHERE session_id=? ORDER BY sequence`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Evidence
	for rows.Next() {
		var evidence Evidence
		var payload []byte
		if err := rows.Scan(&evidence.EvidenceID, &evidence.Kind, &evidence.ArtifactRef, &payload, &evidence.Sequence); err != nil {
			return nil, err
		}
		evidence.Payload = payload
		result = append(result, evidence)
	}
	return result, rows.Err()
}

func (s Service) HasSession(ctx context.Context, sessionID string) (bool, error) {
	var exists int
	err := s.Log.DB().QueryRowContext(ctx, `SELECT 1 FROM session_projection WHERE session_id=?`, sessionID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func randomID(prefix string) string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(data[:])
}
