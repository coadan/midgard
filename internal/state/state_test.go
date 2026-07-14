package state

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"midgard/migrations"
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

func TestMigration004UpgradesExistingAcceptanceChecks(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Conn().ExecContext(ctx, `
DROP TABLE benchmark_acceptance_checks;
DROP TABLE benchmark_acceptance_runs;
DELETE FROM schema_migrations WHERE version IN (3, 4);
`); err != nil {
		t.Fatal(err)
	}
	migration003, err := migrations.FS.ReadFile("003_benchmark_acceptance.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().ExecContext(ctx, string(migration003)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().ExecContext(ctx, `INSERT INTO schema_migrations (version, name) VALUES (3, '003_benchmark_acceptance.sql')`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Conn().ExecContext(ctx, `
INSERT INTO workbenches (id, root, config_path) VALUES ('wb_upgrade', '/tmp/upgrade', '/tmp/upgrade/workbench.toml');
INSERT INTO tasks (id, workbench_id, state, objective) VALUES ('task_upgrade', 'wb_upgrade', 'completed', 'upgrade test');
INSERT INTO benchmark_acceptance_runs (
  id, task_id, item_id, status, started_at, finished_at,
  patch_checksum, artifact_ref, artifact_checksum
) VALUES (
  'run_upgrade', 'task_upgrade', 'item_upgrade', 'passed',
  '2026-01-01T00:00:00Z', '2026-01-01T00:00:01Z',
  'sha256:patch', 'artifact:summary.json', 'sha256:summary'
);
INSERT INTO benchmark_acceptance_checks (
  id, run_id, check_id, repo_id, command, status, exit_code,
  started_at, finished_at
) VALUES (
  'check_upgrade', 'run_upgrade', 'go-test', 'repo_upgrade', 'go test ./...',
  'passed', 0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:01Z'
);
`); err != nil {
		t.Fatal(err)
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var expectedExitCode int
	if err := db.Conn().QueryRowContext(ctx, `
SELECT expected_exit_code
FROM benchmark_acceptance_checks
WHERE id = 'check_upgrade'
`).Scan(&expectedExitCode); err != nil {
		t.Fatal(err)
	}
	if expectedExitCode != 0 {
		t.Fatalf("expected_exit_code = %d, want 0", expectedExitCode)
	}

	var migrationCount int
	if err := db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 4`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 1 {
		t.Fatalf("migration 4 count = %d, want 1", migrationCount)
	}
}

func TestBenchmarkRunRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	run := BenchmarkRun{
		ID: "run_1", ManifestID: "manifest_1", ManifestChecksum: "sha256:manifest",
		ExecutionChecksum: "sha256:execution", ExecutionJSON: `{"model":"fake"}`,
		Status: "running", StartedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	}
	repos := []BenchmarkRunRepo{{RunID: run.ID, RepoID: "repo_1", CheckoutRef: "main", StartCommit: "abc123"}}
	items := []BenchmarkRunItem{
		{RunID: run.ID, ItemID: "item_1", Ordinal: 0, TaskID: "task_1", Phase: "pending", Status: "pending", UpdatedAt: run.UpdatedAt},
		{RunID: run.ID, ItemID: "item_2", Ordinal: 1, TaskID: "task_2", Phase: "pending", Status: "pending", UpdatedAt: run.UpdatedAt},
	}
	if err := db.InsertBenchmarkRun(ctx, run, repos, items); err != nil {
		t.Fatal(err)
	}
	gotRun, err := db.BenchmarkRunByManifest(ctx, run.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(run, gotRun); diff != "" {
		t.Fatalf("run mismatch (-want +got):\n%s", diff)
	}
	gotRepos, err := db.BenchmarkRunRepos(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(repos, gotRepos); diff != "" {
		t.Fatalf("repos mismatch (-want +got):\n%s", diff)
	}
	gotItems, err := db.BenchmarkRunItems(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(items, gotItems); diff != "" {
		t.Fatalf("items mismatch (-want +got):\n%s", diff)
	}

	items[0].Phase = "score"
	items[0].Status = "completed"
	items[0].Score = "pass"
	items[0].StartedAt = "2026-01-01T00:00:01Z"
	items[0].UpdatedAt = "2026-01-01T00:00:02Z"
	items[0].FinishedAt = "2026-01-01T00:00:02Z"
	if err := db.UpdateBenchmarkRunItem(ctx, items[0]); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBenchmarkRun(ctx, run.ID, "completed", items[0].UpdatedAt, items[0].FinishedAt); err != nil {
		t.Fatal(err)
	}
	gotRun, err = db.BenchmarkRunByManifest(ctx, run.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.Status != "completed" || gotRun.FinishedAt != items[0].FinishedAt {
		t.Fatalf("updated run = %#v", gotRun)
	}
	gotItems, err = db.BenchmarkRunItems(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(items[0], gotItems[0]); diff != "" {
		t.Fatalf("updated item mismatch (-want +got):\n%s", diff)
	}
	if err := db.DeleteBenchmarkRunByManifest(ctx, run.ManifestID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BenchmarkRunByManifest(ctx, run.ManifestID); !IsNoBenchmarkRun(err) {
		t.Fatalf("lookup after delete error = %v", err)
	}
	gotRepos, err = db.BenchmarkRunRepos(ctx, run.ID)
	if err != nil || len(gotRepos) != 0 {
		t.Fatalf("repos after cascade = %#v, %v", gotRepos, err)
	}
	gotItems, err = db.BenchmarkRunItems(ctx, run.ID)
	if err != nil || len(gotItems) != 0 {
		t.Fatalf("items after cascade = %#v, %v", gotItems, err)
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
