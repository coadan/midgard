package model

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"midgard/internal/artifact"
	"midgard/internal/stream"
)

type Runner struct {
	Provider                 Provider
	Store                    artifact.Store
	Budget                   stream.Budget
	MaxRepairs               int
	CommandHandler           CommandHandler
	MaxCommandTurns          int
	MaxBlockedCommandRetries int
	Fence                    func(context.Context) error
}

type CommandHandler func(ctx context.Context, commands []stream.CommandProposal) (string, error)

type RepairExhaustedError struct {
	ErrorCodes []string
	Issues     []stream.RepairIssue
	Attempts   int
}

func (e RepairExhaustedError) Error() string {
	return fmt.Sprintf("protocol remained invalid after %d repair attempts: %v", e.Attempts, e.ErrorCodes)
}

func (r Runner) Run(ctx context.Context, packet Packet) (RunResult, error) {
	if r.Provider == nil {
		return RunResult{}, fmt.Errorf("provider is required")
	}
	budget := r.Budget
	if budget == (stream.Budget{}) {
		budget = stream.DefaultBudget()
	}
	maxRepairs := r.MaxRepairs
	if maxRepairs == 0 {
		maxRepairs = budget.MaxRepairAttempts
	}
	maxCommandTurns := r.MaxCommandTurns
	if maxCommandTurns == 0 {
		maxCommandTurns = 8
	}
	maxBlockedCommandRetries := r.MaxBlockedCommandRetries
	if maxBlockedCommandRetries == 0 {
		maxBlockedCommandRetries = 2
	}
	current := packet
	var result RunResult
	var rawTranscript strings.Builder
	var commandTranscript strings.Builder
	repairAttempts := 0
	commandTurns := 0
	commandLimitRepairUsed := false
	blockedCommandRetries := 0
	attempts := 0
	for {
		if r.Fence != nil {
			if err := r.Fence(ctx); err != nil {
				return result, err
			}
		}
		previousParsed := result.Parsed
		var reportSnapshots map[string][]byte
		if current.Repair && previousParsed != nil {
			var err error
			reportSnapshots, err = snapshotReportArtifacts(r.Store, previousParsed)
			if err != nil {
				return result, err
			}
		}
		var raw strings.Builder
		usage, err := r.Provider.Stream(ctx, current, func(delta Delta) error {
			raw.WriteString(delta.Text)
			return nil
		})
		if err != nil {
			return result, err
		}
		if r.Fence != nil {
			if err := r.Fence(ctx); err != nil {
				return result, err
			}
		}
		usage.ProviderID = r.Provider.ID()
		usage.ModelID = current.ModelID
		usage.Role = current.Role
		attempts++
		appendProviderTurn(&rawTranscript, attempts, raw.String())
		parsed, err := stream.NewParser(current.Role.String(), r.Store, budget).ParseString(raw.String())
		if err != nil {
			return result, err
		}
		if current.Repair && previousParsed != nil {
			parsed, err = mergeResultRepairDelta(r.Store, budget, previousParsed, reportSnapshots, parsed)
			if err != nil {
				return result, err
			}
		}
		parsed.Artifacts = mergeReusablePayloadArtifacts(result.Parsed, parsed)
		result = RunResult{
			Packet:   current,
			Raw:      rawTranscript.String(),
			Parsed:   parsed,
			Usage:    append(result.Usage, usage),
			Attempts: attempts,
		}
		if shouldContinueWithCommands(current.Role, parsed) && r.CommandHandler != nil {
			if commandTurns >= maxCommandTurns {
				if commandLimitRepairUsed {
					return result, fmt.Errorf("command continuation turns exhausted after %d turns", maxCommandTurns)
				}
				commandLimitRepairUsed = true
				current = CommandContinuationLimitPacket(packet, parsed, commandTranscript.String(), maxCommandTurns)
				continue
			}
			commandTurns++
			if r.Fence != nil {
				if err := r.Fence(ctx); err != nil {
					return result, err
				}
			}
			commandResult, err := r.CommandHandler(ctx, parsed.Commands)
			if err != nil {
				return result, err
			}
			appendCommandTurn(&commandTranscript, commandTurns, commandResult)
			current = CommandContinuationPacket(packet, parsed, commandTranscript.String())
			continue
		}
		if parsed.Repair == nil &&
			shouldRetryUnproductiveTerminalAfterCommands(current.Role, parsed, commandTurns) &&
			r.CommandHandler != nil {
			if blockedCommandRetries >= maxBlockedCommandRetries {
				return result, nil
			}
			blockedCommandRetries++
			current = UnproductiveCommandContinuationPacket(packet, parsed, commandTranscript.String())
			continue
		}
		if parsed.Repair == nil && shouldRetryUnproductiveTerminalAfterRepair(current, parsed) {
			if blockedCommandRetries >= maxBlockedCommandRetries {
				return result, nil
			}
			blockedCommandRetries++
			current = UnproductiveTerminalRepairPacket(current, parsed)
			continue
		}
		if parsed.Repair == nil && shouldRetryUnproductiveTerminalWithoutProgress(current.Role, parsed) {
			if blockedCommandRetries >= maxBlockedCommandRetries {
				return result, nil
			}
			blockedCommandRetries++
			current = UnproductiveTerminalPacket(current, parsed)
			continue
		}
		if parsed.Repair == nil {
			return result, nil
		}
		if repairAttempts >= maxRepairs {
			return result, RepairExhaustedError{
				ErrorCodes: append([]string(nil), parsed.Repair.ErrorCodes...),
				Issues:     append([]stream.RepairIssue(nil), parsed.Repair.Issues...),
				Attempts:   repairAttempts,
			}
		}
		parsed.Repair.RemainingAttempts = maxRepairs - repairAttempts
		repairAttempts++
		current = RepairPacket(current, parsed.Repair)
	}
}

func snapshotReportArtifacts(store artifact.Store, parsed *stream.ParseResult) (map[string][]byte, error) {
	snapshots := map[string][]byte{}
	for _, rec := range parsed.Artifacts {
		if rec.Type != artifact.TypeReport || rec.State == artifact.StateRejected {
			continue
		}
		data, err := store.Read(rec.Path)
		if err != nil {
			return nil, fmt.Errorf("snapshot report artifact %q for repair: %w", rec.Path, err)
		}
		snapshots[rec.Path] = append([]byte(nil), data...)
	}
	return snapshots, nil
}

func mergeResultRepairDelta(store artifact.Store, budget stream.Budget, previous *stream.ParseResult, snapshots map[string][]byte, current *stream.ParseResult) (*stream.ParseResult, error) {
	if previous == nil || previous.Repair == nil || previous.Repair.Mode != "result-only" || current == nil || current.Result == nil {
		return current, nil
	}
	if !onlyRepairError(current.Errors, "missing_report") || hasRejectedArtifactRecord(current.Artifacts) {
		return current, nil
	}
	reportPath := current.Result.Artifact
	previousReport, ok := reportRecord(previous.Artifacts, reportPath)
	if !ok {
		return current, nil
	}
	previousData, ok := snapshots[reportPath]
	if !ok {
		return current, nil
	}
	currentData := []byte(nil)
	if _, ok := reportRecord(current.Artifacts, reportPath); ok {
		data, err := store.Read(reportPath)
		if err != nil {
			return nil, fmt.Errorf("read repaired report artifact %q: %w", reportPath, err)
		}
		currentData = data
	}
	mergedData := mergeRepairReport(previousData, currentData)
	if budget.MaxReportBytes > 0 && int64(len(mergedData)) > budget.MaxReportBytes {
		return current, nil
	}
	if err := artifact.ValidateSafeMDX(mergedData); err != nil {
		return current, nil
	}
	sealedReport, err := store.Put(artifact.Record{
		Path:         reportPath,
		Type:         artifact.TypeReport,
		State:        artifact.StateSealed,
		ProducerRole: previousReport.ProducerRole,
	}, mergedData)
	if err != nil {
		return nil, fmt.Errorf("seal preserved report artifact %q: %w", reportPath, err)
	}

	offsetCurrentFrameIDs(current, maxFrameID(previous.Frames))
	current.Frames = append(nonResultFrames(previous.Frames), current.Frames...)
	current.Artifacts = mergeRepairArtifacts(previous.Artifacts, current.Artifacts, sealedReport)
	current.Commands = mergeCommands(previous.Commands, current.Commands)
	current.Edits = mergeEdits(previous.Edits, current.Edits)
	current.Refs = mergeRefs(previous.Refs, current.Refs)
	current.Errors = nil
	current.Repair = nil
	current.Normalizations = append(append([]stream.Normalization(nil), previous.Normalizations...), current.Normalizations...)
	current.Normalizations = append(current.Normalizations, stream.Normalization{
		Code:    "preserved_result_repair_state",
		Message: "preserved accepted frames and artifacts while applying a result-only repair delta",
	})
	return current, nil
}

func onlyRepairError(errors []stream.ParserError, allowed string) bool {
	for _, parserErr := range errors {
		if parserErr.Code != allowed {
			return false
		}
	}
	return true
}

func hasRejectedArtifactRecord(records []artifact.Record) bool {
	for _, rec := range records {
		if rec.State == artifact.StateRejected {
			return true
		}
	}
	return false
}

func reportRecord(records []artifact.Record, path string) (artifact.Record, bool) {
	for _, rec := range records {
		if rec.Type == artifact.TypeReport && rec.Path == path && rec.State != artifact.StateRejected {
			return rec, true
		}
	}
	return artifact.Record{}, false
}

func mergeRepairReport(previous, current []byte) []byte {
	previous = bytes.TrimRight(previous, "\r\n")
	current = bytes.TrimRight(current, "\r\n")
	switch {
	case len(current) == 0:
		return append(append([]byte(nil), previous...), '\n')
	case bytes.Equal(previous, current), bytes.HasPrefix(previous, current):
		return append(append([]byte(nil), previous...), '\n')
	case bytes.HasPrefix(current, previous):
		return append(append([]byte(nil), current...), '\n')
	default:
		merged := append(append([]byte(nil), previous...), '\n', '\n')
		merged = append(merged, current...)
		return append(merged, '\n')
	}
}

func maxFrameID(frames []stream.Frame) int {
	maxID := 0
	for _, frame := range frames {
		if frame.ID > maxID {
			maxID = frame.ID
		}
	}
	return maxID
}

func offsetCurrentFrameIDs(parsed *stream.ParseResult, offset int) {
	for i := range parsed.Frames {
		parsed.Frames[i].ID += offset
	}
	for i := range parsed.Commands {
		parsed.Commands[i].FrameID += offset
	}
	for i := range parsed.Edits {
		parsed.Edits[i].FrameID += offset
	}
	for i := range parsed.Refs {
		parsed.Refs[i].FrameID += offset
	}
	if parsed.Result != nil {
		parsed.Result.FrameID += offset
	}
}

func nonResultFrames(frames []stream.Frame) []stream.Frame {
	kept := make([]stream.Frame, 0, len(frames))
	for _, frame := range frames {
		if frame.Type != stream.FrameResult {
			kept = append(kept, frame)
		}
	}
	return kept
}

func mergeRepairArtifacts(previous, current []artifact.Record, report artifact.Record) []artifact.Record {
	merged := make([]artifact.Record, 0, len(previous)+len(current))
	seen := map[string]bool{}
	for _, rec := range append(current, previous...) {
		if rec.Path == report.Path || seen[rec.Path] {
			continue
		}
		merged = append(merged, rec)
		seen[rec.Path] = true
	}
	return append(merged, report)
}

func mergeCommands(previous, current []stream.CommandProposal) []stream.CommandProposal {
	merged := append([]stream.CommandProposal(nil), previous...)
	seen := map[string]bool{}
	for _, proposal := range merged {
		seen[proposal.Repo+"\x00"+proposal.Command] = true
	}
	for _, proposal := range current {
		key := proposal.Repo + "\x00" + proposal.Command
		if !seen[key] {
			merged = append(merged, proposal)
			seen[key] = true
		}
	}
	return merged
}

func mergeEdits(previous, current []stream.EditIntent) []stream.EditIntent {
	merged := append([]stream.EditIntent(nil), previous...)
	seen := map[string]bool{}
	for _, edit := range merged {
		seen[editKey(edit)] = true
	}
	for _, edit := range current {
		key := editKey(edit)
		if !seen[key] {
			merged = append(merged, edit)
			seen[key] = true
		}
	}
	return merged
}

func editKey(edit stream.EditIntent) string {
	return strings.Join([]string{edit.Repo, edit.File, edit.Action, edit.Mode, edit.Reason, edit.Content, edit.To}, "\x00")
}

func mergeRefs(previous, current []stream.Ref) []stream.Ref {
	merged := append([]stream.Ref(nil), previous...)
	seen := map[string]bool{}
	for _, ref := range merged {
		seen[ref.Kind+"\x00"+ref.Target] = true
	}
	for _, ref := range current {
		key := ref.Kind + "\x00" + ref.Target
		if !seen[key] {
			merged = append(merged, ref)
			seen[key] = true
		}
	}
	return merged
}

func mergeReusablePayloadArtifacts(previous, current *stream.ParseResult) []artifact.Record {
	if current == nil {
		return nil
	}
	merged := append([]artifact.Record(nil), current.Artifacts...)
	seen := make(map[string]bool, len(merged))
	for _, rec := range merged {
		seen[rec.Path] = true
	}
	if previous == nil {
		return merged
	}
	for _, rec := range previous.Artifacts {
		if rec.Type != artifact.TypePayload || rec.State != artifact.StateSealed || seen[rec.Path] {
			continue
		}
		merged = append(merged, rec)
		seen[rec.Path] = true
	}
	return merged
}

func shouldContinueWithCommands(role Role, parsed *stream.ParseResult) bool {
	if parsed == nil || len(parsed.Commands) == 0 {
		return false
	}
	if len(parsed.Edits) > 0 {
		return false
	}
	if parsed.Result == nil {
		return commandContinuationErrorsAllowed(parsed, false)
	}
	if !roleCanInspectWithCommands(role) {
		return false
	}
	return commandContinuationErrorsAllowed(parsed, true)
}

func commandContinuationTerminalStatus(status string) bool {
	return status == "blocked" || status == "failed"
}

func commandContinuationErrorsAllowed(parsed *stream.ParseResult, terminal bool) bool {
	if parsed == nil {
		return false
	}
	for _, parserErr := range parsed.Errors {
		switch parserErr.Code {
		case "missing_result":
			if terminal {
				return false
			}
		case "missing_report", "content_after_result":
			if !terminal {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func shouldRetryUnproductiveTerminalAfterCommands(role Role, parsed *stream.ParseResult, commandTurns int) bool {
	if !roleCanInspectWithCommands(role) ||
		commandTurns == 0 ||
		parsed == nil ||
		parsed.Result == nil ||
		len(parsed.Commands) > 0 ||
		len(parsed.Edits) > 0 {
		return false
	}
	switch parsed.Result.Status {
	case "blocked", "failed":
		return terminalBecauseContextMissing(reportText(parsed))
	default:
		return false
	}
}

func shouldRetryUnproductiveTerminalAfterRepair(packet Packet, parsed *stream.ParseResult) bool {
	if !packet.Repair ||
		packet.Role != RoleImplementer ||
		parsed == nil ||
		parsed.Result == nil ||
		len(parsed.Commands) > 0 ||
		len(parsed.Edits) > 0 {
		return false
	}
	switch parsed.Result.Status {
	case "blocked", "failed":
		return true
	default:
		return false
	}
}

func shouldRetryUnproductiveTerminalWithoutProgress(role Role, parsed *stream.ParseResult) bool {
	if !roleCanInspectWithCommands(role) ||
		parsed == nil ||
		parsed.Result == nil ||
		len(parsed.Commands) > 0 ||
		len(parsed.Edits) > 0 {
		return false
	}
	switch parsed.Result.Status {
	case "blocked", "failed":
		return terminalBecauseContextMissing(reportText(parsed))
	default:
		return false
	}
}

func terminalBecauseContextMissing(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	for _, phrase := range []string{
		"need to inspect",
		"need inspect",
		"need more context",
		"not enough context",
		"insufficient context",
		"source_context is insufficient",
		"need more file",
		"need file",
		"cannot edit without",
		"can't edit without",
		"command output truncated",
		"output preview",
		"preview is incomplete",
		"stopped after requesting",
		"stopped before",
		"requesting repository inspection",
		"before any source edits",
		"before source edits",
		"repairing the rejected source edit",
		"emitting a valid unified diff",
		"inspecting the current worktree",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func roleCanInspectWithCommands(role Role) bool {
	return role == RoleImplementer || role == RoleReviewer
}

func reportText(parsed *stream.ParseResult) string {
	if parsed == nil {
		return ""
	}
	var b strings.Builder
	for _, frame := range parsed.Frames {
		if frame.Type == stream.FrameReportText {
			b.WriteString(frame.Text)
			if !strings.HasSuffix(frame.Text, "\n") {
				b.WriteByte('\n')
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func appendProviderTurn(b *strings.Builder, turn int, raw string) {
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	fmt.Fprintf(b, "<<< midgard-provider-turn:%d >>>\n", turn)
	b.WriteString(raw)
	if !strings.HasSuffix(raw, "\n") {
		b.WriteByte('\n')
	}
}

func appendCommandTurn(b *strings.Builder, turn int, text string) {
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	fmt.Fprintf(b, "command_turn:%d\n", turn)
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteByte('\n')
	}
}
