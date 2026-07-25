package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"midgard/internal/cost"
	"midgard/internal/model"
	codexprovider "midgard/internal/model/providers/codex"
	"midgard/internal/state"
	"midgard/internal/stream"
	midgardtask "midgard/internal/task"
)

type runTaskRequest struct {
	Model string `json:"model"`
}

func (api *API) handleTaskRun(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req runTaskRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	provider, err := codexprovider.NewFromLocalAuth()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if baseURL := strings.TrimSpace(os.Getenv("MIDGARD_CODEX_BASE_URL")); baseURL != "" {
		provider.BaseURL = baseURL
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = strings.TrimSpace(os.Getenv("MIDGARD_CODEX_MODEL"))
	}
	if modelID == "" {
		modelID, _ = codexprovider.ConfiguredModel()
	}
	if modelID == "" {
		modelID = codexprovider.DefaultModel
	}
	if err := api.recordAgentEvent(r, taskID, "agent.started", map[string]string{
		"provider": "codex",
		"model":    modelID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	budget := stream.DefaultBudget()
	budget.ProviderMaxTokens = 4096
	result, err := midgardtask.RunAgent(r.Context(), api.root, taskID, midgardtask.RunnerOptions{
		ModelID: modelID,
		Providers: midgardtask.RoleProviders{
			model.RoleImplementer: provider,
		},
		Budget:          budget,
		MaxCommandTurns: 16,
		Pricing: cost.Pricing{
			ID:                   "manual",
			ProviderID:           "codex",
			ModelID:              modelID,
			Currency:             "USD",
			MissingPricingCaveat: "pricing not configured; usage is recorded but cost is unknown",
		},
	})
	eventPayload := map[string]string{"state": result.State, "patch": result.PatchPath}
	if err != nil {
		eventPayload["error"] = err.Error()
		_ = api.recordAgentEvent(r, taskID, "agent.failed", eventPayload)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := api.recordAgentEvent(r, taskID, "agent.finished", eventPayload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api *API) recordAgentEvent(r *http.Request, taskID, eventType string, payload any) error {
	db, err := state.Open(r.Context(), api.layout.State)
	if err != nil {
		return err
	}
	defer db.Close()
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(r.Context(), state.Event{TaskID: taskID, Type: eventType, Payload: string(data)})
	return err
}
