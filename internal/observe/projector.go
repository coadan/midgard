package observe

import (
	"context"
	"encoding/json"
	"fmt"

	"midgard/internal/eventlog"
)

type Evidence struct {
	EvidenceID  string          `json:"evidence_id"`
	Kind        string          `json:"kind"`
	ArtifactRef string          `json:"artifact_ref,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Sequence    int64           `json:"sequence,omitempty"`
}

type Projector struct{}

func (Projector) Name() string { return "completion-evidence" }

func (Projector) Reset(ctx context.Context, db eventlog.DBTX) error {
	_, err := db.ExecContext(ctx, `DELETE FROM completion_evidence_projection`)
	return err
}

func (Projector) Apply(ctx context.Context, db eventlog.DBTX, e eventlog.Event) error {
	if e.Kind != "evidence.recorded" {
		return nil
	}
	var evidence Evidence
	if err := json.Unmarshal(e.Payload, &evidence); err != nil || evidence.EvidenceID == "" || evidence.Kind == "" {
		return fmt.Errorf("invalid completion evidence")
	}
	if evidence.ArtifactRef == "" {
		evidence.ArtifactRef = e.ArtifactRef
	}
	_, err := db.ExecContext(ctx, `INSERT INTO completion_evidence_projection(session_id,evidence_id,kind,artifact_ref,payload_json,sequence) VALUES (?,?,?,?,?,?)`,
		e.SessionID, evidence.EvidenceID, evidence.Kind, nullIfEmpty(evidence.ArtifactRef), []byte(evidence.Payload), e.Sequence)
	return err
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
