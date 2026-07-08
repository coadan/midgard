package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"midgard/internal/artifact"
	"midgard/internal/command"
	"midgard/internal/cost"
	"midgard/internal/gitrepo"
	"midgard/internal/model"
	"midgard/internal/policy"
	"midgard/internal/review"
	"midgard/internal/state"
	"midgard/internal/stream"
	"midgard/internal/workbench"
)

const defaultMaxReviewCycles = 4

func RunLoop(ctx context.Context, root, taskID string, opts RunnerOptions) (LoopResult, error) {
	var loop LoopResult
	maxReviewCycles := opts.MaxReviewCycles
	if maxReviewCycles <= 0 {
		maxReviewCycles = defaultMaxReviewCycles
	}
	history, err := loadRoleHistory(ctx, root, taskID)
	if err != nil {
		return loop, err
	}
	roleCounts := history.RoleCounts
	if !history.ResumeFromReviewChangesRequested() {
		plannerRun, err := RunRole(ctx, root, taskID, model.RolePlanner, opts)
		if err != nil {
			return loop, err
		}
		loop.RoleRuns = append(loop.RoleRuns, plannerRun)
		if err := snapshotRoleAttempt(root, taskID, plannerRun, nextRoleCount(roleCounts, model.RolePlanner)); err != nil {
			return loop, err
		}
		if plannerRun.Status != "ready" {
			return finishLoop(ctx, root, taskID, opts, loop)
		}
	} else if err := recordReworkResumed(ctx, root, taskID, maxReviewCycles); err != nil {
		return loop, err
	}
	for cycle := 1; cycle <= maxReviewCycles; cycle++ {
		implementerRun, err := RunRole(ctx, root, taskID, model.RoleImplementer, opts)
		if err != nil {
			return loop, err
		}
		loop.RoleRuns = append(loop.RoleRuns, implementerRun)
		if err := snapshotRoleAttempt(root, taskID, implementerRun, nextRoleCount(roleCounts, model.RoleImplementer)); err != nil {
			return loop, err
		}
		if implementerRun.Status != "ready" && implementerRun.Status != "no-op" {
			break
		}
		reviewerRun, err := RunRole(ctx, root, taskID, model.RoleReviewer, opts)
		if err != nil {
			return loop, err
		}
		reviewerRun, err = applyReviewGuards(ctx, root, taskID, reviewerRun)
		if err != nil {
			return loop, err
		}
		loop.RoleRuns = append(loop.RoleRuns, reviewerRun)
		if err := snapshotRoleAttempt(root, taskID, reviewerRun, nextRoleCount(roleCounts, model.RoleReviewer)); err != nil {
			return loop, err
		}
		if reviewerRun.Status == string(review.VerdictApproved) {
			break
		}
		if reviewerRun.Status == string(review.VerdictChangesRequested) && cycle < maxReviewCycles {
			if err := recordReworkRequested(ctx, root, taskID, cycle, maxReviewCycles); err != nil {
				return loop, err
			}
			continue
		}
		break
	}
	return finishLoop(ctx, root, taskID, opts, loop)
}

type roleHistory struct {
	RoleCounts       map[model.Role]int
	Latest           roleStatus
	LatestPlanner    roleStatus
	HasLatest        bool
	HasLatestPlanner bool
	NeedsRework      bool
}

type roleStatus struct {
	Role     model.Role
	Status   string
	Artifact string
}

func (h roleHistory) ResumeFromReviewChangesRequested() bool {
	return h.NeedsRework &&
		h.HasLatestPlanner &&
		h.LatestPlanner.Status == "ready"
}

func loadRoleHistory(ctx context.Context, root, taskID string) (roleHistory, error) {
	status, err := workbench.Status(root)
	if err != nil {
		return roleHistory{}, err
	}
	db, err := state.Open(ctx, workbench.NewLayout(status.Root).State)
	if err != nil {
		return roleHistory{}, err
	}
	defer db.Close()
	events, err := db.EventsForTask(ctx, taskID)
	if err != nil {
		return roleHistory{}, err
	}
	history := roleHistory{RoleCounts: map[model.Role]int{}}
	for _, event := range events {
		if event.Type == "feedback.received" {
			feedback, ok := parseFeedbackReceivedEvent(event.Payload)
			if ok && feedback.Status == string(review.VerdictChangesRequested) {
				history.NeedsRework = true
			}
			continue
		}
		if event.Type != "role.completed" {
			continue
		}
		completed, ok := parseRoleCompletedEvent(event.Payload)
		if !ok {
			continue
		}
		history.RoleCounts[completed.Role]++
		history.Latest = completed
		history.HasLatest = true
		if completed.Role == model.RolePlanner {
			history.LatestPlanner = completed
			history.HasLatestPlanner = true
		}
		if completed.Role == model.RoleReviewer {
			history.NeedsRework = completed.Status == string(review.VerdictChangesRequested)
		}
	}
	return history, nil
}

func parseRoleCompletedEvent(payload string) (roleStatus, bool) {
	var parsed struct {
		Role   string `json:"role"`
		Status struct {
			Status   string `json:"Status"`
			Artifact string `json:"Artifact"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return roleStatus{}, false
	}
	if parsed.Role == "" || parsed.Status.Status == "" {
		return roleStatus{}, false
	}
	return roleStatus{
		Role:     model.Role(parsed.Role),
		Status:   parsed.Status.Status,
		Artifact: parsed.Status.Artifact,
	}, true
}

func finishLoop(ctx context.Context, root, taskID string, opts RunnerOptions, loop LoopResult) (LoopResult, error) {
	status, err := Status(ctx, root, taskID)
	if err != nil {
		return loop, err
	}
	patchPath, patchHasContent, err := writePatch(ctx, root, taskID, status.Worktrees)
	if err != nil {
		return loop, err
	}
	finalState := finalState(loop.RoleRuns, patchHasContent)
	if err := updateTaskState(ctx, root, taskID, finalState); err != nil {
		return loop, err
	}
	loop.TaskID = taskID
	loop.State = finalState
	loop.PatchPath = patchPath
	loop.CostUSD = sumCost(loop.RoleRuns, opts.Pricing)
	if err := appendTaskRunSummary(ctx, root, taskID, loop); err != nil {
		return loop, err
	}
	return loop, nil
}

func RunRole(ctx context.Context, root, taskID string, role model.Role, opts RunnerOptions) (RoleRun, error) {
	provider := opts.Providers[role]
	if provider == nil {
		return RoleRun{}, fmt.Errorf("provider missing for role %s", role)
	}
	wbStatus, err := workbench.Status(root)
	if err != nil {
		return RoleRun{}, err
	}
	layout := workbench.NewLayout(wbStatus.Root)
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return RoleRun{}, err
	}
	defer db.Close()
	if _, err := grantLease(ctx, db, taskID, role); err != nil {
		return RoleRun{}, err
	}
	budget := opts.Budget
	if budget == (stream.Budget{}) {
		budget = stream.DefaultBudget()
	}
	store := artifact.NewStore(filepath.Join(layout.Artifacts, taskID))
	maxSourceEditRepairs := sourceEditRepairLimit(opts)
	maxEmptyImplementationRepairs := sourceEditRepairLimit(opts)
	var sourceRepair *sourceEditApplyFailure
	var emptyImplementationRepair *model.EmptyImplementationFailure
	var emptyImplementationRepairs int
	var sourceEditFailures int
	var allUsage []model.Usage
	var totalAttempts int
	for {
		taskStatus, err := Status(ctx, wbStatus.Root, taskID)
		if err != nil {
			return RoleRun{}, err
		}
		packet, err := model.BuildPacket(model.PacketInput{
			TaskID:  taskID,
			Role:    role,
			ModelID: opts.ModelID,
			Context: contextPacket(ctx, taskStatus, layout),
			Budget:  budget,
		})
		if err != nil {
			return RoleRun{}, err
		}
		packet.ProviderID = provider.ID()
		packet.ProviderFingerprint = model.ProviderFingerprint(provider, opts.ModelID)
		if sourceRepair != nil {
			packet = model.SourceEditRepairPacket(packet, sourceRepair.modelFailure(store))
		} else if emptyImplementationRepair != nil {
			packet = model.EmptyImplementationRepairPacket(packet, *emptyImplementationRepair)
		}
		run, err := model.Runner{
			Provider:        provider,
			Store:           store,
			Budget:          budget,
			MaxCommandTurns: opts.MaxCommandTurns,
			CommandHandler: commandContinuationHandler(
				db,
				layout,
				taskID,
				taskStatus.Worktrees,
				store,
			),
		}.Run(ctx, packet)
		if err != nil {
			_ = persistRawRoleStream(store, role, run.Raw, true)
			var repairErr model.RepairExhaustedError
			if !errors.As(err, &repairErr) || !completeMissingResultEditTurn(store, role, run.Parsed) {
				return RoleRun{}, err
			}
			if err := recordProtocolFallback(ctx, db, taskID, role, repairErr.ErrorCodes, "completed_missing_result_edit_turn"); err != nil {
				return RoleRun{}, err
			}
		}
		if err := persistRawRoleStream(store, role, run.Raw, false); err != nil {
			return RoleRun{}, err
		}
		if err := appendRoleReportProvenance(store, run, provider, opts.ModelID, packet); err != nil {
			return RoleRun{}, err
		}
		if err := persistRoleRun(ctx, db, taskID, role, run, opts.Pricing); err != nil {
			return RoleRun{}, err
		}
		allUsage = append(allUsage, run.Usage...)
		totalAttempts += run.Attempts
		sourceEditAttempt := sourceEditFailures + 1
		failure, err := applySourceEdits(ctx, db, taskID, role, taskStatus.Worktrees, store, run.Parsed, sourceEditAttempt, maxSourceEditRepairs)
		if err != nil {
			return RoleRun{}, err
		}
		if failure != nil {
			sourceEditFailures++
			if sourceEditFailures <= maxSourceEditRepairs {
				if err := recordSourceEditRepairRequested(ctx, db, taskID, failure.event.Attempt, maxSourceEditRepairs); err != nil {
					return RoleRun{}, err
				}
				sourceRepair = failure
				continue
			}
			return RoleRun{}, fmt.Errorf("source edit apply repairs exhausted after %d retries: %w", maxSourceEditRepairs, failure)
		}
		for _, proposal := range run.Parsed.Commands {
			if _, err := executeProposal(ctx, db, layout, taskID, taskStatus.Worktrees, proposal); err != nil {
				return RoleRun{}, err
			}
		}
		emptyReady, err := readyWithoutWorktreeDiff(ctx, role, taskStatus.Worktrees, run.Parsed)
		if err != nil {
			return RoleRun{}, err
		}
		if emptyReady {
			if emptyImplementationRepairs < maxEmptyImplementationRepairs {
				emptyImplementationRepairs++
				if err := recordEmptyImplementationRepairRequested(ctx, db, taskID, emptyImplementationRepairs, maxEmptyImplementationRepairs); err != nil {
					return RoleRun{}, err
				}
				emptyImplementationRepair = &model.EmptyImplementationFailure{
					Attempt:           emptyImplementationRepairs,
					RemainingAttempts: max(0, maxEmptyImplementationRepairs-emptyImplementationRepairs),
				}
				sourceRepair = nil
				continue
			}
			blocked := roleRunFromModelRun(role, run, provider, opts.ModelID, allUsage, totalAttempts)
			blocked.Status = "blocked"
			return blocked, nil
		}
		return roleRunFromModelRun(role, run, provider, opts.ModelID, allUsage, totalAttempts), nil
	}
}

func roleRunFromModelRun(role model.Role, run model.RunResult, provider model.Provider, modelID string, usage []model.Usage, attempts int) RoleRun {
	status := ""
	artifactPath := ""
	if run.Parsed.Result != nil {
		status = run.Parsed.Result.Status
		artifactPath = run.Parsed.Result.Artifact
	}
	var inputTokens int64
	var outputTokens int64
	for _, record := range usage {
		inputTokens += record.InputTokens
		outputTokens += record.OutputTokens
	}
	return RoleRun{
		Role:                role,
		Status:              status,
		Artifact:            artifactPath,
		ProviderID:          provider.ID(),
		ModelID:             modelID,
		ProviderFingerprint: model.ProviderFingerprint(provider, modelID),
		Attempts:            attempts,
		InputTokens:         inputTokens,
		OutputTokens:        outputTokens,
	}
}

func completeMissingResultEditTurn(store artifact.Store, role model.Role, parsed *stream.ParseResult) bool {
	if role != model.RoleImplementer ||
		parsed == nil ||
		parsed.Result != nil ||
		len(parsed.Edits) == 0 ||
		!onlyParserError(parsed.Errors, "missing_result") ||
		hasRejectedArtifact(parsed.Artifacts) {
		return false
	}
	reportPath, ok := firstReportArtifact(parsed.Artifacts)
	if !ok {
		return false
	}
	data, err := store.Read(reportPath)
	if err != nil {
		return false
	}
	rec, err := store.Put(artifact.Record{
		Path:         reportPath,
		Type:         artifact.TypeReport,
		State:        artifact.StateSealed,
		ProducerRole: role.String(),
	}, data)
	if err != nil {
		return false
	}
	for i := range parsed.Artifacts {
		if parsed.Artifacts[i].Path == reportPath && parsed.Artifacts[i].Type == artifact.TypeReport {
			parsed.Artifacts[i] = rec
			break
		}
	}
	parsed.Result = &stream.ResultFrame{
		Status:   "ready",
		Artifact: reportPath,
		Fields: map[string]string{
			"status":                    "ready",
			"artifact":                  reportPath,
			"checks":                    "none",
			"midgard_protocol_fallback": "missing_result_after_edits",
		},
	}
	parsed.Repair = nil
	return true
}

func onlyParserError(errors []stream.ParserError, code string) bool {
	if len(errors) == 0 {
		return false
	}
	for _, parserErr := range errors {
		if parserErr.Code != code {
			return false
		}
	}
	return true
}

func hasRejectedArtifact(artifacts []artifact.Record) bool {
	for _, rec := range artifacts {
		if rec.State == artifact.StateRejected {
			return true
		}
	}
	return false
}

func firstReportArtifact(artifacts []artifact.Record) (string, bool) {
	for _, rec := range artifacts {
		if rec.Type == artifact.TypeReport && rec.Path != "" {
			return rec.Path, true
		}
	}
	return "", false
}

func persistRoleRun(ctx context.Context, db *state.DB, taskID string, role model.Role, run model.RunResult, pricing cost.Pricing) error {
	for _, rec := range run.Parsed.Artifacts {
		stateArtifact := state.Artifact{
			ID:           artifactID(taskID, rec.Path),
			TaskID:       taskID,
			Type:         rec.Type,
			Path:         rec.Path,
			Checksum:     rec.Checksum,
			ProducerRole: role.String(),
			State:        rec.State,
		}
		if err := db.InsertArtifact(ctx, stateArtifact); err != nil && !strings.Contains(err.Error(), "constraint failed") {
			return err
		}
	}
	ordinal, err := nextUsageOrdinal(ctx, db, taskID, role.String())
	if err != nil {
		return err
	}
	for i, usage := range run.Usage {
		raw := usage.Raw
		if raw == "" {
			raw = "{}"
		}
		if err := db.InsertUsageRecord(ctx, state.UsageRecord{
			ID:           fmt.Sprintf("usage_%s_%s_%d", taskID, role, ordinal+i+1),
			TaskID:       taskID,
			ProviderID:   usage.ProviderID,
			ModelID:      usage.ModelID,
			Role:         role.String(),
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			RawPayload:   raw,
		}); err != nil && !strings.Contains(err.Error(), "constraint failed") {
			return err
		}
		computed := cost.Compute(usage, pricing)
		if err := db.InsertCostRollup(ctx, state.CostRollup{
			ID:        fmt.Sprintf("cost_%s_%s_%d", taskID, role, ordinal+i+1),
			TaskID:    taskID,
			AmountUSD: strconv.FormatFloat(computed.AmountUSD, 'f', 6, 64),
			Caveats:   computed.Caveat,
		}); err != nil && !strings.Contains(err.Error(), "constraint failed") {
			return err
		}
	}
	payload, err := json.Marshal(map[string]any{
		"role":     role.String(),
		"status":   run.Parsed.Result,
		"attempts": run.Attempts,
	})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "role.completed", Payload: string(payload)})
	return err
}

func nextUsageOrdinal(ctx context.Context, db *state.DB, taskID, role string) (int, error) {
	var count int
	err := db.Conn().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM usage_records
WHERE task_id = ? AND role = ?`, taskID, role).Scan(&count)
	return count, err
}

func persistRawRoleStream(store artifact.Store, role model.Role, raw string, failed bool) error {
	suffix := ".stream"
	if failed {
		suffix = "-failed.stream"
	}
	_, err := store.Put(artifact.Record{
		Path:         "streams/" + role.String() + suffix,
		Type:         artifact.TypePayload,
		State:        artifact.StateSealed,
		ProducerRole: role.String(),
		PayloadType:  "text",
	}, []byte(raw))
	return err
}

func nextRoleCount(counts map[model.Role]int, role model.Role) int {
	counts[role]++
	return counts[role]
}

func snapshotRoleAttempt(root, taskID string, run RoleRun, ordinal int) error {
	if ordinal <= 0 {
		return nil
	}
	status, err := workbench.Status(root)
	if err != nil {
		return err
	}
	store := artifact.NewStore(filepath.Join(workbench.NewLayout(status.Root).Artifacts, taskID))
	prefix := fmt.Sprintf("attempts/%s/%d", run.Role, ordinal)
	streamPath := "streams/" + run.Role.String() + ".stream"
	if data, err := store.Read(streamPath); err == nil {
		if _, err := store.Put(artifact.Record{
			Path:         prefix + "/stream.txt",
			Type:         artifact.TypePayload,
			State:        artifact.StateSealed,
			ProducerRole: run.Role.String(),
			PayloadType:  "text",
		}, data); err != nil {
			return err
		}
	}
	if run.Artifact == "" {
		return nil
	}
	data, err := store.Read(run.Artifact)
	if err != nil {
		return err
	}
	_, err = store.Put(artifact.Record{
		Path:         prefix + "/" + filepath.ToSlash(run.Artifact),
		Type:         artifact.TypeReport,
		State:        artifact.StateSealed,
		ProducerRole: run.Role.String(),
	}, data)
	return err
}

func recordReworkRequested(ctx context.Context, root, taskID string, cycle, maxCycles int) error {
	status, err := workbench.Status(root)
	if err != nil {
		return err
	}
	db, err := state.Open(ctx, workbench.NewLayout(status.Root).State)
	if err != nil {
		return err
	}
	defer db.Close()
	payload, err := json.Marshal(map[string]any{
		"cycle":      cycle,
		"max_cycles": maxCycles,
	})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "rework.requested", Payload: string(payload)})
	return err
}

func recordReworkResumed(ctx context.Context, root, taskID string, maxCycles int) error {
	status, err := workbench.Status(root)
	if err != nil {
		return err
	}
	db, err := state.Open(ctx, workbench.NewLayout(status.Root).State)
	if err != nil {
		return err
	}
	defer db.Close()
	payload, err := json.Marshal(map[string]any{
		"reason":     "latest reviewer requested changes",
		"max_cycles": maxCycles,
	})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "rework.resumed", Payload: string(payload)})
	return err
}

func recordSourceEditRepairRequested(ctx context.Context, db *state.DB, taskID string, attempt, maxRepairs int) error {
	payload, err := json.Marshal(map[string]any{
		"attempt":            attempt,
		"max_repairs":        maxRepairs,
		"remaining_repairs":  max(0, maxRepairs-attempt+1),
		"next_apply_attempt": attempt + 1,
	})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "source_edit.repair_requested", Payload: string(payload)})
	return err
}

func recordEmptyImplementationRepairRequested(ctx context.Context, db *state.DB, taskID string, attempt, maxRepairs int) error {
	payload, err := json.Marshal(map[string]any{
		"attempt":           attempt,
		"max_repairs":       maxRepairs,
		"remaining_repairs": max(0, maxRepairs-attempt),
	})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "implementation.empty_ready_repair_requested", Payload: string(payload)})
	return err
}

func recordProtocolFallback(ctx context.Context, db *state.DB, taskID string, role model.Role, errorCodes []string, action string) error {
	payload, err := json.Marshal(map[string]any{
		"role":        role.String(),
		"error_codes": errorCodes,
		"action":      action,
	})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "role.protocol_fallback", Payload: string(payload)})
	return err
}

func sourceEditRepairLimit(opts RunnerOptions) int {
	if opts.MaxSourceEditRepairs < 0 {
		return 0
	}
	if opts.MaxSourceEditRepairs == 0 {
		return 2
	}
	return opts.MaxSourceEditRepairs
}

func appendRoleReportProvenance(store artifact.Store, run model.RunResult, provider model.Provider, modelID string, packet model.Packet) error {
	if run.Parsed == nil || run.Parsed.Result == nil {
		return nil
	}
	reportPath := run.Parsed.Result.Artifact
	data, err := store.Read(reportPath)
	if err != nil {
		return err
	}
	var inputTokens int64
	var outputTokens int64
	for _, usage := range run.Usage {
		inputTokens += usage.InputTokens
		outputTokens += usage.OutputTokens
	}
	provenance := fmt.Sprintf(`

## Midgard Provenance

- provider: %s
- model: %s
- provider_fingerprint: %s
- protocol: %s
- protocol_fingerprint: %s
- attempts: %d
- usage: in=%d out=%d
`, provider.ID(), modelID, model.ProviderFingerprint(provider, modelID), packet.ProtocolVersion, packet.ProtocolFingerprint, run.Attempts, inputTokens, outputTokens)
	updated := append(append([]byte(nil), data...), []byte(provenance)...)
	rec, err := store.Put(artifact.Record{Path: reportPath, Type: artifact.TypeReport, State: artifact.StateSealed, ProducerRole: run.Packet.Role.String()}, updated)
	if err != nil {
		return err
	}
	for i := range run.Parsed.Artifacts {
		if run.Parsed.Artifacts[i].Path == reportPath && run.Parsed.Artifacts[i].Type == artifact.TypeReport {
			run.Parsed.Artifacts[i].Checksum = rec.Checksum
			run.Parsed.Artifacts[i].Size = rec.Size
			run.Parsed.Artifacts[i].State = rec.State
		}
	}
	return nil
}

const maxCommandContinuationPreviewBytes = 12000

func commandContinuationHandler(db *state.DB, layout workbench.Layout, taskID string, worktrees []WorktreeStatus, store artifact.Store) model.CommandHandler {
	return func(ctx context.Context, commands []stream.CommandProposal) (string, error) {
		var b strings.Builder
		for i, proposal := range commands {
			result, err := executeProposal(ctx, db, layout, taskID, worktrees, proposal)
			if err != nil {
				return "", err
			}
			if i > 0 {
				b.WriteByte('\n')
			}
			appendCommandContinuationResult(&b, store, result)
		}
		return b.String(), nil
	}
}

func appendCommandContinuationResult(b *strings.Builder, store artifact.Store, result command.Result) {
	fmt.Fprintf(
		b,
		"command id:%s repo:%s exit:%d timed_out:%t stdout:artifact:%s stderr:artifact:%s result:artifact:%s stdout_truncated:%t stderr_truncated:%t\n",
		result.ID,
		result.RepoID,
		result.ExitCode,
		result.TimedOut,
		result.StdoutPath,
		result.StderrPath,
		result.ResultPath,
		result.StdoutTruncated,
		result.StderrTruncated,
	)
	if stdout := commandPreview(store, result.StdoutPath); stdout != "" {
		b.WriteString("stdout_preview:\n")
		b.WriteString(stdout)
		if !strings.HasSuffix(stdout, "\n") {
			b.WriteByte('\n')
		}
	}
	if stderr := commandPreview(store, result.StderrPath); stderr != "" {
		b.WriteString("stderr_preview:\n")
		b.WriteString(stderr)
		if !strings.HasSuffix(stderr, "\n") {
			b.WriteByte('\n')
		}
	}
}

func commandPreview(store artifact.Store, path string) string {
	data, err := store.Read(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	text := string(data)
	if len(text) > maxCommandContinuationPreviewBytes {
		head := maxCommandContinuationPreviewBytes * 2 / 3
		tail := maxCommandContinuationPreviewBytes - head
		omitted := len(text) - head - tail
		text = text[:head] +
			fmt.Sprintf("\n[command output preview truncated; %d bytes omitted. Run a narrower command for missing middle content.]\n", omitted) +
			text[len(text)-tail:]
	}
	return text
}

func executeProposal(ctx context.Context, db *state.DB, layout workbench.Layout, taskID string, worktrees []WorktreeStatus, proposal stream.CommandProposal) (command.Result, error) {
	wt, err := worktreeForRepo(worktrees, proposal.Repo)
	if err != nil {
		return command.Result{}, err
	}
	artifactDir := filepath.Join(layout.Artifacts, taskID)
	executor := command.NewExecutor(policy.DefaultCommandPolicy(wt.Path, artifactDir))
	result, err := executor.Run(ctx, command.Request{
		TaskID:      taskID,
		RepoID:      wt.RepoID,
		Command:     proposal.Command,
		CWD:         wt.Path,
		ArtifactDir: artifactDir,
	})
	if err != nil {
		return command.Result{}, err
	}
	events, err := command.Events(result)
	if err != nil {
		return command.Result{}, err
	}
	for _, event := range events {
		if _, err := db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: event.Type, Payload: event.Payload}); err != nil {
			return command.Result{}, err
		}
	}
	return result, nil
}

func writePatch(ctx context.Context, root, taskID string, worktrees []WorktreeStatus) (string, bool, error) {
	status, err := workbench.Status(root)
	if err != nil {
		return "", false, err
	}
	layout := workbench.NewLayout(status.Root)
	var patch strings.Builder
	for _, wt := range worktrees {
		diff, err := gitrepo.Diff(ctx, wt.Path)
		if err != nil {
			return "", false, err
		}
		if diff == "" {
			continue
		}
		if patch.Len() > 0 {
			patch.WriteByte('\n')
		}
		patch.WriteString("# repo:")
		patch.WriteString(wt.RepoID)
		patch.WriteByte('\n')
		patch.WriteString(diff)
	}
	store := artifact.NewStore(filepath.Join(layout.Artifacts, taskID))
	rec, err := store.Put(artifact.Record{Path: "patch.diff", Type: artifact.TypePayload, State: artifact.StateSealed, PayloadType: "patch"}, []byte(patch.String()))
	if err != nil {
		return "", false, err
	}
	return rec.Path, strings.TrimSpace(patch.String()) != "", nil
}

func readyWithoutWorktreeDiff(ctx context.Context, role model.Role, worktrees []WorktreeStatus, parsed *stream.ParseResult) (bool, error) {
	if role != model.RoleImplementer ||
		parsed == nil ||
		parsed.Result == nil ||
		parsed.Result.Status != "ready" {
		return false, nil
	}
	hasDiff, err := worktreesHaveDiff(ctx, worktrees)
	if err != nil {
		return false, err
	}
	return !hasDiff, nil
}

func worktreesHaveDiff(ctx context.Context, worktrees []WorktreeStatus) (bool, error) {
	for _, wt := range worktrees {
		diff, err := gitrepo.Diff(ctx, wt.Path)
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(diff) != "" {
			return true, nil
		}
	}
	return false, nil
}

func updateTaskState(ctx context.Context, root, taskID, taskState string) error {
	status, err := workbench.Status(root)
	if err != nil {
		return err
	}
	db, err := state.Open(ctx, workbench.NewLayout(status.Root).State)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.UpdateTaskState(ctx, taskID, taskState)
}

func worktreeForRepo(worktrees []WorktreeStatus, repoID string) (WorktreeStatus, error) {
	if repoID == "" && len(worktrees) == 1 {
		return worktrees[0], nil
	}
	for _, wt := range worktrees {
		if wt.RepoID == repoID {
			return wt, nil
		}
	}
	return WorktreeStatus{}, fmt.Errorf("repo %q not found for task", repoID)
}

func finalState(runs []RoleRun, patchHasContent bool) string {
	if len(runs) == 0 {
		return "open"
	}
	last := runs[len(runs)-1]
	if last.Role == model.RoleReviewer && last.Status == string(review.VerdictApproved) {
		if implementerStatus(runs) == "no-op" || patchHasContent {
			return "completed"
		}
		return "blocked"
	}
	if last.Role == model.RoleImplementer && last.Status == "no-op" {
		return "completed"
	}
	if last.Status == "failed" {
		return "failed"
	}
	return "blocked"
}

func implementerStatus(runs []RoleRun) string {
	for _, run := range runs {
		if run.Role == model.RoleImplementer {
			return run.Status
		}
	}
	return ""
}

func sumCost(runs []RoleRun, pricing cost.Pricing) float64 {
	var amount float64
	for _, run := range runs {
		amount += (float64(run.InputTokens)/1_000_000)*pricing.InputUSDPerMillion +
			(float64(run.OutputTokens)/1_000_000)*pricing.OutputUSDPerMillion
	}
	return amount
}

func appendTaskRunSummary(ctx context.Context, root, taskID string, loop LoopResult) error {
	status, err := workbench.Status(root)
	if err != nil {
		return err
	}
	path := filepath.Join(status.Root, ".midgard", "tasks", taskID+".mdx")
	var b strings.Builder
	b.WriteString("\n\n## Midgard Run Summary\n\n")
	b.WriteString("- state: ")
	b.WriteString(loop.State)
	b.WriteString("\n")
	b.WriteString("- patch: artifact:")
	b.WriteString(loop.PatchPath)
	b.WriteString("\n")
	b.WriteString("- cost: $")
	b.WriteString(strconv.FormatFloat(loop.CostUSD, 'f', 6, 64))
	b.WriteString("\n")
	for _, run := range loop.RoleRuns {
		fmt.Fprintf(
			&b,
			"- role: %s status:%s artifact:%s provider:%s model:%s provider_fingerprint:%s usage:in=%d,out=%d\n",
			run.Role,
			run.Status,
			run.Artifact,
			run.ProviderID,
			run.ModelID,
			run.ProviderFingerprint,
			run.InputTokens,
			run.OutputTokens,
		)
	}
	sourceEditSummary, err := sourceEditTaskSummary(ctx, status.Root, taskID)
	if err != nil {
		return err
	}
	if sourceEditSummary != "" {
		b.WriteString("\n## Source Edit Summary\n\n")
		b.WriteString(sourceEditSummary)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(b.String())
	return err
}

func sourceEditTaskSummary(ctx context.Context, root, taskID string) (string, error) {
	db, err := state.Open(ctx, filepath.Join(root, ".midgard", "state.sqlite"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	events, err := db.EventsForTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, event := range events {
		switch event.Type {
		case "source_edit.apply_failed":
			var failed sourceEditApplyFailedEvent
			if err := json.Unmarshal([]byte(event.Payload), &failed); err != nil {
				continue
			}
			fmt.Fprintf(
				&b,
				"- apply_failed attempt:%d file:%s partial_applied:%t patch:%s stderr:%s context:%s remaining_repairs:%d\n",
				failed.Attempt,
				failed.File,
				failed.PartialApplied,
				artifactRef(failed.FailedPatchArtifact),
				artifactRef(failed.StderrArtifact),
				artifactRef(failed.SourceContextArtifact),
				failed.RemainingRepairs,
			)
		case "source_edit.repair_requested":
			var repair struct {
				Attempt          int `json:"attempt"`
				NextApplyAttempt int `json:"next_apply_attempt"`
			}
			if err := json.Unmarshal([]byte(event.Payload), &repair); err != nil {
				continue
			}
			fmt.Fprintf(&b, "- repair_requested after_attempt:%d next_attempt:%d\n", repair.Attempt, repair.NextApplyAttempt)
		case "source_edit.applied":
			var applied sourceEditAppliedEvent
			if err := json.Unmarshal([]byte(event.Payload), &applied); err != nil {
				continue
			}
			fmt.Fprintf(
				&b,
				"- applied file:%s action:%s mode:%s content:%s\n",
				applied.File,
				applied.Action,
				applied.Mode,
				applied.Content,
			)
		}
	}
	return b.String(), nil
}

func artifactID(taskID, path string) string {
	replacer := strings.NewReplacer("/", "_", ".", "_", ":", "_")
	return taskID + "_" + replacer.Replace(path)
}
