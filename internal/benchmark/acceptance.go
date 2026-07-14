package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"midgard/internal/artifact"
	"midgard/internal/command"
	"midgard/internal/gitrepo"
	"midgard/internal/policy"
	"midgard/internal/state"
	midgardtask "midgard/internal/task"
	"midgard/internal/workbench"
)

const acceptanceSchemaVersion = 1

func hasAcceptanceChecks(item Item) bool {
	return len(item.Checks) > 0 || len(item.AcceptanceChecks) > 0
}

type AcceptanceOptions struct {
	DefaultTimeout time.Duration
	MaxStdoutBytes int64
	MaxStderrBytes int64
}

type AcceptanceRunEvidence struct {
	SchemaVersion    int                          `json:"schema_version"`
	RunID            string                       `json:"run_id"`
	TaskID           string                       `json:"task_id"`
	ItemID           string                       `json:"item_id"`
	Status           string                       `json:"status"`
	StartedAt        string                       `json:"started_at"`
	FinishedAt       string                       `json:"finished_at"`
	PatchChecksum    string                       `json:"patch_checksum"`
	Worktrees        []AcceptanceWorktreeEvidence `json:"worktrees"`
	Checks           []AcceptanceCheckEvidence    `json:"checks"`
	ArtifactRef      string                       `json:"-"`
	ArtifactChecksum string                       `json:"-"`
}

type AcceptanceWorktreeEvidence struct {
	RepoID      string `json:"repo_id"`
	HeadSHA     string `json:"head_sha"`
	Fingerprint string `json:"fingerprint"`
}

type AcceptanceCheckEvidence struct {
	ID                string `json:"id"`
	RepoID            string `json:"repo_id"`
	Command           string `json:"command"`
	CWD               string `json:"cwd,omitempty"`
	Status            string `json:"status"`
	ExpectedExitCode  int    `json:"expected_exit_code"`
	ExitCode          int    `json:"exit_code"`
	TimedOut          bool   `json:"timed_out"`
	StdoutTruncated   bool   `json:"stdout_truncated"`
	StderrTruncated   bool   `json:"stderr_truncated"`
	Error             string `json:"error,omitempty"`
	StartedAt         string `json:"started_at"`
	FinishedAt        string `json:"finished_at"`
	ResultArtifactRef string `json:"result_artifact_ref,omitempty"`
	ResultChecksum    string `json:"result_checksum,omitempty"`
	StdoutArtifactRef string `json:"stdout_artifact_ref,omitempty"`
	StdoutChecksum    string `json:"stdout_checksum,omitempty"`
	StderrArtifactRef string `json:"stderr_artifact_ref,omitempty"`
	StderrChecksum    string `json:"stderr_checksum,omitempty"`
}

type acceptanceVerification struct {
	Required bool
	Valid    bool
	Passed   bool
	Status   string
	Reason   string
	Path     string
	Checksum string
	Checks   []AcceptanceCheckScore
}

func RunAcceptanceChecks(ctx context.Context, root string, item Item, opts AcceptanceOptions) (result AcceptanceRunEvidence, retErr error) {
	execution, err := midgardtask.AcquireExecution(ctx, root, item.TaskID)
	if err != nil {
		return AcceptanceRunEvidence{}, err
	}
	defer func() {
		if err := execution.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	return runAcceptanceChecks(execution.Context, root, item, opts)
}

func runAcceptanceChecks(ctx context.Context, root string, item Item, opts AcceptanceOptions) (AcceptanceRunEvidence, error) {
	checks, err := normalizeAcceptanceChecks(item)
	if err != nil {
		return AcceptanceRunEvidence{}, err
	}
	if len(checks) == 0 {
		return AcceptanceRunEvidence{}, fmt.Errorf("benchmark item %q has no acceptance checks", item.ID)
	}
	status, err := workbench.Status(root)
	if err != nil {
		return AcceptanceRunEvidence{}, err
	}
	layout := workbench.NewLayout(status.Root)
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return AcceptanceRunEvidence{}, err
	}
	defer db.Close()
	taskRow, err := db.Task(ctx, item.TaskID)
	if err != nil {
		return AcceptanceRunEvidence{}, fmt.Errorf("benchmark task %q: %w", item.TaskID, err)
	}
	_ = taskRow
	worktrees, err := db.WorktreesForTask(ctx, item.TaskID)
	if err != nil {
		return AcceptanceRunEvidence{}, err
	}
	worktreeByRepo := make(map[string]state.Worktree, len(worktrees))
	worktreeEvidence := make([]AcceptanceWorktreeEvidence, 0, len(worktrees))
	for _, wt := range worktrees {
		worktreeByRepo[wt.RepoID] = wt
		head, fingerprint, err := acceptanceWorktreeFingerprint(ctx, wt)
		if err != nil {
			return AcceptanceRunEvidence{}, fmt.Errorf("fingerprint repo %s: %w", wt.RepoID, err)
		}
		worktreeEvidence = append(worktreeEvidence, AcceptanceWorktreeEvidence{RepoID: wt.RepoID, HeadSHA: head, Fingerprint: fingerprint})
	}
	slices.SortFunc(worktreeEvidence, func(a, b AcceptanceWorktreeEvidence) int { return strings.Compare(a.RepoID, b.RepoID) })
	checks, err = resolveAcceptanceCheckRepos(checks, item.RepoIDs, worktreeByRepo)
	if err != nil {
		return AcceptanceRunEvidence{}, err
	}
	patchPath := filepath.Join(layout.Artifacts, item.TaskID, "patch.diff")
	patchChecksum, err := fileChecksum(patchPath)
	if err != nil {
		return AcceptanceRunEvidence{}, fmt.Errorf("benchmark patch artifact: %w", err)
	}
	startedAt := time.Now().UTC()
	runID := fmt.Sprintf("accept_%s_%d", safeBenchmarkID(item.ID), startedAt.UnixNano())
	runPrefix := filepath.ToSlash(filepath.Join("benchmark", "acceptance", safeBenchmarkID(item.ID), runID))
	artifactDir := filepath.Join(layout.Artifacts, item.TaskID)
	evidence := AcceptanceRunEvidence{
		SchemaVersion: acceptanceSchemaVersion,
		RunID:         runID, TaskID: item.TaskID, ItemID: item.ID, Status: "passed",
		StartedAt: startedAt.Format(time.RFC3339Nano), PatchChecksum: patchChecksum,
		Worktrees: worktreeEvidence,
	}
	stateChecks := make([]state.BenchmarkAcceptanceCheck, 0, len(checks))
	for _, check := range checks {
		if err := midgardtask.CheckExecution(ctx); err != nil {
			return AcceptanceRunEvidence{}, err
		}
		result := runAcceptanceCheck(ctx, artifactDir, runPrefix, item.TaskID, check, worktreeByRepo[check.RepoID], opts)
		if err := midgardtask.CheckExecution(ctx); err != nil {
			return AcceptanceRunEvidence{}, err
		}
		if ctx.Err() != nil {
			return AcceptanceRunEvidence{}, ctx.Err()
		}
		if result.Status == "error" {
			evidence.Status = "error"
		} else if result.Status == "failed" && evidence.Status == "passed" {
			evidence.Status = "failed"
		}
		if err := persistAcceptanceCommandArtifacts(ctx, db, item.TaskID, result); err != nil {
			return AcceptanceRunEvidence{}, err
		}
		evidence.Checks = append(evidence.Checks, result)
		stateChecks = append(stateChecks, state.BenchmarkAcceptanceCheck{
			ID: safeBenchmarkID(runID + "_" + result.ID), RunID: runID, CheckID: result.ID,
			RepoID: result.RepoID, Command: result.Command, CWD: result.CWD, Status: result.Status,
			ExpectedExitCode: result.ExpectedExitCode, ExitCode: result.ExitCode, TimedOut: result.TimedOut, Error: result.Error,
			StdoutTruncated: result.StdoutTruncated, StderrTruncated: result.StderrTruncated,
			ResultArtifactRef: result.ResultArtifactRef, StdoutArtifactRef: result.StdoutArtifactRef,
			StderrArtifactRef: result.StderrArtifactRef, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt,
		})
	}
	for _, before := range worktreeEvidence {
		wt := worktreeByRepo[before.RepoID]
		head, fingerprint, err := acceptanceWorktreeFingerprint(ctx, wt)
		if err != nil {
			return AcceptanceRunEvidence{}, err
		}
		if head != before.HeadSHA || fingerprint != before.Fingerprint {
			return AcceptanceRunEvidence{}, fmt.Errorf("acceptance checks changed canonical worktree for repo %q", before.RepoID)
		}
	}
	evidence.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := midgardtask.CheckExecution(ctx); err != nil {
		return AcceptanceRunEvidence{}, err
	}
	summaryData, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return AcceptanceRunEvidence{}, err
	}
	store := artifact.NewStore(artifactDir)
	summaryRec, err := store.Put(artifact.Record{
		Path: filepath.ToSlash(filepath.Join(runPrefix, "summary.json")), Type: artifact.TypePayload,
		State: artifact.StateSealed, PayloadType: "json",
	}, append(summaryData, '\n'))
	if err != nil {
		return AcceptanceRunEvidence{}, err
	}
	if err := persistAcceptanceArtifact(ctx, db, item.TaskID, summaryRec); err != nil {
		return AcceptanceRunEvidence{}, err
	}
	evidence.ArtifactRef = summaryRec.Ref()
	evidence.ArtifactChecksum = summaryRec.Checksum
	if err := db.InsertBenchmarkAcceptanceRun(ctx, state.BenchmarkAcceptanceRun{
		ID: runID, TaskID: item.TaskID, ItemID: item.ID, Status: evidence.Status,
		StartedAt: evidence.StartedAt, FinishedAt: evidence.FinishedAt, PatchChecksum: evidence.PatchChecksum,
		ArtifactRef: evidence.ArtifactRef, ArtifactChecksum: evidence.ArtifactChecksum,
	}, stateChecks); err != nil {
		return AcceptanceRunEvidence{}, err
	}
	payload, _ := json.Marshal(map[string]any{
		"run_id": runID, "item_id": item.ID, "status": evidence.Status,
		"artifact_ref": evidence.ArtifactRef, "artifact_checksum": evidence.ArtifactChecksum,
	})
	if _, err := db.InsertEvent(ctx, state.Event{TaskID: item.TaskID, Type: "benchmark.acceptance.completed", Payload: string(payload)}); err != nil {
		return AcceptanceRunEvidence{}, err
	}
	return evidence, nil
}

func verifyAcceptanceEvidence(ctx context.Context, db *state.DB, artifactRoot string, item Item) (acceptanceVerification, error) {
	verification := acceptanceVerification{Required: hasAcceptanceChecks(item), Status: "missing"}
	if !verification.Required {
		return verification, nil
	}
	checks, err := normalizeAcceptanceChecks(item)
	if err != nil {
		verification.Reason = err.Error()
		return verification, nil
	}
	worktrees, err := db.WorktreesForTask(ctx, item.TaskID)
	if err != nil {
		return verification, err
	}
	worktreeByRepo := make(map[string]state.Worktree, len(worktrees))
	for _, wt := range worktrees {
		worktreeByRepo[wt.RepoID] = wt
	}
	checks, err = resolveAcceptanceCheckRepos(checks, item.RepoIDs, worktreeByRepo)
	if err != nil {
		verification.Reason = err.Error()
		return verification, nil
	}
	run, err := db.LatestBenchmarkAcceptanceRun(ctx, item.TaskID, item.ID)
	if state.IsNoBenchmarkAcceptanceRun(err) {
		verification.Reason = "acceptance evidence is missing"
		return verification, nil
	}
	if err != nil {
		return verification, err
	}
	verification.Status = run.Status
	verification.Path = strings.TrimPrefix(run.ArtifactRef, "artifact:")
	verification.Checksum = run.ArtifactChecksum
	if err := verifyAcceptanceArtifact(ctx, db, artifactRoot, item.TaskID, run.ArtifactRef, run.ArtifactChecksum); err != nil {
		verification.Reason = "acceptance summary: " + err.Error()
		return verification, nil
	}
	summaryData, err := os.ReadFile(filepath.Join(artifactRoot, filepath.FromSlash(verification.Path)))
	if err != nil {
		verification.Reason = "acceptance summary: " + err.Error()
		return verification, nil
	}
	var summary AcceptanceRunEvidence
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		verification.Reason = "acceptance summary JSON is invalid"
		return verification, nil
	}
	if summary.SchemaVersion != acceptanceSchemaVersion || summary.RunID != run.ID ||
		summary.TaskID != item.TaskID || summary.ItemID != item.ID || summary.Status != run.Status ||
		summary.StartedAt != run.StartedAt || summary.FinishedAt != run.FinishedAt ||
		summary.PatchChecksum != run.PatchChecksum {
		verification.Reason = "acceptance summary does not match canonical run metadata"
		return verification, nil
	}
	patchChecksum, err := fileChecksum(filepath.Join(artifactRoot, "patch.diff"))
	if err != nil || patchChecksum != run.PatchChecksum {
		verification.Reason = "evaluated patch artifact changed after acceptance"
		return verification, nil
	}
	if len(summary.Worktrees) != len(worktrees) {
		verification.Reason = "acceptance worktree evidence is incomplete"
		return verification, nil
	}
	summaryWorktrees := make(map[string]AcceptanceWorktreeEvidence, len(summary.Worktrees))
	for _, recorded := range summary.Worktrees {
		if _, duplicate := summaryWorktrees[recorded.RepoID]; duplicate {
			verification.Reason = "acceptance worktree evidence has duplicate repos"
			return verification, nil
		}
		summaryWorktrees[recorded.RepoID] = recorded
	}
	for _, wt := range worktrees {
		recorded, ok := summaryWorktrees[wt.RepoID]
		if !ok {
			verification.Reason = "acceptance worktree evidence is incomplete"
			return verification, nil
		}
		head, fingerprint, err := acceptanceWorktreeFingerprint(ctx, wt)
		if err != nil {
			return verification, err
		}
		if recorded.HeadSHA != head || recorded.Fingerprint != fingerprint {
			verification.Reason = fmt.Sprintf("repo %s changed after acceptance", wt.RepoID)
			return verification, nil
		}
	}
	rows, err := db.BenchmarkAcceptanceChecks(ctx, run.ID)
	if err != nil {
		return verification, err
	}
	if len(rows) != len(checks) || len(summary.Checks) != len(checks) {
		verification.Reason = "acceptance check evidence is incomplete"
		return verification, nil
	}
	rowByID := make(map[string]state.BenchmarkAcceptanceCheck, len(rows))
	for _, row := range rows {
		if _, duplicate := rowByID[row.CheckID]; duplicate {
			verification.Reason = "acceptance check state has duplicate ids"
			return verification, nil
		}
		rowByID[row.CheckID] = row
	}
	summaryByID := make(map[string]AcceptanceCheckEvidence, len(summary.Checks))
	for _, recorded := range summary.Checks {
		if _, duplicate := summaryByID[recorded.ID]; duplicate {
			verification.Reason = "acceptance summary has duplicate check ids"
			return verification, nil
		}
		summaryByID[recorded.ID] = recorded
	}
	computedStatus := "passed"
	for _, spec := range checks {
		recorded, ok := summaryByID[spec.ID]
		row, rowOK := rowByID[spec.ID]
		if !ok || !rowOK {
			verification.Reason = fmt.Sprintf("acceptance check %s evidence is missing", spec.ID)
			return verification, nil
		}
		if recorded.RepoID != spec.RepoID || recorded.Command != spec.Command || recorded.CWD != spec.CWD || recorded.ExpectedExitCode != spec.ExpectedExitCode ||
			row.RepoID != recorded.RepoID || row.Command != recorded.Command || row.CWD != recorded.CWD ||
			row.Status != recorded.Status || row.ExpectedExitCode != recorded.ExpectedExitCode || row.ExitCode != recorded.ExitCode || row.TimedOut != recorded.TimedOut ||
			row.StdoutTruncated != recorded.StdoutTruncated || row.StderrTruncated != recorded.StderrTruncated ||
			row.Error != recorded.Error || row.ResultArtifactRef != recorded.ResultArtifactRef ||
			row.StdoutArtifactRef != recorded.StdoutArtifactRef || row.StderrArtifactRef != recorded.StderrArtifactRef ||
			row.StartedAt != recorded.StartedAt || row.FinishedAt != recorded.FinishedAt {
			verification.Reason = fmt.Sprintf("acceptance check %s does not match its manifest or canonical state", spec.ID)
			return verification, nil
		}
		if recorded.Status != "passed" && recorded.Status != "failed" && recorded.Status != "error" {
			verification.Reason = fmt.Sprintf("acceptance check %s has invalid status", spec.ID)
			return verification, nil
		}
		if recorded.Status == "error" {
			computedStatus = "error"
		} else if recorded.Status == "failed" && computedStatus == "passed" {
			computedStatus = "failed"
		}
		if recorded.Status != "error" {
			if err := verifyAcceptanceCommandResult(ctx, db, artifactRoot, item.TaskID, recorded); err != nil {
				verification.Reason = fmt.Sprintf("acceptance check %s: %v", spec.ID, err)
				return verification, nil
			}
		}
		verification.Checks = append(verification.Checks, AcceptanceCheckScore{
			ID: recorded.ID, RepoID: recorded.RepoID, Status: recorded.Status,
			ExpectedExitCode: recorded.ExpectedExitCode, ExitCode: recorded.ExitCode, TimedOut: recorded.TimedOut,
			StdoutTruncated: recorded.StdoutTruncated, StderrTruncated: recorded.StderrTruncated,
			ResultPath: strings.TrimPrefix(recorded.ResultArtifactRef, "artifact:"),
			StdoutPath: strings.TrimPrefix(recorded.StdoutArtifactRef, "artifact:"),
			StderrPath: strings.TrimPrefix(recorded.StderrArtifactRef, "artifact:"),
		})
	}
	if computedStatus != summary.Status || computedStatus != run.Status {
		verification.Reason = "acceptance status does not match check results"
		return verification, nil
	}
	if computedStatus == "error" {
		verification.Reason = "acceptance execution encountered a harness or policy error"
		return verification, nil
	}
	verification.Valid = true
	verification.Passed = computedStatus == "passed"
	return verification, nil
}

func verifyAcceptanceCommandResult(ctx context.Context, db *state.DB, artifactRoot, taskID string, recorded AcceptanceCheckEvidence) error {
	for _, entry := range []struct {
		label    string
		ref      string
		checksum string
	}{
		{"result", recorded.ResultArtifactRef, recorded.ResultChecksum},
		{"stdout", recorded.StdoutArtifactRef, recorded.StdoutChecksum},
		{"stderr", recorded.StderrArtifactRef, recorded.StderrChecksum},
	} {
		if err := verifyAcceptanceArtifact(ctx, db, artifactRoot, taskID, entry.ref, entry.checksum); err != nil {
			return fmt.Errorf("%s artifact: %w", entry.label, err)
		}
	}
	resultPath := strings.TrimPrefix(recorded.ResultArtifactRef, "artifact:")
	data, err := os.ReadFile(filepath.Join(artifactRoot, filepath.FromSlash(resultPath)))
	if err != nil {
		return err
	}
	var result command.Result
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("invalid result JSON")
	}
	if result.TaskID != "" && result.TaskID != taskID {
		return fmt.Errorf("result task does not match")
	}
	if result.RepoID != recorded.RepoID || result.Command != recorded.Command ||
		result.ExitCode != recorded.ExitCode || result.TimedOut != recorded.TimedOut ||
		result.StdoutTruncated != recorded.StdoutTruncated || result.StderrTruncated != recorded.StderrTruncated ||
		"artifact:"+result.ResultPath != recorded.ResultArtifactRef ||
		"artifact:"+result.StdoutPath != recorded.StdoutArtifactRef ||
		"artifact:"+result.StderrPath != recorded.StderrArtifactRef ||
		result.StdoutChecksum != recorded.StdoutChecksum || result.StderrChecksum != recorded.StderrChecksum {
		return fmt.Errorf("result JSON does not match acceptance summary")
	}
	return nil
}

func verifyAcceptanceArtifact(ctx context.Context, db *state.DB, artifactRoot, taskID, ref, checksum string) error {
	if !strings.HasPrefix(ref, "artifact:") || checksum == "" {
		return fmt.Errorf("artifact reference or checksum is missing")
	}
	path := strings.TrimPrefix(ref, "artifact:")
	if err := artifact.ValidatePath(path); err != nil {
		return err
	}
	metadata, err := db.Artifact(ctx, benchmarkArtifactID(taskID, path))
	if err != nil {
		return fmt.Errorf("artifact metadata: %w", err)
	}
	if metadata.TaskID != taskID || metadata.Path != path || metadata.Checksum != checksum || metadata.State != artifact.StateSealed {
		return fmt.Errorf("artifact metadata does not match")
	}
	actual, err := fileChecksum(filepath.Join(artifactRoot, filepath.FromSlash(path)))
	if err != nil {
		return err
	}
	if actual != checksum {
		return fmt.Errorf("artifact checksum does not match")
	}
	return nil
}

func runAcceptanceCheck(ctx context.Context, artifactDir, runPrefix, taskID string, check AcceptanceCheck, wt state.Worktree, opts AcceptanceOptions) AcceptanceCheckEvidence {
	startedAt := time.Now().UTC()
	evidence := AcceptanceCheckEvidence{
		ID: check.ID, RepoID: check.RepoID, Command: check.Command, CWD: check.CWD,
		Status: "error", ExpectedExitCode: check.ExpectedExitCode, ExitCode: -1, StartedAt: startedAt.Format(time.RFC3339Nano),
	}
	finish := func() AcceptanceCheckEvidence {
		evidence.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		return evidence
	}
	tmp, err := os.MkdirTemp("", "midgard-acceptance-")
	if err != nil {
		evidence.Error = err.Error()
		return finish()
	}
	defer os.RemoveAll(tmp)
	snapshot := filepath.Join(tmp, "worktree")
	if err := gitrepo.AddSnapshotWorktree(ctx, wt.Path, snapshot); err != nil {
		evidence.Error = err.Error()
		return finish()
	}
	defer gitrepo.RemoveSnapshotWorktree(context.WithoutCancel(ctx), wt.Path, snapshot)
	cwd := snapshot
	if check.CWD != "" {
		cwd = filepath.Join(snapshot, filepath.FromSlash(check.CWD))
	}
	commandPolicy := policy.ReadOnlyCommandPolicy(snapshot, artifactDir)
	commandPolicy.Limits.Timeout = acceptanceTimeout(check, opts)
	if opts.MaxStdoutBytes > 0 {
		commandPolicy.Limits.MaxStdoutBytes = opts.MaxStdoutBytes
	}
	if opts.MaxStderrBytes > 0 {
		commandPolicy.Limits.MaxStderrBytes = opts.MaxStderrBytes
	}
	prefix := filepath.ToSlash(filepath.Join(runPrefix, "checks", safeBenchmarkID(check.ID)))
	result, err := command.NewExecutor(commandPolicy).Run(ctx, command.Request{
		ID: safeBenchmarkID("accept_" + check.ID), TaskID: taskID, RepoID: check.RepoID,
		Command: check.Command, CWD: cwd, ArtifactDir: artifactDir, ArtifactPrefix: prefix,
		Fence: midgardtask.CheckExecution,
	})
	if err != nil {
		evidence.Error = err.Error()
		return finish()
	}
	evidence.ExitCode = result.ExitCode
	evidence.TimedOut = result.TimedOut
	evidence.StdoutTruncated = result.StdoutTruncated
	evidence.StderrTruncated = result.StderrTruncated
	evidence.ResultArtifactRef = "artifact:" + result.ResultPath
	evidence.ResultChecksum = result.ResultChecksum
	evidence.StdoutArtifactRef = "artifact:" + result.StdoutPath
	evidence.StdoutChecksum = result.StdoutChecksum
	evidence.StderrArtifactRef = "artifact:" + result.StderrPath
	evidence.StderrChecksum = result.StderrChecksum
	evidence.StartedAt = result.StartedAt.UTC().Format(time.RFC3339Nano)
	evidence.FinishedAt = result.FinishedAt.UTC().Format(time.RFC3339Nano)
	if result.ExitCode == check.ExpectedExitCode && !result.TimedOut {
		evidence.Status = "passed"
	} else {
		evidence.Status = "failed"
	}
	return evidence
}

func normalizeAcceptanceChecks(item Item) ([]AcceptanceCheck, error) {
	checks := append([]AcceptanceCheck(nil), item.AcceptanceChecks...)
	for _, commandText := range item.Checks {
		checks = append(checks, AcceptanceCheck{Command: commandText})
	}
	seen := map[string]bool{}
	for i := range checks {
		check := &checks[i]
		check.ID = strings.TrimSpace(check.ID)
		if check.ID == "" {
			check.ID = fmt.Sprintf("check-%d", i+1)
		}
		if safeBenchmarkID(check.ID) != check.ID {
			return nil, fmt.Errorf("benchmark acceptance check id %q is invalid", check.ID)
		}
		if seen[check.ID] {
			return nil, fmt.Errorf("benchmark acceptance check id %q is duplicated", check.ID)
		}
		seen[check.ID] = true
		check.Command = strings.TrimSpace(check.Command)
		if check.Command == "" {
			return nil, fmt.Errorf("benchmark acceptance check %q has no command", check.ID)
		}
		check.RepoID = strings.TrimSpace(check.RepoID)
		check.CWD = filepath.ToSlash(strings.TrimSpace(check.CWD))
		if check.CWD != "" {
			clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(check.CWD)))
			if filepath.IsAbs(check.CWD) || clean != check.CWD || clean == ".." || strings.HasPrefix(clean, "../") {
				return nil, fmt.Errorf("benchmark acceptance check %q cwd %q escapes its repo", check.ID, check.CWD)
			}
		}
		if check.TimeoutSeconds < 0 {
			return nil, fmt.Errorf("benchmark acceptance check %q has a negative timeout", check.ID)
		}
		if check.ExpectedExitCode < 0 || check.ExpectedExitCode > 255 {
			return nil, fmt.Errorf("benchmark acceptance check %q expected_exit_code must be between 0 and 255", check.ID)
		}
	}
	return checks, nil
}

func resolveAcceptanceCheckRepos(checks []AcceptanceCheck, itemRepoIDs []string, worktrees map[string]state.Worktree) ([]AcceptanceCheck, error) {
	defaultRepo := ""
	if len(itemRepoIDs) == 1 {
		defaultRepo = itemRepoIDs[0]
	} else if len(itemRepoIDs) == 0 && len(worktrees) == 1 {
		for repoID := range worktrees {
			defaultRepo = repoID
		}
	}
	for i := range checks {
		if checks[i].RepoID == "" {
			checks[i].RepoID = defaultRepo
		}
		if checks[i].RepoID == "" {
			return nil, fmt.Errorf("benchmark acceptance check %q requires repo_id for a multi-repo task", checks[i].ID)
		}
		if _, ok := worktrees[checks[i].RepoID]; !ok {
			return nil, fmt.Errorf("benchmark acceptance check %q references task repo %q with no worktree", checks[i].ID, checks[i].RepoID)
		}
	}
	return checks, nil
}

func acceptanceTimeout(check AcceptanceCheck, opts AcceptanceOptions) time.Duration {
	if check.TimeoutSeconds > 0 {
		return time.Duration(check.TimeoutSeconds) * time.Second
	}
	if opts.DefaultTimeout > 0 {
		return opts.DefaultTimeout
	}
	return 5 * time.Minute
}

func acceptanceWorktreeFingerprint(ctx context.Context, wt state.Worktree) (string, string, error) {
	head, err := gitrepo.CurrentCommit(ctx, wt.Path)
	if err != nil {
		return "", "", err
	}
	diff, err := gitrepo.Run(ctx, wt.Path, "diff", "--binary", "--no-ext-diff", "HEAD", "--")
	if err != nil {
		return "", "", err
	}
	untracked, err := gitrepo.Run(ctx, wt.Path, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", "", err
	}
	paths := strings.Split(untracked, "\x00")
	paths = slices.DeleteFunc(paths, func(path string) bool { return path == "" })
	slices.Sort(paths)
	hash := sha256.New()
	_, _ = hash.Write([]byte("head\x00" + head + "\x00diff\x00" + diff + "\x00"))
	for _, rel := range paths {
		path := filepath.Join(wt.Path, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			return "", "", err
		}
		_, _ = hash.Write([]byte("file\x00" + rel + "\x00"))
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return head, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func persistAcceptanceCommandArtifacts(ctx context.Context, db *state.DB, taskID string, result AcceptanceCheckEvidence) error {
	for _, entry := range []struct {
		ref      string
		checksum string
	}{
		{result.ResultArtifactRef, result.ResultChecksum},
		{result.StdoutArtifactRef, result.StdoutChecksum},
		{result.StderrArtifactRef, result.StderrChecksum},
	} {
		if entry.ref == "" {
			continue
		}
		rec := artifact.Record{
			Path: strings.TrimPrefix(entry.ref, "artifact:"), Type: artifact.TypePayload,
			State: artifact.StateSealed, Checksum: entry.checksum,
		}
		if err := persistAcceptanceArtifact(ctx, db, taskID, rec); err != nil {
			return err
		}
	}
	return nil
}

func persistAcceptanceArtifact(ctx context.Context, db *state.DB, taskID string, rec artifact.Record) error {
	return db.UpdateArtifact(ctx, state.Artifact{
		ID: benchmarkArtifactID(taskID, rec.Path), TaskID: taskID, Type: rec.Type,
		Path: rec.Path, Checksum: rec.Checksum, State: rec.State,
	})
}

func fileChecksum(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func benchmarkArtifactID(taskID, path string) string {
	sum := sha256.Sum256([]byte(taskID + "\x00" + path))
	return "benchmark_artifact_" + hex.EncodeToString(sum[:])[:20]
}

func safeBenchmarkID(value string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ".", "_", ":", "_", "#", "_", " ", "_")
	return replacer.Replace(strings.TrimSpace(value))
}
