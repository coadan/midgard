package action

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"midgard/internal/eventlog"
)

var ErrSteeringPending = errors.New("steering control is pending")

type State string

const (
	StateIntent                State = "intent"
	StateValidated             State = "validated"
	StateApprovalPending       State = "approval_pending"
	StateRejected              State = "rejected"
	StateRetracted             State = "retracted"
	StateCommitted             State = "committed"
	StateDispatched            State = "dispatched"
	StateSucceeded             State = "succeeded"
	StateFailed                State = "failed"
	StateCompensationCommitted State = "compensation_committed"
)

type Projection struct {
	ActionID             string
	SessionID            string
	State                State
	Version              int
	Capability           string
	Arguments            json.RawMessage
	ApprovalRequired     bool
	Approved             bool
	CommitID             string
	IdempotencyKey       string
	DispatchOwner        string
	DispatchFence        int64
	CompensationActionID string
	Result               json.RawMessage
	LastSequence         int64
}

type Projector struct{}

func (Projector) Name() string { return "action" }

func (Projector) Reset(ctx context.Context, db eventlog.DBTX) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM approval_projection`); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `DELETE FROM action_projection`)
	return err
}

type intentPayload struct {
	ActionID         string          `json:"action_id"`
	Version          int             `json:"version"`
	Capability       string          `json:"capability"`
	Arguments        json.RawMessage `json:"arguments"`
	ApprovalRequired bool            `json:"approval_required"`
}

type actionVersionPayload struct {
	ActionID string `json:"action_id"`
	Version  int    `json:"version"`
}

type decisionPayload struct {
	ActionID string `json:"action_id"`
	Actor    string `json:"actor"`
}

type commitPayload struct {
	ActionID       string `json:"action_id"`
	Version        int    `json:"version"`
	CommitID       string `json:"commit_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type dispatchPayload struct {
	ActionID string `json:"action_id"`
	Owner    string `json:"owner"`
	Fence    int64  `json:"fence"`
}

type resultPayload struct {
	ActionID string          `json:"action_id"`
	Owner    string          `json:"owner"`
	Fence    int64           `json:"fence"`
	Result   json.RawMessage `json:"result"`
}

type compensationPayload struct {
	ActionID             string `json:"action_id"`
	CompensationActionID string `json:"compensation_action_id"`
}

func (Projector) Apply(ctx context.Context, db eventlog.DBTX, e eventlog.Event) error {
	switch e.Kind {
	case "action.intent":
		var p intentPayload
		if err := decode(e.Payload, &p); err != nil || p.ActionID == "" || p.Version != 1 || p.Capability == "" || !validObject(p.Arguments) {
			return fmt.Errorf("invalid action.intent payload")
		}
		_, err := db.ExecContext(ctx, `INSERT INTO action_projection(action_id,session_id,state,version,capability,arguments_json,approval_required,last_sequence)
VALUES (?,?,?,?,?,?,?,?)`, p.ActionID, e.SessionID, StateIntent, p.Version, p.Capability, []byte(p.Arguments), p.ApprovalRequired, e.Sequence)
		return err
	case "action.intent_revised":
		var p intentPayload
		if err := decode(e.Payload, &p); err != nil || p.ActionID == "" || !validObject(p.Arguments) {
			return fmt.Errorf("invalid action.intent_revised payload")
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM approval_projection WHERE action_id=?`, p.ActionID); err != nil {
			return err
		}
		return updateExactlyOne(ctx, db, `UPDATE action_projection SET state=?,version=?,capability=?,arguments_json=?,approval_required=?,approved=0,last_sequence=?
WHERE action_id=? AND session_id=? AND state IN (?,?,?) AND version=?`, StateIntent, p.Version, p.Capability, []byte(p.Arguments), p.ApprovalRequired, e.Sequence,
			p.ActionID, e.SessionID, StateIntent, StateValidated, StateApprovalPending, p.Version-1)
	case "action.validated":
		var p actionVersionPayload
		if err := decode(e.Payload, &p); err != nil {
			return err
		}
		return updateExactlyOne(ctx, db, `UPDATE action_projection SET state=?,last_sequence=? WHERE action_id=? AND session_id=? AND state=? AND version=?`, StateValidated, e.Sequence, p.ActionID, e.SessionID, StateIntent, p.Version)
	case "action.approval_requested":
		var p actionVersionPayload
		if err := decode(e.Payload, &p); err != nil {
			return err
		}
		if err := updateExactlyOne(ctx, db, `UPDATE action_projection SET state=?,last_sequence=? WHERE action_id=? AND session_id=? AND state=? AND version=? AND approval_required=1`, StateApprovalPending, e.Sequence, p.ActionID, e.SessionID, StateValidated, p.Version); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `INSERT INTO approval_projection(action_id,session_id,state) VALUES (?,?,'pending')`, p.ActionID, e.SessionID)
		return err
	case "action.approved", "action.rejected":
		var p decisionPayload
		if err := decode(e.Payload, &p); err != nil || p.Actor == "" {
			return fmt.Errorf("invalid approval decision")
		}
		state := StateValidated
		approved := 1
		decision := "approved"
		if e.Kind == "action.rejected" {
			state, approved, decision = StateRejected, 0, "rejected"
		}
		if err := updateExactlyOne(ctx, db, `UPDATE action_projection SET state=?,approved=?,last_sequence=? WHERE action_id=? AND session_id=? AND state=?`, state, approved, e.Sequence, p.ActionID, e.SessionID, StateApprovalPending); err != nil {
			return err
		}
		return updateExactlyOne(ctx, db, `UPDATE approval_projection SET state=?,decided_by=?,decision_sequence=? WHERE action_id=? AND state='pending'`, decision, p.Actor, e.Sequence, p.ActionID)
	case "action.retracted":
		var p actionVersionPayload
		if err := decode(e.Payload, &p); err != nil {
			return err
		}
		if err := updateExactlyOne(ctx, db, `UPDATE action_projection SET state=?,last_sequence=? WHERE action_id=? AND session_id=? AND state IN (?,?,?) AND version=?`, StateRetracted, e.Sequence, p.ActionID, e.SessionID, StateIntent, StateValidated, StateApprovalPending, p.Version); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `UPDATE approval_projection SET state='retracted',decision_sequence=? WHERE action_id=? AND state='pending'`, e.Sequence, p.ActionID)
		return err
	case "action.committed":
		var p commitPayload
		if err := decode(e.Payload, &p); err != nil || p.CommitID == "" || p.IdempotencyKey == "" {
			return fmt.Errorf("invalid action.committed payload")
		}
		var pending int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM control_projection WHERE session_id=? AND kind='control.steer' AND acknowledged=0`, e.SessionID).Scan(&pending); err != nil {
			return err
		}
		if pending != 0 {
			return ErrSteeringPending
		}
		return updateExactlyOne(ctx, db, `UPDATE action_projection SET state=?,commit_id=?,idempotency_key=?,last_sequence=?
WHERE action_id=? AND session_id=? AND state=? AND version=? AND (approval_required=0 OR approved=1)`, StateCommitted, p.CommitID, p.IdempotencyKey, e.Sequence, p.ActionID, e.SessionID, StateValidated, p.Version)
	case "action.dispatched":
		var p dispatchPayload
		if err := decode(e.Payload, &p); err != nil || p.Owner == "" || p.Fence < 1 {
			return fmt.Errorf("invalid action.dispatched payload")
		}
		return updateExactlyOne(ctx, db, `UPDATE action_projection SET state=?,dispatch_owner=?,dispatch_fence=?,last_sequence=? WHERE action_id=? AND session_id=? AND state=?`, StateDispatched, p.Owner, p.Fence, e.Sequence, p.ActionID, e.SessionID, StateCommitted)
	case "action.dispatch_reassigned":
		var p dispatchPayload
		if err := decode(e.Payload, &p); err != nil || p.Owner == "" || p.Fence < 2 {
			return fmt.Errorf("invalid reassignment payload")
		}
		return updateExactlyOne(ctx, db, `UPDATE action_projection SET dispatch_owner=?,dispatch_fence=?,last_sequence=? WHERE action_id=? AND session_id=? AND state=? AND dispatch_fence=?`, p.Owner, p.Fence, e.Sequence, p.ActionID, e.SessionID, StateDispatched, p.Fence-1)
	case "action.succeeded", "action.failed":
		var p resultPayload
		if err := decode(e.Payload, &p); err != nil || !json.Valid(p.Result) {
			return fmt.Errorf("invalid action result payload")
		}
		state := StateSucceeded
		if e.Kind == "action.failed" {
			state = StateFailed
		}
		return updateExactlyOne(ctx, db, `UPDATE action_projection SET state=?,result_json=?,last_sequence=? WHERE action_id=? AND session_id=? AND state=? AND dispatch_owner=? AND dispatch_fence=?`, state, []byte(p.Result), e.Sequence, p.ActionID, e.SessionID, StateDispatched, p.Owner, p.Fence)
	case "action.compensation_committed":
		var p compensationPayload
		if err := decode(e.Payload, &p); err != nil || p.CompensationActionID == "" {
			return fmt.Errorf("invalid compensation payload")
		}
		return updateExactlyOne(ctx, db, `UPDATE action_projection SET state=?,compensation_action_id=?,last_sequence=?
WHERE action_id=? AND session_id=? AND state=? AND EXISTS (
  SELECT 1 FROM action_projection compensation WHERE compensation.action_id=? AND compensation.session_id=? AND compensation.state=?
)`, StateCompensationCommitted, p.CompensationActionID, e.Sequence, p.ActionID, e.SessionID, StateFailed, p.CompensationActionID, e.SessionID, StateCommitted)
	default:
		return nil
	}
}

func decode(raw json.RawMessage, target any) error {
	if !json.Valid(raw) {
		return fmt.Errorf("invalid JSON payload")
	}
	return json.Unmarshal(raw, target)
}

func validObject(raw json.RawMessage) bool {
	var value map[string]any
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func updateExactlyOne(ctx context.Context, db eventlog.DBTX, query string, args ...any) error {
	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("illegal action transition")
	}
	return nil
}
