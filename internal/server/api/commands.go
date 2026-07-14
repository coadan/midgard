package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"

	"midgard/internal/command"
	"midgard/internal/policy"
	"midgard/internal/state"
	midgardtask "midgard/internal/task"
)

type runCommandRequest struct {
	TaskID  string            `json:"task_id"`
	RepoID  string            `json:"repo_id"`
	Command string            `json:"command"`
	CWD     string            `json:"cwd"`
	Env     map[string]string `json:"env"`
}

func (api *API) handleCommandRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req runCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	execution, err := midgardtask.AcquireExecution(r.Context(), api.root, req.TaskID)
	if err != nil {
		var heldErr state.ExecutionLeaseHeldError
		if errors.As(err, &heldErr) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer execution.Close()
	r = r.WithContext(execution.Context)
	taskStatus, err := midgardtask.Status(r.Context(), api.root, req.TaskID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	wt, err := selectWorktree(taskStatus.Worktrees, req.RepoID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cwd := wt.Path
	if req.CWD != "" {
		if filepath.IsAbs(req.CWD) {
			cwd = req.CWD
		} else {
			cwd = filepath.Join(wt.Path, req.CWD)
		}
	}
	artifactDir := filepath.Join(api.layout.Artifacts, req.TaskID)
	executor := command.NewExecutor(policy.DefaultCommandPolicy(wt.Path, artifactDir))
	result, err := executor.Run(r.Context(), command.Request{
		TaskID:      req.TaskID,
		RepoID:      wt.RepoID,
		Command:     req.Command,
		CWD:         cwd,
		ArtifactDir: artifactDir,
		Env:         req.Env,
		Fence:       midgardtask.CheckExecution,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := api.persistCommandEvents(r, result); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api *API) persistCommandEvents(r *http.Request, result command.Result) error {
	db, err := state.Open(r.Context(), api.layout.State)
	if err != nil {
		return err
	}
	defer db.Close()
	events, err := command.Events(result)
	if err != nil {
		return err
	}
	for _, event := range events {
		if _, err := db.InsertEvent(r.Context(), state.Event{TaskID: result.TaskID, Type: event.Type, Payload: event.Payload}); err != nil {
			return err
		}
	}
	return nil
}
