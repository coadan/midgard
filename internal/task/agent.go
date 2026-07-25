package task

import (
	"context"
	"errors"

	"midgard/internal/model"
)

// RunAgent executes the normal Midgard coding path: one implementer agent owns
// discovery, editing, and verification for the task. Multi-role workflows
// remain available through RunLoop for benchmarks and explicitly requested
// review workflows.
func RunAgent(ctx context.Context, root, taskID string, opts RunnerOptions) (result LoopResult, retErr error) {
	execution, err := AcquireExecution(ctx, root, taskID)
	if err != nil {
		return LoopResult{}, err
	}
	defer func() {
		if err := execution.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	ctx = execution.Context

	run, runErr := RunRole(ctx, root, taskID, model.RoleImplementer, opts)
	result.TaskID = taskID
	if run.Role != "" {
		result.RoleRuns = append(result.RoleRuns, run)
	}

	status, statusErr := Status(ctx, root, taskID)
	if statusErr != nil {
		return result, errors.Join(runErr, statusErr)
	}
	patchPath, patchHasContent, patchErr := writePatch(ctx, root, taskID, status.Worktrees)
	if patchErr != nil {
		return result, errors.Join(runErr, patchErr)
	}
	result.PatchPath = patchPath
	result.State = agentFinalState(run, patchHasContent, runErr)
	if runErr != nil {
		result.Error = runErr.Error()
	}
	if err := updateTaskState(ctx, root, taskID, result.State); err != nil {
		return result, errors.Join(runErr, err)
	}
	result.CostUSD, result.CostCaveats, err = taskCostSummary(ctx, root, taskID)
	if err != nil {
		return result, errors.Join(runErr, err)
	}
	if err := appendTaskRunSummary(ctx, root, taskID, result); err != nil {
		return result, errors.Join(runErr, err)
	}
	return result, runErr
}

func agentFinalState(run RoleRun, patchHasContent bool, runErr error) string {
	if runErr != nil || run.Status == "failed" {
		return "failed"
	}
	switch run.Status {
	case "no-op":
		return "completed"
	case "ready":
		if patchHasContent {
			return "completed"
		}
		return "blocked"
	case "blocked":
		return "blocked"
	default:
		return "failed"
	}
}
