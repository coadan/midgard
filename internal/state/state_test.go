package state

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestOpenMigratesIdempotently(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var count int
	if err := db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("migration count = %d, want 1", count)
	}
}

func TestRoundTripWorkbenchArtifactAndEvent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	wb := Workbench{ID: "wb_1", Root: "/tmp/workbench", ConfigPath: "/tmp/workbench/.midgard/workbench.toml"}
	if err := db.UpsertWorkbench(ctx, wb); err != nil {
		t.Fatal(err)
	}
	gotWB, err := db.Workbench(ctx, wb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(wb, gotWB); diff != "" {
		t.Fatalf("workbench mismatch (-want +got):\n%s", diff)
	}

	artifact := Artifact{
		ID:           "artifact_1",
		TaskID:       "task_1",
		Type:         "report",
		Path:         ".midgard/artifacts/task_1/plan.mdx",
		Checksum:     "sha256:test",
		ProducerRole: "planner",
		State:        "sealed",
	}
	if err := db.InsertArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	gotArtifact, err := db.Artifact(ctx, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(artifact, gotArtifact); diff != "" {
		t.Fatalf("artifact mismatch (-want +got):\n%s", diff)
	}

	event := Event{TaskID: "task_1", Type: "artifact", Payload: `{"id":"artifact_1"}`}
	eventID, err := db.InsertEvent(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	gotEvent, err := db.Event(ctx, eventID)
	if err != nil {
		t.Fatal(err)
	}
	event.ID = eventID
	if diff := cmp.Diff(event, gotEvent); diff != "" {
		t.Fatalf("event mismatch (-want +got):\n%s", diff)
	}

	usage := UsageRecord{
		ID:           "usage_1",
		TaskID:       "task_1",
		ProviderID:   "fake",
		ModelID:      "fake-model",
		Role:         "planner",
		InputTokens:  10,
		OutputTokens: 5,
		RawPayload:   `{"ok":true}`,
	}
	if err := db.InsertUsageRecord(ctx, usage); err != nil {
		t.Fatal(err)
	}
	gotUsage, err := db.UsageRecord(ctx, usage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(usage, gotUsage); diff != "" {
		t.Fatalf("usage mismatch (-want +got):\n%s", diff)
	}

	cost := CostRollup{ID: "cost_1", TaskID: "task_1", AmountUSD: "0.0012", Caveats: "none"}
	if err := db.InsertCostRollup(ctx, cost); err != nil {
		t.Fatal(err)
	}
	gotCost, err := db.CostRollup(ctx, cost.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(cost, gotCost); diff != "" {
		t.Fatalf("cost mismatch (-want +got):\n%s", diff)
	}
}
