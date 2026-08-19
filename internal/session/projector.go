package session

import (
	"context"
	"encoding/json"
	"fmt"

	"midgard/internal/eventlog"
)

type Projector struct{}

func (Projector) Name() string { return "session" }

func (Projector) Reset(ctx context.Context, db eventlog.DBTX) error {
	for _, table := range []string{"message_projection", "control_projection", "turn_projection", "session_projection"} {
		if _, err := db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	return nil
}

func (Projector) Apply(ctx context.Context, db eventlog.DBTX, e eventlog.Event) error {
	if e.Kind == "session.created" {
		var payload struct {
			Objective string `json:"objective"`
			ProjectID string `json:"project_id"`
		}
		if len(e.Payload) > 0 {
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				return fmt.Errorf("invalid session.created payload")
			}
		}
		if e.SchemaVersion >= 2 && payload.ProjectID == "" {
			return fmt.Errorf("session.created v2 requires project_id")
		}
		_, err := db.ExecContext(ctx, `INSERT INTO session_projection(session_id,project_id,objective,status,last_sequence,created_at,updated_at)
VALUES (?, ?, ?, 'active', ?, ?, ?)`, e.SessionID, payload.ProjectID, payload.Objective, e.Sequence, e.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), e.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE session_projection SET last_sequence=?, updated_at=? WHERE session_id=?`, e.Sequence, e.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), e.SessionID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("session %s has no session.created event", e.SessionID)
	}
	switch e.Kind {
	case "session.model_selected":
		var selection ModelSelection
		if err = json.Unmarshal(e.Payload, &selection); err != nil || selection.Provider == "" || selection.Model == "" || selection.Effort == "" {
			return fmt.Errorf("model selection requires provider, model, and effort")
		}
		var active int
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM turn_projection WHERE session_id=? AND status='active'`, e.SessionID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return fmt.Errorf("session %s has an active turn", e.SessionID)
		}
		_, err = db.ExecContext(ctx, `UPDATE session_projection SET provider=?,profile=?,model=?,effort=? WHERE session_id=?`, selection.Provider, selection.Profile, selection.Model, selection.Effort, e.SessionID)
	case "session.completed":
		_, err = db.ExecContext(ctx, `UPDATE session_projection SET status='completed' WHERE session_id=?`, e.SessionID)
	case "session.cancelled":
		_, err = db.ExecContext(ctx, `UPDATE session_projection SET status='cancelled' WHERE session_id=?`, e.SessionID)
	case "session.failed":
		_, err = db.ExecContext(ctx, `UPDATE session_projection SET status='failed' WHERE session_id=?`, e.SessionID)
	case "turn.started":
		if e.TurnID == "" {
			return fmt.Errorf("turn.started requires turn_id")
		}
		var active int
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM turn_projection WHERE session_id=? AND status='active'`, e.SessionID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return fmt.Errorf("session %s already has an active turn", e.SessionID)
		}
		_, err = db.ExecContext(ctx, `INSERT INTO turn_projection(turn_id,session_id,status,started_sequence) VALUES (?,?,'active',?)`, e.TurnID, e.SessionID, e.Sequence)
	case "turn.completed", "turn.interrupted", "turn.failed":
		status := map[string]string{"turn.completed": "completed", "turn.interrupted": "interrupted", "turn.failed": "failed"}[e.Kind]
		result, err = db.ExecContext(ctx, `UPDATE turn_projection SET status=?, ended_sequence=? WHERE turn_id=? AND session_id=? AND status='active'`, status, e.Sequence, e.TurnID, e.SessionID)
		if err == nil {
			if affected, _ := result.RowsAffected(); affected != 1 {
				return fmt.Errorf("turn %s is not active", e.TurnID)
			}
		}
	case "message.user", "message.assistant":
		var payload struct {
			MessageID string `json:"message_id"`
			Content   string `json:"content"`
		}
		if err = json.Unmarshal(e.Payload, &payload); err != nil || payload.MessageID == "" || payload.Content == "" || e.TurnID == "" {
			return fmt.Errorf("message event requires message_id, turn_id, and content")
		}
		role := "user"
		if e.Kind == "message.assistant" {
			role = "assistant"
		}
		var active int
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM turn_projection WHERE turn_id=? AND session_id=? AND status='active'`, e.TurnID, e.SessionID).Scan(&active); err != nil {
			return err
		}
		if active != 1 {
			return fmt.Errorf("turn %s is not active", e.TurnID)
		}
		_, err = db.ExecContext(ctx, `INSERT INTO message_projection(message_id,session_id,turn_id,role,content,artifact_ref,sequence) VALUES (?,?,?,?,?,NULLIF(?,''),?)`,
			payload.MessageID, e.SessionID, e.TurnID, role, payload.Content, e.ArtifactRef, e.Sequence)
	case "control.steer", "control.interrupt", "control.approve", "control.reject":
		var payload struct {
			ControlID string `json:"control_id"`
			Content   string `json:"content"`
		}
		if err = json.Unmarshal(e.Payload, &payload); err != nil || payload.ControlID == "" {
			return fmt.Errorf("control event requires control_id")
		}
		if e.Kind == "control.steer" && payload.Content == "" {
			return fmt.Errorf("steering control requires content")
		}
		_, err = db.ExecContext(ctx, `INSERT INTO control_projection(control_id,session_id,kind,sequence) VALUES (?,?,?,?)`, payload.ControlID, e.SessionID, e.Kind, e.Sequence)
	case "control.acknowledged":
		var payload struct {
			ControlID string `json:"control_id"`
		}
		if err = json.Unmarshal(e.Payload, &payload); err != nil || payload.ControlID == "" {
			return fmt.Errorf("control acknowledgement requires control_id")
		}
		result, err = db.ExecContext(ctx, `UPDATE control_projection SET acknowledged=1 WHERE control_id=? AND session_id=?`, payload.ControlID, e.SessionID)
		if err == nil {
			if affected, _ := result.RowsAffected(); affected != 1 {
				return fmt.Errorf("unknown control %s", payload.ControlID)
			}
		}
	}
	return err
}
