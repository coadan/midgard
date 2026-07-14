package benchmark

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"midgard/internal/cost"
	"midgard/internal/gitrepo"
	"midgard/internal/model"
	"midgard/internal/state"
	"midgard/internal/stream"
	midgardtask "midgard/internal/task"
	"midgard/internal/workbench"
)

type ProviderFactory func(Item) (midgardtask.RoleProviders, string, cost.Pricing, error)

type SuiteOptions struct {
	ProviderFactory      ProviderFactory
	ProviderOptions      string
	Budget               stream.Budget
	MaxReviewCycles      int
	MaxSourceEditRepairs int
	Acceptance           AcceptanceOptions
	ResetTasks           bool
}

type SuiteResult struct {
	Report        Report
	PreparedRepos []PreparedRepo
	TaskRuns      []SuiteTaskRun
	RunID         string
	RunStatus     string
}

type PreparedRepo struct {
	ID          string
	Path        string
	CheckoutRef string
	StartCommit string
}

type SuiteTaskRun struct {
	ItemID     string
	TaskID     string
	State      string
	CostUSD    float64
	Error      string
	ErrorClass string
	Action     string
}

func RunSuite(ctx context.Context, root string, manifest Manifest, opts SuiteOptions) (result SuiteResult, retErr error) {
	if manifest.ID == "" {
		return SuiteResult{}, fmt.Errorf("manifest id is required")
	}
	if opts.ProviderFactory == nil {
		return SuiteResult{}, fmt.Errorf("provider factory is required")
	}
	init, err := workbench.Init(root, workbench.InitOptions{Name: manifest.ID})
	if err != nil {
		return SuiteResult{}, err
	}
	normalizeSuiteManifest(&manifest, root)
	manifestChecksum, err := benchmarkManifestChecksum(manifest)
	if err != nil {
		return SuiteResult{}, err
	}
	executions, executionChecksum, executionJSON, err := prepareSuiteExecutions(manifest, opts)
	if err != nil {
		return SuiteResult{}, err
	}
	layout := workbench.NewLayout(init.Root)
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return SuiteResult{}, err
	}
	defer db.Close()
	benchmarkExecution, err := acquireBenchmarkExecutionWithDB(ctx, db, manifest.ID)
	if err != nil {
		return SuiteResult{}, err
	}
	defer func() {
		if err := benchmarkExecution.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	ctx = benchmarkExecution.Context
	if opts.ResetTasks {
		if err := resetBenchmarkSuite(ctx, init.Root, db, manifest); err != nil {
			return SuiteResult{}, err
		}
	}
	taskExecutions, err := acquireSuiteTaskExecutions(ctx, init.Root, manifest.Items)
	if err != nil {
		return SuiteResult{}, err
	}
	defer func() {
		if err := closeSuiteTaskExecutions(taskExecutions); retErr == nil && err != nil {
			retErr = err
		}
	}()
	existingRun, existingErr := db.BenchmarkRunByManifest(ctx, manifest.ID)
	if existingErr != nil && !state.IsNoBenchmarkRun(existingErr) {
		return SuiteResult{}, existingErr
	}
	if existingErr == nil {
		if existingRun.ManifestChecksum != manifestChecksum {
			return SuiteResult{}, fmt.Errorf("benchmark manifest drift for %q; use --reset to start a new run", manifest.ID)
		}
		if existingRun.ExecutionChecksum != executionChecksum {
			return SuiteResult{}, fmt.Errorf("benchmark provider/model/options drift for %q; use --reset to start a new run", manifest.ID)
		}
	}
	preparedRepos, err := prepareSuiteRepos(ctx, init.Root, manifest)
	if err != nil {
		return SuiteResult{}, err
	}
	var run state.BenchmarkRun
	var runItems []state.BenchmarkRunItem
	if existingErr == nil {
		run = existingRun
		runRepos, err := db.BenchmarkRunRepos(ctx, run.ID)
		if err != nil {
			return SuiteResult{}, err
		}
		runItems, err = db.BenchmarkRunItems(ctx, run.ID)
		if err != nil {
			return SuiteResult{}, err
		}
		if err := validateBenchmarkRun(run, runRepos, runItems, manifest, manifestChecksum, executionChecksum, preparedRepos); err != nil {
			return SuiteResult{}, err
		}
		if err := markBenchmarkRun(ctx, db, run.ID, "running", false); err != nil {
			return SuiteResult{}, err
		}
		run.Status = "running"
	} else {
		if err := ensureNoLegacyBenchmarkTasks(ctx, db, manifest); err != nil {
			return SuiteResult{}, err
		}
		newRun, runRepos, items := newBenchmarkRun(manifest, manifestChecksum, executionChecksum, executionJSON, preparedRepos)
		run = newRun
		if err := db.InsertBenchmarkRun(ctx, run, runRepos, items); err != nil {
			return SuiteResult{}, err
		}
		runItems = items
	}
	runActive := true
	defer func() {
		if retErr == nil || !runActive {
			return
		}
		status := "error"
		if ctx.Err() != nil {
			status = "interrupted"
		}
		cleanupCtx := context.WithoutCancel(ctx)
		if checkBenchmarkExecution(cleanupCtx) == nil {
			_ = markBenchmarkRun(cleanupCtx, db, run.ID, status, false)
		}
	}()

	itemsByID := make(map[string]state.BenchmarkRunItem, len(runItems))
	for _, item := range runItems {
		itemsByID[item.ItemID] = item
	}
	results := make([]ItemResult, 0, len(manifest.Items))
	taskRuns := make([]SuiteTaskRun, 0, len(manifest.Items))
	runIncomplete := false
	for _, item := range manifest.Items {
		runItem := itemsByID[item.ID]
		scored, taskRun, updatedItem, incomplete, err := runSuiteItem(
			taskExecutions[item.TaskID].Context, init.Root, db, layout, item, runItem, preparedRepos, executions[item.ID], opts,
		)
		if err != nil {
			return SuiteResult{}, err
		}
		itemsByID[item.ID] = updatedItem
		results = append(results, scored)
		taskRuns = append(taskRuns, taskRun)
		runIncomplete = runIncomplete || incomplete
	}
	runStatus := "completed"
	if runIncomplete {
		runStatus = "incomplete"
	}
	if err := checkBenchmarkExecution(ctx); err != nil {
		return SuiteResult{}, err
	}
	if err := markBenchmarkRun(ctx, db, run.ID, runStatus, runStatus == "completed"); err != nil {
		return SuiteResult{}, err
	}
	run.Status = runStatus
	if err := checkBenchmarkExecution(ctx); err != nil {
		return SuiteResult{}, err
	}
	report, err := WriteReportForRun(init.Root, manifest, results, run)
	if err != nil {
		return SuiteResult{}, err
	}
	runActive = false
	return SuiteResult{
		Report: report, PreparedRepos: preparedRepos, TaskRuns: taskRuns,
		RunID: run.ID, RunStatus: runStatus,
	}, nil
}

func runSuiteItem(
	ctx context.Context,
	root string,
	db *state.DB,
	layout workbench.Layout,
	item Item,
	runItem state.BenchmarkRunItem,
	preparedRepos []PreparedRepo,
	execution suiteItemExecution,
	opts SuiteOptions,
) (scored ItemResult, taskRun SuiteTaskRun, updatedItem state.BenchmarkRunItem, incomplete bool, retErr error) {
	if err := midgardtask.CheckExecution(ctx); err != nil {
		return ItemResult{}, SuiteTaskRun{}, runItem, false, err
	}
	created, err := ensureBenchmarkTask(ctx, root, db, item, runItem, preparedRepos)
	if err != nil {
		return ItemResult{}, SuiteTaskRun{}, runItem, false, err
	}
	action := "resumed"
	if created {
		action = "created"
	}
	runRoleLoop := runItem.Status != "completed" && runItem.Phase != "acceptance" && runItem.Phase != "score"
	var loop midgardtask.LoopResult
	if runRoleLoop {
		runItem, err = updateBenchmarkItem(ctx, db, runItem, "role_loop", "running", "", "", "", false)
		if err != nil {
			return ItemResult{}, SuiteTaskRun{}, runItem, false, err
		}
		loop, err = midgardtask.RunLoop(ctx, root, item.TaskID, midgardtask.RunnerOptions{
			ModelID: execution.ModelID, Providers: execution.Providers, Budget: opts.Budget, Pricing: execution.Pricing,
			MaxReviewCycles: opts.MaxReviewCycles, MaxSourceEditRepairs: opts.MaxSourceEditRepairs,
			ExternalContext: benchmarkExternalContext(item),
		})
		if err != nil {
			if ctx.Err() != nil {
				cleanupCtx := context.WithoutCancel(ctx)
				if fenceErr := midgardtask.CheckExecution(cleanupCtx); fenceErr != nil {
					return ItemResult{}, SuiteTaskRun{}, runItem, false, fenceErr
				}
				_, _ = updateBenchmarkItem(cleanupCtx, db, runItem, "role_loop", "interrupted", "canceled", context.Cause(ctx).Error(), "", false)
				return ItemResult{}, SuiteTaskRun{}, runItem, false, context.Cause(ctx)
			}
			errorClass := suiteErrorClass("role_loop", err)
			runItem, _ = updateBenchmarkItem(ctx, db, runItem, "role_loop", "error", errorClass, err.Error(), "", false)
			_ = recordSuiteItemError(ctx, root, item, "role_loop", errorClass, err)
			scored, scoreErr := ScoreItem(ctx, root, item)
			if scoreErr != nil {
				return ItemResult{}, SuiteTaskRun{}, runItem, false, scoreErr
			}
			scored.Evidence.RunError = err.Error()
			scored.Evidence.RunErrorClass = errorClass
			return scored, SuiteTaskRun{
				ItemID: item.ID, TaskID: item.TaskID, State: "error", CostUSD: loop.CostUSD,
				Error: err.Error(), ErrorClass: errorClass, Action: action,
			}, runItem, true, nil
		}
		runItem, err = updateBenchmarkItem(ctx, db, runItem, "acceptance", "pending", "", "", "", false)
		if err != nil {
			return ItemResult{}, SuiteTaskRun{}, runItem, false, err
		}
		if err := clearSuiteItemError(ctx, root, item); err != nil {
			return ItemResult{}, SuiteTaskRun{}, runItem, false, err
		}
	} else if runItem.Status == "completed" {
		action = "reused"
	}

	acceptanceRan := false
	if hasAcceptanceChecks(item) && taskHasNonEmptyPatch(root, item.TaskID) {
		verification, err := currentAcceptanceVerification(ctx, db, layout, item)
		if err != nil {
			return ItemResult{}, SuiteTaskRun{}, runItem, false, err
		}
		if !verification.Valid {
			if !created {
				action = "resumed"
			}
			acceptanceRan = true
			runItem, err = updateBenchmarkItem(ctx, db, runItem, "acceptance", "running", "", "", "", false)
			if err != nil {
				return ItemResult{}, SuiteTaskRun{}, runItem, false, err
			}
			if _, err := RunAcceptanceChecks(ctx, root, item, opts.Acceptance); err != nil {
				if ctx.Err() != nil {
					cleanupCtx := context.WithoutCancel(ctx)
					if fenceErr := midgardtask.CheckExecution(cleanupCtx); fenceErr != nil {
						return ItemResult{}, SuiteTaskRun{}, runItem, false, fenceErr
					}
					_, _ = updateBenchmarkItem(cleanupCtx, db, runItem, "acceptance", "interrupted", "canceled", context.Cause(ctx).Error(), "", false)
					return ItemResult{}, SuiteTaskRun{}, runItem, false, context.Cause(ctx)
				}
				errorClass := suiteErrorClass("acceptance", err)
				runItem, _ = updateBenchmarkItem(ctx, db, runItem, "acceptance", "error", errorClass, err.Error(), "", false)
				_ = recordSuiteItemError(ctx, root, item, "acceptance", errorClass, err)
				scored, scoreErr := ScoreItem(ctx, root, item)
				if scoreErr != nil {
					return ItemResult{}, SuiteTaskRun{}, runItem, false, scoreErr
				}
				scored.Evidence.RunError = err.Error()
				scored.Evidence.RunErrorClass = errorClass
				return scored, SuiteTaskRun{
					ItemID: item.ID, TaskID: item.TaskID, State: "error", CostUSD: scored.Evidence.CostUSD,
					Error: err.Error(), ErrorClass: errorClass, Action: action,
				}, runItem, true, nil
			}
			verification, err = currentAcceptanceVerification(ctx, db, layout, item)
			if err != nil {
				return ItemResult{}, SuiteTaskRun{}, runItem, false, err
			}
			if !verification.Valid {
				runErr := fmt.Errorf("acceptance evidence remains invalid: %s", verification.Reason)
				runItem, _ = updateBenchmarkItem(ctx, db, runItem, "acceptance", "error", "acceptance", runErr.Error(), "", false)
				_ = recordSuiteItemError(ctx, root, item, "acceptance", "acceptance", runErr)
				scored, scoreErr := ScoreItem(ctx, root, item)
				if scoreErr != nil {
					return ItemResult{}, SuiteTaskRun{}, runItem, false, scoreErr
				}
				scored.Evidence.RunError = runErr.Error()
				scored.Evidence.RunErrorClass = "acceptance"
				return scored, SuiteTaskRun{
					ItemID: item.ID, TaskID: item.TaskID, State: "error", CostUSD: scored.Evidence.CostUSD,
					Error: runErr.Error(), ErrorClass: "acceptance", Action: action,
				}, runItem, true, nil
			}
		}
	}
	if acceptanceRan {
		if err := clearSuiteItemError(ctx, root, item); err != nil {
			return ItemResult{}, SuiteTaskRun{}, runItem, false, err
		}
	}
	if err := midgardtask.CheckExecution(ctx); err != nil {
		return ItemResult{}, SuiteTaskRun{}, runItem, false, err
	}
	scored, err = ScoreItem(ctx, root, item)
	if err != nil {
		return ItemResult{}, SuiteTaskRun{}, runItem, false, err
	}
	runItem, err = updateBenchmarkItem(ctx, db, runItem, "score", "completed", "", "", string(scored.Score), true)
	if err != nil {
		return ItemResult{}, SuiteTaskRun{}, runItem, false, err
	}
	return scored, SuiteTaskRun{
		ItemID: item.ID, TaskID: item.TaskID, State: scored.Evidence.TaskState,
		CostUSD: scored.Evidence.CostUSD, Action: action,
	}, runItem, false, nil
}

func acquireSuiteTaskExecutions(ctx context.Context, root string, items []Item) (map[string]*midgardtask.Execution, error) {
	taskIDs := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if !seen[item.TaskID] {
			seen[item.TaskID] = true
			taskIDs = append(taskIDs, item.TaskID)
		}
	}
	sort.Strings(taskIDs)
	executions := make(map[string]*midgardtask.Execution, len(taskIDs))
	for _, taskID := range taskIDs {
		execution, err := midgardtask.AcquireExecution(ctx, root, taskID)
		if err != nil {
			return nil, errors.Join(err, closeSuiteTaskExecutions(executions))
		}
		executions[taskID] = execution
	}
	return executions, nil
}

func closeSuiteTaskExecutions(executions map[string]*midgardtask.Execution) error {
	taskIDs := make([]string, 0, len(executions))
	for taskID := range executions {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(taskIDs)))
	var closeErr error
	for _, taskID := range taskIDs {
		closeErr = errors.Join(closeErr, executions[taskID].Close())
	}
	return closeErr
}

func recordSuiteItemError(ctx context.Context, root string, item Item, phase, errorClass string, runErr error) error {
	db, err := state.Open(ctx, workbench.NewLayout(root).State)
	if err != nil {
		return err
	}
	defer db.Close()
	payload, err := json.Marshal(map[string]string{
		"item_id": item.ID, "phase": phase, "error_class": errorClass, "error": runErr.Error(),
	})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: item.TaskID, Type: "benchmark.item.error", Payload: string(payload)})
	return err
}

func suiteErrorClass(phase string, runErr error) string {
	if phase == "acceptance" {
		return "acceptance"
	}
	var protocolErr model.RepairExhaustedError
	if errors.As(runErr, &protocolErr) {
		return "protocol"
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return "canceled"
	}
	return "harness"
}

func benchmarkExternalContext(item Item) string {
	worker := WorkerContext(item)
	data, err := json.Marshal(worker)
	if err != nil {
		return ""
	}
	checkIDs := make([]string, 0, len(worker.Checks))
	for _, check := range worker.Checks {
		checkIDs = append(checkIDs, check.ID)
	}
	return string(data) + "\nresult_field_rule: In @result checks:, use only comma-separated check ids with no spaces. Valid ids: " + strings.Join(checkIDs, ",") + ". Never place shell command text in a tagged-frame field."
}

func taskHasNonEmptyPatch(root, taskID string) bool {
	info, err := os.Stat(filepath.Join(workbench.NewLayout(root).Artifacts, taskID, "patch.diff"))
	return err == nil && info.Size() > 0
}

func prepareSuiteRepos(ctx context.Context, root string, manifest Manifest) ([]PreparedRepo, error) {
	if len(manifest.Repos) == 0 {
		return nil, fmt.Errorf("benchmark suite manifest requires repos")
	}
	status, err := workbench.Status(root)
	if err != nil {
		return nil, err
	}
	layout := workbench.NewLayout(status.Root)
	prepared := make([]PreparedRepo, 0, len(manifest.Repos))
	for _, repo := range manifest.Repos {
		if strings.TrimSpace(repo.ID) == "" {
			return nil, fmt.Errorf("repo id is required")
		}
		source, err := repoSource(repo, manifest.BaseDir)
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", repo.ID, err)
		}
		checkoutRef := repo.CheckoutRef
		if checkoutRef == "" {
			checkoutRef = repo.MainRef
		}
		if checkoutRef == "" {
			checkoutRef = "HEAD"
		}
		target := filepath.Join(layout.Benchmarks, manifest.ID, "sources", repo.ID)
		if err := cloneOrFetchRepo(ctx, source, target); err != nil {
			return nil, fmt.Errorf("repo %s: %w", repo.ID, err)
		}
		if _, err := gitrepo.Run(ctx, target, "checkout", "--detach", checkoutRef); err != nil {
			return nil, fmt.Errorf("repo %s checkout %s: %w", repo.ID, checkoutRef, err)
		}
		startCommit, err := gitrepo.CurrentCommit(ctx, target)
		if err != nil {
			return nil, err
		}
		if _, err := workbench.AddRepo(status.Root, workbench.AddRepoOptions{ID: repo.ID, Path: target, MainRef: startCommit}); err != nil {
			return nil, err
		}
		prepared = append(prepared, PreparedRepo{ID: repo.ID, Path: target, CheckoutRef: checkoutRef, StartCommit: startCommit})
	}
	return prepared, nil
}

func cloneOrFetchRepo(ctx context.Context, source, target string) error {
	if _, err := os.Stat(filepath.Join(target, ".git")); err == nil {
		if _, err := gitrepo.Run(ctx, target, "fetch", "--all", "--tags", "--prune"); err != nil {
			return err
		}
		if _, err := gitrepo.Run(ctx, target, "reset", "--hard"); err != nil {
			return err
		}
		if _, err := gitrepo.Run(ctx, target, "clean", "-ffd"); err != nil {
			return err
		}
		return nil
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return gitrepo.Clone(ctx, source, target)
}

func repoSource(repo RepoSource, baseDir string) (string, error) {
	source := strings.TrimSpace(repo.URL)
	if source == "" {
		source = strings.TrimSpace(repo.Path)
	}
	if source == "" {
		return "", fmt.Errorf("url or path is required")
	}
	if repo.URL == "" && !filepath.IsAbs(source) && baseDir != "" {
		source = filepath.Join(baseDir, source)
	}
	return source, nil
}

func resetTaskIfRequested(ctx context.Context, root, taskID string, reset bool) error {
	if !reset {
		return nil
	}
	wbStatus, err := workbench.Status(root)
	if err != nil {
		return err
	}
	layout := workbench.NewLayout(wbStatus.Root)
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Task(ctx, taskID); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	worktrees, err := db.WorktreesForTask(ctx, taskID)
	if err != nil {
		return err
	}
	for _, wt := range worktrees {
		if _, err := os.Stat(wt.Path); err == nil {
			if _, err := gitrepo.Run(ctx, wt.Path, "worktree", "remove", "--force", wt.Path); err != nil {
				return err
			}
		}
		repo, err := db.Repo(ctx, wt.RepoID)
		if err == nil {
			_, _ = gitrepo.Run(ctx, repo.Path, "branch", "-D", fmt.Sprintf("midgard/%s/%s", taskID, wt.RepoID))
		}
	}
	if err := os.RemoveAll(filepath.Join(layout.Artifacts, taskID)); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(layout.Tasks, taskID+".mdx")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return db.DeleteTaskCascade(ctx, taskID)
}

func validateLoopProviders(providers midgardtask.RoleProviders) error {
	for _, role := range []model.Role{model.RolePlanner, model.RoleImplementer, model.RoleReviewer} {
		if providers[role] == nil {
			return fmt.Errorf("provider missing for %s", role)
		}
	}
	return nil
}

func repoIDs(repos []RepoSource) []string {
	ids := make([]string, 0, len(repos))
	for _, repo := range repos {
		if repo.ID != "" {
			ids = append(ids, repo.ID)
		}
	}
	return ids
}

func baseDirOrRoot(baseDir, root string) string {
	if baseDir != "" {
		return baseDir
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return root
	}
	return abs
}

var benchmarkTaskIDPattern = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func benchmarkTaskID(manifestID, itemID string) string {
	value := "bench_" + manifestID + "_" + itemID
	value = benchmarkTaskIDPattern.ReplaceAllString(value, "_")
	value = strings.Trim(value, "._-")
	if value == "" {
		return "bench_task"
	}
	return value
}
