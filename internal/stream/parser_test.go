package stream

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"midgard/internal/artifact"
)

func TestParseValidImplementationFixture(t *testing.T) {
	raw := readFixture(t, "valid_implementation.stream")
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("implementer", store, DefaultBudget()).ParseString(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Raw != raw {
		t.Fatal("raw stream was not preserved")
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected parser errors: %#v", result.Errors)
	}
	if result.Result == nil || result.Result.Status != "ready" {
		t.Fatalf("result = %#v, want ready", result.Result)
	}
	if result.Repair != nil {
		t.Fatalf("repair = %#v, want nil", result.Repair)
	}
	report := artifactByPath(result.Artifacts, "implementation.mdx")
	if report.State != artifact.StateSealed || report.Checksum == "" {
		t.Fatalf("report artifact = %#v, want sealed with checksum", report)
	}
	payload := artifactByPath(result.Artifacts, "scripts/edit_timeout.py")
	if payload.State != artifact.StateSealed || !payload.CanExecute() || payload.Checksum == "" {
		t.Fatalf("payload artifact = %#v, want sealed executable payload", payload)
	}
	payloadData, err := store.Read("scripts/edit_timeout.py")
	if err != nil {
		t.Fatal(err)
	}
	if string(payloadData) == "" {
		t.Fatal("payload was not written")
	}
	if len(result.Edits) != 1 {
		t.Fatalf("edits = %#v, want 1", result.Edits)
	}
	if diff := cmp.Diff(CommandProposal{
		FrameID: result.Commands[0].FrameID,
		Repo:    "midgard",
		Command: "python3 .midgard/artifacts/task_123/scripts/edit_timeout.py",
	}, result.Commands[0]); diff != "" {
		t.Fatalf("command mismatch (-want +got):\n%s", diff)
	}
}

func TestOpenPayloadStaysDraftAndRepairable(t *testing.T) {
	raw := readFixture(t, "open_payload.stream")
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("implementer", store, DefaultBudget()).ParseString(raw)
	if err != nil {
		t.Fatal(err)
	}
	payload := artifactByPath(result.Artifacts, "scripts/incomplete.py")
	if payload.State != artifact.StateDraft {
		t.Fatalf("payload state = %s, want draft", payload.State)
	}
	if payload.CanExecute() {
		t.Fatal("draft payload should not be executable")
	}
	if result.Repair == nil {
		t.Fatal("expected repair packet")
	}
	if !contains(result.Repair.ErrorCodes, "open_payload") || !contains(result.Repair.ErrorCodes, "missing_result") {
		t.Fatalf("repair codes = %#v, want open_payload and missing_result", result.Repair.ErrorCodes)
	}
	if len(result.Repair.DraftPayloadRefs) != 1 || result.Repair.DraftPayloadRefs[0] != "artifact:scripts/incomplete.py" {
		t.Fatalf("draft payload refs = %#v", result.Repair.DraftPayloadRefs)
	}
}

func TestMissingResultCreatesRepairPacket(t *testing.T) {
	raw := readFixture(t, "missing_result.stream")
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("planner", store, DefaultBudget()).ParseString(raw)
	if err != nil {
		t.Fatal(err)
	}
	report := artifactByPath(result.Artifacts, "plan.mdx")
	if report.State != artifact.StateDraft {
		t.Fatalf("report state = %s, want draft", report.State)
	}
	if result.Repair == nil || !contains(result.Repair.ErrorCodes, "missing_result") {
		t.Fatalf("repair = %#v, want missing_result", result.Repair)
	}
}

func TestResultInfersOpenedCanonicalReportArtifact(t *testing.T) {
	raw := readFixture(t, "planner_missing_result_artifact.stream")
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("planner", store, DefaultBudget()).ParseString(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repair != nil || len(result.Errors) != 0 {
		t.Fatalf("unexpected repair/errors: repair=%#v errors=%#v", result.Repair, result.Errors)
	}
	if result.Result == nil || result.Result.Artifact != "plan.mdx" || result.Result.Status != "ready" {
		t.Fatalf("result = %#v", result.Result)
	}
	if len(result.Normalizations) != 1 || result.Normalizations[0].Code != "inferred_result_artifact" {
		t.Fatalf("normalizations = %#v", result.Normalizations)
	}
	report := artifactByPath(result.Artifacts, "plan.mdx")
	if report.State != artifact.StateSealed {
		t.Fatalf("report state = %s, want sealed", report.State)
	}
}

func TestMissingResultArtifactWithoutOpenedReportStillNeedsRepair(t *testing.T) {
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("planner", store, DefaultBudget()).ParseString("@result status:ready checks:none\n")
	if err != nil {
		t.Fatal(err)
	}
	if result.Result != nil || result.Repair == nil {
		t.Fatalf("result=%#v repair=%#v", result.Result, result.Repair)
	}
	if !contains(result.Repair.ErrorCodes, "missing_result_artifact") || !contains(result.Repair.ErrorCodes, "missing_result") {
		t.Fatalf("repair codes = %#v", result.Repair.ErrorCodes)
	}
}

func TestPlannerDisallowedToolFramesAreDiscardedWithEvidence(t *testing.T) {
	raw := readFixture(t, "planner_disallowed_tool_frames.stream")
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("planner", store, DefaultBudget()).ParseString(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repair != nil || len(result.Errors) != 0 || result.Result == nil {
		t.Fatalf("result=%#v repair=%#v errors=%#v", result.Result, result.Repair, result.Errors)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Path != "plan.mdx" || result.Artifacts[0].State != artifact.StateSealed {
		t.Fatalf("artifacts = %#v", result.Artifacts)
	}
	if len(result.Edits) != 0 || len(result.Commands) != 0 {
		t.Fatalf("edits=%#v commands=%#v", result.Edits, result.Commands)
	}
	if len(result.Normalizations) != 3 {
		t.Fatalf("normalizations = %#v, want payload/edit/cmd discards", result.Normalizations)
	}
	for _, normalization := range result.Normalizations {
		if normalization.Code != "ignored_disallowed_control" {
			t.Fatalf("normalization = %#v", normalization)
		}
	}
	if _, err := store.Read("patches/fix-localdate-comment.diff"); !os.IsNotExist(err) {
		t.Fatalf("discarded payload was stored: %v", err)
	}
}

func TestImplementerMalformedCommandStillNeedsRepair(t *testing.T) {
	store := artifact.NewStore(t.TempDir())
	raw := "@report implementation.mdx\nInspect and test.\n@cmd go test ./...\n@result status:ready artifact:implementation.mdx checks:none\n"
	result, err := NewParser("implementer", store, DefaultBudget()).ParseString(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repair == nil || !contains(result.Repair.ErrorCodes, "malformed_known_tag") {
		t.Fatalf("repair = %#v", result.Repair)
	}
}

func TestInlineResultAfterReportTextIsRecovered(t *testing.T) {
	raw := "@report implementation.mdx\nInspected files and finished.@result status:failed artifact:implementation.mdx checks:none\n"
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("implementer", store, DefaultBudget()).ParseString(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repair != nil {
		t.Fatalf("repair = %#v, want nil", result.Repair)
	}
	if result.Result == nil || result.Result.Status != "failed" {
		t.Fatalf("result = %#v", result.Result)
	}
	data, err := store.Read("implementation.mdx")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "Inspected files and finished." {
		t.Fatalf("report = %q", data)
	}
}

func TestInlineCommandAfterReportTextIsRecovered(t *testing.T) {
	raw := "@report implementation.mdx\nNeed inspect.@cmd repo:repo1 -- sed -n '1,5p' README.md\n"
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("implementer", store, DefaultBudget()).ParseString(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Commands) != 1 {
		t.Fatalf("commands = %#v, want one", result.Commands)
	}
	if result.Commands[0].Repo != "repo1" || result.Commands[0].Command != "sed -n '1,5p' README.md" {
		t.Fatalf("command = %#v", result.Commands[0])
	}
	if result.Repair == nil || !contains(result.Repair.ErrorCodes, "missing_result") {
		t.Fatalf("repair = %#v, want missing_result", result.Repair)
	}
	data, err := store.Read("implementation.mdx")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "Need inspect." {
		t.Fatalf("report = %q", data)
	}
}

func TestInlineControlMentionInReportTextIsNotParsed(t *testing.T) {
	raw := strings.Join([]string{
		"@report plan.mdx",
		"# Plan",
		"",
		"4. Emit @result status:ready artifact:plan.mdx checks:go-test.",
		"@result status:ready artifact:plan.mdx checks:go-test",
		"",
	}, "\n")
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("planner", store, DefaultBudget()).ParseString(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repair != nil || len(result.Errors) != 0 || result.Result == nil {
		t.Fatalf("result=%#v repair=%#v errors=%#v", result.Result, result.Repair, result.Errors)
	}
	resultFrames := 0
	for _, frame := range result.Frames {
		if frame.Type == FrameResult {
			resultFrames++
		}
	}
	if resultFrames != 1 {
		t.Fatalf("result frames = %d, want 1: %#v", resultFrames, result.Frames)
	}
	report, err := store.Read("plan.mdx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "Emit @result") {
		t.Fatalf("report lost inline control mention:\n%s", report)
	}
}

func TestResultWithoutReportCreatesRepairPacket(t *testing.T) {
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("planner", store, DefaultBudget()).ParseString("@result status:ready artifact:plan.mdx checks:none\n")
	if err != nil {
		t.Fatal(err)
	}
	if result.Repair == nil || !contains(result.Repair.ErrorCodes, "missing_report") {
		t.Fatalf("repair = %#v, want missing_report", result.Repair)
	}
}

func TestInvalidPayloadPathIsRejected(t *testing.T) {
	stream := "@report implementation.mdx\n# Implementation\n@payload begin type:script path:../escape.py lang:python\nprint('x')\n@payload end\n@result status:ready artifact:implementation.mdx\n"
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("implementer", store, DefaultBudget()).ParseString(stream)
	if err != nil {
		t.Fatal(err)
	}
	rejected := artifactByPath(result.Artifacts, "../escape.py")
	if rejected.State != artifact.StateRejected {
		t.Fatalf("invalid payload artifact = %#v, want rejected", rejected)
	}
	if !containsError(result.Errors, "invalid_artifact_path") {
		t.Fatalf("errors = %#v, want invalid_artifact_path", result.Errors)
	}
	if result.Repair == nil || !contains(result.Repair.ErrorCodes, "invalid_artifact_path") {
		t.Fatalf("repair = %#v, want invalid_artifact_path", result.Repair)
	}
}

func TestPayloadOverBudgetStaysRejectedAfterTerminator(t *testing.T) {
	stream := "@report implementation.mdx\n@payload begin type:script path:scripts/large.py lang:python\n123456\n@payload end\n@result status:ready artifact:implementation.mdx\n"
	budget := DefaultBudget()
	budget.MaxSinglePayloadBytes = 4
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("implementer", store, budget).ParseString(stream)
	if err != nil {
		t.Fatal(err)
	}
	payload := artifactByPath(result.Artifacts, "scripts/large.py")
	if payload.State != artifact.StateRejected {
		t.Fatalf("payload state = %s, want rejected", payload.State)
	}
	if payload.CanExecute() {
		t.Fatal("rejected payload should not be executable")
	}
	if result.Repair == nil || !contains(result.Repair.ErrorCodes, "rejected_payload") {
		t.Fatalf("repair = %#v, want rejected_payload", result.Repair)
	}
}

func TestPayloadTreatsControlLikeTextExceptTerminator(t *testing.T) {
	stream := "@report implementation.mdx\n@payload begin type:script path:scripts/decorator.py lang:python\n@decorator\n@payload end\n@result status:ready artifact:implementation.mdx\n"
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("implementer", store, DefaultBudget()).ParseString(stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected errors: %#v", result.Errors)
	}
	data, err := store.Read("scripts/decorator.py")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "@decorator\n" {
		t.Fatalf("payload data = %q", data)
	}
}

func TestPayloadPreservesUnifiedDiffHunkHeaders(t *testing.T) {
	stream := "@report implementation.mdx\n@payload begin type:patch path:patches/readme.diff\n@@ -1 +1 @@\n-old\n+new\n@payload end\n@result status:ready artifact:implementation.mdx checks:none\n"
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("implementer", store, DefaultBudget()).ParseString(stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected errors: %#v", result.Errors)
	}
	data, err := store.Read("patches/readme.diff")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "@@ -1 +1 @@") {
		t.Fatalf("payload data = %q", data)
	}
}

func TestPatchEditCanUseQuotedReasonAndFollowingPayload(t *testing.T) {
	stream := strings.Join([]string{
		"@report implementation.mdx",
		"# Plan",
		"",
		"@edit file:README.md action:edit mode:patch reason:\"Fix awkward sentence about UnmarshalTOML interface\"",
		"@payload begin type:patch path:README.md",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"@payload end",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	store := artifact.NewStore(t.TempDir())
	result, err := NewParser("implementer", store, DefaultBudget()).ParseString(stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected parser errors: %#v", result.Errors)
	}
	if len(result.Edits) != 1 {
		t.Fatalf("edits = %#v, want 1", result.Edits)
	}
	edit := result.Edits[0]
	if edit.Reason != "Fix awkward sentence about UnmarshalTOML interface" || edit.Content != "artifact:README.md" {
		t.Fatalf("edit = %#v", edit)
	}
	payload := artifactByPath(result.Artifacts, "README.md")
	if payload.State != artifact.StateSealed || payload.PayloadType != "patch" {
		t.Fatalf("payload = %#v, want sealed patch", payload)
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "streams", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func artifactByPath(records []artifact.Record, path string) artifact.Record {
	for _, record := range records {
		if record.Path == path {
			return record
		}
	}
	return artifact.Record{}
}

func containsError(errors []ParserError, code string) bool {
	for _, parserErr := range errors {
		if parserErr.Code == code {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
