package api

import (
	"encoding/json"
	"net/http"
	"strings"

	midgardtask "midgard/internal/task"
)

type createTaskRequest struct {
	ID        string   `json:"id"`
	Objective string   `json:"objective"`
	Repos     []string `json:"repos"`
}

func (api *API) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/tasks" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := midgardtask.Create(r.Context(), api.root, midgardtask.CreateOptions{
		ID:        req.ID,
		Objective: req.Objective,
		RepoIDs:   req.Repos,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (api *API) handleTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	result, err := midgardtask.Status(r.Context(), api.root, id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
