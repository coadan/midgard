package bragi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const DefaultMaxLineBytes = 16 << 10

type DecoderOptions struct {
	MaxLineBytes int
	AllowCRLF    bool
	// StrictSource disables Bragi 1.0's bounded deterministic recovery. It is
	// useful for diagnostics, but tolerant decoding is the protocol default.
	StrictSource bool
}

type Decoder struct {
	options       DecoderOptions
	buffer        []byte
	line          int
	literalTarget string
	discarding    bool
	finished      bool
}

func NewDecoder(options DecoderOptions) *Decoder {
	if options.MaxLineBytes <= 0 {
		options.MaxLineBytes = DefaultMaxLineBytes
	}
	return &Decoder{options: options}
}

func (d *Decoder) Write(chunk []byte) ([]Record, []Diagnostic) {
	if d.finished {
		return nil, []Diagnostic{{
			Code: "decoder_finished", Message: "decoder cannot accept bytes after Finish", Line: d.line + 1,
		}}
	}
	d.buffer = append(d.buffer, chunk...)
	var records []Record
	var diagnostics []Diagnostic
	for {
		if d.discarding {
			newline := bytes.IndexByte(d.buffer, '\n')
			if newline < 0 {
				d.buffer = d.buffer[:0]
				break
			}
			d.buffer = d.buffer[newline+1:]
			d.line++
			d.discarding = false
			continue
		}

		newline := bytes.IndexByte(d.buffer, '\n')
		if newline < 0 {
			if len(d.buffer) > d.options.MaxLineBytes {
				diagnostics = append(diagnostics, Diagnostic{
					Code: "line_too_long", Message: "source record exceeded the line byte limit", Line: d.line + 1,
				})
				d.buffer = d.buffer[:0]
				d.discarding = true
			}
			break
		}

		lineBytes := d.buffer[:newline]
		d.buffer = d.buffer[newline+1:]
		d.line++
		if len(lineBytes) > d.options.MaxLineBytes {
			diagnostics = append(diagnostics, Diagnostic{
				Code: "line_too_long", Message: "source record exceeded the line byte limit", Line: d.line,
			})
			continue
		}
		line := string(lineBytes)
		recoveredCRLF := false
		if strings.HasSuffix(line, "\r") {
			if d.options.StrictSource && !d.options.AllowCRLF {
				diagnostics = append(diagnostics, diagnostic("unexpected_cr", "CRLF is not enabled", d.line, line))
				continue
			}
			line = strings.TrimSuffix(line, "\r")
			recoveredCRLF = true
		}
		record, issue := d.parseLine(line)
		if issue != nil {
			diagnostics = append(diagnostics, *issue)
			continue
		}
		if recoveredCRLF {
			record.Normalizations = append(record.Normalizations, Normalization{
				Kind: "crlf", Source: "CRLF", Canonical: "LF",
			})
		}
		records = append(records, *record)
	}
	return records, diagnostics
}

// FinishCompleted closes a stream after the provider authoritatively reports
// normal completion. A final record missing only its LF is decoded and marked
// as recovered. Abrupt, cancelled, or failed streams must use Finish instead.
func (d *Decoder) FinishCompleted() ([]Record, []Diagnostic) {
	if d.finished {
		return nil, []Diagnostic{{
			Code: "decoder_finished", Message: "decoder cannot finish twice", Line: d.line + 1,
		}}
	}
	var records []Record
	var diagnostics []Diagnostic
	if len(d.buffer) > 0 && !d.discarding {
		records, diagnostics = d.Write([]byte{'\n'})
		for index := range records {
			records[index].Normalizations = append(records[index].Normalizations, Normalization{
				Kind: "terminal-lf", Source: "EOF", Canonical: "LF",
			})
		}
	}
	diagnostics = append(diagnostics, d.Finish()...)
	return records, diagnostics
}

func (d *Decoder) Finish() []Diagnostic {
	if d.finished {
		return nil
	}
	d.finished = true
	var diagnostics []Diagnostic
	if len(d.buffer) > 0 && !d.discarding {
		diagnostics = append(diagnostics, diagnostic(
			"incomplete_record", "provider stream ended before LF", d.line+1, string(d.buffer),
		))
	}
	if d.literalTarget != "" {
		diagnostics = append(diagnostics, diagnostic(
			"open_literal", fmt.Sprintf("literal %s was not sealed", d.literalTarget), d.line+1, "",
		))
	}
	d.buffer = nil
	return diagnostics
}

func (d *Decoder) parseLine(line string) (*Record, *Diagnostic) {
	if !utf8.ValidString(line) {
		issue := diagnostic("invalid_utf8", "source record is not valid UTF-8", d.line, "")
		return nil, &issue
	}
	if strings.IndexByte(line, 0) >= 0 {
		issue := diagnostic("nul_forbidden", "source record contains NUL", d.line, "")
		return nil, &issue
	}
	if d.literalTarget != "" {
		sealTarget := ""
		if strings.HasPrefix(line, "! ") {
			sealTarget, _, _, _ = normalizePath(line[2:], !d.options.StrictSource)
		}
		if sealTarget == d.literalTarget {
			target := d.literalTarget
			d.literalTarget = ""
			record := &Record{Operation: OpLiteralSeal, Target: target, Line: d.line, Raw: line}
			record.noteCaseRecovery(line[2:], target)
			return record, nil
		}
		if strings.HasPrefix(line, "|") {
			value := Value{Kind: ValueString, String: line[1:]}
			return &Record{
				Operation: OpLiteralAppend, Target: d.literalTarget, Value: &value, Line: d.line, Raw: line,
			}, nil
		}
		issue := diagnostic("literal_record_required", "open literal accepts only | continuation or its canonical-equivalent ! seal", d.line, line)
		return nil, &issue
	}
	if line == "" {
		issue := diagnostic("empty_record", "empty source records are not allowed", d.line, line)
		return nil, &issue
	}
	if strings.HasPrefix(line, "! ") {
		rawTarget := line[2:]
		target, valid := normalizeEntityID(rawTarget, !d.options.StrictSource)
		if !valid {
			issue := diagnostic("invalid_commit_target", "commit target must be one entity ID", d.line, line)
			return nil, &issue
		}
		record := &Record{Operation: OpCommit, Target: target, Line: d.line, Raw: line}
		record.noteCaseRecovery(rawTarget, target)
		return record, nil
	}
	if strings.HasPrefix(line, "+ ") || strings.HasPrefix(line, "~ ") {
		return d.parseWrite(line)
	}
	if strings.HasPrefix(line, "- ") {
		return d.parseRemove(line)
	}
	issue := diagnostic("unknown_operator", "source record must begin with +, ~, -, or ! followed by one space", d.line, line)
	return nil, &issue
}

func (d *Decoder) parseWrite(line string) (*Record, *Diagnostic) {
	rest := line[2:]
	rawTarget, tail, ok := strings.Cut(rest, " ")
	if !ok || rawTarget == "" || tail == "" || strings.HasPrefix(tail, " ") || strings.HasSuffix(tail, " ") {
		issue := diagnostic("invalid_record_shape", "write record requires target and value separated by one space", d.line, line)
		return nil, &issue
	}
	target, entityID, field, valid := normalizePath(rawTarget, !d.options.StrictSource)
	if !valid {
		issue := diagnostic("invalid_path", "target is not a valid entity or field path", d.line, line)
		return nil, &issue
	}
	if field == "" {
		entityType, typeValid := normalizeName(tail, !d.options.StrictSource)
		if line[0] != '+' || !typeValid {
			issue := diagnostic("invalid_entity_create", "entity creation is + @id type", d.line, line)
			return nil, &issue
		}
		record := &Record{Operation: OpCreate, Target: entityID, EntityType: entityType, Line: d.line, Raw: line}
		record.noteCaseRecovery(rawTarget, entityID)
		record.noteCaseRecovery(tail, entityType)
		return record, nil
	}
	if tail == "|" {
		d.literalTarget = target
		op := OpLiteralOpen
		if line[0] == '~' {
			op = OpLiteralReplace
		}
		record := &Record{Operation: op, Target: target, Line: d.line, Raw: line}
		record.noteCaseRecovery(rawTarget, target)
		return record, nil
	}
	value, valueNormalization, err := d.parseValue(tail)
	if err != nil {
		issue := diagnostic("invalid_value", err.Error(), d.line, line)
		return nil, &issue
	}
	op := OpAdd
	if line[0] == '~' {
		op = OpReplace
	}
	record := &Record{Operation: op, Target: target, Value: &value, Line: d.line, Raw: line}
	record.noteCaseRecovery(rawTarget, target)
	if valueNormalization != nil {
		record.Normalizations = append(record.Normalizations, *valueNormalization)
	}
	return record, nil
}

func (d *Decoder) parseRemove(line string) (*Record, *Diagnostic) {
	rest := line[2:]
	if rest == "" || strings.HasPrefix(rest, " ") || strings.HasSuffix(rest, " ") {
		issue := diagnostic("invalid_remove", "remove record requires one field path", d.line, line)
		return nil, &issue
	}
	rawTarget, rawMember, hasMember := strings.Cut(rest, " ")
	target, _, field, valid := normalizePath(rawTarget, !d.options.StrictSource)
	if !valid || field == "" {
		issue := diagnostic("invalid_path", "remove target must be a field path", d.line, line)
		return nil, &issue
	}
	member := ""
	if hasMember {
		member, valid = normalizeEntityID(rawMember, !d.options.StrictSource)
	}
	if hasMember && (!valid || strings.Contains(rawMember, " ")) {
		issue := diagnostic("invalid_member_ref", "collection removal requires one entity reference", d.line, line)
		return nil, &issue
	}
	record := &Record{Operation: OpRemove, Target: target, MemberRef: member, Line: d.line, Raw: line}
	record.noteCaseRecovery(rawTarget, target)
	if hasMember {
		record.noteCaseRecovery(rawMember, member)
	}
	return record, nil
}

func (d *Decoder) parseValue(text string) (Value, *Normalization, error) {
	if strings.TrimSpace(text) != text {
		return Value{}, nil, fmt.Errorf("value has non-canonical surrounding whitespace")
	}
	if strings.HasPrefix(text, "@") {
		canonical, valid := normalizeEntityID(text, !d.options.StrictSource)
		if !valid {
			return Value{}, nil, fmt.Errorf("reference is not a valid entity ID")
		}
		return Value{Kind: ValueRef, String: canonical}, caseRecovery(text, canonical), nil
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return Value{}, nil, fmt.Errorf("value is not an RFC 8259 scalar: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Value{}, nil, fmt.Errorf("value contains trailing JSON")
		}
		return Value{}, nil, fmt.Errorf("value contains invalid trailing data: %w", err)
	}
	switch value := decoded.(type) {
	case string:
		return Value{Kind: ValueString, String: value}, nil, nil
	case json.Number:
		return Value{Kind: ValueNumber, Number: value.String()}, nil, nil
	case bool:
		return Value{Kind: ValueBool, Bool: value}, nil, nil
	case nil:
		return Value{Kind: ValueNull}, nil, nil
	default:
		return Value{}, nil, fmt.Errorf("composite JSON values are not supported")
	}
}

func (record *Record) noteCaseRecovery(source, canonical string) {
	if recovered := caseRecovery(source, canonical); recovered != nil {
		record.Normalizations = append(record.Normalizations, *recovered)
	}
}

func caseRecovery(source, canonical string) *Normalization {
	if source == canonical {
		return nil
	}
	return &Normalization{Kind: "ascii-name-case", Source: source, Canonical: canonical}
}

func normalizePath(path string, recoverCase bool) (canonical, entityID, field string, ok bool) {
	rawEntityID, rawField, hasField := strings.Cut(path, ".")
	entityID, ok = normalizeEntityID(rawEntityID, recoverCase)
	if !ok {
		return "", "", "", false
	}
	if !hasField {
		return entityID, entityID, "", true
	}
	if rawField == "" {
		return "", "", "", false
	}
	segments := strings.Split(rawField, ".")
	for index, segment := range segments {
		segments[index], ok = normalizeName(segment, recoverCase)
		if !ok {
			return "", "", "", false
		}
	}
	field = strings.Join(segments, ".")
	return entityID + "." + field, entityID, field, true
}

func normalizeEntityID(value string, recoverCase bool) (string, bool) {
	if !strings.HasPrefix(value, "@") {
		return "", false
	}
	name, ok := normalizeName(value[1:], recoverCase)
	if !ok {
		return "", false
	}
	return "@" + name, true
}

func normalizeName(value string, recoverCase bool) (string, bool) {
	if value == "" || !asciiLetter(value[0], recoverCase) {
		return "", false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if !asciiLetter(character, recoverCase) && (character < '0' || character > '9') && character != '_' && character != '-' {
			return "", false
		}
	}
	if recoverCase {
		return strings.ToLower(value), true
	}
	return value, true
}

func asciiLetter(character byte, recoverCase bool) bool {
	return character >= 'a' && character <= 'z' || recoverCase && character >= 'A' && character <= 'Z'
}

func splitPath(path string) (entityID, field string, ok bool) {
	entityID, field, hasField := strings.Cut(path, ".")
	if !validEntityID(entityID) {
		return "", "", false
	}
	if !hasField {
		return entityID, "", true
	}
	if field == "" {
		return "", "", false
	}
	for _, segment := range strings.Split(field, ".") {
		if !validName(segment) {
			return "", "", false
		}
	}
	return entityID, field, true
}

func validEntityID(value string) bool {
	return strings.HasPrefix(value, "@") && validName(value[1:])
}

func validName(value string) bool {
	if value == "" || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func diagnostic(code, message string, line int, raw string) Diagnostic {
	return Diagnostic{Code: code, Message: message, Line: line, Raw: raw}
}
