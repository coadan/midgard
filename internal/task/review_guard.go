package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"midgard/internal/artifact"
	"midgard/internal/model"
	"midgard/internal/review"
	"midgard/internal/state"
	"midgard/internal/workbench"
)

type phraseReplacement struct {
	Old string
	New string
}

type autoReviewEvent struct {
	Reason string `json:"reason"`
	Old    string `json:"old"`
	New    string `json:"new"`
}

var phraseReplacementPattern = regexp.MustCompile(`(?i)chang(?:e|ing)\s+the\s+phrase\s+(.+?)\s+to\s+(.+?)(?:\.|$)`)

func applyReviewGuards(ctx context.Context, root, taskID string, run RoleRun) (RoleRun, error) {
	if run.Role != model.RoleReviewer || run.Status != string(review.VerdictApproved) {
		return run, nil
	}
	status, err := Status(ctx, root, taskID)
	if err != nil {
		return run, err
	}
	replacement, ok := objectivePhraseReplacement(status.Task.Objective)
	if !ok {
		return run, nil
	}
	ok, reason := replacementSatisfied(status.Worktrees, replacement)
	if ok {
		return run, nil
	}
	if err := appendAutoReviewFinding(root, taskID, replacement, reason); err != nil {
		return run, err
	}
	if err := recordAutoReviewEvent(ctx, root, taskID, replacement, reason); err != nil {
		return run, err
	}
	run.Status = string(review.VerdictChangesRequested)
	return run, nil
}

func objectivePhraseReplacement(objective string) (phraseReplacement, bool) {
	match := phraseReplacementPattern.FindStringSubmatch(objective)
	if len(match) != 3 {
		return phraseReplacement{}, false
	}
	oldPhrase := strings.Trim(strings.TrimSpace(match[1]), "\"'")
	newPhrase := strings.Trim(strings.TrimSpace(match[2]), "\"'")
	if oldPhrase == "" || newPhrase == "" {
		return phraseReplacement{}, false
	}
	return phraseReplacement{Old: oldPhrase, New: newPhrase}, true
}

func replacementSatisfied(worktrees []WorktreeStatus, replacement phraseReplacement) (bool, string) {
	var sawNew bool
	var sawOld bool
	for _, wt := range worktrees {
		_ = filepath.WalkDir(wt.Path, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if shouldSkipDir(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !looksTextPath(path) {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil || !looksText(data) {
				return nil
			}
			text := string(data)
			if strings.Contains(text, replacement.New) {
				sawNew = true
			}
			if strings.Contains(text, replacement.Old) {
				sawOld = true
			}
			return nil
		})
	}
	switch {
	case sawOld:
		return false, "old phrase remains in worktree"
	case !sawNew:
		return false, "replacement phrase is absent from worktree"
	default:
		return true, ""
	}
}

func appendAutoReviewFinding(root, taskID string, replacement phraseReplacement, reason string) error {
	status, err := workbench.Status(root)
	if err != nil {
		return err
	}
	store := artifact.NewStore(filepath.Join(workbench.NewLayout(status.Root).Artifacts, taskID))
	data, err := store.Read("review.mdx")
	if err != nil {
		return err
	}
	addition := fmt.Sprintf(`

## Midgard Auto Review

- status: changes-requested
- reason: %s
- expected_old_absent: %s
- expected_new_present: %s
`, reason, replacement.Old, replacement.New)
	_, err = store.Put(artifact.Record{
		Path:         "review.mdx",
		Type:         artifact.TypeReport,
		State:        artifact.StateSealed,
		ProducerRole: model.RoleReviewer.String(),
	}, append(data, []byte(addition)...))
	return err
}

func recordAutoReviewEvent(ctx context.Context, root, taskID string, replacement phraseReplacement, reason string) error {
	status, err := workbench.Status(root)
	if err != nil {
		return err
	}
	db, err := state.Open(ctx, workbench.NewLayout(status.Root).State)
	if err != nil {
		return err
	}
	defer db.Close()
	payload, err := json.Marshal(autoReviewEvent{Reason: reason, Old: replacement.Old, New: replacement.New})
	if err != nil {
		return err
	}
	_, err = db.InsertEvent(ctx, state.Event{TaskID: taskID, Type: "review.auto_changes_requested", Payload: string(payload)})
	return err
}
