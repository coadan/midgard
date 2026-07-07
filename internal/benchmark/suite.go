package benchmark

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	Budget               stream.Budget
	MaxReviewCycles      int
	MaxSourceEditRepairs int
	ResetTasks           bool
}

type SuiteResult struct {
	Report        Report
	PreparedRepos []PreparedRepo
	TaskRuns      []SuiteTaskRun
}

type PreparedRepo struct {
	ID          string
	Path        string
	CheckoutRef string
	StartCommit string
}

type SuiteTaskRun struct {
	ItemID  string
	TaskID  string
	State   string
	CostUSD float64
}

func RunSuite(ctx context.Context, root string, manifest Manifest, opts SuiteOptions) (SuiteResult, error) {
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

	preparedRepos, err := prepareSuiteRepos(ctx, init.Root, manifest)
	if err != nil {
		return SuiteResult{}, err
	}

	taskRuns := make([]SuiteTaskRun, 0, len(manifest.Items))
	results := make([]ItemResult, 0, len(manifest.Items))
	for _, item := range manifest.Items {
		if err := resetTaskIfRequested(ctx, init.Root, item.TaskID, opts.ResetTasks); err != nil {
			return SuiteResult{}, err
		}
		if _, err := midgardtask.Create(ctx, init.Root, midgardtask.CreateOptions{
			ID:        item.TaskID,
			Objective: item.Objective,
			RepoIDs:   item.RepoIDs,
		}); err != nil {
			return SuiteResult{}, err
		}
		providers, modelID, pricing, err := opts.ProviderFactory(item)
		if err != nil {
			return SuiteResult{}, err
		}
		if err := validateLoopProviders(providers); err != nil {
			return SuiteResult{}, err
		}
		loop, err := midgardtask.RunLoop(ctx, init.Root, item.TaskID, midgardtask.RunnerOptions{
			ModelID:              modelID,
			Providers:            providers,
			Budget:               opts.Budget,
			Pricing:              pricing,
			MaxReviewCycles:      opts.MaxReviewCycles,
			MaxSourceEditRepairs: opts.MaxSourceEditRepairs,
		})
		if err != nil {
			return SuiteResult{}, err
		}
		taskRuns = append(taskRuns, SuiteTaskRun{ItemID: item.ID, TaskID: item.TaskID, State: loop.State, CostUSD: loop.CostUSD})
		result, err := ScoreItem(ctx, init.Root, item)
		if err != nil {
			return SuiteResult{}, err
		}
		results = append(results, result)
	}
	report, err := WriteReport(init.Root, manifest, results)
	if err != nil {
		return SuiteResult{}, err
	}
	return SuiteResult{Report: report, PreparedRepos: preparedRepos, TaskRuns: taskRuns}, nil
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
