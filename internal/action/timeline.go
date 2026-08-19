package action

import (
	"context"
	"errors"
	"time"
)

// TimelineItem is the bounded presentation read model for one action. It is
// derived from the action projection and its durable intent/result boundaries;
// it does not introduce another source of action state.
type TimelineItem struct {
	ActionID        string
	SessionID       string
	TurnID          string
	Capability      string
	State           State
	Arguments       []byte
	Result          []byte
	StartedSequence int64
	LastSequence    int64
	StartedAt       time.Time
	FinishedAt      time.Time
}

// TimelineWindow keeps the presentation bounded while preserving the fact
// that older durable action history exists outside the current TUI window.
type TimelineWindow struct {
	Items   []TimelineItem
	Omitted int
}

// RecentTimeline returns the latest action cards that overlap the visible
// session window. Cards are ordered by their original intent, while their
// content is the latest durable action state. The action projection remains
// the owner of state; events supply the timeline position and timestamps.
func (s Service) RecentTimeline(ctx context.Context, sessionID string, minSequence int64, limit int) (TimelineWindow, error) {
	if limit <= 0 {
		return TimelineWindow{}, errors.New("action timeline limit must be positive")
	}
	var total int
	if err := s.Log.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM action_projection WHERE session_id=? AND last_sequence>=?`, sessionID, minSequence).Scan(&total); err != nil {
		return TimelineWindow{}, err
	}
	rows, err := s.Log.DB().QueryContext(ctx, `
SELECT ap.action_id,ap.session_id,COALESCE(intent.turn_id,''),ap.capability,ap.state,
  ap.arguments_json,COALESCE(ap.result_json,''),intent.sequence,ap.last_sequence,
  intent.created_at,last_event.created_at
FROM action_projection ap
JOIN events intent ON intent.session_id=ap.session_id
  AND intent.correlation_id=ap.action_id AND intent.kind='action.intent'
JOIN events last_event ON last_event.session_id=ap.session_id AND last_event.sequence=ap.last_sequence
WHERE ap.session_id=? AND ap.last_sequence>=?
ORDER BY intent.sequence DESC
LIMIT ?`, sessionID, minSequence, limit)
	if err != nil {
		return TimelineWindow{}, err
	}
	defer rows.Close()
	var newestFirst []TimelineItem
	for rows.Next() {
		var item TimelineItem
		var state, startedAt, finishedAt string
		if err := rows.Scan(&item.ActionID, &item.SessionID, &item.TurnID, &item.Capability, &state,
			&item.Arguments, &item.Result, &item.StartedSequence, &item.LastSequence, &startedAt, &finishedAt); err != nil {
			return TimelineWindow{}, err
		}
		item.State = State(state)
		var err error
		item.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
		if err != nil {
			return TimelineWindow{}, err
		}
		item.FinishedAt, err = time.Parse(time.RFC3339Nano, finishedAt)
		if err != nil {
			return TimelineWindow{}, err
		}
		newestFirst = append(newestFirst, item)
	}
	if err := rows.Err(); err != nil {
		return TimelineWindow{}, err
	}
	for left, right := 0, len(newestFirst)-1; left < right; left, right = left+1, right-1 {
		newestFirst[left], newestFirst[right] = newestFirst[right], newestFirst[left]
	}
	return TimelineWindow{Items: newestFirst, Omitted: max(0, total-len(newestFirst))}, nil
}
