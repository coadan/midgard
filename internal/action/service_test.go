package action_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"midgard/internal/action"
	"midgard/internal/eventlog"
	"midgard/internal/session"
)

func TestActionLifecycleApprovalAndStaleWorkerFence(t *testing.T) {
	ctx := context.Background()
	store, service := actionStore(t)
	defer store.Close()

	intent, err := service.Intent(ctx, "session-1", "action-1", "shell", json.RawMessage(`{"command":"pwd"}`), true)
	if err != nil || intent.State != action.StateIntent {
		t.Fatalf("intent: %#v, %v", intent, err)
	}
	validated, err := service.Validate(ctx, "action-1")
	if err != nil || validated.State != action.StateValidated {
		t.Fatalf("validate: %#v, %v", validated, err)
	}
	if _, err := service.Commit(ctx, "action-1", "key-1"); err == nil {
		t.Fatal("commit bypassed required approval")
	}
	if _, err := service.RequestApproval(ctx, "action-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Decide(ctx, "action-1", "maintainer", true); err != nil {
		t.Fatal(err)
	}
	committed, err := service.Commit(ctx, "action-1", "key-1")
	if err != nil || committed.State != action.StateCommitted || committed.CommitID == "" {
		t.Fatalf("commit: %#v, %v", committed, err)
	}
	if _, err := service.Revise(ctx, "action-1", "shell", json.RawMessage(`{"command":"false"}`), false); err == nil {
		t.Fatal("repair after commit succeeded")
	}
	claim1, err := service.Dispatch(ctx, "action-1", "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	claim2, err := service.Reassign(ctx, claim1, "worker-2")
	if err != nil {
		t.Fatal(err)
	}
	headBefore, _ := store.Head(ctx, "session-1")
	if _, err := service.Result(ctx, claim1, true, json.RawMessage(`{"exit_code":0}`)); err == nil {
		t.Fatal("stale worker recorded a result")
	}
	headAfter, _ := store.Head(ctx, "session-1")
	if headAfter != headBefore {
		t.Fatalf("rejected result advanced head from %d to %d", headBefore, headAfter)
	}
	finished, err := service.Result(ctx, claim2, true, json.RawMessage(`{"exit_code":0}`))
	if err != nil || finished.State != action.StateSucceeded {
		t.Fatalf("result: %#v, %v", finished, err)
	}
}

func TestRejectedAndRetractedActionsCannotCommit(t *testing.T) {
	ctx := context.Background()
	store, service := actionStore(t)
	defer store.Close()
	if _, err := service.Intent(ctx, "session-1", "a-reject", "shell", json.RawMessage(`{"command":"pwd"}`), true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Validate(ctx, "a-reject"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RequestApproval(ctx, "a-reject"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Decide(ctx, "a-reject", "maintainer", false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Commit(ctx, "a-reject", "key"); err == nil {
		t.Fatal("rejected action committed")
	}
	if _, err := service.Intent(ctx, "session-1", "a-retract", "shell", json.RawMessage(`{"command":"pwd"}`), false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Retract(ctx, "a-retract"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Validate(ctx, "a-retract"); err == nil {
		t.Fatal("retracted action validated")
	}
}

func TestLifecycleSurvivesReopenAfterEveryTransition(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	open := func() (*eventlog.Store, action.Service) {
		store, err := eventlog.Open(ctx, path, session.Projector{}, action.Projector{})
		if err != nil {
			t.Fatal(err)
		}
		return store, action.Service{Log: store, Validator: action.CapabilitySet{"shell": nil}}
	}
	store, service := open()
	if _, err := (session.Service{Log: store}).Create(ctx, "session-1", "restart test"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	steps := []func(action.Service) error{
		func(s action.Service) error {
			_, err := s.Intent(ctx, "session-1", "action-1", "shell", json.RawMessage(`{"command":"pwd"}`), true)
			return err
		},
		func(s action.Service) error {
			_, err := s.Revise(ctx, "action-1", "shell", json.RawMessage(`{"command":"go test ./..."}`), true)
			return err
		},
		func(s action.Service) error { _, err := s.Validate(ctx, "action-1"); return err },
		func(s action.Service) error { _, err := s.RequestApproval(ctx, "action-1"); return err },
		func(s action.Service) error { _, err := s.Decide(ctx, "action-1", "maintainer", true); return err },
		func(s action.Service) error { _, err := s.Commit(ctx, "action-1", "restart-key"); return err },
		func(s action.Service) error { _, err := s.Dispatch(ctx, "action-1", "worker"); return err },
	}
	for i, step := range steps {
		store, service = open()
		if err := step(service); err != nil {
			store.Close()
			t.Fatalf("step %d: %v", i, err)
		}
		store.Close()
	}
	store, service = open()
	defer store.Close()
	current, err := service.Get(ctx, "action-1")
	if err != nil {
		t.Fatal(err)
	}
	claim := action.Claim{ActionID: current.ActionID, CommitID: current.CommitID, Owner: current.DispatchOwner, Fence: current.DispatchFence}
	finished, err := service.Result(ctx, claim, true, json.RawMessage(`{"exit_code":0}`))
	if err != nil || finished.State != action.StateSucceeded {
		t.Fatalf("finish after reopen: %#v, %v", finished, err)
	}
	if err := store.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := service.Get(ctx, "action-1")
	if err != nil || rebuilt.State != action.StateSucceeded || rebuilt.Version != 2 {
		t.Fatalf("rebuilt action: %#v, %v", rebuilt, err)
	}
}

func TestActionTimelineReplaysASequencedTurnCard(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	open := func() (*eventlog.Store, action.Service) {
		store, err := eventlog.Open(ctx, path, session.Projector{}, action.Projector{})
		if err != nil {
			t.Fatal(err)
		}
		return store, action.Service{Log: store, Validator: action.CapabilitySet{"shell": nil}}
	}
	store, service := open()
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-1", "timeline test"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.StartTurn(ctx, "session-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	intent, err := service.IntentInTurn(ctx, "session-1", "turn-1", "action-1", "shell", json.RawMessage(`{"command":"pwd"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Validate(ctx, "action-1"); err != nil {
		t.Fatal(err)
	}
	committed, err := service.Commit(ctx, "action-1", "timeline-key")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := service.Dispatch(ctx, committed.ActionID, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Result(ctx, claim, true, json.RawMessage(`{"exit_code":0}`)); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store, service = open()
	defer store.Close()
	window, err := service.RecentTimeline(ctx, "session-1", intent.LastSequence, 8)
	if err != nil {
		t.Fatal(err)
	}
	if window.Omitted != 0 || len(window.Items) != 1 {
		t.Fatalf("timeline window = %#v", window)
	}
	item := window.Items[0]
	if item.ActionID != "action-1" || item.TurnID != "turn-1" || item.Capability != "shell" || item.State != action.StateSucceeded || item.StartedSequence != intent.LastSequence || string(item.Result) != `{"exit_code":0}` || item.StartedAt.IsZero() || item.FinishedAt.Before(item.StartedAt) {
		t.Fatalf("timeline item = %#v", item)
	}
	if err := store.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := service.RecentTimeline(ctx, "session-1", intent.LastSequence, 8)
	if err != nil || len(replayed.Items) != 1 || replayed.Items[0].TurnID != "turn-1" || replayed.Items[0].State != action.StateSucceeded {
		t.Fatalf("replayed timeline = %#v, %v", replayed, err)
	}
}

func TestCancellationBlocksNewActionTransitions(t *testing.T) {
	ctx := context.Background()
	store, service := actionStore(t)
	defer store.Close()
	if _, err := service.Intent(ctx, "session-1", "action-1", "shell", json.RawMessage(`{"command":"pwd"}`), false); err != nil {
		t.Fatal(err)
	}
	if _, err := (session.Service{Log: store}).Cancel(ctx, "session-1", "user interrupt"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Validate(ctx, "action-1"); err == nil {
		t.Fatal("action advanced after cancellation")
	}
	if _, err := service.Intent(ctx, "session-1", "action-2", "shell", json.RawMessage(`{"command":"pwd"}`), false); err == nil {
		t.Fatal("new intent created after cancellation")
	}
}

func TestFailedActionRecordsCommittedCompensation(t *testing.T) {
	ctx := context.Background()
	store, service := actionStore(t)
	defer store.Close()
	commitAction := func(id, key string) action.Projection {
		if _, err := service.Intent(ctx, "session-1", id, "shell", json.RawMessage(`{"command":"pwd"}`), false); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Validate(ctx, id); err != nil {
			t.Fatal(err)
		}
		committed, err := service.Commit(ctx, id, key)
		if err != nil {
			t.Fatal(err)
		}
		return committed
	}
	failed := commitAction("failed", "failed-key")
	claim, err := service.Dispatch(ctx, failed.ActionID, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Result(ctx, claim, false, json.RawMessage(`{"error":"injected"}`)); err != nil {
		t.Fatal(err)
	}
	compensation := commitAction("compensation", "compensation-key")
	recorded, err := service.RecordCompensationCommit(ctx, failed.ActionID, compensation.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.State != action.StateCompensationCommitted || recorded.CompensationActionID != compensation.ActionID {
		t.Fatalf("compensation record = %#v", recorded)
	}
}

func TestIdempotencyKeyIsUniqueWithinSession(t *testing.T) {
	ctx := context.Background()
	store, service := actionStore(t)
	defer store.Close()
	for _, id := range []string{"first", "second"} {
		if _, err := service.Intent(ctx, "session-1", id, "shell", json.RawMessage(`{"command":"pwd"}`), false); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Validate(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Commit(ctx, "first", "same-key"); err != nil {
		t.Fatal(err)
	}
	head, _ := store.Head(ctx, "session-1")
	if _, err := service.Commit(ctx, "second", "same-key"); err == nil {
		t.Fatal("duplicate idempotency key committed")
	}
	if after, _ := store.Head(ctx, "session-1"); after != head {
		t.Fatalf("failed commit advanced head from %d to %d", head, after)
	}
}

func TestPendingSteerTransactionallyBlocksCommit(t *testing.T) {
	ctx := context.Background()
	store, service := actionStore(t)
	defer store.Close()
	sessions := session.Service{Log: store}
	if _, err := sessions.StartTurn(ctx, "session-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Intent(ctx, "session-1", "action-1", "shell", json.RawMessage(`{"command":"pwd"}`), false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Validate(ctx, "action-1"); err != nil {
		t.Fatal(err)
	}
	control, err := sessions.Steer(ctx, "session-1", "change direction")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Commit(ctx, "action-1", "key-1"); !errors.Is(err, action.ErrSteeringPending) {
		t.Fatalf("commit error = %v", err)
	}
	current, err := service.Get(ctx, "action-1")
	if err != nil || current.State != action.StateValidated {
		t.Fatalf("action after rejected commit = %#v, %v", current, err)
	}
	if _, err := sessions.AcknowledgeControl(ctx, "session-1", control.ControlID); err != nil {
		t.Fatal(err)
	}
	committed, err := service.Commit(ctx, "action-1", "key-1")
	if err != nil || committed.State != action.StateCommitted {
		t.Fatalf("commit after steer acknowledgement = %#v, %v", committed, err)
	}
}

func actionStore(t *testing.T) (*eventlog.Store, action.Service) {
	t.Helper()
	ctx := context.Background()
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), session.Projector{}, action.Projector{})
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.Service{Log: store}
	if _, err := sessions.Create(ctx, "session-1", "test objective"); err != nil {
		store.Close()
		t.Fatal(err)
	}
	service := action.Service{Log: store, Validator: action.CapabilitySet{
		"shell": func(raw json.RawMessage) error {
			var value struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(raw, &value); err != nil || value.Command == "" {
				return &validationError{}
			}
			return nil
		},
	}}
	return store, service
}

type validationError struct{}

func (*validationError) Error() string { return "command required" }
