package stream

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"midgard/internal/artifact"
)

type Parser struct {
	Role   string
	Store  artifact.Store
	Budget Budget
}

type reportDraft struct {
	path  string
	data  bytes.Buffer
	state string
}

type payloadDraft struct {
	record artifact.Record
	data   bytes.Buffer
}

type lineSpan struct {
	text      string
	line      int
	byteStart int64
	byteEnd   int64
}

func NewParser(role string, store artifact.Store, budget Budget) Parser {
	if budget == (Budget{}) {
		budget = DefaultBudget()
	}
	return Parser{Role: role, Store: store, Budget: budget}
}

func (p Parser) ParseString(raw string) (*ParseResult, error) {
	return p.Parse(strings.NewReader(raw))
}

func (p Parser) Parse(r io.Reader) (*ParseResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	raw := string(data)
	result := &ParseResult{
		Raw:               raw,
		ProviderMaxTokens: p.Budget.ProviderMaxTokens,
	}
	reports := map[string]*reportDraft{}
	payloads := map[string]*payloadDraft{}
	var activeReport *reportDraft
	var activePayload *payloadDraft
	var totalReportBytes int64
	var totalPayloadBytes int64
	var commandCount int
	pendingPatchEditIndex := -1

	if p.Budget.MaxStreamBytes > 0 && int64(len(data)) > p.Budget.MaxStreamBytes {
		result.Errors = append(result.Errors, recoverableError("stream_limit_exceeded", "stream byte limit exceeded", 0))
	}

	for _, originalSpan := range splitLines(data) {
		spans := []lineSpan{originalSpan}
		if activePayload == nil {
			spans = splitInlineControlSpan(originalSpan)
		}
		for _, span := range spans {
			if p.Budget.MaxLineBytes > 0 && len(span.text) > p.Budget.MaxLineBytes {
				result.Errors = append(result.Errors, recoverableError("line_limit_exceeded", "line byte limit exceeded", span.line))
			}
			if activePayload != nil {
				if strings.TrimRight(span.text, "\r\n") == "@payload end" {
					frame := p.frame(result, FramePayload, span, map[string]string{"action": "end"}, "")
					_ = frame
					if activePayload.record.State != artifact.StateRejected {
						activePayload.record.State = artifact.StateSealed
						if rec, err := p.Store.Put(activePayload.record, activePayload.data.Bytes()); err != nil {
							activePayload.record.State = artifact.StateRejected
							result.Errors = append(result.Errors, recoverableError("rejected_payload", err.Error(), span.line))
						} else {
							activePayload.record = rec
							if pendingPatchEditIndex >= 0 &&
								pendingPatchEditIndex < len(result.Edits) &&
								result.Edits[pendingPatchEditIndex].Content == "" &&
								rec.PayloadType == "patch" {
								result.Edits[pendingPatchEditIndex].Content = rec.Ref()
								pendingPatchEditIndex = -1
							}
						}
					}
					activePayload = nil
					continue
				}
				payloadLine := span.text
				activePayload.data.WriteString(payloadLine)
				totalPayloadBytes += int64(len(payloadLine))
				if payloadLimitExceeded(p.Budget, totalPayloadBytes, int64(activePayload.data.Len())) {
					activePayload.record.State = artifact.StateRejected
					result.Errors = append(result.Errors, recoverableError("rejected_payload", "payload byte limit exceeded", span.line))
				}
				continue
			}

			if strings.HasPrefix(span.text, "@@") {
				appendContent(activeReport, unescapeControlLine(span.text), &totalReportBytes)
				continue
			}
			if !strings.HasPrefix(span.text, "@") {
				appendContent(activeReport, span.text, &totalReportBytes)
				if activeReport != nil {
					p.frame(result, FrameReportText, span, nil, span.text)
				}
				continue
			}

			tag, payload, known := splitControl(span.text)
			if !known {
				appendContent(activeReport, span.text, &totalReportBytes)
				continue
			}
			if result.Result != nil {
				result.Errors = append(result.Errors, recoverableError("content_after_result", "stream continued after @result", span.line))
			}

			switch tag {
			case "say":
				p.frame(result, FrameSay, span, nil, payload)
			case "report":
				path := strings.TrimSpace(payload)
				if err := artifact.ValidateReportPath(p.Role, path); err != nil {
					result.Errors = append(result.Errors, recoverableError("invalid_artifact_path", err.Error(), span.line))
					appendContent(activeReport, span.text, &totalReportBytes)
					continue
				}
				report := reports[path]
				if report == nil {
					report = &reportDraft{path: path, state: artifact.StateDraft}
					reports[path] = report
				}
				activeReport = report
				p.frame(result, FrameReport, span, map[string]string{"path": path}, "")
			case "payload":
				fields, ok := parsePayloadControl(payload)
				if !ok {
					result.Errors = append(result.Errors, recoverableError("malformed_known_tag", "malformed @payload control line", span.line))
					appendContent(activeReport, span.text, &totalReportBytes)
					continue
				}
				if fields["action"] != "begin" {
					result.Errors = append(result.Errors, recoverableError("unexpected_payload_end", "@payload end without active payload", span.line))
					continue
				}
				path := fields["path"]
				payloadType := fields["type"]
				if err := artifact.ValidatePath(path); err != nil {
					result.Errors = append(result.Errors, recoverableError("invalid_artifact_path", err.Error(), span.line))
					result.Artifacts = append(result.Artifacts, artifact.Record{
						Path:         path,
						Type:         artifact.TypePayload,
						State:        artifact.StateRejected,
						ProducerRole: p.Role,
						PayloadType:  payloadType,
						Lang:         fields["lang"],
					})
					continue
				}
				if err := artifact.ValidatePayloadType(payloadType); err != nil {
					result.Errors = append(result.Errors, recoverableError("rejected_payload", err.Error(), span.line))
					result.Artifacts = append(result.Artifacts, artifact.Record{
						Path:         path,
						Type:         artifact.TypePayload,
						State:        artifact.StateRejected,
						ProducerRole: p.Role,
						PayloadType:  payloadType,
						Lang:         fields["lang"],
					})
					continue
				}
				activePayload = &payloadDraft{record: artifact.Record{
					Path:         path,
					Type:         artifact.TypePayload,
					State:        artifact.StateDraft,
					ProducerRole: p.Role,
					PayloadType:  payloadType,
					Lang:         fields["lang"],
				}}
				payloads[path] = activePayload
				p.frame(result, FramePayload, span, fields, "")
			case "edit":
				fields, ok := parseFields(payload)
				if !ok || fields["file"] == "" || fields["action"] == "" || fields["mode"] == "" || fields["reason"] == "" {
					result.Errors = append(result.Errors, recoverableError("malformed_known_tag", "malformed @edit control line", span.line))
					appendContent(activeReport, span.text, &totalReportBytes)
					continue
				}
				if err := validateRepoPath(fields["file"]); err != nil {
					result.Errors = append(result.Errors, recoverableError("invalid_artifact_path", err.Error(), span.line))
					continue
				}
				frame := p.frame(result, FrameEdit, span, fields, "")
				result.Edits = append(result.Edits, EditIntent{
					FrameID: frame.ID,
					File:    fields["file"],
					Action:  fields["action"],
					Mode:    fields["mode"],
					Reason:  fields["reason"],
					Content: fields["content"],
					Repo:    fields["repo"],
					To:      fields["to"],
				})
				if fields["mode"] == "patch" && fields["content"] == "" {
					pendingPatchEditIndex = len(result.Edits) - 1
				}
			case "ref":
				kind, target, ok := strings.Cut(strings.TrimSpace(payload), ":")
				if !ok || kind == "" || target == "" {
					result.Errors = append(result.Errors, recoverableError("malformed_known_tag", "malformed @ref control line", span.line))
					appendContent(activeReport, span.text, &totalReportBytes)
					continue
				}
				frame := p.frame(result, FrameRef, span, map[string]string{"kind": kind, "target": target}, "")
				result.Refs = append(result.Refs, Ref{FrameID: frame.ID, Kind: kind, Target: target})
			case "cmd":
				fields, command, ok := parseCommand(payload)
				if !ok {
					result.Errors = append(result.Errors, recoverableError("malformed_known_tag", "malformed @cmd control line", span.line))
					appendContent(activeReport, span.text, &totalReportBytes)
					continue
				}
				commandCount++
				if p.Budget.MaxCommandProposals > 0 && commandCount > p.Budget.MaxCommandProposals {
					result.Errors = append(result.Errors, recoverableError("command_limit_exceeded", "command proposal limit exceeded", span.line))
					continue
				}
				frame := p.frame(result, FrameCommand, span, fields, command)
				result.Commands = append(result.Commands, CommandProposal{FrameID: frame.ID, Repo: fields["repo"], Command: command})
			case "result":
				fields, ok := parseFields(payload)
				if !ok || fields["status"] == "" || fields["artifact"] == "" {
					result.Errors = append(result.Errors, recoverableError("malformed_known_tag", "malformed @result control line", span.line))
					appendContent(activeReport, span.text, &totalReportBytes)
					continue
				}
				if result.Result != nil {
					result.Errors = append(result.Errors, recoverableError("multiple_results", "multiple @result lines", span.line))
					continue
				}
				if !statusAllowed(p.Role, fields["status"]) {
					result.Errors = append(result.Errors, recoverableError("invalid_status", fmt.Sprintf("status %q is not allowed for role %q", fields["status"], p.Role), span.line))
					continue
				}
				frame := p.frame(result, FrameResult, span, fields, "")
				result.Result = &ResultFrame{FrameID: frame.ID, Status: fields["status"], Artifact: fields["artifact"], Fields: fields}
			case "err":
				fields, ok := parseFields(payload)
				if !ok || fields["code"] == "" || fields["severity"] == "" {
					result.Errors = append(result.Errors, recoverableError("malformed_known_tag", "malformed @err control line", span.line))
					appendContent(activeReport, span.text, &totalReportBytes)
					continue
				}
				p.frame(result, FrameError, span, fields, "")
			}
		}
	}

	if activePayload != nil {
		result.Errors = append(result.Errors, recoverableError("open_payload", "payload was still open at end of stream", 0))
	}
	if result.Result == nil {
		result.Errors = append(result.Errors, recoverableError("missing_result", "stream did not end with @result", 0))
	}
	if p.Budget.MaxReportBytes > 0 && totalReportBytes > p.Budget.MaxReportBytes {
		result.Errors = append(result.Errors, recoverableError("report_limit_exceeded", "report byte limit exceeded", 0))
	}

	for _, report := range reports {
		state := artifact.StateDraft
		if result.Result != nil && result.Result.Artifact == report.path {
			state = artifact.StateSealed
		}
		if err := artifact.ValidateSafeMDX(report.data.Bytes()); err != nil {
			state = artifact.StateRejected
			result.Errors = append(result.Errors, recoverableError("report_rejected", err.Error(), 0))
		}
		if p.Budget.MaxReportBytes > 0 && int64(report.data.Len()) > p.Budget.MaxReportBytes {
			state = artifact.StateRejected
		}
		rec, err := p.Store.Put(artifact.Record{
			Path:         report.path,
			Type:         artifact.TypeReport,
			State:        state,
			ProducerRole: p.Role,
		}, report.data.Bytes())
		if err != nil {
			return nil, err
		}
		result.Artifacts = append(result.Artifacts, rec)
	}
	if result.Result != nil {
		if _, ok := reports[result.Result.Artifact]; !ok {
			result.Errors = append(result.Errors, recoverableError("missing_report", "result artifact was not opened with @report", 0))
		}
	}
	for _, payload := range payloads {
		if payload.record.State == artifact.StateDraft {
			rec, err := p.Store.Put(payload.record, payload.data.Bytes())
			if err != nil {
				return nil, err
			}
			payload.record = rec
		}
		result.Artifacts = append(result.Artifacts, payload.record)
	}
	for _, edit := range result.Edits {
		if edit.Mode == "patch" && edit.Content == "" {
			result.Errors = append(result.Errors, recoverableError("missing_edit_content", "patch edit is missing content artifact", 0))
		}
	}
	result.Repair = buildRepairPacket(result, p.Budget)
	return result, nil
}

func (p Parser) frame(result *ParseResult, frameType FrameType, span lineSpan, fields map[string]string, text string) Frame {
	if fields == nil {
		fields = map[string]string{}
	}
	frame := Frame{
		ID:        len(result.Frames) + 1,
		Type:      frameType,
		Raw:       span.text,
		Line:      span.line,
		ByteStart: span.byteStart,
		ByteEnd:   span.byteEnd,
		Fields:    fields,
		Text:      text,
	}
	result.Frames = append(result.Frames, frame)
	if p.Budget.MaxFrames > 0 && len(result.Frames) > p.Budget.MaxFrames {
		result.Errors = append(result.Errors, recoverableError("frame_limit_exceeded", "frame count limit exceeded", span.line))
	}
	return frame
}

func splitLines(data []byte) []lineSpan {
	var spans []lineSpan
	var offset int64
	line := 1
	for len(data) > 0 {
		next := bytes.IndexByte(data, '\n')
		var part []byte
		if next == -1 {
			part = data
			data = nil
		} else {
			part = data[:next+1]
			data = data[next+1:]
		}
		spans = append(spans, lineSpan{
			text:      string(part),
			line:      line,
			byteStart: offset,
			byteEnd:   offset + int64(len(part)),
		})
		offset += int64(len(part))
		line++
	}
	return spans
}

func splitInlineControlSpan(span lineSpan) []lineSpan {
	if strings.HasPrefix(span.text, "@") {
		return []lineSpan{span}
	}
	index := inlineControlIndex(span.text)
	if index <= 0 {
		return []lineSpan{span}
	}
	return []lineSpan{
		{
			text:      span.text[:index],
			line:      span.line,
			byteStart: span.byteStart,
			byteEnd:   span.byteStart + int64(index),
		},
		{
			text:      span.text[index:],
			line:      span.line,
			byteStart: span.byteStart + int64(index),
			byteEnd:   span.byteEnd,
		},
	}
}

func inlineControlIndex(text string) int {
	best := -1
	for _, marker := range []string{"@say ", "@report ", "@payload ", "@edit ", "@ref ", "@cmd ", "@result ", "@err "} {
		index := strings.Index(text, marker)
		if index <= 0 {
			continue
		}
		if !inlineControlLooksValid(text[index:]) {
			continue
		}
		if best == -1 || index < best {
			best = index
		}
	}
	return best
}

func inlineControlLooksValid(text string) bool {
	tag, payload, known := splitControl(text)
	if !known {
		return false
	}
	switch tag {
	case "say":
		return true
	case "report":
		return strings.TrimSpace(payload) != ""
	case "payload":
		_, ok := parsePayloadControl(payload)
		return ok
	case "edit":
		fields, ok := parseFields(payload)
		return ok && fields["file"] != "" && fields["action"] != "" && fields["mode"] != "" && fields["reason"] != ""
	case "ref":
		kind, target, ok := strings.Cut(strings.TrimSpace(payload), ":")
		return ok && kind != "" && target != ""
	case "cmd":
		_, _, ok := parseCommand(payload)
		return ok
	case "result":
		fields, ok := parseFields(payload)
		return ok && fields["status"] != "" && fields["artifact"] != ""
	case "err":
		fields, ok := parseFields(payload)
		return ok && fields["code"] != "" && fields["severity"] != ""
	default:
		return false
	}
}

func splitControl(line string) (tag, payload string, known bool) {
	trimmed := strings.TrimRight(line, "\r\n")
	withoutAt := strings.TrimPrefix(trimmed, "@")
	tag, payload, _ = strings.Cut(withoutAt, " ")
	if !slices.Contains([]string{"say", "report", "payload", "edit", "ref", "cmd", "result", "err"}, tag) {
		return "", "", false
	}
	return tag, payload, true
}

func parsePayloadControl(payload string) (map[string]string, bool) {
	parts := strings.Fields(payload)
	if len(parts) == 1 && parts[0] == "end" {
		return map[string]string{"action": "end"}, true
	}
	if len(parts) < 2 || parts[0] != "begin" {
		return nil, false
	}
	fields, ok := parseFields(strings.Join(parts[1:], " "))
	if !ok || fields["type"] == "" || fields["path"] == "" {
		return nil, false
	}
	fields["action"] = "begin"
	return fields, true
}

func parseFields(payload string) (map[string]string, bool) {
	fields := map[string]string{}
	i := 0
	for {
		for i < len(payload) && isSpace(payload[i]) {
			i++
		}
		if i >= len(payload) {
			break
		}
		keyStart := i
		for i < len(payload) && payload[i] != ':' && !isSpace(payload[i]) {
			i++
		}
		if keyStart == i || i >= len(payload) || payload[i] != ':' {
			return nil, false
		}
		key := payload[keyStart:i]
		i++
		if i >= len(payload) {
			return nil, false
		}
		value := ""
		if payload[i] == '"' {
			valueStart := i
			i++
			escaped := false
			for i < len(payload) {
				if escaped {
					escaped = false
					i++
					continue
				}
				if payload[i] == '\\' {
					escaped = true
					i++
					continue
				}
				if payload[i] == '"' {
					i++
					break
				}
				i++
			}
			if i > len(payload) || payload[i-1] != '"' {
				return nil, false
			}
			unquoted, err := strconv.Unquote(payload[valueStart:i])
			if err != nil || unquoted == "" {
				return nil, false
			}
			value = unquoted
			if i < len(payload) && !isSpace(payload[i]) {
				return nil, false
			}
		} else {
			valueStart := i
			for i < len(payload) && !isSpace(payload[i]) {
				i++
			}
			if valueStart == i {
				return nil, false
			}
			value = payload[valueStart:i]
		}
		fields[key] = value
	}
	return fields, true
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

func parseCommand(payload string) (map[string]string, string, bool) {
	head, command, ok := strings.Cut(payload, " -- ")
	if !ok {
		return nil, "", false
	}
	fields, ok := parseFields(head)
	if !ok || fields["repo"] == "" || strings.TrimSpace(command) == "" {
		return nil, "", false
	}
	return fields, strings.TrimRight(command, "\r\n"), true
}

func appendContent(report *reportDraft, text string, totalReportBytes *int64) {
	if report == nil {
		return
	}
	report.data.WriteString(text)
	*totalReportBytes += int64(len(text))
}

func unescapeControlLine(line string) string {
	if strings.HasPrefix(line, "@@") {
		return line[1:]
	}
	return line
}

func payloadLimitExceeded(budget Budget, totalPayloadBytes, singlePayloadBytes int64) bool {
	return (budget.MaxPayloadBytes > 0 && totalPayloadBytes > budget.MaxPayloadBytes) ||
		(budget.MaxSinglePayloadBytes > 0 && singlePayloadBytes > budget.MaxSinglePayloadBytes)
}

func validateRepoPath(path string) error {
	if path == "" {
		return fmt.Errorf("repo path is empty")
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("repo path %q is absolute", path)
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean != path {
		return fmt.Errorf("repo path %q is not clean", path)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return fmt.Errorf("repo path %q escapes repo root", path)
		}
	}
	return nil
}

func statusAllowed(role, status string) bool {
	allowed := map[string][]string{
		"planner":     {"ready", "blocked", "failed"},
		"implementer": {"ready", "no-op", "blocked", "failed"},
		"reviewer":    {"approved", "changes-requested", "blocked", "failed"},
		"compactor":   {"ready", "blocked", "failed"},
	}
	return slices.Contains(allowed[role], status)
}

func recoverableError(code, message string, line int) ParserError {
	return ParserError{Code: code, Message: message, Line: line, Recoverable: true}
}
