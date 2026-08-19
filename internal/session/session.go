package session

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"midgard/internal/eventlog"
)

type Service struct{ Log *eventlog.Store }

type Projection struct {
	SessionID string
	ProjectID string
	Objective string
	Status    string
	Head      int64
	Provider  string
	Profile   string
	Model     string
	Effort    string
}

type ModelSelection struct {
	Provider string
	Profile  string
	Model    string
	Effort   string
}

type Message struct {
	MessageID   string
	SessionID   string
	TurnID      string
	Role        string
	Content     string
	ArtifactRef string
	Sequence    int64
}

type TranscriptWindow struct {
	Messages          []Message
	OmittedMessages   int
	ShortenedMessages int
}

type Interruption struct {
	TurnID         string
	Sequence       int64
	UnknownOutcome bool
}

type TurnUsage struct {
	TurnID                 string
	Model                  string
	InputTokens            int64
	CacheHitInputTokens    int64
	CacheMissInputTokens   int64
	OutputTokens           int64
	ThinkingTokens         int64
	ProviderDurationMillis int64
	PeakContextTokens      int64
	ContextLimitTokens     int64
	Compactions            int
	Sequence               int64
}

// TurnFailure is a server-authored, safe-to-display receipt for a failed turn.
// It intentionally records a stable category and recovery message, never a raw
// provider or host error that could include sensitive request content.
type TurnFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Control struct {
	ControlID    string
	SessionID    string
	TurnID       string
	Kind         string
	Content      string
	Acknowledged bool
	Sequence     int64
}

type Summary struct {
	SessionID    string
	Objective    string
	Status       string
	UpdatedAt    string
	WorktreeRoot string
	ActiveTurnID string
}

func (s Service) Create(ctx context.Context, sessionID string, objective string) (eventlog.Event, error) {
	return s.CreateInProject(ctx, sessionID, "", objective)
}

func (s Service) CreateInProject(ctx context.Context, sessionID, projectID string, objective string) (eventlog.Event, error) {
	if objective == "" {
		return eventlog.Event{}, errors.New("objective is required")
	}
	payload, _ := json.Marshal(struct {
		Objective string `json:"objective"`
		ProjectID string `json:"project_id,omitempty"`
	}{objective, projectID})
	version := 1
	if projectID != "" {
		version = 2
	}
	return s.Log.Append(ctx, eventlog.Draft{
		EventID: newID("evt"), SessionID: sessionID, Actor: eventlog.ActorUser,
		Kind: "session.created", SchemaVersion: version, Visibility: eventlog.VisibilityPublic,
		Payload: payload,
	}, 0)
}

func (s Service) StartTurn(ctx context.Context, sessionID, turnID string) (eventlog.Event, error) {
	if err := s.requireActive(ctx, sessionID); err != nil {
		return eventlog.Event{}, err
	}
	return s.append(ctx, eventlog.Draft{EventID: newID("evt"), SessionID: sessionID,
		TurnID: turnID, Actor: eventlog.ActorServer, Kind: "turn.started",
		SchemaVersion: 1, Visibility: eventlog.VisibilityInternal})
}

// SelectModel changes the model transport only while no turn is active. The
// event is the durable safe-boundary receipt used when reopening the session.
func (s Service) SelectModel(ctx context.Context, sessionID string, selection ModelSelection) (eventlog.Event, error) {
	if strings.TrimSpace(selection.Provider) == "" || strings.TrimSpace(selection.Model) == "" || strings.TrimSpace(selection.Effort) == "" {
		return eventlog.Event{}, errors.New("model selection requires provider, model, and effort")
	}
	if err := s.requireActive(ctx, sessionID); err != nil {
		return eventlog.Event{}, err
	}
	turnID, err := s.ActiveTurn(ctx, sessionID)
	if err != nil {
		return eventlog.Event{}, err
	}
	if turnID != "" {
		return eventlog.Event{}, errors.New("the agent is still working; the model can change at the next safe turn boundary")
	}
	payload, _ := json.Marshal(selection)
	return s.append(ctx, eventlog.Draft{EventID: newID("evt"), SessionID: sessionID, Actor: eventlog.ActorUser,
		Kind: "session.model_selected", SchemaVersion: 1, Visibility: eventlog.VisibilityPublic, Payload: payload})
}

func (s Service) ModelSelection(ctx context.Context, sessionID string) (ModelSelection, error) {
	var selection ModelSelection
	err := s.Log.DB().QueryRowContext(ctx, `SELECT provider,profile,model,effort FROM session_projection WHERE session_id=?`, sessionID).
		Scan(&selection.Provider, &selection.Profile, &selection.Model, &selection.Effort)
	return selection, err
}

func (s Service) Cancel(ctx context.Context, sessionID, reason string) (eventlog.Event, error) {
	if err := s.requireActive(ctx, sessionID); err != nil {
		return eventlog.Event{}, err
	}
	payload, _ := json.Marshal(struct {
		Reason string `json:"reason"`
	}{reason})
	return s.append(ctx, eventlog.Draft{EventID: newID("evt"), SessionID: sessionID,
		Actor: eventlog.ActorUser, Kind: "session.cancelled", SchemaVersion: 1,
		Visibility: eventlog.VisibilityPublic, Payload: payload})
}

func (s Service) Control(ctx context.Context, sessionID, kind, controlID string, payload json.RawMessage) (eventlog.Event, error) {
	if kind != "steer" && kind != "interrupt" && kind != "approve" && kind != "reject" {
		return eventlog.Event{}, fmt.Errorf("invalid control kind %q", kind)
	}
	if controlID == "" {
		return eventlog.Event{}, fmt.Errorf("control_id is required")
	}
	if kind == "steer" {
		return eventlog.Event{}, errors.New("use Steer to submit content-bearing steering guidance")
	}
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		return eventlog.Event{}, err
	}
	value["control_id"] = controlID
	raw, _ := json.Marshal(value)
	return s.append(ctx, eventlog.Draft{EventID: newID("evt"), SessionID: sessionID,
		Actor: eventlog.ActorUser, Kind: "control." + kind, SchemaVersion: 1,
		Visibility: eventlog.VisibilityPublic, Payload: raw})
}

// Steer durably queues user guidance for the active turn. Persistence is the
// receipt acknowledgement; control.acknowledged means the coordinator has
// incorporated the guidance into provider context.
func (s Service) Steer(ctx context.Context, sessionID, content string) (Control, error) {
	if strings.TrimSpace(content) == "" {
		return Control{}, errors.New("steering content is required")
	}
	turnID, err := s.ActiveTurn(ctx, sessionID)
	if err != nil {
		return Control{}, err
	}
	if turnID == "" {
		return Control{}, fmt.Errorf("session %s has no active turn", sessionID)
	}
	controlID := newID("control")
	payload, _ := json.Marshal(map[string]string{"control_id": controlID, "content": content})
	event, err := s.append(ctx, eventlog.Draft{EventID: newID("evt"), SessionID: sessionID,
		TurnID: turnID, Actor: eventlog.ActorUser, Kind: "control.steer", SchemaVersion: 1,
		Visibility: eventlog.VisibilityPublic, Payload: payload})
	if err != nil {
		return Control{}, err
	}
	return Control{ControlID: controlID, SessionID: sessionID, TurnID: turnID, Kind: "control.steer", Content: content, Sequence: event.Sequence}, nil
}

func (s Service) AcknowledgeControl(ctx context.Context, sessionID, controlID string) (eventlog.Event, error) {
	payload, _ := json.Marshal(map[string]string{"control_id": controlID})
	return s.append(ctx, eventlog.Draft{EventID: newID("evt"), SessionID: sessionID,
		Actor: eventlog.ActorServer, Kind: "control.acknowledged", SchemaVersion: 1,
		Visibility: eventlog.VisibilityInternal, Payload: payload})
}

func (s Service) EndTurn(ctx context.Context, sessionID, turnID, outcome string) (eventlog.Event, error) {
	return s.endTurn(ctx, sessionID, turnID, outcome, nil)
}

// FailTurn ends a turn with a sanitized, durable recovery receipt. The receipt
// is evidence for a later prompt-maintenance review; it is not model output.
func (s Service) FailTurn(ctx context.Context, sessionID, turnID string, failure TurnFailure) (eventlog.Event, error) {
	if strings.TrimSpace(failure.Code) == "" || strings.TrimSpace(failure.Message) == "" {
		return eventlog.Event{}, errors.New("turn failure requires code and message")
	}
	payload, _ := json.Marshal(failure)
	return s.endTurn(ctx, sessionID, turnID, "failed", payload)
}

func (s Service) endTurn(ctx context.Context, sessionID, turnID, outcome string, payload json.RawMessage) (eventlog.Event, error) {
	kind := "turn." + outcome
	if outcome != "completed" && outcome != "interrupted" && outcome != "failed" {
		return eventlog.Event{}, fmt.Errorf("invalid turn outcome %q", outcome)
	}
	return s.append(ctx, eventlog.Draft{EventID: newID("evt"), SessionID: sessionID,
		TurnID: turnID, Actor: eventlog.ActorServer, Kind: kind,
		SchemaVersion: 1, Visibility: eventlog.VisibilityInternal, Payload: payload})
}

func (s Service) RecordTurnUsage(ctx context.Context, sessionID string, usage TurnUsage) (eventlog.Event, error) {
	if usage.TurnID == "" || usage.Model == "" {
		return eventlog.Event{}, errors.New("turn usage requires turn_id and model")
	}
	if usage.InputTokens < 0 || usage.CacheHitInputTokens < 0 || usage.CacheMissInputTokens < 0 || usage.OutputTokens < 0 || usage.ThinkingTokens < 0 || usage.ProviderDurationMillis < 0 || usage.PeakContextTokens < 0 || usage.ContextLimitTokens < 0 || usage.Compactions < 0 {
		return eventlog.Event{}, errors.New("turn usage token counts cannot be negative")
	}
	if usage.ThinkingTokens > usage.OutputTokens {
		return eventlog.Event{}, errors.New("turn usage thinking tokens cannot exceed output tokens")
	}
	if usage.CacheHitInputTokens+usage.CacheMissInputTokens != usage.InputTokens {
		return eventlog.Event{}, errors.New("turn usage input tokens must equal cache hit plus cache miss tokens")
	}
	var active int
	if err := s.Log.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM turn_projection WHERE turn_id=? AND session_id=? AND status='active'`, usage.TurnID, sessionID).Scan(&active); err != nil {
		return eventlog.Event{}, err
	}
	if active != 1 {
		return eventlog.Event{}, fmt.Errorf("turn %s is not active", usage.TurnID)
	}
	payload, _ := json.Marshal(struct {
		Model                  string `json:"model"`
		InputTokens            int64  `json:"input_tokens"`
		CacheHitInputTokens    int64  `json:"cache_hit_input_tokens"`
		CacheMissInputTokens   int64  `json:"cache_miss_input_tokens"`
		OutputTokens           int64  `json:"output_tokens"`
		ThinkingTokens         int64  `json:"thinking_tokens,omitempty"`
		ProviderDurationMillis int64  `json:"provider_duration_ms,omitempty"`
		PeakContextTokens      int64  `json:"peak_context_tokens,omitempty"`
		ContextLimitTokens     int64  `json:"context_limit_tokens,omitempty"`
		Compactions            int    `json:"compactions,omitempty"`
	}{usage.Model, usage.InputTokens, usage.CacheHitInputTokens, usage.CacheMissInputTokens, usage.OutputTokens, usage.ThinkingTokens, usage.ProviderDurationMillis, usage.PeakContextTokens, usage.ContextLimitTokens, usage.Compactions})
	version := 1
	if usage.ContextLimitTokens > 0 {
		version = 2
	}
	if usage.ThinkingTokens > 0 || usage.ProviderDurationMillis > 0 {
		version = 3
	}
	return s.append(ctx, eventlog.Draft{EventID: newID("evt"), SessionID: sessionID,
		TurnID: usage.TurnID, Actor: eventlog.ActorServer, Kind: "turn.usage_recorded",
		SchemaVersion: version, Visibility: eventlog.VisibilityInternal, Payload: payload})
}

func (s Service) RecordMessage(ctx context.Context, sessionID, turnID, role, content string) (Message, error) {
	if err := s.requireActive(ctx, sessionID); err != nil {
		return Message{}, err
	}
	if role != "user" && role != "assistant" {
		return Message{}, fmt.Errorf("invalid message role %q", role)
	}
	if content == "" {
		return Message{}, errors.New("message content is required")
	}
	payload, _ := json.Marshal(map[string]string{"message_id": newID("msg"), "content": content})
	event, err := s.append(ctx, eventlog.Draft{EventID: newID("evt"), SessionID: sessionID,
		TurnID: turnID, Actor: map[string]eventlog.Actor{"user": eventlog.ActorUser, "assistant": eventlog.ActorModel}[role],
		Kind: "message." + role, SchemaVersion: 1, Visibility: eventlog.VisibilityPublic, Payload: payload})
	if err != nil {
		return Message{}, err
	}
	var stored struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(payload, &stored)
	return Message{MessageID: stored.MessageID, SessionID: sessionID, TurnID: turnID, Role: role, Content: content, Sequence: event.Sequence}, nil
}

func (s Service) Messages(ctx context.Context, sessionID string) ([]Message, error) {
	rows, err := s.Log.DB().QueryContext(ctx, `
SELECT message_id,session_id,turn_id,role,content,artifact_ref,sequence FROM (
  SELECT message_id,session_id,turn_id,role,content,COALESCE(artifact_ref,'') AS artifact_ref,sequence
  FROM message_projection WHERE session_id=?
  UNION ALL
  SELECT cp.control_id,cp.session_id,COALESCE(e.turn_id,''),'user',
    COALESCE(json_extract(e.payload_json,'$.content'),''),'',cp.sequence
  FROM control_projection cp
  JOIN events e ON e.session_id=cp.session_id AND e.sequence=cp.sequence
  JOIN turn_projection tp ON tp.session_id=e.session_id AND tp.turn_id=e.turn_id
  WHERE cp.session_id=? AND cp.kind='control.steer' AND cp.acknowledged=1
    AND tp.status!='interrupted'
) ORDER BY sequence`, sessionID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []Message
	for rows.Next() {
		var message Message
		if err := rows.Scan(&message.MessageID, &message.SessionID, &message.TurnID, &message.Role, &message.Content, &message.ArtifactRef, &message.Sequence); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

// RecentMessages is a read-only presentation projection. Canonical messages
// remain complete; only the TUI window is byte-bounded and per-message bounded.
func (s Service) RecentMessages(ctx context.Context, sessionID string, byteLimit, messageLimit int) (TranscriptWindow, error) {
	if byteLimit <= 0 || messageLimit <= 0 {
		return TranscriptWindow{}, errors.New("transcript byte and message limits must be positive")
	}
	var total int
	if err := s.Log.DB().QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM message_projection WHERE session_id=?) +
  (SELECT COUNT(*) FROM control_projection cp
    JOIN events e ON e.session_id=cp.session_id AND e.sequence=cp.sequence
    JOIN turn_projection tp ON tp.session_id=e.session_id AND tp.turn_id=e.turn_id
    WHERE cp.session_id=? AND cp.kind='control.steer' AND cp.acknowledged=1 AND tp.status!='interrupted')`, sessionID, sessionID).Scan(&total); err != nil {
		return TranscriptWindow{}, err
	}
	rows, err := s.Log.DB().QueryContext(ctx, `
SELECT message_id,session_id,turn_id,role,content,artifact_ref,sequence,shortened FROM (
  SELECT message_id,session_id,turn_id,role,substr(content,1,?) AS content,
    COALESCE(artifact_ref,'') AS artifact_ref,sequence,length(content)>? AS shortened
  FROM message_projection WHERE session_id=?
  UNION ALL
  SELECT cp.control_id,cp.session_id,COALESCE(e.turn_id,''),'user',
    substr(COALESCE(json_extract(e.payload_json,'$.content'),''),1,?),'',cp.sequence,
    length(COALESCE(json_extract(e.payload_json,'$.content'),''))>? AS shortened
  FROM control_projection cp
  JOIN events e ON e.session_id=cp.session_id AND e.sequence=cp.sequence
  JOIN turn_projection tp ON tp.session_id=e.session_id AND tp.turn_id=e.turn_id
  WHERE cp.session_id=? AND cp.kind='control.steer' AND cp.acknowledged=1
    AND tp.status!='interrupted'
) ORDER BY sequence DESC`, messageLimit, messageLimit, sessionID, messageLimit, messageLimit, sessionID)
	if err != nil {
		return TranscriptWindow{}, err
	}
	defer rows.Close()
	window := TranscriptWindow{}
	used := 0
	for rows.Next() {
		var message Message
		var shortened bool
		if err := rows.Scan(&message.MessageID, &message.SessionID, &message.TurnID, &message.Role, &message.Content, &message.ArtifactRef, &message.Sequence, &shortened); err != nil {
			return TranscriptWindow{}, err
		}
		size := len(message.Content) + len(message.Role) + len(message.TurnID) + 64
		if len(window.Messages) > 0 && used+size > byteLimit {
			break
		}
		if shortened {
			message.Content += "\n\n[Message shortened in the TUI; complete content remains in session history.]"
			window.ShortenedMessages++
		}
		window.Messages = append(window.Messages, message)
		used += size
	}
	if err := rows.Err(); err != nil {
		return TranscriptWindow{}, err
	}
	slices.Reverse(window.Messages)
	window.OmittedMessages = max(0, total-len(window.Messages))
	return window, nil
}

func (s Service) Interruptions(ctx context.Context, sessionID string) ([]Interruption, error) {
	rows, err := s.Log.DB().QueryContext(ctx, `
SELECT tp.turn_id,tp.ended_sequence,
  EXISTS(
    SELECT 1 FROM events e
    WHERE e.session_id=tp.session_id
      AND e.sequence BETWEEN tp.started_sequence AND tp.ended_sequence
      AND e.kind='action.failed'
      AND json_extract(e.payload_json,'$.result.error')='worker_lost_outcome_unknown'
  )
FROM turn_projection tp
WHERE tp.session_id=? AND tp.status='interrupted'
ORDER BY tp.ended_sequence`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var interruptions []Interruption
	for rows.Next() {
		var interruption Interruption
		if err := rows.Scan(&interruption.TurnID, &interruption.Sequence, &interruption.UnknownOutcome); err != nil {
			return nil, err
		}
		interruptions = append(interruptions, interruption)
	}
	return interruptions, rows.Err()
}

func (s Service) RecentInterruptions(ctx context.Context, sessionID string, minSequence int64, limit int) ([]Interruption, error) {
	if limit <= 0 {
		return nil, errors.New("interruption limit must be positive")
	}
	rows, err := s.Log.DB().QueryContext(ctx, `
SELECT turn_id,ended_sequence,unknown_outcome FROM (
  SELECT tp.turn_id,tp.ended_sequence,
    EXISTS(
      SELECT 1 FROM events e
      WHERE e.session_id=tp.session_id
        AND e.sequence BETWEEN tp.started_sequence AND tp.ended_sequence
        AND e.kind='action.failed'
        AND json_extract(e.payload_json,'$.result.error')='worker_lost_outcome_unknown'
    ) AS unknown_outcome
  FROM turn_projection tp
  WHERE tp.session_id=? AND tp.status='interrupted' AND tp.ended_sequence>=?
  ORDER BY tp.ended_sequence DESC LIMIT ?
) ORDER BY ended_sequence`, sessionID, minSequence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var interruptions []Interruption
	for rows.Next() {
		var interruption Interruption
		if err := rows.Scan(&interruption.TurnID, &interruption.Sequence, &interruption.UnknownOutcome); err != nil {
			return nil, err
		}
		interruptions = append(interruptions, interruption)
	}
	return interruptions, rows.Err()
}

func (s Service) TurnUsages(ctx context.Context, sessionID string) ([]TurnUsage, error) {
	rows, err := s.Log.DB().QueryContext(ctx, `
SELECT e.turn_id,
  json_extract(e.payload_json,'$.model'),
  json_extract(e.payload_json,'$.input_tokens'),
  json_extract(e.payload_json,'$.cache_hit_input_tokens'),
  json_extract(e.payload_json,'$.cache_miss_input_tokens'),
  json_extract(e.payload_json,'$.output_tokens'),
	COALESCE(json_extract(e.payload_json,'$.thinking_tokens'),0),
	COALESCE(json_extract(e.payload_json,'$.provider_duration_ms'),0),
	COALESCE(json_extract(e.payload_json,'$.peak_context_tokens'),0),
	COALESCE(json_extract(e.payload_json,'$.context_limit_tokens'),0),
	COALESCE(json_extract(e.payload_json,'$.compactions'),0),
  e.sequence
FROM events e
JOIN turn_projection tp ON tp.session_id=e.session_id AND tp.turn_id=e.turn_id
WHERE e.session_id=? AND e.kind='turn.usage_recorded' AND tp.status='completed'
ORDER BY e.sequence`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var usages []TurnUsage
	for rows.Next() {
		var usage TurnUsage
		if err := rows.Scan(&usage.TurnID, &usage.Model, &usage.InputTokens, &usage.CacheHitInputTokens, &usage.CacheMissInputTokens, &usage.OutputTokens, &usage.ThinkingTokens, &usage.ProviderDurationMillis, &usage.PeakContextTokens, &usage.ContextLimitTokens, &usage.Compactions, &usage.Sequence); err != nil {
			return nil, err
		}
		usages = append(usages, usage)
	}
	return usages, rows.Err()
}

func (s Service) RecentTurnUsages(ctx context.Context, sessionID string, limit int) ([]TurnUsage, error) {
	if limit <= 0 {
		return nil, errors.New("turn usage limit must be positive")
	}
	rows, err := s.Log.DB().QueryContext(ctx, `
SELECT turn_id,model,input_tokens,cache_hit_input_tokens,cache_miss_input_tokens,output_tokens,thinking_tokens,provider_duration_ms,
  peak_context_tokens,context_limit_tokens,compactions,sequence FROM (
  SELECT e.turn_id,
    json_extract(e.payload_json,'$.model') AS model,
    json_extract(e.payload_json,'$.input_tokens') AS input_tokens,
    json_extract(e.payload_json,'$.cache_hit_input_tokens') AS cache_hit_input_tokens,
    json_extract(e.payload_json,'$.cache_miss_input_tokens') AS cache_miss_input_tokens,
    json_extract(e.payload_json,'$.output_tokens') AS output_tokens,
	COALESCE(json_extract(e.payload_json,'$.thinking_tokens'),0) AS thinking_tokens,
	COALESCE(json_extract(e.payload_json,'$.provider_duration_ms'),0) AS provider_duration_ms,
    COALESCE(json_extract(e.payload_json,'$.peak_context_tokens'),0) AS peak_context_tokens,
    COALESCE(json_extract(e.payload_json,'$.context_limit_tokens'),0) AS context_limit_tokens,
    COALESCE(json_extract(e.payload_json,'$.compactions'),0) AS compactions,
    e.sequence
  FROM events e
  JOIN turn_projection tp ON tp.session_id=e.session_id AND tp.turn_id=e.turn_id
  WHERE e.session_id=? AND e.kind='turn.usage_recorded' AND tp.status='completed'
  ORDER BY e.sequence DESC LIMIT ?
) ORDER BY sequence`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var usages []TurnUsage
	for rows.Next() {
		var usage TurnUsage
		if err := rows.Scan(&usage.TurnID, &usage.Model, &usage.InputTokens, &usage.CacheHitInputTokens, &usage.CacheMissInputTokens, &usage.OutputTokens, &usage.ThinkingTokens, &usage.ProviderDurationMillis, &usage.PeakContextTokens, &usage.ContextLimitTokens, &usage.Compactions, &usage.Sequence); err != nil {
			return nil, err
		}
		usages = append(usages, usage)
	}
	return usages, rows.Err()
}

func (s Service) PendingSteers(ctx context.Context, sessionID string) ([]Control, error) {
	rows, err := s.Log.DB().QueryContext(ctx, `SELECT cp.control_id,cp.session_id,COALESCE(e.turn_id,''),cp.kind,
COALESCE(json_extract(e.payload_json,'$.content'),''),cp.acknowledged,cp.sequence
FROM control_projection cp
JOIN events e ON e.session_id=cp.session_id AND e.sequence=cp.sequence
JOIN turn_projection tp ON tp.session_id=e.session_id AND tp.turn_id=e.turn_id
WHERE cp.session_id=? AND cp.kind='control.steer' AND cp.acknowledged=0
  AND tp.status='active'
ORDER BY cp.sequence`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var controls []Control
	for rows.Next() {
		var control Control
		if err := rows.Scan(&control.ControlID, &control.SessionID, &control.TurnID, &control.Kind, &control.Content, &control.Acknowledged, &control.Sequence); err != nil {
			return nil, err
		}
		controls = append(controls, control)
	}
	return controls, rows.Err()
}

func (s Service) ActiveTurn(ctx context.Context, sessionID string) (string, error) {
	var turnID string
	err := s.Log.DB().QueryRowContext(ctx, `SELECT turn_id FROM turn_projection WHERE session_id=? AND status='active' ORDER BY started_sequence DESC LIMIT 1`, sessionID).Scan(&turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return turnID, err
}

func (s Service) ListByRepository(ctx context.Context, repository string) ([]Summary, error) {
	rows, err := s.Log.DB().QueryContext(ctx, `SELECT sp.session_id,sp.objective,sp.status,sp.updated_at,wp.worktree_root,
COALESCE((SELECT turn_id FROM turn_projection tp WHERE tp.session_id=sp.session_id AND tp.status='active' ORDER BY started_sequence DESC LIMIT 1),'')
FROM session_projection sp JOIN workspace_projection wp ON wp.session_id=sp.session_id
WHERE wp.repository_root=? ORDER BY CASE WHEN sp.status='active' THEN 0 ELSE 1 END, sp.updated_at DESC`, repository)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var summaries []Summary
	for rows.Next() {
		var summary Summary
		if err := rows.Scan(&summary.SessionID, &summary.Objective, &summary.Status, &summary.UpdatedAt, &summary.WorktreeRoot, &summary.ActiveTurnID); err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

func (s Service) Get(ctx context.Context, sessionID string) (Projection, error) {
	var projection Projection
	err := s.Log.DB().QueryRowContext(ctx, `SELECT session_id,project_id,objective,status,last_sequence,provider,profile,model,effort FROM session_projection WHERE session_id=?`, sessionID).
		Scan(&projection.SessionID, &projection.ProjectID, &projection.Objective, &projection.Status, &projection.Head, &projection.Provider, &projection.Profile, &projection.Model, &projection.Effort)
	if errors.Is(err, sql.ErrNoRows) {
		return Projection{}, fmt.Errorf("session %s not found", sessionID)
	}
	return projection, err
}

func (s Service) Finish(ctx context.Context, sessionID, outcome, reason string) (eventlog.Event, error) {
	if err := s.requireActive(ctx, sessionID); err != nil {
		return eventlog.Event{}, err
	}
	if outcome != "completed" && outcome != "failed" {
		return eventlog.Event{}, fmt.Errorf("invalid session outcome %q", outcome)
	}
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	return s.append(ctx, eventlog.Draft{EventID: newID("evt"), SessionID: sessionID,
		Actor: eventlog.ActorServer, Kind: "session." + outcome, SchemaVersion: 1,
		Visibility: eventlog.VisibilityPublic, Payload: payload})
}

func (s Service) append(ctx context.Context, draft eventlog.Draft) (eventlog.Event, error) {
	return s.Log.AppendCurrent(ctx, draft)
}

func (s Service) requireActive(ctx context.Context, sessionID string) error {
	var status string
	err := s.Log.DB().QueryRowContext(ctx, `SELECT status FROM session_projection WHERE session_id=?`, sessionID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("session %s not found", sessionID)
		}
		return err
	}
	if status != "active" {
		return fmt.Errorf("session %s is %s", sessionID, status)
	}
	return nil
}

func newID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
