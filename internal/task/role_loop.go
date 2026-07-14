package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

func RunLoop(ctx context.Context, root, taskID string, opts RunnerOptions) (result LoopResult, retErr error) {
	execution, err := AcquireExecution(ctx, root, taskID)
	if err != nil {
		return LoopResult{}, err
	}
	defer func() {
		if err := execution.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	return runLoop(execution.Context, root, taskID, opts)
}

func runLoop(ctx context.Context, root, taskID string, opts RunnerOptions) (LoopResult, error) {
	var loop LoopResult
	maxReviewCycles := opts.MaxReviewCycles
	if maxReviewCycles <= 0 {
		maxReviewCycles = defaultMaxReviewCycles
	}
	history, err := loadRoleHistory(ctx, root, taskID)
	if err != nil {
		return loop, err
	}
	nextRole := history.NextRole()
	if nextRole == model.RolePlanner {
		plannerRun, err := RunRole(ctx, root, taskID, model.RolePlanner, opts)
		if plannerRun.Role != "" {
			loop.RoleRuns = append(loop.RoleRuns, plannerRun)
		}
		if err != nil {
			return finishErroredLoop(ctx, root, taskID, loop, err)
		}
		if plannerRun.Status != "ready" {
			return finishLoop(ctx, root, taskID, loop)
		}
		nextRole = model.RoleImplementer
	} else if history.ResumeFromReviewChangesRequested() {
		if err := recordReworkResumed(ctx, root, taskID, maxReviewCycles); err != nil {
			return loop, err
		}
	}
	if nextRole == "" {
		return finishLoop(ctx, root, taskID, loop)
	}
	startWithReviewer := nextRole == model.RoleReviewer
	for cycle := 1; cycle <= maxReviewCycles; cycle++ {
		if !startWithReviewer {
			implementerRun, err := RunRole(ctx, root, taskID, model.RoleImplementer, opts)
			if implementerRun.Role != "" {
				loop.RoleRuns = append(loop.RoleRuns, implementerRun)
			}
			if err != nil {
				return finishErroredLoop(ctx, root, taskID, loop, err)
			}
			if implementerRun.Status != "ready" && implementerRun.Status != "no-op" {
				break
			}
		}
		startWithReviewer = false
		reviewerRun, err := RunRole(ctx, root, taskID, model.RoleReviewer, opts)
		if reviewerRun.Role != "" {
			loop.RoleRuns = append(loop.RoleRuns, reviewerRun)
		}
		if err != nil {
			return finishErroredLoop(ctx, root, taskID, loop, err)
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
	return finishLoop(ctx, root, taskID, loop)
}

type roleHistory struct {
	RoleCounts           map[model.Role]int
	Latest               roleStatus
	LatestPlanner        roleStatus
	LatestImplementer    roleStatus
	HasLatest            bool
	HasLatestPlanner     bool
	HasLatestImplementer bool
	NeedsRework          bool
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

func (h roleHistory) NextRole() model.Role {
	if h.ResumeFromReviewChangesRequested() {
		return model.RoleImplementer
	}
	if !h.HasLatest {
		return model.RolePlanner
	}
	switch h.Latest.Role {
	case model.RolePlanner:
		if h.Latest.Status == "ready" {
			return model.RoleImplementer
		}
		return model.RolePlanner
	case model.RoleImplementer:
		if h.Latest.Status == "ready" || h.Latest.Status == "no-op" {
			return model.RoleReviewer
		}
		return model.RoleImplementer
	case model.RoleReviewer:
		if h.Latest.Status == string(review.VerdictApproved) {
			return ""
		}
		if h.Latest.Status == string(review.VerdictChangesRequested) {
			return model.RoleImplementer
		}
		return model.RoleReviewer
	default:
		return model.RolePlanner
	}
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
		if completed.Role == model.RoleImplementer {
			history.LatestImplementer = completed
			history.HasLatestImplementer = true
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

func finishLoop(ctx context.Context, root, taskID string, loop LoopResult) (LoopResult, error) {
	if err := CheckExecution(ctx); err != nil {
		return loop, err
	}
	status, err := Status(ctx, root, taskID)
	if err != nil {
		return loop, err
	}
	patchPath, patchHasContent, err := writePatch(ctx, root, taskID, status.Worktrees)
	if err != nil {
		return loop, err
	}
	history, err := loadRoleHistory(ctx, root, taskID)
	if err != nil {
		return loop, err
	}
	finalState := finalState(history, patchHasContent)
	if err := updateTaskState(ctx, root, taskID, finalState); err != nil {
		return loop, err
	}
	loop.TaskID = taskID
	loop.State = finalState
	loop.PatchPath = patchPath
	loop.CostUSD, loop.CostCaveats, err = taskCostSummary(ctx, root, taskID)
	if err != nil {
		return loop, err
	}
	if err := appendTaskRunSummary(ctx, root, taskID, loop); err != nil {
		return loop, err
	}
	return loop, nil
}

func finishErroredLoop(ctx context.Context, root, taskID string, loop LoopResult, runErr error) (LoopResult, error) {
	if err := CheckExecution(ctx); err != nil {
		return loop, errors.Join(runErr, err)
	}
	status, err := Status(ctx, root, taskID)
	if err != nil {
		return loop, errors.Join(runErr, err)
	}
	patchPath, _, err := writePatch(ctx, root, taskID, status.Worktrees)
	if err != nil {
		return loop, errors.Join(runErr, err)
	}
	loop.TaskID = taskID
	loop.State = status.Task.State
	loop.PatchPath = patchPath
	loop.Error = runErr.Error()
	loop.CostUSD, loop.CostCaveats, err = taskCostSummary(ctx, root, taskID)
	if err != nil {
		return loop, errors.Join(runErr, err)
	}
	if err := appendTaskRunSummary(ctx, root, taskID, loop); err != nil {
		return loop, errors.Join(runErr, err)
	}
	return loop, runErr
}

func RunRole(ctx context.Context, root, taskID string, role model.Role, opts RunnerOptions) (result RoleRun, retErr error) {
	if err := CheckExecution(ctx); err != nil {
		return RoleRun{}, err
	}
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
	execution, err := acquireExecutionWithDB(ctx, db, taskID)
	if err != nil {
		return RoleRun{}, err
	}
	defer func() {
		if err := execution.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	ctx = execution.Context
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
		if err := CheckExecution(ctx); err != nil {
			return RoleRun{}, err
		}
		taskStatus, err := Status(ctx, wbStatus.Root, taskID)
		if err != nil {
			return RoleRun{}, err
		}
		packetContext := contextPacket(ctx, taskStatus, layout)
		if strings.TrimSpace(opts.ExternalContext) != "" {
			packetContext += "\nexternal_task_context:\n" + strings.TrimSpace(opts.ExternalContext) + "\n"
		}
		packet, err := model.BuildPacket(model.PacketInput{
			TaskID:  taskID,
			Role:    role,
			ModelID: opts.ModelID,
			Context: packetContext,
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
				role,
				taskStatus.Worktrees,
				store,
			),
			Fence: CheckExecution,
		}.Run(ctx, packet)
		if fenceErr := CheckExecution(ctx); fenceErr != nil {
			return RoleRun{}, fenceErr
		}
		if err != nil {
			_ = persistRawRoleStream(ctx, store, role, run.Raw, true)
			var repairErr model.RepairExhaustedError
			if !errors.As(err, &repairErr) || !completeMissingResultEditTurn(store, role, run.Parsed) {
				failed := roleRunFromModelRun(role, run, provider, opts.ModelID, append(allUsage, run.Usage...), totalAttempts+run.Attempts)
				failed.Status = "failed"
				if run.Parsed != nil {
					if persistErr := persistRoleAttempt(ctx, db, taskID, role, run, opts.Pricing); persistErr != nil {
						return failed, errors.Join(err, persistErr)
					}
				}
				if recordErr := recordRoleFailed(ctx, db, taskID, failed, err); recordErr != nil {
					return failed, errors.Join(err, recordErr)
				}
				return failed, err
			}
			if err := recordProtocolFallback(ctx, db, taskID, role, repairErr.ErrorCodes, "completed_missing_result_edit_turn"); err != nil {
				return RoleRun{}, err
			}
		}
		if err := persistRawRoleStream(ctx, store, role, run.Raw, false); err != nil {
			return RoleRun{}, err
		}
		if err := appendRoleReportProvenance(ctx, store, run, provider, opts.ModelID, packet); err != nil {
			return RoleRun{}, err
		}
		if err := persistRoleAttempt(ctx, db, taskID, role, run, opts.Pricing); err != nil {
			return RoleRun{}, err
		}
		allUsage = append(allUsage, run.Usage...)
		totalAttempts += run.Attempts
		sourceEditAttempt := sourceEditFailures + 1
		if err := CheckExecution(ctx); err != nil {
			return RoleRun{}, err
		}
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
			if err := CheckExecution(ctx); err != nil {
				return RoleRun{}, err
			}
			if _, err := executeProposal(ctx, db, layout, taskID, role, taskStatus.Worktrees, proposal); err != nil {
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
			return acceptRoleRun(ctx, root, taskID, db, store, run, blocked)
		}
		accepted := roleRunFromModelRun(role, run, provider, opts.ModelID, allUsage, totalAttempts)
		if err := CheckExecution(ctx); err != nil {
			return RoleRun{}, err
		}
		return acceptRoleRun(ctx, root, taskID, db, store, run, accepted)
	}
}

func acceptRoleRun(ctx context.Context, root, taskID string, db *state.DB, store artifact.Store, run model.RunResult, accepted RoleRun) (RoleRun, error) {
	if err := CheckExecution(ctx); err != nil {
		return RoleRun{}, err
	}
	guarded, err := applyReviewGuards(ctx, root, taskID, accepted)
	if err != nil {
		return RoleRun{}, err
	}
	if err := refreshRoleReportArtifact(ctx, db, taskID, store, guarded); err != nil {
		return RoleRun{}, err
	}
	history, err := loadRoleHistory(ctx, root, taskID)
	if err != nil {
		return RoleRun{}, err
	}
	if err := snapshotRoleAttempt(ctx, root, taskID, guarded, history.RoleCounts[guarded.Role]+1); err != nil {
		return RoleRun{}, err
	}
	if err := recordRoleCompleted(ctx, db, taskID, run, guarded); err != nil {
		return RoleRun{}, err
	}
	return guarded, nil
}

func refreshRoleReportArtifact(ctx context.Context, db *state.DB, taskID string, store artifact.Store, run RoleRun) error {
	if run.Artifact == "" {
		return nil
	}
	if err := CheckExecution(ctx); err != nil {
		return err
	}
	data, err := store.Read(run.Artifact)
	if err != nil {
		return err
	}
	rec, err := store.Put(artifact.Record{
		Path:         run.Artifact,
		Type:         artifact.TypeReport,
		State:        artifact.StateSealed,
		ProducerRole: run.Role.String(),
	}, data)
	if err != nil {
		return err
	}
	return db.UpdateArtifact(ctx, state.Artifact{
		ID:           artifactID(taskID, rec.Path),
		TaskID:       taskID,
		Type:         rec.Type,
		Path:         rec.Path,
		Checksum:     rec.Checksum,
		ProducerRole: run.Role.String(),
		State:        rec.State,
	})
}

func recordRoleCompleted(ctx context.Context, db *state.DB, taskID string, run model.RunResult, accepted RoleRun) error {
	if run.Parsed == nil || run.Parsed.Result == nil {
		return fmt.Errorf("accepted role %s has no result frame", accepted.Role)
	}
	result := *run.Parsed.Result
	result.Status = accepted.Status
	result.Artifact = accepted.Artifact
	payload, err := json.Marshal(map[string]any{
		"role":     accepted.Role.String(),
		"status":   result,
		"attempts": accepted.Attempts,
	})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "role.completed", Payload: string(payload)})
	return err
}

func roleRunFromModelRun(role model.Role, run model.RunResult, provider model.Provider, modelID string, usage []model.Usage, attempts int) RoleRun {
	status := ""
	artifactPath := ""
	if run.Parsed != nil && run.Parsed.Result != nil {
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

func recordRoleFailed(ctx context.Context, db *state.DB, taskID string, failed RoleRun, runErr error) error {
	payload, err := json.Marshal(map[string]any{
		"role": failed.Role.String(), "status": failed.Status, "artifact": failed.Artifact,
		"attempts": failed.Attempts, "input_tokens": failed.InputTokens, "output_tokens": failed.OutputTokens,
		"provider_id": failed.ProviderID, "model_id": failed.ModelID, "error": runErr.Error(),
		"failure_class": roleFailureClass(runErr),
	})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "role.failed", Payload: string(payload)})
	return err
}

func roleFailureClass(runErr error) string {
	var protocolErr model.RepairExhaustedError
	if errors.As(runErr, &protocolErr) {
		return "protocol"
	}
	if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
		return "canceled"
	}
	return "runtime"
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

func persistRoleAttempt(ctx context.Context, db *state.DB, taskID string, role model.Role, run model.RunResult, pricing cost.Pricing) error {
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
	for _, normalization := range run.Parsed.Normalizations {
		payload, err := json.Marshal(map[string]any{
			"role": role.String(), "code": normalization.Code,
			"message": normalization.Message, "line": normalization.Line,
			"attempts": run.Attempts,
		})
		if err != nil {
			return err
		}
		if _, err := db.InsertEvent(ctx, state.Event{
			TaskID:  taskID,
			Type:    "role.protocol_normalized",
			Payload: string(payload),
		}); err != nil {
			return err
		}
	}
	return nil
}

func nextUsageOrdinal(ctx context.Context, db *state.DB, taskID, role string) (int, error) {
	var count int
	err := db.Conn().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM usage_records
WHERE task_id = ? AND role = ?`, taskID, role).Scan(&count)
	return count, err
}

func persistRawRoleStream(ctx context.Context, store artifact.Store, role model.Role, raw string, failed bool) error {
	if err := CheckExecution(ctx); err != nil {
		return err
	}
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

func snapshotRoleAttempt(ctx context.Context, root, taskID string, run RoleRun, ordinal int) error {
	if err := CheckExecution(ctx); err != nil {
		return err
	}
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

func appendRoleReportProvenance(ctx context.Context, store artifact.Store, run model.RunResult, provider model.Provider, modelID string, packet model.Packet) error {
	if err := CheckExecution(ctx); err != nil {
		return err
	}
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

func commandContinuationHandler(db *state.DB, layout workbench.Layout, taskID string, role model.Role, worktrees []WorktreeStatus, store artifact.Store) model.CommandHandler {
	return func(ctx context.Context, commands []stream.CommandProposal) (string, error) {
		var b strings.Builder
		for i, proposal := range commands {
			result, err := executeProposal(ctx, db, layout, taskID, role, worktrees, proposal)
			if err != nil {
				var denied policy.CommandDeniedError
				if errors.As(err, &denied) {
					if i > 0 {
						b.WriteByte('\n')
					}
					if recordErr := recordCommandRejected(ctx, db, taskID, role, proposal, denied.Reason); recordErr != nil {
						return "", recordErr
					}
					fmt.Fprintf(&b, "command repo:%s rejected:true reason:%s\n", proposal.Repo, singleLine(denied.Reason))
					continue
				}
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

func recordCommandRejected(ctx context.Context, db *state.DB, taskID string, role model.Role, proposal stream.CommandProposal, reason string) error {
	payload, err := json.Marshal(map[string]string{
		"role":    role.String(),
		"repo_id": proposal.Repo,
		"command": proposal.Command,
		"reason":  reason,
	})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "command.rejected", Payload: string(payload)})
	return err
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
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

func executeProposal(ctx context.Context, db *state.DB, layout workbench.Layout, taskID string, role model.Role, worktrees []WorktreeStatus, proposal stream.CommandProposal) (command.Result, error) {
	wt, err := worktreeForRepo(worktrees, proposal.Repo)
	if err != nil {
		return command.Result{}, err
	}
	artifactDir := filepath.Join(layout.Artifacts, taskID)
	commandRoot := wt.Path
	commandPolicy := policy.DefaultCommandPolicy(wt.Path, artifactDir)
	cleanup := func() {}
	if role == model.RoleReviewer {
		commandPolicy = policy.ReadOnlyCommandPolicy(wt.Path, artifactDir)
		if err := commandPolicy.ValidateCommand(proposal.Command); err != nil {
			return command.Result{}, err
		}
		snapshotParent, err := os.MkdirTemp("", "midgard-review-")
		if err != nil {
			return command.Result{}, err
		}
		commandRoot = filepath.Join(snapshotParent, "worktree")
		if err := gitrepo.AddSnapshotWorktree(ctx, wt.Path, commandRoot); err != nil {
			_ = os.RemoveAll(snapshotParent)
			return command.Result{}, err
		}
		cleanup = func() {
			_ = gitrepo.RemoveSnapshotWorktree(context.WithoutCancel(ctx), wt.Path, commandRoot)
			_ = os.RemoveAll(snapshotParent)
		}
		commandPolicy = policy.ReadOnlyCommandPolicy(commandRoot, artifactDir)
	}
	defer cleanup()
	executor := command.NewExecutor(commandPolicy)
	result, err := executor.Run(ctx, command.Request{
		TaskID:      taskID,
		RepoID:      wt.RepoID,
		Command:     proposal.Command,
		CWD:         commandRoot,
		ArtifactDir: artifactDir,
		Fence:       CheckExecution,
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
	if err := CheckExecution(ctx); err != nil {
		return "", false, err
	}
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
	if err := CheckExecution(ctx); err != nil {
		return "", false, err
	}
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

func finalState(history roleHistory, patchHasContent bool) string {
	if !history.HasLatest {
		return "open"
	}
	last := history.Latest
	if last.Role == model.RoleReviewer && last.Status == string(review.VerdictApproved) {
		if (history.HasLatestImplementer && history.LatestImplementer.Status == "no-op") || patchHasContent {
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

func taskCostSummary(ctx context.Context, root, taskID string) (float64, []string, error) {
	status, err := workbench.Status(root)
	if err != nil {
		return 0, nil, err
	}
	db, err := state.Open(ctx, workbench.NewLayout(status.Root).State)
	if err != nil {
		return 0, nil, err
	}
	defer db.Close()
	rollups, err := db.CostRollupsForTask(ctx, taskID)
	if err != nil {
		return 0, nil, err
	}
	var amount float64
	var caveats []string
	seenCaveats := map[string]bool{}
	for _, rollup := range rollups {
		value, err := strconv.ParseFloat(rollup.AmountUSD, 64)
		if err != nil {
			return 0, nil, fmt.Errorf("parse cost rollup %s: %w", rollup.ID, err)
		}
		amount += value
		if rollup.Caveats != "" && !seenCaveats[rollup.Caveats] {
			seenCaveats[rollup.Caveats] = true
			caveats = append(caveats, rollup.Caveats)
		}
	}
	return amount, caveats, nil
}

func appendTaskRunSummary(ctx context.Context, root, taskID string, loop LoopResult) error {
	if err := CheckExecution(ctx); err != nil {
		return err
	}
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
	appendCostSummaryLine(&b, loop.CostUSD, loop.CostCaveats)
	b.WriteString("\n")
	if loop.Error != "" {
		fmt.Fprintf(&b, "- run_error: %s\n\n", strings.ReplaceAll(loop.Error, "\n", " "))
	}
	roleRuns, err := taskRoleRunSummary(ctx, status.Root, taskID)
	if err != nil {
		return err
	}
	for _, run := range roleRuns {
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
	if err := CheckExecution(ctx); err != nil {
		return err
	}
	return replaceTaskReportSummary(path, b.String())
}

func taskRoleRunSummary(ctx context.Context, root, taskID string) ([]RoleRun, error) {
	db, err := state.Open(ctx, filepath.Join(root, ".midgard", "state.sqlite"))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	events, err := db.EventsForTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	latest := map[model.Role]roleStatus{}
	for _, event := range events {
		if event.Type != "role.completed" {
			continue
		}
		completed, ok := parseRoleCompletedEvent(event.Payload)
		if ok {
			latest[completed.Role] = completed
		}
	}
	usage, err := db.UsageRecordsForTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	byRole := map[model.Role]*RoleRun{}
	for _, record := range usage {
		role := model.Role(record.Role)
		run := byRole[role]
		if run == nil {
			run = &RoleRun{Role: role}
			byRole[role] = run
		}
		run.ProviderID = record.ProviderID
		run.ModelID = record.ModelID
		run.ProviderFingerprint = model.ProviderFingerprint(roleProviderIdentity(record.ProviderID), record.ModelID)
		run.InputTokens += record.InputTokens
		run.OutputTokens += record.OutputTokens
	}
	orderedRoles := []model.Role{model.RolePlanner, model.RoleImplementer, model.RoleReviewer, model.RoleCompactor}
	runs := make([]RoleRun, 0, len(latest))
	for _, role := range orderedRoles {
		completed, ok := latest[role]
		if !ok {
			continue
		}
		run := byRole[role]
		if run == nil {
			run = &RoleRun{Role: role}
		}
		run.Status = completed.Status
		run.Artifact = completed.Artifact
		runs = append(runs, *run)
	}
	return runs, nil
}

type roleProviderIdentity string

func (p roleProviderIdentity) ID() string {
	return string(p)
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
	summaries := map[string]*sourceEditFileEventSummary{}
	var appliedTotal, failedTotal, partialTotal, normalizedTotal, repairTotal int
	for _, event := range events {
		switch event.Type {
		case "source_edit.apply_failed":
			var failed sourceEditApplyFailedEvent
			if err := json.Unmarshal([]byte(event.Payload), &failed); err != nil {
				continue
			}
			summary := sourceEditFileSummary(summaries, failed.File)
			summary.failed++
			summary.lastFailure = failed
			summary.hasFailure = true
			summary.lastFailureID = event.ID
			failedTotal++
			if failed.PartialApplied {
				summary.partial++
				partialTotal++
			}
		case "source_edit.repair_requested":
			repairTotal++
		case "source_edit.normalized":
			var normalized sourceEditNormalizedEvent
			if err := json.Unmarshal([]byte(event.Payload), &normalized); err != nil {
				continue
			}
			summary := sourceEditFileSummary(summaries, normalized.File)
			summary.normalized++
			normalizedTotal++
		case "source_edit.applied":
			var applied sourceEditAppliedEvent
			if err := json.Unmarshal([]byte(event.Payload), &applied); err != nil {
				continue
			}
			summary := sourceEditFileSummary(summaries, applied.File)
			summary.applied++
			summary.lastContent = applied.Content
			summary.lastAppliedID = event.ID
			appliedTotal++
		}
	}
	if appliedTotal == 0 && failedTotal == 0 && repairTotal == 0 {
		return "", nil
	}
	files := make([]string, 0, len(summaries))
	for file := range summaries {
		files = append(files, file)
	}
	sort.Strings(files)
	var b strings.Builder
	fmt.Fprintf(
		&b,
		"- totals: applied:%d failed_apply:%d partial_apply:%d normalized:%d repair_requests:%d files:%d\n",
		appliedTotal,
		failedTotal,
		partialTotal,
		normalizedTotal,
		repairTotal,
		len(files),
	)
	if appliedTotal > 0 {
		b.WriteString("- outcome: source edits survived in the worktree; see patch artifact for final diff\n")
	} else if failedTotal > 0 {
		b.WriteString("- outcome: no source edits applied; inspect failed patch diagnostics\n")
	}
	b.WriteString("- files:\n")
	for _, file := range files {
		summary := summaries[file]
		fmt.Fprintf(
			&b,
			"  - file:%s applied:%d failed_apply:%d partial_apply:%d normalized:%d",
			file,
			summary.applied,
			summary.failed,
			summary.partial,
			summary.normalized,
		)
		if summary.lastContent != "" {
			fmt.Fprintf(&b, " last_applied:%s", summary.lastContent)
		}
		if summary.hasFailure {
			failed := summary.lastFailure
			fmt.Fprintf(
				&b,
				" last_failure_attempt:%d partial_applied:%t remaining_repairs:%d refs:patch:%s stderr:%s context:%s",
				failed.Attempt,
				failed.PartialApplied,
				failed.RemainingRepairs,
				artifactRef(failed.FailedPatchArtifact),
				artifactRef(failed.StderrArtifact),
				artifactRef(failed.SourceContextArtifact),
			)
		}
		if summary.hasFailure && summary.lastFailureID > summary.lastAppliedID {
			b.WriteString(" unresolved:true")
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

type sourceEditFileEventSummary struct {
	applied       int
	failed        int
	partial       int
	normalized    int
	lastContent   string
	lastFailure   sourceEditApplyFailedEvent
	hasFailure    bool
	lastAppliedID int64
	lastFailureID int64
}

func sourceEditFileSummary(summaries map[string]*sourceEditFileEventSummary, file string) *sourceEditFileEventSummary {
	if file == "" {
		file = "(unknown)"
	}
	summary := summaries[file]
	if summary == nil {
		summary = &sourceEditFileEventSummary{}
		summaries[file] = summary
	}
	return summary
}

func appendCostSummaryLine(b *strings.Builder, amount float64, caveats []string) {
	if len(caveats) > 0 {
		b.WriteString("- cost: unknown")
		b.WriteString(" (")
		b.WriteString(strings.Join(caveats, "; "))
		b.WriteString(")")
		return
	}
	b.WriteString("- cost: $")
	b.WriteString(strconv.FormatFloat(amount, 'f', 6, 64))
}

func replaceTaskReportSummary(path, summary string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	base := strings.TrimRight(string(data), "\n")
	cut := len(base)
	for _, marker := range []string{"\n\n## Midgard Run Summary", "\n\n## Source Edit Summary"} {
		if idx := strings.Index(base, marker); idx >= 0 && idx < cut {
			cut = idx
		}
	}
	base = strings.TrimRight(base[:cut], "\n")
	if base != "" {
		base += "\n\n"
	}
	return os.WriteFile(path, []byte(base+strings.TrimLeft(summary, "\n")), 0o644)
}

func artifactID(taskID, path string) string {
	replacer := strings.NewReplacer("/", "_", ".", "_", ":", "_")
	return taskID + "_" + replacer.Replace(path)
}
