package benchmark

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"midgard/internal/cost"
	"midgard/internal/gitrepo"
	"midgard/internal/model"
	"midgard/internal/state"
	"midgard/internal/stream"
	midgardtask "midgard/internal/task"
	"midgard/internal/workbench"
)

type suiteItemExecution struct {
	Providers midgardtask.RoleProviders
	ModelID   string
	Pricing   cost.Pricing
}

type suiteExecutionIdentity struct {
	ProtocolVersion      string                       `json:"protocol_version"`
	ProtocolFingerprint  string                       `json:"protocol_fingerprint"`
	ProviderOptions      string                       `json:"provider_options,omitempty"`
	AcceptanceSchema     int                          `json:"acceptance_schema"`
	Budget               stream.Budget                `json:"budget"`
	MaxReviewCycles      int                          `json:"max_review_cycles"`
	MaxSourceEditRepairs int                          `json:"max_source_edit_repairs"`
	Acceptance           suiteAcceptanceIdentity      `json:"acceptance"`
	Items                []suiteItemExecutionIdentity `json:"items"`
}

type suiteAcceptanceIdentity struct {
	DefaultTimeoutNanos int64 `json:"default_timeout_nanos"`
	MaxStdoutBytes      int64 `json:"max_stdout_bytes"`
	MaxStderrBytes      int64 `json:"max_stderr_bytes"`
}

type suiteItemExecutionIdentity struct {
	ItemID  string                       `json:"item_id"`
	ModelID string                       `json:"model_id"`
	Pricing cost.Pricing                 `json:"pricing"`
	Roles   []suiteRoleExecutionIdentity `json:"roles"`
}

type suiteRoleExecutionIdentity struct {
	Role                string `json:"role"`
	ProviderID          string `json:"provider_id"`
	ProviderFingerprint string `json:"provider_fingerprint"`
}

type manifestReferenceIdentity struct {
	ItemID   string `json:"item_id"`
	Path     string `json:"path"`
	Checksum string `json:"checksum"`
}

type manifestIdentity struct {
	Manifest         Manifest                    `json:"manifest"`
	ReferencePatches []manifestReferenceIdentity `json:"reference_patches,omitempty"`
}

func prepareSuiteExecutions(manifest Manifest, opts SuiteOptions) (map[string]suiteItemExecution, string, string, error) {
	executions := make(map[string]suiteItemExecution, len(manifest.Items))
	identity := suiteExecutionIdentity{
		ProtocolVersion: model.ProtocolVersion, ProtocolFingerprint: model.ProtocolFingerprint(),
		ProviderOptions:  opts.ProviderOptions,
		AcceptanceSchema: acceptanceSchemaVersion, Budget: opts.Budget,
		MaxReviewCycles: opts.MaxReviewCycles, MaxSourceEditRepairs: opts.MaxSourceEditRepairs,
		Acceptance: suiteAcceptanceIdentity{
			DefaultTimeoutNanos: int64(opts.Acceptance.DefaultTimeout),
			MaxStdoutBytes:      opts.Acceptance.MaxStdoutBytes, MaxStderrBytes: opts.Acceptance.MaxStderrBytes,
		},
	}
	roles := []model.Role{model.RolePlanner, model.RoleImplementer, model.RoleReviewer}
	for _, item := range manifest.Items {
		providers, modelID, pricing, err := opts.ProviderFactory(item)
		if err != nil {
			return nil, "", "", err
		}
		if err := validateLoopProviders(providers); err != nil {
			return nil, "", "", err
		}
		executions[item.ID] = suiteItemExecution{Providers: providers, ModelID: modelID, Pricing: pricing}
		itemIdentity := suiteItemExecutionIdentity{ItemID: item.ID, ModelID: modelID, Pricing: pricing}
		for _, role := range roles {
			provider := providers[role]
			itemIdentity.Roles = append(itemIdentity.Roles, suiteRoleExecutionIdentity{
				Role: role.String(), ProviderID: provider.ID(),
				ProviderFingerprint: model.ProviderFingerprint(provider, modelID),
			})
		}
		identity.Items = append(identity.Items, itemIdentity)
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return nil, "", "", err
	}
	return executions, checksumBytes(data), string(data), nil
}

func benchmarkManifestChecksum(manifest Manifest) (string, error) {
	identity := manifestIdentity{Manifest: manifest}
	identity.Manifest.BaseDir = ""
	for i := range identity.Manifest.Items {
		identity.Manifest.Items[i].ManifestBaseDir = ""
	}
	for _, item := range manifest.Items {
		if item.HiddenReferencePatch == "" {
			continue
		}
		path := item.HiddenReferencePatch
		if !filepath.IsAbs(path) {
			path = filepath.Join(item.ManifestBaseDir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("hidden reference patch for item %s: %w", item.ID, err)
		}
		identity.ReferencePatches = append(identity.ReferencePatches, manifestReferenceIdentity{
			ItemID: item.ID, Path: item.HiddenReferencePatch, Checksum: checksumBytes(data),
		})
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	return checksumBytes(data), nil
}

func benchmarkRunID(manifestChecksum, executionChecksum string, repos []PreparedRepo) string {
	sorted := append([]PreparedRepo(nil), repos...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	hash := sha256.New()
	_, _ = hash.Write([]byte(manifestChecksum + "\x00" + executionChecksum + "\x00"))
	for _, repo := range sorted {
		_, _ = hash.Write([]byte(repo.ID + "\x00" + repo.CheckoutRef + "\x00" + repo.StartCommit + "\x00"))
	}
	return "benchmark_run_" + hex.EncodeToString(hash.Sum(nil))[:20]
}

func newBenchmarkRun(manifest Manifest, manifestChecksum, executionChecksum, executionJSON string, repos []PreparedRepo) (state.BenchmarkRun, []state.BenchmarkRunRepo, []state.BenchmarkRunItem) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	run := state.BenchmarkRun{
		ID: benchmarkRunID(manifestChecksum, executionChecksum, repos), ManifestID: manifest.ID,
		ManifestChecksum: manifestChecksum, ExecutionChecksum: executionChecksum, ExecutionJSON: executionJSON,
		Status: "running", StartedAt: now, UpdatedAt: now,
	}
	stateRepos := make([]state.BenchmarkRunRepo, 0, len(repos))
	for _, repo := range repos {
		stateRepos = append(stateRepos, state.BenchmarkRunRepo{
			RunID: run.ID, RepoID: repo.ID, CheckoutRef: repo.CheckoutRef, StartCommit: repo.StartCommit,
		})
	}
	stateItems := make([]state.BenchmarkRunItem, 0, len(manifest.Items))
	for ordinal, item := range manifest.Items {
		stateItems = append(stateItems, state.BenchmarkRunItem{
			RunID: run.ID, ItemID: item.ID, Ordinal: ordinal, TaskID: item.TaskID,
			Phase: "pending", Status: "pending", UpdatedAt: now,
		})
	}
	return run, stateRepos, stateItems
}

func validateBenchmarkRun(run state.BenchmarkRun, repos []state.BenchmarkRunRepo, items []state.BenchmarkRunItem, manifest Manifest, manifestChecksum, executionChecksum string, prepared []PreparedRepo) error {
	if run.ManifestChecksum != manifestChecksum {
		return fmt.Errorf("benchmark manifest drift for %q; use --reset to start a new run", manifest.ID)
	}
	if run.ExecutionChecksum != executionChecksum {
		return fmt.Errorf("benchmark provider/model/options drift for %q; use --reset to start a new run", manifest.ID)
	}
	if len(repos) != len(prepared) {
		return fmt.Errorf("benchmark repository set drift for %q; use --reset to start a new run", manifest.ID)
	}
	preparedByID := make(map[string]PreparedRepo, len(prepared))
	for _, repo := range prepared {
		preparedByID[repo.ID] = repo
	}
	for _, recorded := range repos {
		current, ok := preparedByID[recorded.RepoID]
		if !ok || current.CheckoutRef != recorded.CheckoutRef || current.StartCommit != recorded.StartCommit {
			return fmt.Errorf("benchmark base commit drift for repo %q; use --reset to start a new run", recorded.RepoID)
		}
	}
	if len(items) != len(manifest.Items) {
		return fmt.Errorf("benchmark item set drift for %q; use --reset to start a new run", manifest.ID)
	}
	for ordinal, item := range manifest.Items {
		recorded := items[ordinal]
		if recorded.Ordinal != ordinal || recorded.ItemID != item.ID || recorded.TaskID != item.TaskID {
			return fmt.Errorf("benchmark item order or task id drift at item %q; use --reset to start a new run", item.ID)
		}
	}
	return nil
}

func markBenchmarkRun(ctx context.Context, db *state.DB, runID, status string, finished bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	finishedAt := ""
	if finished {
		finishedAt = now
	}
	return db.UpdateBenchmarkRun(ctx, runID, status, now, finishedAt)
}

func updateBenchmarkItem(ctx context.Context, db *state.DB, item state.BenchmarkRunItem, phase, status, errorClass, runError, score string, finished bool) (state.BenchmarkRunItem, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	item.Phase = phase
	item.Status = status
	item.ErrorClass = errorClass
	item.Error = runError
	item.Score = score
	item.StartedAt = now
	item.UpdatedAt = now
	if finished {
		item.FinishedAt = now
	} else {
		item.FinishedAt = ""
	}
	return item, db.UpdateBenchmarkRunItem(ctx, item)
}

func checksumBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalizeSuiteManifest(manifest *Manifest, root string) {
	manifest.BaseDir = baseDirOrRoot(manifest.BaseDir, root)
	for i := range manifest.Items {
		manifest.Items[i].ManifestBaseDir = manifest.BaseDir
		if manifest.Items[i].TaskID == "" {
			manifest.Items[i].TaskID = benchmarkTaskID(manifest.ID, manifest.Items[i].ID)
		}
		if len(manifest.Items[i].RepoIDs) == 0 {
			manifest.Items[i].RepoIDs = repoIDs(manifest.Repos)
		}
	}
}

func resetBenchmarkSuite(ctx context.Context, root string, db *state.DB, manifest Manifest) error {
	taskIDs := map[string]bool{}
	for _, item := range manifest.Items {
		taskIDs[item.TaskID] = true
	}
	if run, err := db.BenchmarkRunByManifest(ctx, manifest.ID); err == nil {
		items, err := db.BenchmarkRunItems(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, item := range items {
			taskIDs[item.TaskID] = true
		}
	} else if !state.IsNoBenchmarkRun(err) {
		return err
	}
	ids := make([]string, 0, len(taskIDs))
	for taskID := range taskIDs {
		ids = append(ids, taskID)
	}
	sort.Strings(ids)
	for _, taskID := range ids {
		execution, err := midgardtask.AcquireExecution(ctx, root, taskID)
		if err != nil {
			return err
		}
		resetErr := resetTaskIfRequested(execution.Context, root, taskID, true)
		closeErr := execution.Close()
		if resetErr != nil || closeErr != nil {
			return errors.Join(resetErr, closeErr)
		}
		if err := checkBenchmarkExecution(ctx); err != nil {
			return err
		}
	}
	return db.DeleteBenchmarkRunByManifest(ctx, manifest.ID)
}

func ensureNoLegacyBenchmarkTasks(ctx context.Context, db *state.DB, manifest Manifest) error {
	for _, item := range manifest.Items {
		if _, err := db.Task(ctx, item.TaskID); err == nil {
			return fmt.Errorf("benchmark task %q exists without durable run identity; use --reset to start a new run", item.TaskID)
		} else if err != sql.ErrNoRows {
			return err
		}
	}
	return nil
}

func ensureBenchmarkTask(ctx context.Context, root string, db *state.DB, item Item, runItem state.BenchmarkRunItem, prepared []PreparedRepo) (bool, error) {
	taskRow, err := db.Task(ctx, item.TaskID)
	if err == sql.ErrNoRows {
		if runItem.Status == "completed" || runItem.Phase == "acceptance" || runItem.Phase == "score" {
			return false, fmt.Errorf("benchmark task %q is missing after phase %q; use --reset to start a new run", item.TaskID, runItem.Phase)
		}
		_, err := midgardtask.Create(ctx, root, midgardtask.CreateOptions{ID: item.TaskID, Objective: item.Objective, RepoIDs: item.RepoIDs})
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	if taskRow.Objective != item.Objective {
		return false, fmt.Errorf("benchmark task %q objective drift; use --reset to start a new run", item.TaskID)
	}
	if err := validateBenchmarkTaskWorktrees(ctx, db, item, prepared); err != nil {
		hasEvidence, evidenceErr := benchmarkTaskHasExecutionEvidence(ctx, db, item.TaskID)
		if evidenceErr != nil {
			return false, evidenceErr
		}
		if hasEvidence {
			return false, err
		}
		if resetErr := resetTaskIfRequested(ctx, root, item.TaskID, true); resetErr != nil {
			return false, resetErr
		}
		_, createErr := midgardtask.Create(ctx, root, midgardtask.CreateOptions{ID: item.TaskID, Objective: item.Objective, RepoIDs: item.RepoIDs})
		return createErr == nil, createErr
	}
	return false, nil
}

func validateBenchmarkTaskWorktrees(ctx context.Context, db *state.DB, item Item, prepared []PreparedRepo) error {
	worktrees, err := db.WorktreesForTask(ctx, item.TaskID)
	if err != nil {
		return err
	}
	if len(worktrees) != len(item.RepoIDs) {
		return fmt.Errorf("benchmark task %q worktree set is incomplete; use --reset to start a new run", item.TaskID)
	}
	preparedByID := make(map[string]PreparedRepo, len(prepared))
	for _, repo := range prepared {
		preparedByID[repo.ID] = repo
	}
	expected := make(map[string]bool, len(item.RepoIDs))
	for _, repoID := range item.RepoIDs {
		expected[repoID] = true
	}
	for _, wt := range worktrees {
		repo, ok := preparedByID[wt.RepoID]
		if !expected[wt.RepoID] || !ok || wt.StartCommit != repo.StartCommit {
			return fmt.Errorf("benchmark task %q worktree base drift for repo %q; use --reset to start a new run", item.TaskID, wt.RepoID)
		}
		if _, err := os.Stat(wt.Path); err != nil {
			return fmt.Errorf("benchmark task %q worktree for repo %q is missing: %w", item.TaskID, wt.RepoID, err)
		}
		head, err := gitrepo.CurrentCommit(ctx, wt.Path)
		if err != nil || head != wt.StartCommit {
			return fmt.Errorf("benchmark task %q worktree HEAD drift for repo %q; use --reset to start a new run", item.TaskID, wt.RepoID)
		}
		delete(expected, wt.RepoID)
	}
	if len(expected) != 0 {
		return fmt.Errorf("benchmark task %q worktree set is incomplete; use --reset to start a new run", item.TaskID)
	}
	return nil
}

func benchmarkTaskHasExecutionEvidence(ctx context.Context, db *state.DB, taskID string) (bool, error) {
	var count int
	err := db.Conn().QueryRowContext(ctx, `
SELECT (SELECT COUNT(*) FROM events WHERE task_id = ?) +
       (SELECT COUNT(*) FROM usage_records WHERE task_id = ?)`, taskID, taskID).Scan(&count)
	return count > 0, err
}

func currentAcceptanceVerification(ctx context.Context, db *state.DB, layout workbench.Layout, item Item) (acceptanceVerification, error) {
	artifactRoot := filepath.Join(layout.Artifacts, item.TaskID)
	return verifyAcceptanceEvidence(ctx, db, artifactRoot, item)
}

func clearSuiteItemError(ctx context.Context, root string, item Item) error {
	layout := workbench.NewLayout(root)
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return err
	}
	defer db.Close()
	latest, err := latestBenchmarkItemError(ctx, db, item)
	if err != nil || latest.Message == "" {
		return err
	}
	payload, err := json.Marshal(map[string]string{"item_id": item.ID})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: item.TaskID, Type: "benchmark.item.error_cleared", Payload: string(payload)})
	return err
}
