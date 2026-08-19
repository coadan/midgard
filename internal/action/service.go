package action

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"midgard/internal/eventlog"
)

type CapabilityValidator interface {
	Validate(capability string, arguments json.RawMessage) error
}

type CapabilitySet map[string]func(json.RawMessage) error

func (set CapabilitySet) Validate(capability string, arguments json.RawMessage) error {
	validate, ok := set[capability]
	if !ok {
		return fmt.Errorf("capability %q is not exposed", capability)
	}
	if validate == nil {
		return nil
	}
	return validate(arguments)
}

type Service struct {
	Log       *eventlog.Store
	Validator CapabilityValidator
}

func (s Service) Intent(ctx context.Context, sessionID, actionID, capability string, arguments json.RawMessage, approvalRequired bool) (Projection, error) {
	return s.IntentInTurn(ctx, sessionID, "", actionID, capability, arguments, approvalRequired)
}

// IntentInTurn records an action intent in the turn that proposed it. The
// action lifecycle remains usable outside an agent turn through Intent, whose
// durable turn id is intentionally empty.
func (s Service) IntentInTurn(ctx context.Context, sessionID, turnID, actionID, capability string, arguments json.RawMessage, approvalRequired bool) (Projection, error) {
	if err := s.requireActiveSession(ctx, sessionID); err != nil {
		return Projection{}, err
	}
	if !validObject(arguments) {
		return Projection{}, errors.New("action arguments must be a JSON object")
	}
	p := intentPayload{ActionID: actionID, Version: 1, Capability: capability, Arguments: arguments, ApprovalRequired: approvalRequired}
	if _, err := s.append(ctx, sessionID, turnID, "action.intent", p, eventlog.ActorModel); err != nil {
		return Projection{}, err
	}
	return s.Get(ctx, actionID)
}

func (s Service) Revise(ctx context.Context, actionID, capability string, arguments json.RawMessage, approvalRequired bool) (Projection, error) {
	current, err := s.Get(ctx, actionID)
	if err != nil {
		return Projection{}, err
	}
	if err := s.requireActiveSession(ctx, current.SessionID); err != nil {
		return Projection{}, err
	}
	p := intentPayload{ActionID: actionID, Version: current.Version + 1, Capability: capability, Arguments: arguments, ApprovalRequired: approvalRequired}
	if _, err := s.append(ctx, current.SessionID, "", "action.intent_revised", p, eventlog.ActorModel); err != nil {
		return Projection{}, err
	}
	return s.Get(ctx, actionID)
}

func (s Service) Retract(ctx context.Context, actionID string) (Projection, error) {
	current, err := s.Get(ctx, actionID)
	if err != nil {
		return Projection{}, err
	}
	if err := s.requireActiveSession(ctx, current.SessionID); err != nil {
		return Projection{}, err
	}
	if _, err := s.append(ctx, current.SessionID, "", "action.retracted", actionVersionPayload{ActionID: actionID, Version: current.Version}, eventlog.ActorModel); err != nil {
		return Projection{}, err
	}
	return s.Get(ctx, actionID)
}

func (s Service) Validate(ctx context.Context, actionID string) (Projection, error) {
	current, err := s.Get(ctx, actionID)
	if err != nil {
		return Projection{}, err
	}
	if err := s.requireActiveSession(ctx, current.SessionID); err != nil {
		return Projection{}, err
	}
	if s.Validator == nil {
		return Projection{}, errors.New("no capability validator configured")
	}
	if err := s.Validator.Validate(current.Capability, current.Arguments); err != nil {
		return Projection{}, err
	}
	if _, err := s.append(ctx, current.SessionID, "", "action.validated", actionVersionPayload{ActionID: actionID, Version: current.Version}, eventlog.ActorPolicy); err != nil {
		return Projection{}, err
	}
	return s.Get(ctx, actionID)
}

func (s Service) RequestApproval(ctx context.Context, actionID string) (Projection, error) {
	current, err := s.Get(ctx, actionID)
	if err != nil {
		return Projection{}, err
	}
	if err := s.requireActiveSession(ctx, current.SessionID); err != nil {
		return Projection{}, err
	}
	if _, err := s.append(ctx, current.SessionID, "", "action.approval_requested", actionVersionPayload{ActionID: actionID, Version: current.Version}, eventlog.ActorPolicy); err != nil {
		return Projection{}, err
	}
	return s.Get(ctx, actionID)
}

func (s Service) Decide(ctx context.Context, actionID, decidedBy string, approved bool) (Projection, error) {
	current, err := s.Get(ctx, actionID)
	if err != nil {
		return Projection{}, err
	}
	if err := s.requireActiveSession(ctx, current.SessionID); err != nil {
		return Projection{}, err
	}
	kind := "action.rejected"
	if approved {
		kind = "action.approved"
	}
	if _, err := s.append(ctx, current.SessionID, "", kind, decisionPayload{ActionID: actionID, Actor: decidedBy}, eventlog.ActorUser); err != nil {
		return Projection{}, err
	}
	return s.Get(ctx, actionID)
}

func (s Service) Commit(ctx context.Context, actionID, idempotencyKey string) (Projection, error) {
	current, err := s.Get(ctx, actionID)
	if err != nil {
		return Projection{}, err
	}
	if err := s.requireActiveSession(ctx, current.SessionID); err != nil {
		return Projection{}, err
	}
	p := commitPayload{ActionID: actionID, Version: current.Version, CommitID: randomID("commit"), IdempotencyKey: idempotencyKey}
	if _, err := s.append(ctx, current.SessionID, "", "action.committed", p, eventlog.ActorServer); err != nil {
		return Projection{}, err
	}
	return s.Get(ctx, actionID)
}

type Claim struct {
	ActionID string
	CommitID string
	Owner    string
	Fence    int64
}

func (s Service) Dispatch(ctx context.Context, actionID, owner string) (Claim, error) {
	current, err := s.Get(ctx, actionID)
	if err != nil {
		return Claim{}, err
	}
	if err := s.requireActiveSession(ctx, current.SessionID); err != nil {
		return Claim{}, err
	}
	p := dispatchPayload{ActionID: actionID, Owner: owner, Fence: 1}
	if _, err := s.append(ctx, current.SessionID, "", "action.dispatched", p, eventlog.ActorServer); err != nil {
		return Claim{}, err
	}
	return Claim{ActionID: actionID, CommitID: current.CommitID, Owner: owner, Fence: 1}, nil
}

// Reassign increments the ownership fence. It is a recovery operation; callers
// must establish that the previous owner is no longer authoritative.
func (s Service) Reassign(ctx context.Context, claim Claim, newOwner string) (Claim, error) {
	current, err := s.Get(ctx, claim.ActionID)
	if err != nil {
		return Claim{}, err
	}
	if err := s.requireActiveSession(ctx, current.SessionID); err != nil {
		return Claim{}, err
	}
	if current.DispatchOwner != claim.Owner || current.DispatchFence != claim.Fence || current.CommitID != claim.CommitID {
		return Claim{}, errors.New("stale dispatch claim")
	}
	p := dispatchPayload{ActionID: claim.ActionID, Owner: newOwner, Fence: claim.Fence + 1}
	if _, err := s.append(ctx, current.SessionID, "", "action.dispatch_reassigned", p, eventlog.ActorServer); err != nil {
		return Claim{}, err
	}
	return Claim{ActionID: claim.ActionID, CommitID: claim.CommitID, Owner: newOwner, Fence: claim.Fence + 1}, nil
}

func (s Service) Result(ctx context.Context, claim Claim, success bool, result json.RawMessage) (Projection, error) {
	current, err := s.Get(ctx, claim.ActionID)
	if err != nil {
		return Projection{}, err
	}
	if current.CommitID != claim.CommitID {
		return Projection{}, errors.New("claim commit does not match action")
	}
	kind := "action.failed"
	if success {
		kind = "action.succeeded"
	}
	p := resultPayload{ActionID: claim.ActionID, Owner: claim.Owner, Fence: claim.Fence, Result: result}
	if _, err := s.append(ctx, current.SessionID, "", kind, p, eventlog.ActorTool); err != nil {
		return Projection{}, err
	}
	return s.Get(ctx, claim.ActionID)
}

func (s Service) RecordCompensationCommit(ctx context.Context, failedActionID, compensationActionID string) (Projection, error) {
	failed, err := s.Get(ctx, failedActionID)
	if err != nil {
		return Projection{}, err
	}
	if _, err := s.append(ctx, failed.SessionID, "", "action.compensation_committed", compensationPayload{
		ActionID: failedActionID, CompensationActionID: compensationActionID,
	}, eventlog.ActorServer); err != nil {
		return Projection{}, err
	}
	return s.Get(ctx, failedActionID)
}

func (s Service) Get(ctx context.Context, actionID string) (Projection, error) {
	var p Projection
	var state string
	var approvalRequired, approved int
	var arguments, result []byte
	err := s.Log.DB().QueryRowContext(ctx, `SELECT action_id,session_id,state,version,capability,arguments_json,
approval_required,approved,COALESCE(commit_id,''),COALESCE(idempotency_key,''),
COALESCE(dispatch_owner,''),COALESCE(dispatch_fence,0),COALESCE(compensation_action_id,''),COALESCE(result_json,''),last_sequence
FROM action_projection WHERE action_id=?`, actionID).Scan(&p.ActionID, &p.SessionID, &state,
		&p.Version, &p.Capability, &arguments, &approvalRequired, &approved, &p.CommitID,
		&p.IdempotencyKey, &p.DispatchOwner, &p.DispatchFence, &p.CompensationActionID, &result, &p.LastSequence)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Projection{}, fmt.Errorf("action %s not found", actionID)
		}
		return Projection{}, err
	}
	p.State = State(state)
	p.Arguments = arguments
	p.Result = result
	p.ApprovalRequired = approvalRequired != 0
	p.Approved = approved != 0
	return p, nil
}

func (s Service) NonTerminal(ctx context.Context, sessionID string) ([]Projection, error) {
	rows, err := s.Log.DB().QueryContext(ctx, `SELECT action_id FROM action_projection
WHERE session_id=? AND state NOT IN ('rejected','retracted','succeeded','failed','compensation_committed') ORDER BY last_sequence`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]Projection, 0, len(ids))
	for _, id := range ids {
		projection, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, projection)
	}
	return result, nil
}

// HasSucceededInTurn reports whether a capability in the supplied set reached
// a successful terminal result in the given turn. The action projection owns
// lifecycle state; the intent event supplies the durable turn association.
func (s Service) HasSucceededInTurn(ctx context.Context, sessionID, turnID string, capabilities ...string) (bool, error) {
	if sessionID == "" || turnID == "" || len(capabilities) == 0 {
		return false, nil
	}
	placeholders := make([]string, len(capabilities))
	arguments := make([]any, 0, len(capabilities)+3)
	arguments = append(arguments, sessionID, turnID, StateSucceeded)
	for index, capability := range capabilities {
		placeholders[index] = "?"
		arguments = append(arguments, capability)
	}
	query := `SELECT COUNT(*) FROM action_projection AS action
JOIN events AS intent ON intent.session_id=action.session_id
  AND intent.correlation_id=action.action_id AND intent.kind='action.intent'
WHERE action.session_id=? AND intent.turn_id=? AND action.state=?
  AND action.capability IN (` + strings.Join(placeholders, ",") + `)`
	var count int
	if err := s.Log.DB().QueryRowContext(ctx, query, arguments...).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// HasSucceededInTurnWithStringArgument reports whether a capability completed
// successfully in a turn with one exact top-level string argument. It keeps
// action-state queries generic while allowing policy to require evidence for a
// particular named operating instruction.
func (s Service) HasSucceededInTurnWithStringArgument(ctx context.Context, sessionID, turnID, capability, field, want string) (bool, error) {
	if sessionID == "" || turnID == "" || capability == "" || field == "" || want == "" {
		return false, nil
	}
	rows, err := s.Log.DB().QueryContext(ctx, `SELECT action.arguments_json FROM action_projection AS action
JOIN events AS intent ON intent.session_id=action.session_id
  AND intent.correlation_id=action.action_id AND intent.kind='action.intent'
WHERE action.session_id=? AND intent.turn_id=? AND action.state=? AND action.capability=?`,
		sessionID, turnID, StateSucceeded, capability)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var arguments map[string]any
		if err := json.Unmarshal(raw, &arguments); err != nil {
			return false, err
		}
		if value, ok := arguments[field].(string); ok && value == want {
			return true, nil
		}
	}
	return false, rows.Err()
}

// HasPriorAttemptInTurn reports whether a capability was proposed earlier in
// this turn. The excluded action is normally the current validated intent, so
// a policy can offer one corrective recovery without blocking a later repair.
func (s Service) HasPriorAttemptInTurn(ctx context.Context, sessionID, turnID, capability, excludeActionID string) (bool, error) {
	if sessionID == "" || turnID == "" || capability == "" {
		return false, nil
	}
	var count int
	err := s.Log.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM action_projection AS action
JOIN events AS intent ON intent.session_id=action.session_id
  AND intent.correlation_id=action.action_id AND intent.kind='action.intent'
WHERE action.session_id=? AND intent.turn_id=? AND action.capability=? AND action.action_id<>?`,
		sessionID, turnID, capability, excludeActionID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s Service) append(ctx context.Context, sessionID, turnID, kind string, payload any, actor eventlog.Actor) (eventlog.Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Event{}, err
	}
	return s.Log.AppendCurrent(ctx, eventlog.Draft{EventID: randomID("evt"), SessionID: sessionID,
		TurnID: turnID, Actor: actor, Kind: kind, SchemaVersion: 1, Visibility: eventlog.VisibilityInternal,
		Payload: raw, CorrelationID: actionCorrelation(payload)})
}

func actionCorrelation(payload any) string {
	switch p := payload.(type) {
	case intentPayload:
		return p.ActionID
	case actionVersionPayload:
		return p.ActionID
	case decisionPayload:
		return p.ActionID
	case commitPayload:
		return p.ActionID
	case dispatchPayload:
		return p.ActionID
	case resultPayload:
		return p.ActionID
	case compensationPayload:
		return p.ActionID
	default:
		return ""
	}
}

func randomID(prefix string) string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}

func (s Service) requireActiveSession(ctx context.Context, sessionID string) error {
	var status string
	if err := s.Log.DB().QueryRowContext(ctx, `SELECT status FROM session_projection WHERE session_id=?`, sessionID).Scan(&status); err != nil {
		return err
	}
	if status != "active" {
		return fmt.Errorf("session %s is %s", sessionID, status)
	}
	return nil
}
