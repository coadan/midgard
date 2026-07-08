package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"midgard/internal/state"
	"midgard/internal/workbench"
)

type FeedbackInput struct {
	Status  string
	Source  string
	Message string
}

type feedbackStatus struct {
	Status  string
	Source  string
	Message string
}

func RecordFeedback(ctx context.Context, root, taskID string, input FeedbackInput) error {
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = "changes-requested"
	}
	if status != "changes-requested" && status != "note" {
		return fmt.Errorf("feedback status %q is not supported", status)
	}
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = "external"
	}
	message := strings.TrimSpace(input.Message)
	if message == "" {
		return fmt.Errorf("feedback message is required")
	}
	wbStatus, err := workbench.Status(root)
	if err != nil {
		return err
	}
	db, err := state.Open(ctx, workbench.NewLayout(wbStatus.Root).State)
	if err != nil {
		return err
	}
	defer db.Close()
	payload, err := json.Marshal(feedbackStatus{
		Status:  status,
		Source:  source,
		Message: message,
	})
	if err != nil {
		return err
	}
	if _, err := db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "feedback.received", Payload: string(payload)}); err != nil {
		return err
	}
	if status == "changes-requested" {
		return db.UpdateTaskState(ctx, taskID, StateOpen)
	}
	return nil
}

func parseFeedbackReceivedEvent(payload string) (feedbackStatus, bool) {
	var parsed feedbackStatus
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return feedbackStatus{}, false
	}
	if parsed.Status == "" {
		return feedbackStatus{}, false
	}
	return parsed, true
}
