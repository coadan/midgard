package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"midgard/internal/stream"
)

const ProtocolVersion = "midgard-agent-stream-v1"

type Packet struct {
	TaskID              string
	Role                Role
	ModelID             string
	ProviderID          string
	ProviderFingerprint string
	ProtocolVersion     string
	ProtocolFingerprint string
	System              string
	Context             string
	MaxOutputTokens     int
	Repair              bool
	RepairInstructions  string
	ProviderInstruction string
}

type PacketInput struct {
	TaskID              string
	Role                Role
	ModelID             string
	Context             string
	Budget              stream.Budget
	ProviderInstruction string
}

type SourceEditApplyFailure struct {
	Attempt               int
	RemainingAttempts     int
	File                  string
	Repo                  string
	Action                string
	Reason                string
	ContentArtifact       string
	FailedPatchArtifact   string
	StderrArtifact        string
	SourceContextArtifact string
	FailedPatchPreview    string
	StderrPreview         string
	SourceContextPreview  string
	PartialApplied        bool
	Error                 string
}

type EmptyImplementationFailure struct {
	Attempt           int
	RemainingAttempts int
}

func BuildPacket(input PacketInput) (Packet, error) {
	if _, err := input.Role.ReportPath(); err != nil {
		return Packet{}, err
	}
	budget := input.Budget
	if budget == (stream.Budget{}) {
		budget = stream.DefaultBudget()
	}
	return Packet{
		TaskID:              input.TaskID,
		Role:                input.Role,
		ModelID:             input.ModelID,
		ProtocolVersion:     ProtocolVersion,
		ProtocolFingerprint: ProtocolFingerprint(),
		System:              commonProtocolInstruction() + "\n\n" + roleInstruction(input.Role),
		Context:             input.Context,
		MaxOutputTokens:     budget.ProviderMaxTokens,
		ProviderInstruction: input.ProviderInstruction,
	}, nil
}

func ProtocolFingerprint() string {
	sum := sha256.Sum256([]byte(commonProtocolInstruction()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func RepairPacket(base Packet, repair *stream.RepairPacket) Packet {
	base.Repair = true
	reportPath, err := base.Role.ReportPath()
	if err != nil {
		reportPath = repair.ReportArtifact
	}
	if repair.Mode == "result-only" {
		base.RepairInstructions = fmt.Sprintf(`Repair only the unresolved final result frame from the previous Midgard stream.
Midgard preserved the accepted report, payloads, edits, commands, refs, and frame ids.
Emit exactly one corrected @result line and nothing else. Do not repeat @report or report prose.
Use the canonical report artifact for this role: %s.

required_shape:
%s

remaining_repair_attempts:%d
parser_issues:
%s
accepted_last_frame:%d
sealed_payloads:%v
raw_tail:
%s`, reportPath, repair.ResultTemplate, repair.RemainingAttempts, formatRepairIssues(repair.Issues), repair.LastFrameID, repair.SealedPayloadRefs, repair.RawTail)
		return base
	}
	base.RepairInstructions = fmt.Sprintf(`Repair the previous Midgard stream.
The replacement response is parsed independently; it must be a complete valid stream.
Start the repair response with exactly @report %s.
Do not emit @result until every required @payload, @edit, and @cmd frame has already been emitted.
End with exactly one @result line whose artifact is %s, and emit nothing after it.
Use minimal report text, and reuse existing sealed payload artifact refs when possible instead of repeating large payloads.
Use the required report artifact for this role: %s.
Do not use repair.mdx or any other report path.
If raw_tail shows frames after an earlier @result, re-emit the intended frames in valid order before the final @result.

remaining_repair_attempts:%d
parser_issues:
%s
accepted_last_frame:%d
report:%s
draft_payloads:%v
sealed_payloads:%v
raw_tail:
%s`, reportPath, reportPath, reportPath, repair.RemainingAttempts, formatRepairIssues(repair.Issues), repair.LastFrameID, repair.ReportArtifact, repair.DraftPayloadRefs, repair.SealedPayloadRefs, repair.RawTail)
	return base
}

func formatRepairIssues(issues []stream.RepairIssue) string {
	if len(issues) == 0 {
		return "- none"
	}
	var b strings.Builder
	for _, issue := range issues {
		fmt.Fprintf(&b, "- code:%s line:%d message:%s\n", issue.Code, issue.Line, strings.ReplaceAll(issue.Message, "\n", " "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func CommandContinuationPacket(base Packet, parsed *stream.ParseResult, commandResults string) Packet {
	reportPath, err := base.Role.ReportPath()
	if err != nil {
		reportPath = ""
	}
	if base.Role == RoleReviewer {
		base.ProviderInstruction = fmt.Sprintf(`Continue the same reviewer role after executing your requested command(s).
Use the command results below as evidence. Do not claim command output you do not see.
Open with @report %s, write a concise review update, and either:
- emit more read-only @cmd lines if another bounded inspection/check is required, without @result, or
- end with exactly one @result line when the review verdict is ready.
Do not emit @payload or @edit. Reviewer commands are for inspection/checks, not source mutation.
Keep review findings high-signal: correctness, security, behavior regressions, and missing tests for changed behavior.
Return status:approved only when there are no P0/P1 findings. Return status:changes-requested when any P0/P1 finding remains.

previous_stream_tail:
%s

command_results:
%s`, reportPath, packetRawTail(parsed.Raw, 2048), commandResults)
		return base
	}
	base.ProviderInstruction = fmt.Sprintf(`Continue the same Midgard role after executing your requested command(s).
Use the command results below as evidence. Do not claim command output you do not see.
Open with @report %s, write a concise updated report, and either:
- emit more @cmd lines if another bounded inspection/check is required, without @result, or
- emit any required @payload/@edit frames and end with exactly one @result line when done.
Do not return status:blocked merely because a preview is incomplete; ask for a narrower @cmd.
Only return status:blocked for a concrete blocker that cannot be resolved by more bounded repo inspection/check commands.
Do not mutate source files with @cmd unless you are running an audited generated script or patch command; prefer @payload/@edit for source changes.

previous_stream_tail:
%s

command_results:
%s`, reportPath, packetRawTail(parsed.Raw, 2048), commandResults)
	return base
}

func UnproductiveCommandContinuationPacket(base Packet, parsed *stream.ParseResult, commandResults string) Packet {
	reportPath, err := base.Role.ReportPath()
	if err != nil {
		reportPath = ""
	}
	if base.Role == RoleReviewer {
		base.ProviderInstruction = fmt.Sprintf(`Continue the same reviewer role.
Your previous continuation returned a terminal status after command output, but Midgard did not accept it because it still described missing context.
Open with @report %s, write a concise review update, and choose one:
- if more source context is needed, emit narrow read-only @cmd lines and no @result;
- if the command output is enough, end with status:approved or status:changes-requested;
- if the task truly cannot be reviewed, end with status:blocked and name the exact blocker that no further bounded command can resolve.
Do not emit @payload or @edit.

previous_terminal_stream_tail:
%s

command_results:
%s`, reportPath, packetRawTail(parsed.Raw, 2048), commandResults)
		return base
	}
	base.ProviderInstruction = fmt.Sprintf(`Continue the same Midgard role.
Your previous continuation returned a terminal status after command output, but Midgard did not accept it because it did not include edits, follow-up @cmd, or a concrete external blocker.
Open with @report %s, write a concise updated report, and choose one:
- if more source context is needed, emit narrow @cmd lines and no @result;
- if the command output is enough, emit the required @payload/@edit frames and end with status:ready;
- if the task truly cannot proceed, end with status:blocked and name the exact blocker that no further bounded command can resolve.
Do not mutate source files with @cmd unless you are running an audited generated script or patch command; prefer @payload/@edit for source changes.

previous_terminal_stream_tail:
%s

command_results:
%s`, reportPath, packetRawTail(parsed.Raw, 2048), commandResults)
	return base
}

func CommandContinuationLimitPacket(base Packet, parsed *stream.ParseResult, commandResults string, maxCommandTurns int) Packet {
	reportPath, err := base.Role.ReportPath()
	if err != nil {
		reportPath = ""
	}
	if base.Role == RoleReviewer {
		base.ProviderInstruction = fmt.Sprintf(`Continue the same reviewer role.
Command inspection budget is exhausted after %d command continuation turns.
Midgard will not execute more @cmd lines for this role.
Open with @report %s, write a concise final review, do not emit @cmd, and end with exactly one @result:
- status:approved if there are no P0/P1 findings;
- status:changes-requested if any P0/P1 finding remains;
- status:blocked only for a concrete external blocker.
Do not emit @payload or @edit.

requested_command_stream_tail:
%s

command_results:
%s`, maxCommandTurns, reportPath, packetRawTail(parsed.Raw, 2048), commandResults)
		return base
	}
	base.ProviderInstruction = fmt.Sprintf(`Continue the same Midgard role.
Command inspection budget is exhausted after %d command continuation turns.
Midgard will not execute more @cmd lines for this role.
Open with @report %s, write a concise updated report, and do not emit @cmd.
Choose one:
- if enough information is available, emit the required @payload/@edit frames and end with status:ready;
- if no source change is appropriate, explain why and end with status:no-op;
- if the task truly cannot proceed, end with status:blocked and name the exact external blocker.
Do not return status:ready unless the output includes source edits or the current worktree already has the needed diff.

requested_command_stream_tail:
%s

command_results:
%s`, maxCommandTurns, reportPath, packetRawTail(parsed.Raw, 2048), commandResults)
	return base
}

func UnproductiveTerminalRepairPacket(base Packet, parsed *stream.ParseResult) Packet {
	reportPath, err := base.Role.ReportPath()
	if err != nil {
		reportPath = ""
	}
	base.Repair = true
	extra := fmt.Sprintf(`The previous repair response returned a terminal status without source edits, follow-up @cmd, or a concrete blocker.
Midgard did not accept that as a completed repair.
Open with @report %s and choose one:
- if more source context is needed, emit narrow @cmd lines and no @result;
- if enough context is available, emit corrected @payload/@edit patch frames and status:ready;
- if the task truly cannot proceed, end with status:blocked and name the exact blocker that no further bounded command can resolve.

previous_terminal_stream_tail:
%s`, reportPath, packetRawTail(parsed.Raw, 2048))
	if strings.TrimSpace(base.RepairInstructions) == "" {
		base.RepairInstructions = extra
	} else {
		base.RepairInstructions += "\n\n" + extra
	}
	return base
}

func UnproductiveTerminalPacket(base Packet, parsed *stream.ParseResult) Packet {
	reportPath, err := base.Role.ReportPath()
	if err != nil {
		reportPath = ""
	}
	if base.Role == RoleReviewer {
		base.ProviderInstruction = fmt.Sprintf(`Continue the same reviewer role.
Your previous response returned a terminal status because more inspection was needed, but it did not emit @cmd.
Midgard did not accept that as completed review output.
Open with @report %s and choose one:
- if more source context is needed, emit narrow read-only @cmd lines and no @result;
- if enough context is available, end with status:approved or status:changes-requested;
- if the task truly cannot be reviewed, end with status:blocked and name the exact blocker that no further bounded command can resolve.
Do not emit @payload or @edit.

previous_terminal_stream_tail:
%s`, reportPath, packetRawTail(parsed.Raw, 2048))
		return base
	}
	base.ProviderInstruction = fmt.Sprintf(`Continue the same Midgard role.
Your previous response returned a terminal status because more inspection was needed, but it did not emit @cmd and did not make source edits.
Midgard did not accept that as completed output.
Open with @report %s and choose one:
- if more source context is needed, emit narrow @cmd lines and no @result;
- if enough context is available, emit @payload/@edit patch frames and status:ready;
- if the task truly cannot proceed, end with status:blocked and name the exact blocker that no further bounded command can resolve.

previous_terminal_stream_tail:
%s`, reportPath, packetRawTail(parsed.Raw, 2048))
	return base
}

func packetRawTail(raw string, limit int) string {
	if len(raw) <= limit {
		return raw
	}
	return raw[len(raw)-limit:]
}

func SourceEditRepairPacket(base Packet, failure SourceEditApplyFailure) Packet {
	base.Repair = true
	base.RepairInstructions = fmt.Sprintf(`Repair the previous implementer source edit.
The Midgard stream parsed successfully, but git could not apply the patch.
Open with @report implementation.mdx and write only a short repair note.
Patch the current worktree state. Do not reuse the failed patch unchanged. Earlier edits from the previous stream may already be applied.
If partial_applied is true, preserve the current diff and emit only the remaining correction needed on top of it.
Treat source_context_preview as authoritative: copy exact adjacent source lines, not context remembered from the failed patch.
For a corrected patch, use narrow hunks with exact current context, emit the patch payload and @edit, then end with @result.
For repeated literal replacements where another patch would be fragile, emit a sealed Python script payload, a mode:script @edit, and an @cmd that runs the script from artifact_dir before @result.
If the exact current text is still unclear, emit a narrow read-only @cmd and no @result so Midgard can return the output.
Use compact output and do not repeat the full diagnostics.

attempt:%d
remaining_attempts:%d
partial_applied:%t
file:%s
repo:%s
action:%s
reason:%s
content:%s
failed_patch:%s
stderr:%s
source_context:%s
error:%s

failed_patch_preview:
%s

stderr_preview:
%s

source_context_preview:
%s`, failure.Attempt, failure.RemainingAttempts, failure.PartialApplied, failure.File, failure.Repo, failure.Action, failure.Reason, failure.ContentArtifact, failure.FailedPatchArtifact, failure.StderrArtifact, failure.SourceContextArtifact, failure.Error, failure.FailedPatchPreview, failure.StderrPreview, failure.SourceContextPreview)
	return base
}

func EmptyImplementationRepairPacket(base Packet, failure EmptyImplementationFailure) Packet {
	base.Repair = true
	base.RepairInstructions = fmt.Sprintf(`Repair the previous implementer output.
The stream ended with status:ready, but after applying edits and executing commands, Midgard found no worktree diff.
That is not a completed implementation for an edit task.
Open with @report implementation.mdx and choose one:
- if a code change is required, inspect with @cmd as needed, then emit @payload/@edit patch frames and status:ready;
- if no source change is actually appropriate, explain why and end with status:no-op.
Do not return status:ready unless the output includes source edits or leaves a non-empty worktree diff.

attempt:%d
remaining_attempts:%d`, failure.Attempt, failure.RemainingAttempts)
	return base
}

func (p Packet) UserContent() string {
	content := p.Context
	if p.ProviderInstruction != "" {
		content = p.ProviderInstruction + "\n\n" + content
	}
	if p.Repair {
		content += "\n\n" + p.RepairInstructions
	}
	return content
}

func commonProtocolInstruction() string {
	return `You must follow the Midgard Agent Stream Protocol V1.
Use top-level control tags only: @say, @report, @payload, @edit, @ref, @cmd, @result, and @err.
Open your role report with @report before writing report prose.
Write human-facing report content as safe MDX.
For substantial generated edit content, use @payload begin, stream payload bytes, then @payload end.
For source-file patches, use this exact sequence:
@payload begin type:patch path:patches/<short-name>.diff
<unified diff>
@payload end
@edit file:<repo-relative-path> action:<create|modify|delete|move> mode:patch content:artifact:patches/<short-name>.diff reason:<slug> [repo:<repo-id>]
Midgard applies implementer mode:patch edits to the task worktree.
Patch payloads must be raw unified diffs only: no markdown fences, no explanatory text,
and every replacement must include both the removed '-' line and added '+' line.
Use @cmd only for commands that should actually run.
When you need command output before you can finish a role, emit one or more
@cmd lines and do not emit @result in that turn; Midgard will execute them and
continue with bounded stdout/stderr previews.
Never emit @payload, @edit, @cmd, @ref, @say, or report prose after @result.
Do not invent command output. Only refer to file content present in the task
context packet or generated by your own payloads.
Payload controls must use the full form:
@payload begin type:<script|patch|file|json|text> path:<artifact-path> [lang:<id>]
Edit controls must include file, action, mode, and reason fields.
Do not emit large JSON. Prefer refs and compact result fields.
End with exactly one @result line.`
}

func roleInstruction(role Role) string {
	switch role {
	case RolePlanner:
		return `Planner output must start with exactly:
@report plan.mdx

Then write a short safe-MDX report. End with exactly one:
@result status:ready artifact:plan.mdx checks:<ids-or-none>

Do not emit @payload, @edit, or @cmd.
Allowed planner statuses: ready, blocked, failed.`
	case RoleImplementer:
		return `Implementer output must start with exactly:
@report implementation.mdx

Use mode:patch @edit frames for direct source edits. Use shell command proposals for checks or script execution. End with exactly one:
@result status:ready artifact:implementation.mdx checks:<ids-or-none>

If source_context is insufficient, inspect with @cmd repo:<repo-id> -- <command> and wait for command results before editing.
Do not return blocked solely because file context is missing while @cmd inspection can still gather it.
Prefer rg/sed/git commands for inspection. Do not use @cmd to make source edits unless executing an audited generated artifact.
Treat repository_guidance as authoritative repository instructions. Before editing a nested directory, inspect any closer AGENTS.md that applies.
When Heimdal is listed as available and browser behavior is in scope, use it for Playwright browser QA from the task worktree.
If review_findings is present, address every P0/P1 finding against the current worktree_diff before returning ready.
If latest_role_statuses or latest_role_reports contains a changes-requested review, fix that review against the current worktree_diff.
Patch the current worktree state; do not reapply the same rejected patch.
Allowed implementer statuses: ready, no-op, blocked, failed.`
	case RoleReviewer:
		return `Reviewer output must start with exactly:
@report review.mdx

Act like a high-signal Codex code review pass. Review the actual worktree_diff, not just implementation prose.
Focus on P0/P1 findings: correctness bugs, security/privacy risks, behavior regressions, broken API contracts,
missing tests for changed behavior, and task-objective gaps. Avoid style-only comments unless they hide a real bug.
Approve only when the diff satisfies the objective and no P0/P1 finding remains.
Request changes if the old defect remains, the patch duplicates text, the diff is empty for an edit task,
the implementation report claims a change that the diff does not show, or tests/evidence are materially missing.

You may inspect like any other role: emit read-only @cmd repo:<repo-id> -- <command> lines and no @result when you need
bounded source context, git diff details, or check output before deciding. Prefer rg, sed, git diff/show/status,
and focused test/check commands. Do not use reviewer @cmd to mutate source files.

When findings exist, write them in review.mdx as concise bullets with severity/id, path/line if known,
evidence, impact, and the expected fix. End with exactly one:
@result status:changes-requested artifact:review.mdx findings:<ids>

When no P0/P1 findings remain, state that and end with exactly one:
@result status:approved artifact:review.mdx findings:none

Do not emit @payload or @edit.
Allowed reviewer statuses: approved, changes-requested, blocked, failed.`
	case RoleCompactor:
		return `Compactor output must start with exactly:
@report compaction.mdx

Preserve task state and hydration refs. End with exactly one:
@result status:ready artifact:compaction.mdx refs:<ids-or-none>

Allowed compactor statuses: ready, blocked, failed.`
	default:
		return ""
	}
}
