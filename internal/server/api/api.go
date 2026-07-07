package api

import (
	"encoding/json"
	"net/http"

	"midgard/internal/workbench"
)

type API struct {
	root   string
	layout workbench.Layout
	mux    *http.ServeMux
}

func New(root string) (*API, error) {
	status, err := workbench.Status(root)
	if err != nil {
		return nil, err
	}
	api := &API{root: status.Root, layout: workbench.NewLayout(status.Root), mux: http.NewServeMux()}
	api.routes()
	return api, nil
}

func (api *API) Handler() http.Handler {
	return api.mux
}

func (api *API) routes() {
	api.mux.HandleFunc("/api/tasks", api.handleTasks)
	api.mux.HandleFunc("/api/tasks/", api.handleTask)
	api.mux.HandleFunc("/api/artifacts", api.handleArtifacts)
	api.mux.HandleFunc("/api/artifacts/", api.handleArtifact)
	api.mux.HandleFunc("/api/commands/run", api.handleCommandRun)
	api.mux.HandleFunc("/api/events", api.handleEvents)
	api.mux.HandleFunc("/api/events/stream", api.handleEventStream)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
