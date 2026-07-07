package model

import (
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
}

type CommandHandler func(ctx context.Context, commands []stream.CommandProposal) (string, error)

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
		var raw strings.Builder
		usage, err := r.Provider.Stream(ctx, current, func(delta Delta) error {
			raw.WriteString(delta.Text)
			return nil
		})
		if err != nil {
			return result, err
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
			return result, fmt.Errorf("repair attempts exhausted: %v", parsed.Repair.ErrorCodes)
		}
		repairAttempts++
		current = RepairPacket(current, parsed.Repair)
	}
}

func shouldContinueWithCommands(role Role, parsed *stream.ParseResult) bool {
	if parsed == nil || len(parsed.Commands) == 0 {
		return false
	}
	if parsed.Result == nil {
		return commandContinuationErrorsAllowed(parsed, false)
	}
	if role != RoleImplementer {
		return false
	}
	if len(parsed.Edits) == 0 {
		return commandContinuationErrorsAllowed(parsed, true)
	}
	if !commandContinuationTerminalStatus(parsed.Result.Status) {
		return false
	}
	return parsed.Repair == nil || commandContinuationErrorsAllowed(parsed, true)
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
	if role != RoleImplementer ||
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
	if role != RoleImplementer ||
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
