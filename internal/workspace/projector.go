package workspace

import (
	"context"
	"encoding/json"
	"fmt"

	"midgard/internal/eventlog"
)

type Binding struct {
	SessionID         string `json:"session_id"`
	ProjectID         string `json:"project_id"`
	RepositoryName    string `json:"repository_name"`
	RepositoryRoot    string `json:"repository_root"`
	WorktreeRoot      string `json:"worktree_root"`
	StartCommit       string `json:"start_commit"`
	DefaultBranch     string `json:"default_branch"`
	LandingStrategy   string `json:"landing_strategy"`
	CleanupWhenLanded bool   `json:"cleanup_when_landed"`
	CleanupState      string `json:"cleanup_state"`
	LastSequence      int64  `json:"last_sequence"`
}

type Projector struct{}

func (Projector) Name() string { return "workspace" }

func (Projector) Reset(ctx context.Context, db eventlog.DBTX) error {
	_, err := db.ExecContext(ctx, `DELETE FROM workspace_projection`)
	return err
}

func (Projector) Apply(ctx context.Context, db eventlog.DBTX, e eventlog.Event) error {
	switch e.Kind {
	case "workspace.bound":
		var binding Binding
		if err := json.Unmarshal(e.Payload, &binding); err != nil || binding.RepositoryRoot == "" || binding.WorktreeRoot == "" || binding.StartCommit == "" || (e.SchemaVersion >= 2 && (binding.DefaultBranch == "" || binding.LandingStrategy == "" || binding.CleanupState == "")) || (e.SchemaVersion >= 3 && (binding.ProjectID == "" || binding.RepositoryName == "")) {
			return fmt.Errorf("invalid workspace binding")
		}
		binding.SessionID, binding.LastSequence = e.SessionID, e.Sequence
		_, err := db.ExecContext(ctx, `INSERT INTO workspace_projection(session_id,project_id,repository_name,repository_root,worktree_root,start_commit,default_branch,landing_strategy,cleanup_when_landed,cleanup_state,last_sequence) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			binding.SessionID, binding.ProjectID, binding.RepositoryName, binding.RepositoryRoot, binding.WorktreeRoot, binding.StartCommit, binding.DefaultBranch, binding.LandingStrategy, binding.CleanupWhenLanded, binding.CleanupState, binding.LastSequence)
		return err
	case "workspace.cleanup_committed":
		repositoryName := cleanupRepository(e.Payload)
		query, arguments := cleanupUpdate(`cleanup_state='committed'`, e, repositoryName, "retained")
		result, err := db.ExecContext(ctx, query, arguments...)
		return requireOne(result, err, "commit cleanup")
	case "workspace.cleaned":
		repositoryName := cleanupRepository(e.Payload)
		query, arguments := cleanupUpdate(`cleanup_state='cleaned'`, e, repositoryName, "committed")
		result, err := db.ExecContext(ctx, query, arguments...)
		return requireOne(result, err, "finish cleanup")
	default:
		return nil
	}
}

func cleanupRepository(payload json.RawMessage) string {
	var value struct {
		RepositoryName string `json:"repository_name"`
	}
	_ = json.Unmarshal(payload, &value)
	return value.RepositoryName
}

func cleanupUpdate(set string, event eventlog.Event, repositoryName, currentState string) (string, []any) {
	query := `UPDATE workspace_projection SET ` + set + `,last_sequence=? WHERE session_id=? AND cleanup_state=?`
	arguments := []any{event.Sequence, event.SessionID, currentState}
	if repositoryName != "" {
		query += ` AND repository_name=?`
		arguments = append(arguments, repositoryName)
	}
	return query, arguments
}

func requireOne(result interface{ RowsAffected() (int64, error) }, err error, transition string) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("workspace %s requires one current binding", transition)
	}
	return nil
}
