package stream

import (
	"slices"

	"midgard/internal/artifact"
)

type RepairPacket struct {
	ErrorCodes        []string
	LastFrameID       int
	ReportArtifact    string
	DraftPayloadRefs  []string
	RawTail           string
	RemainingAttempts int
}

func buildRepairPacket(result *ParseResult, budget Budget) *RepairPacket {
	codes := make([]string, 0, len(result.Errors))
	for _, parserErr := range result.Errors {
		if !parserErr.Recoverable || slices.Contains(codes, parserErr.Code) {
			continue
		}
		codes = append(codes, parserErr.Code)
	}
	if len(codes) == 0 {
		return nil
	}

	packet := &RepairPacket{
		ErrorCodes:        codes,
		LastFrameID:       len(result.Frames),
		RawTail:           rawTail(result.Raw, 2048),
		RemainingAttempts: budget.MaxRepairAttempts,
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
	}
	return packet
}

func rawTail(raw string, limit int) string {
	if len(raw) <= limit {
		return raw
	}
	return raw[len(raw)-limit:]
}
