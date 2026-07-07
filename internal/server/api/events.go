package api

import (
	"net/http"
	"strconv"

	"midgard/internal/server/sse"
	"midgard/internal/state"
)

func (api *API) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/events" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	events, err := api.eventsForRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (api *API) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	events, err := api.eventsForRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sse.Prepare(w)
	for _, event := range events {
		if err := sse.Write(w, sse.Event{
			ID:   strconv.FormatInt(event.ID, 10),
			Type: event.Type,
			Data: event.Payload,
		}); err != nil {
			return
		}
	}
}

func (api *API) eventsForRequest(r *http.Request) ([]state.Event, error) {
	taskID := r.URL.Query().Get("task")
	if taskID == "" {
		return nil, errBadRequest("task is required")
	}
	db, err := state.Open(r.Context(), api.layout.State)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return db.EventsForTask(r.Context(), taskID)
}
