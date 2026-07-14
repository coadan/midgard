package stream

import (
	"fmt"
	"slices"
	"strings"

	"midgard/internal/artifact"
)

type RepairIssue struct {
	Code    string
	Message string
	Line    int
}

type RepairPacket struct {
	ErrorCodes        []string
	Issues            []RepairIssue
	LastFrameID       int
	ReportArtifact    string
	DraftPayloadRefs  []string
	SealedPayloadRefs []string
	Mode              string
	ResultTemplate    string
	RawTail           string
	RemainingAttempts int
}

func buildRepairPacket(result *ParseResult, budget Budget, role string) *RepairPacket {
	codes := make([]string, 0, len(result.Errors))
	issues := make([]RepairIssue, 0, len(result.Errors))
	for _, parserErr := range result.Errors {
		if !parserErr.Recoverable {
			continue
		}
		issues = append(issues, RepairIssue{Code: parserErr.Code, Message: parserErr.Message, Line: parserErr.Line})
		if !slices.Contains(codes, parserErr.Code) {
			codes = append(codes, parserErr.Code)
		}
	}
	if len(codes) == 0 {
		return nil
	}

	packet := &RepairPacket{
		ErrorCodes:        codes,
		Issues:            issues,
		LastFrameID:       len(result.Frames),
		RawTail:           rawTail(result.Raw, 2048),
		RemainingAttempts: budget.MaxRepairAttempts,
		Mode:              "replacement",
	}
	if result.Result != nil {
		packet.ReportArtifact = result.Result.Artifact
	}
	for _, rec := range result.Artifacts {
		if rec.Type == artifact.TypeReport && packet.ReportArtifact == "" {
			packet.ReportArtifact = rec.Path
		}
		if rec.Type == artifact.TypePayload && rec.State == artifact.StateDraft {
			packet.DraftPayloadRefs = append(packet.DraftPayloadRefs, rec.Ref())
		}
		if rec.Type == artifact.TypePayload && rec.State == artifact.StateSealed {
			packet.SealedPayloadRefs = append(packet.SealedPayloadRefs, rec.Ref())
		}
	}
	if packet.ReportArtifact == "" {
		packet.ReportArtifact, _ = artifact.RoleReportPath(role)
	}
	if resultRepairCanPreserveAcceptedState(result) {
		packet.Mode = "result-only"
		packet.ResultTemplate = resultRepairTemplate(role, packet.ReportArtifact, result)
	}
	return packet
}

func resultRepairCanPreserveAcceptedState(result *ParseResult) bool {
	if result == nil || hasRejectedArtifact(result.Artifacts) || !hasReportArtifact(result.Artifacts) {
		return false
	}
	for _, parserErr := range result.Errors {
		switch parserErr.Code {
		case "missing_result", "malformed_result", "missing_result_status", "missing_result_artifact", "invalid_status", "missing_report":
		default:
			return false
		}
	}
	return len(result.Errors) > 0
}

func resultRepairTemplate(role, reportPath string, result *ParseResult) string {
	fields := result.ResultCandidate
	if result.Result != nil {
		fields = result.Result.Fields
	}
	status := fields["status"]
	if status == "" {
		status = "<" + strings.Join(allowedStatuses(role), "|") + ">"
	}
	extraKey := map[string]string{
		"planner":     "checks",
		"implementer": "checks",
		"reviewer":    "findings",
		"compactor":   "refs",
	}[role]
	extraValue := fields[extraKey]
	if extraValue == "" {
		extraValue = "none"
	}
	if extraKey == "" {
		return fmt.Sprintf("@result status:%s artifact:%s", status, reportPath)
	}
	return fmt.Sprintf("@result status:%s artifact:%s %s:%s", status, reportPath, extraKey, extraValue)
}

func allowedStatuses(role string) []string {
	switch role {
	case "planner":
		return []string{"ready", "blocked", "failed"}
	case "implementer":
		return []string{"ready", "no-op", "blocked", "failed"}
	case "reviewer":
		return []string{"approved", "changes-requested", "blocked", "failed"}
	case "compactor":
		return []string{"ready", "blocked", "failed"}
	default:
		return []string{"ready", "blocked", "failed"}
	}
}

func hasReportArtifact(records []artifact.Record) bool {
	for _, rec := range records {
		if rec.Type == artifact.TypeReport && rec.State != artifact.StateRejected {
			return true
		}
	}
	return false
}

func hasRejectedArtifact(records []artifact.Record) bool {
	for _, rec := range records {
		if rec.State == artifact.StateRejected {
			return true
		}
	}
	return false
}

func rawTail(raw string, limit int) string {
	if len(raw) <= limit {
		return raw
	}
	return raw[len(raw)-limit:]
}
