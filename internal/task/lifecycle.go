package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"midgard/internal/gitrepo"
	"midgard/internal/state"
	"midgard/internal/workbench"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func Create(ctx context.Context, root string, opts CreateOptions) (CreateResult, error) {
	status, err := workbench.Status(root)
	if err != nil {
		return CreateResult{}, err
	}
	if strings.TrimSpace(opts.Objective) == "" {
		return CreateResult{}, fmt.Errorf("objective is required")
	}
	taskID := opts.ID
	if taskID == "" {
		taskID = newTaskID()
	}
	if err := validateID(taskID); err != nil {
		return CreateResult{}, fmt.Errorf("task id: %w", err)
	}

	layout := workbench.NewLayout(status.Root)
	repos, err := selectedRepos(status.Config.Repos, opts.RepoIDs)
	if err != nil {
		return CreateResult{}, err
	}
	if len(repos) == 0 {
		return CreateResult{}, fmt.Errorf("no repos configured")
	}

	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return CreateResult{}, err
	}
	defer db.Close()

	if err := db.UpsertWorkbench(ctx, state.Workbench{ID: status.Config.Name, Root: status.Root, ConfigPath: status.ConfigPath}); err != nil {
		return CreateResult{}, err
	}
	taskRow := state.Task{ID: taskID, WorkbenchID: status.Config.Name, State: StateOpen, Objective: opts.Objective}
	if err := db.InsertTask(ctx, taskRow); err != nil {
		return CreateResult{}, err
	}

	artifactDir := filepath.Join(layout.Artifacts, taskID)
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return CreateResult{}, err
	}
	reportPath := filepath.Join(layout.Tasks, taskID+".mdx")
	if err := writeTaskReport(reportPath, taskID, opts.Objective); err != nil {
		return CreateResult{}, err
	}

	var worktrees []WorktreeStatus
	for _, repo := range repos {
		if err := validateID(repo.id); err != nil {
			return CreateResult{}, fmt.Errorf("repo id %q: %w", repo.id, err)
		}
		if err := gitrepo.IsRepo(ctx, repo.config.Path); err != nil {
			return CreateResult{}, err
		}
		startRef := repo.config.MainRef
		if startRef == "" {
			startRef = "main"
		}
		startCommit, err := gitrepo.ResolveRef(ctx, repo.config.Path, startRef)
		if err != nil {
			return CreateResult{}, err
		}
		if err := db.UpsertRepo(ctx, state.Repo{ID: repo.id, WorkbenchID: status.Config.Name, Path: repo.config.Path, MainRef: startRef}); err != nil {
			return CreateResult{}, err
		}
		if err := db.LinkTaskRepo(ctx, taskID, repo.id); err != nil {
			return CreateResult{}, err
		}
		wtPath := filepath.Join(layout.Worktrees, taskID, repo.id)
		branch := fmt.Sprintf("midgard/%s/%s", taskID, repo.id)
		if err := gitrepo.AddWorktree(ctx, repo.config.Path, wtPath, branch, startCommit); err != nil {
			return CreateResult{}, err
		}
		wt := state.Worktree{
			ID:          taskID + ":" + repo.id,
			TaskID:      taskID,
			RepoID:      repo.id,
			Path:        wtPath,
			StartRef:    startRef,
			StartCommit: startCommit,
		}
		if err := db.InsertWorktree(ctx, wt); err != nil {
			return CreateResult{}, err
		}
		worktreeStatus, err := gitrepo.WorktreeStatus(ctx, wtPath)
		if err != nil {
			return CreateResult{}, err
		}
		worktrees = append(worktrees, WorktreeStatus{
			RepoID:      repo.id,
			Path:        wtPath,
			StartRef:    startRef,
			StartCommit: startCommit,
			Dirty:       worktreeStatus.Dirty,
			Porcelain:   worktreeStatus.Porcelain,
		})
	}

	return CreateResult{Task: taskRow, Worktrees: worktrees, ReportPath: reportPath}, nil
}

func Status(ctx context.Context, root, taskID string) (StatusResult, error) {
	status, err := workbench.Status(root)
	if err != nil {
		return StatusResult{}, err
	}
	layout := workbench.NewLayout(status.Root)
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return StatusResult{}, err
	}
	defer db.Close()

	taskRow, err := db.Task(ctx, taskID)
	if err != nil {
		return StatusResult{}, err
	}
	rows, err := db.WorktreesForTask(ctx, taskID)
	if err != nil {
		return StatusResult{}, err
	}
	worktrees := make([]WorktreeStatus, 0, len(rows))
	for _, row := range rows {
		status, err := gitrepo.WorktreeStatus(ctx, row.Path)
		if err != nil {
			return StatusResult{}, err
		}
		worktrees = append(worktrees, WorktreeStatus{
			RepoID:      row.RepoID,
			Path:        row.Path,
			StartRef:    row.StartRef,
			StartCommit: row.StartCommit,
			Dirty:       status.Dirty,
			Porcelain:   status.Porcelain,
		})
	}
	return StatusResult{Task: taskRow, Worktrees: worktrees, NextAction: nextAction(worktrees)}, nil
}

func Diff(ctx context.Context, root, taskID, repoID string) (string, error) {
	status, err := Status(ctx, root, taskID)
	if err != nil {
		return "", err
	}
	for _, wt := range status.Worktrees {
		if repoID == "" || wt.RepoID == repoID {
			return gitrepo.Diff(ctx, wt.Path)
		}
	}
	return "", fmt.Errorf("repo %q not found for task %q", repoID, taskID)
}

type selectedRepo struct {
	id     string
	config workbench.RepoConfig
}

func selectedRepos(configured map[string]workbench.RepoConfig, ids []string) ([]selectedRepo, error) {
	if len(ids) == 0 {
		repos := make([]selectedRepo, 0, len(configured))
		for id, cfg := range configured {
			repos = append(repos, selectedRepo{id: id, config: cfg})
		}
		sort.Slice(repos, func(i, j int) bool { return repos[i].id < repos[j].id })
		return repos, nil
	}
	repos := make([]selectedRepo, 0, len(ids))
	for _, id := range ids {
		cfg, ok := configured[id]
		if !ok {
			return nil, fmt.Errorf("repo %q is not configured", id)
		}
		repos = append(repos, selectedRepo{id: id, config: cfg})
	}
	return repos, nil
}

func validateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("must match %s", idPattern.String())
	}
	return nil
}

func newTaskID() string {
	return "task_" + time.Now().UTC().Format("20060102T150405.000000000")
}
