package benchmark

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"midgard/internal/model"
	"midgard/internal/state"
	midgardtask "midgard/internal/task"
	"midgard/internal/workbench"
)

type Score string

const (
	ScorePass    Score = "pass"
	ScorePartial Score = "partial"
	ScoreFail    Score = "fail"
	ScoreInvalid Score = "invalid"
)

type Evidence struct {
	TaskID                string
	TaskState             string
	PatchPath             string
	PatchBytes            int64
	ReferencePatchChecked bool
	ReferencePatchMatched bool
	ExpectedFilesChecked  bool
	ExpectedFilesMatched  bool
	ExpectedTouchedFiles  []string
	TouchedFiles          []string
	CostUSD               float64
	CostCaveats           []string
	PlanPath              string
	ReportPath            string
	ReviewPath            string
	RunError              string
	RunErrorClass         string
	AcceptanceRequired    bool
	AcceptanceValid       bool
	AcceptancePassed      bool
	AcceptanceStatus      string
	AcceptanceReason      string
	AcceptancePath        string
	AcceptanceChecksum    string
	AcceptanceChecks      []AcceptanceCheckScore
	ProviderModels        []ProviderModelEvidence
}

type AcceptanceCheckScore struct {
	ID               string
	RepoID           string
	Status           string
	ExpectedExitCode int
	ExitCode         int
	TimedOut         bool
	StdoutTruncated  bool
	StderrTruncated  bool
	ResultPath       string
	StdoutPath       string
	StderrPath       string
}

type ProviderModelEvidence struct {
	Role                string
	ProviderID          string
	ModelID             string
	ProviderFingerprint string
	InputTokens         int64
	OutputTokens        int64
}

type ItemResult struct {
	ItemID   string
	Score    Score
	Evidence Evidence
}

func ScoreItem(ctx context.Context, root string, item Item) (ItemResult, error) {
	status, err := midgardtask.Status(ctx, root, item.TaskID)
	if err != nil {
		return ItemResult{ItemID: item.ID, Score: ScoreInvalid}, nil
	}
	wbStatus, err := workbench.Status(root)
	if err != nil {
		return ItemResult{}, err
	}
	artifactRoot := filepath.Join(wbStatus.Root, ".midgard", "artifacts", item.TaskID)
	db, err := state.Open(ctx, filepath.Join(wbStatus.Root, ".midgard", "state.sqlite"))
	if err != nil {
		return ItemResult{}, err
	}
	defer db.Close()
	costRollups, err := db.CostRollupsForTask(ctx, item.TaskID)
	if err != nil {
		return ItemResult{}, err
	}
	usageRecords, err := db.UsageRecordsForTask(ctx, item.TaskID)
	if err != nil {
		return ItemResult{}, err
	}
	patchPath, patchBytes := existingArtifactInfo(artifactRoot, "patch.diff")
	patchText := existingArtifactText(artifactRoot, "patch.diff")
	referenceChecked, referenceMatched := referencePatchMatch(artifactRoot, item)
	expectedChecked, expectedMatched, touchedFiles := expectedTouchedFilesMatch(patchText, item.ExpectedTouchedFiles)
	costUSD, costCaveats := costEvidence(costRollups)
	acceptance, err := verifyAcceptanceEvidence(ctx, db, artifactRoot, item)
	if err != nil {
		return ItemResult{}, err
	}
	runError, err := latestBenchmarkItemError(ctx, db, item)
	if err != nil {
		return ItemResult{}, err
	}
	evidence := Evidence{
		TaskID:                item.TaskID,
		TaskState:             status.Task.State,
		PatchPath:             patchPath,
		PatchBytes:            patchBytes,
		ReferencePatchChecked: referenceChecked,
		ReferencePatchMatched: referenceMatched,
		ExpectedFilesChecked:  expectedChecked,
		ExpectedFilesMatched:  expectedMatched,
		ExpectedTouchedFiles:  append([]string(nil), item.ExpectedTouchedFiles...),
		TouchedFiles:          touchedFiles,
		CostUSD:               costUSD,
		CostCaveats:           costCaveats,
		PlanPath:              existingArtifact(artifactRoot, "plan.mdx"),
		ReportPath:            existingArtifact(artifactRoot, "implementation.mdx"),
		ReviewPath:            existingArtifact(artifactRoot, "review.mdx"),
		RunError:              runError.Message,
		RunErrorClass:         runError.Class,
		AcceptanceRequired:    acceptance.Required,
		AcceptanceValid:       acceptance.Valid,
		AcceptancePassed:      acceptance.Passed,
		AcceptanceStatus:      acceptance.Status,
		AcceptanceReason:      acceptance.Reason,
		AcceptancePath:        acceptance.Path,
		AcceptanceChecksum:    acceptance.Checksum,
		AcceptanceChecks:      acceptance.Checks,
		ProviderModels:        usageEvidence(usageRecords),
	}
	baseComplete := status.Task.State == "completed" && evidence.PatchPath != "" && evidence.PatchBytes > 0 && evidence.ReviewPath != ""
	if evidence.AcceptanceRequired {
		switch {
		case !baseComplete && evidence.PatchPath != "" && evidence.PatchBytes > 0:
			return ItemResult{ItemID: item.ID, Score: ScorePartial, Evidence: evidence}, nil
		case !baseComplete:
			return ItemResult{ItemID: item.ID, Score: ScoreFail, Evidence: evidence}, nil
		case !evidence.AcceptanceValid:
			return ItemResult{ItemID: item.ID, Score: ScoreInvalid, Evidence: evidence}, nil
		case !evidence.AcceptancePassed:
			return ItemResult{ItemID: item.ID, Score: ScoreFail, Evidence: evidence}, nil
		default:
			score := ScorePass
			if evidence.ExpectedFilesChecked && !evidence.ExpectedFilesMatched {
				score = ScorePartial
			}
			return ItemResult{ItemID: item.ID, Score: score, Evidence: evidence}, nil
		}
	}
	score := ScoreFail
	if baseComplete {
		if !evidence.ReferencePatchChecked || evidence.ReferencePatchMatched {
			score = ScorePass
		} else {
			score = ScorePartial
		}
	} else if evidence.PatchPath != "" && evidence.PatchBytes > 0 {
		score = ScorePartial
	}
	if score == ScorePass && evidence.ExpectedFilesChecked && !evidence.ExpectedFilesMatched {
		score = ScorePartial
	}
	return ItemResult{ItemID: item.ID, Score: score, Evidence: evidence}, nil
}

type benchmarkItemError struct {
	Message string
	Class   string
}

func latestBenchmarkItemError(ctx context.Context, db *state.DB, item Item) (benchmarkItemError, error) {
	events, err := db.EventsForTask(ctx, item.TaskID)
	if err != nil {
		return benchmarkItemError{}, err
	}
	var latest benchmarkItemError
	for _, event := range events {
		if event.Type == "benchmark.item.error_cleared" {
			var payload struct {
				ItemID string `json:"item_id"`
			}
			if json.Unmarshal([]byte(event.Payload), &payload) == nil && payload.ItemID == item.ID {
				latest = benchmarkItemError{}
			}
			continue
		}
		if event.Type != "benchmark.item.error" {
			continue
		}
		var payload struct {
			ItemID     string `json:"item_id"`
			Error      string `json:"error"`
			ErrorClass string `json:"error_class"`
		}
		if json.Unmarshal([]byte(event.Payload), &payload) == nil && payload.ItemID == item.ID {
			latest = benchmarkItemError{Message: payload.Error, Class: payload.ErrorClass}
		}
	}
	return latest, nil
}

func referencePatchMatch(artifactRoot string, item Item) (bool, bool) {
	if item.HiddenReferencePatch == "" {
		return false, false
	}
	actual, err := os.ReadFile(filepath.Join(artifactRoot, "patch.diff"))
	if err != nil {
		return true, false
	}
	referencePath := item.HiddenReferencePatch
	if !filepath.IsAbs(referencePath) && item.ManifestBaseDir != "" {
		referencePath = filepath.Join(item.ManifestBaseDir, referencePath)
	}
	reference, err := os.ReadFile(referencePath)
	if err != nil {
		return true, false
	}
	return true, patchChangesMatch(string(actual), string(reference))
}

type patchChanges struct {
	Added   []string
	Deleted []string
}

func patchChangesMatch(actual, reference string) bool {
	return reflect.DeepEqual(extractPatchChanges(actual), extractPatchChanges(reference))
}

func extractPatchChanges(patch string) patchChanges {
	var changes patchChanges
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			changes.Added = append(changes.Added, strings.TrimPrefix(line, "+"))
		case strings.HasPrefix(line, "-"):
			changes.Deleted = append(changes.Deleted, strings.TrimPrefix(line, "-"))
		}
	}
	return changes
}

func expectedTouchedFilesMatch(patch string, expected []string) (bool, bool, []string) {
	touched := touchedFilesFromPatch(patch)
	if len(expected) == 0 {
		return false, false, touched
	}
	expected = normalizedFiles(expected)
	return true, reflect.DeepEqual(touched, expected), touched
}

func touchedFilesFromPatch(patch string) []string {
	seen := map[string]bool{}
	var files []string
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		path := strings.TrimPrefix(parts[3], "b/")
		if path == "/dev/null" || path == "" || seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	slices.Sort(files)
	return files
}

func normalizedFiles(files []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(files))
	for _, file := range files {
		file = strings.TrimSpace(strings.TrimPrefix(file, "b/"))
		if file == "" || seen[file] {
			continue
		}
		seen[file] = true
		out = append(out, file)
	}
	slices.Sort(out)
	return out
}

func costEvidence(rollups []state.CostRollup) (float64, []string) {
	var total float64
	var caveats []string
	seenCaveats := map[string]bool{}
	for _, rollup := range rollups {
		amount, err := strconv.ParseFloat(rollup.AmountUSD, 64)
		if err == nil {
			total += amount
		}
		if rollup.Caveats != "" && !seenCaveats[rollup.Caveats] {
			seenCaveats[rollup.Caveats] = true
			caveats = append(caveats, rollup.Caveats)
		}
	}
	return total, caveats
}

func usageEvidence(records []state.UsageRecord) []ProviderModelEvidence {
	evidence := make([]ProviderModelEvidence, 0, len(records))
	for _, record := range records {
		evidence = append(evidence, ProviderModelEvidence{
			Role:                record.Role,
			ProviderID:          record.ProviderID,
			ModelID:             record.ModelID,
			ProviderFingerprint: model.ProviderFingerprint(providerID(record.ProviderID), record.ModelID),
			InputTokens:         record.InputTokens,
			OutputTokens:        record.OutputTokens,
		})
	}
	return evidence
}

type providerID string

func (p providerID) ID() string {
	return string(p)
}

func existingArtifact(root, path string) string {
	if _, err := os.Stat(filepath.Join(root, path)); err == nil {
		return path
	}
	return ""
}

func existingArtifactInfo(root, path string) (string, int64) {
	info, err := os.Stat(filepath.Join(root, path))
	if err != nil {
		return "", 0
	}
	return path, info.Size()
}

func existingArtifactText(root, path string) string {
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		return ""
	}
	return string(data)
}
