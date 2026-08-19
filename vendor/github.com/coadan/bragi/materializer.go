package bragi

import (
	"fmt"
	"reflect"
	"slices"
)

type EntityStatus string

const (
	StatusDraft     EntityStatus = "draft"
	StatusCommitted EntityStatus = "committed"
)

type Field struct {
	Scalar     *Value   `json:"scalar,omitempty"`
	References []string `json:"references,omitempty"`
	Literal    string   `json:"literal,omitempty"`
	Open       bool     `json:"open,omitempty"`
}

type ResolvedReference struct {
	Target   string `json:"target"`
	Revision int    `json:"revision"`
}

type Revision struct {
	Number     int                            `json:"number"`
	Fields     map[string]Field               `json:"fields"`
	References map[string][]ResolvedReference `json:"references,omitempty"`
}

type Entity struct {
	ID        string           `json:"id"`
	Type      string           `json:"type"`
	Revision  int              `json:"revision"`
	Status    EntityStatus     `json:"status"`
	Fields    map[string]Field `json:"fields"`
	Committed []Revision       `json:"committed,omitempty"`
}

type Event struct {
	Sequence   uint64                         `json:"seq"`
	Kind       string                         `json:"kind"`
	Record     *Record                        `json:"record,omitempty"`
	Diagnostic *Diagnostic                    `json:"diagnostic,omitempty"`
	EntityID   string                         `json:"entity_id,omitempty"`
	Revision   int                            `json:"revision,omitempty"`
	Effect     string                         `json:"effect,omitempty"`
	References map[string][]ResolvedReference `json:"references,omitempty"`
}

type Materializer struct {
	profile  Profile
	entities map[string]Entity
	events   []Event
	records  int
}

func NewMaterializer(profile Profile) (*Materializer, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	return &Materializer{profile: profile, entities: map[string]Entity{}}, nil
}

// Replay reconstructs materialized state from a canonical event sequence.
// It does not execute effects or reinterpret rejected source.
func Replay(profile Profile, events []Event) (*Materializer, error) {
	materializer, err := NewMaterializer(profile)
	if err != nil {
		return nil, err
	}
	pendingCommit := ""
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			return nil, fmt.Errorf("canonical sequence at index %d is %d, want %d", index, event.Sequence, index+1)
		}
		if pendingCommit != "" && event.Kind != "commit.accepted" && event.Kind != "commit.rejected" {
			return nil, fmt.Errorf("event %d does not resolve commit proposal for %s", event.Sequence, pendingCommit)
		}
		switch event.Kind {
		case "op.accepted":
			if event.Record == nil || event.Record.Operation == OpCommit || event.Record.Raw != "" || event.EntityID != entityIDFromTarget(event.Record.Target) {
				return nil, fmt.Errorf("event %d has invalid accepted operation", event.Sequence)
			}
			if issue := materializer.applyOperation(*event.Record); issue != nil {
				return nil, fmt.Errorf("replay event %d: %s", event.Sequence, issue.Message)
			}
			materializer.records++
		case "commit.proposed":
			if event.Record == nil || event.Record.Operation != OpCommit || event.Record.Raw != "" || event.EntityID != event.Record.Target {
				return nil, fmt.Errorf("event %d has invalid commit proposal", event.Sequence)
			}
			materializer.records++
			pendingCommit = event.EntityID
		case "commit.accepted":
			if pendingCommit == "" || pendingCommit != event.EntityID {
				return nil, fmt.Errorf("event %d does not match a commit proposal", event.Sequence)
			}
			accepted, issue := materializer.commit(Record{Operation: OpCommit, Target: event.EntityID})
			if issue != nil {
				return nil, fmt.Errorf("replay event %d: %s", event.Sequence, issue.Message)
			}
			if accepted.EntityID != event.EntityID || accepted.Revision != event.Revision || accepted.Effect != event.Effect || !reflect.DeepEqual(accepted.References, event.References) {
				return nil, fmt.Errorf("replay event %d does not match committed revision", event.Sequence)
			}
			pendingCommit = ""
		case "commit.rejected":
			if pendingCommit == "" || pendingCommit != event.EntityID {
				return nil, fmt.Errorf("event %d does not match a commit proposal", event.Sequence)
			}
			if event.Diagnostic == nil {
				return nil, fmt.Errorf("event %d has no rejection diagnostic", event.Sequence)
			}
			if _, issue := materializer.commit(Record{Operation: OpCommit, Target: event.EntityID}); issue == nil {
				return nil, fmt.Errorf("event %d rejects a valid commit", event.Sequence)
			}
			pendingCommit = ""
		case "op.rejected":
			if event.Record == nil || event.Record.Operation == OpCommit || event.Record.Raw != "" || event.Diagnostic == nil || event.EntityID != entityIDFromTarget(event.Record.Target) {
				return nil, fmt.Errorf("event %d has invalid rejected operation", event.Sequence)
			}
			if issue := materializer.applyOperation(*event.Record); issue == nil {
				return nil, fmt.Errorf("event %d rejects a valid operation", event.Sequence)
			}
			materializer.records++
		case "source.rejected":
			if event.Diagnostic == nil {
				return nil, fmt.Errorf("event %d has no source diagnostic", event.Sequence)
			}
			// Rejected input is evidence only and never changes materialized state.
		default:
			return nil, fmt.Errorf("event %d has unknown core kind %q", event.Sequence, event.Kind)
		}
	}
	if pendingCommit != "" {
		return nil, fmt.Errorf("canonical log ends with unresolved commit proposal for %s", pendingCommit)
	}
	materializer.events = cloneEvents(events)
	return materializer, nil
}

func (m *Materializer) Apply(record Record) []Event {
	if m.records >= m.profile.Limits.MaxRecords {
		return []Event{m.rejectRecord(record, "record_limit", "profile record limit exceeded")}
	}
	m.records++
	if record.Operation == OpCommit {
		proposal := m.appendEvent(Event{Kind: "commit.proposed", Record: canonicalRecord(record), EntityID: record.Target})
		accepted, issue := m.commit(record)
		if issue != nil {
			rejected := m.appendEvent(Event{Kind: "commit.rejected", Diagnostic: issue, EntityID: record.Target})
			return []Event{proposal, rejected}
		}
		accepted = m.appendEvent(accepted)
		return []Event{proposal, accepted}
	}
	if issue := m.applyOperation(record); issue != nil {
		return []Event{m.rejectRecord(record, issue.Code, issue.Message)}
	}
	return []Event{m.appendEvent(Event{Kind: "op.accepted", Record: canonicalRecord(record), EntityID: entityIDFromTarget(record.Target)})}
}

func (m *Materializer) RejectSource(issue Diagnostic) Event {
	return m.appendEvent(Event{Kind: "source.rejected", Diagnostic: &issue})
}

func (m *Materializer) Events() []Event {
	return cloneEvents(m.events)
}

func (m *Materializer) Entity(id string) (Entity, bool) {
	entity, ok := m.entities[id]
	return cloneEntity(entity), ok
}

func (m *Materializer) Publishable(id string) bool {
	entity, ok := m.entities[id]
	if !ok {
		return false
	}
	rule := m.profile.Types[entity.Type]
	for _, guard := range rule.PublicationGuards {
		field, present := entity.Fields[guard]
		if !present || field.Open {
			return false
		}
	}
	return true
}

func (m *Materializer) ValidateComplete() []Diagnostic {
	var issues []Diagnostic
	for _, entity := range m.entities {
		for path, field := range entity.Fields {
			if field.Open {
				issues = append(issues, Diagnostic{Code: "open_literal", Message: fmt.Sprintf("%s.%s is open", entity.ID, path)})
			}
		}
		if entity.Status != StatusCommitted {
			issues = append(issues, Diagnostic{Code: "draft_entity", Message: fmt.Sprintf("%s revision %d is not committed", entity.ID, entity.Revision)})
		}
	}
	return issues
}

func (m *Materializer) applyOperation(record Record) *Diagnostic {
	switch record.Operation {
	case OpCreate:
		if len(m.entities) >= m.profile.Limits.MaxEntities {
			return issueFor(record, "entity_limit", "profile entity limit exceeded")
		}
		if _, exists := m.entities[record.Target]; exists {
			return issueFor(record, "entity_exists", "entity ID already exists")
		}
		if _, exists := m.profile.Types[record.EntityType]; !exists {
			return issueFor(record, "unknown_entity_type", "entity type is not declared by the profile")
		}
		m.entities[record.Target] = Entity{
			ID: record.Target, Type: record.EntityType, Revision: 1, Status: StatusDraft, Fields: map[string]Field{},
		}
		return nil
	case OpAdd, OpReplace, OpRemove, OpLiteralOpen, OpLiteralReplace, OpLiteralAppend, OpLiteralSeal:
		return m.applyFieldOperation(record)
	default:
		return issueFor(record, "unsupported_operation", "operation is not supported by the materializer")
	}
}

func (m *Materializer) applyFieldOperation(record Record) *Diagnostic {
	entityID, path, valid := splitPath(record.Target)
	if !valid || path == "" {
		return issueFor(record, "invalid_path", "field operation requires a field path")
	}
	current, exists := m.entities[entityID]
	if !exists {
		return issueFor(record, "unknown_entity", "target entity does not exist")
	}
	typeRule := m.profile.Types[current.Type]
	fieldRule, declared := typeRule.fieldRule(path)
	if !declared {
		return issueFor(record, "unknown_field", "field is not declared by the entity profile")
	}
	if current.Status == StatusCommitted {
		if typeRule.Mutation == "immutable" {
			return issueFor(record, "immutable_entity", "accepted entity is immutable; create a new entity")
		}
		current.Status = StatusDraft
		current.Revision++
		current.Fields = cloneFields(current.Fields)
	}
	field, present := current.Fields[path]
	switch record.Operation {
	case OpAdd:
		if record.Value == nil || !valueAllowed(fieldRule, record.Value.Kind) {
			return issueFor(record, "field_type", "value kind is not allowed for this field")
		}
		if fieldRule.Cardinality == "collection" {
			if slices.Contains(field.References, record.Value.String) {
				return issueFor(record, "duplicate_member", "collection already contains the reference")
			}
			field.References = append(field.References, record.Value.String)
			current.Fields[path] = field
			break
		}
		if present {
			return issueFor(record, "field_exists", "scalar field already exists; use replace")
		}
		value := *record.Value
		current.Fields[path] = Field{Scalar: &value}
	case OpReplace:
		if !present || fieldRule.Cardinality != "scalar" || field.Scalar == nil || record.Value == nil {
			return issueFor(record, "replace_target", "replace requires an existing scalar field")
		}
		if !valueAllowed(fieldRule, record.Value.Kind) {
			return issueFor(record, "field_type", "value kind is not allowed for this field")
		}
		value := *record.Value
		current.Fields[path] = Field{Scalar: &value}
	case OpRemove:
		if !present {
			return issueFor(record, "missing_field", "remove target does not exist")
		}
		if fieldRule.Cardinality == "collection" {
			if record.MemberRef == "" || !slices.Contains(field.References, record.MemberRef) {
				return issueFor(record, "missing_member", "collection does not contain the reference")
			}
			field.References = slices.DeleteFunc(field.References, func(value string) bool { return value == record.MemberRef })
			current.Fields[path] = field
			break
		}
		if record.MemberRef != "" {
			return issueFor(record, "unexpected_member", "scalar removal cannot name a collection member")
		}
		delete(current.Fields, path)
	case OpLiteralOpen, OpLiteralReplace:
		if !fieldRule.Literal {
			return issueFor(record, "literal_forbidden", "field does not allow literal mode")
		}
		if record.Operation == OpLiteralOpen && present {
			return issueFor(record, "field_exists", "literal field already exists; use replace")
		}
		if record.Operation == OpLiteralReplace && !present {
			return issueFor(record, "replace_target", "literal replace requires an existing field")
		}
		current.Fields[path] = Field{Literal: "", Open: true}
	case OpLiteralAppend:
		if !present || !field.Open || record.Value == nil {
			return issueFor(record, "literal_not_open", "literal continuation requires an open literal")
		}
		if len(field.Literal)+len(record.Value.String)+1 > m.profile.Limits.MaxLiteralBytes {
			return issueFor(record, "literal_limit", "literal exceeded the profile byte limit")
		}
		field.Literal += record.Value.String + "\n"
		current.Fields[path] = field
	case OpLiteralSeal:
		if !present || !field.Open {
			return issueFor(record, "literal_not_open", "literal seal requires an open literal")
		}
		field.Open = false
		current.Fields[path] = field
	}
	m.entities[entityID] = current
	return nil
}

func (m *Materializer) commit(record Record) (Event, *Diagnostic) {
	entity, exists := m.entities[record.Target]
	if !exists {
		return Event{}, issueFor(record, "unknown_entity", "commit target does not exist")
	}
	if entity.Status != StatusDraft {
		return Event{}, issueFor(record, "entity_not_draft", "entity has no draft revision to commit")
	}
	rule := m.profile.Types[entity.Type]
	for _, required := range rule.Required {
		field, present := entity.Fields[required]
		if !present || field.Open {
			return Event{}, issueFor(record, "missing_required_field", fmt.Sprintf("required field %s is missing or open", required))
		}
	}
	resolved := map[string][]ResolvedReference{}
	for path, field := range entity.Fields {
		if field.Open {
			return Event{}, issueFor(record, "open_literal", fmt.Sprintf("literal %s is still open", path))
		}
		var refs []string
		if field.Scalar != nil && field.Scalar.Kind == ValueRef {
			refs = append(refs, field.Scalar.String)
		}
		refs = append(refs, field.References...)
		for _, ref := range refs {
			target, ok := m.entities[ref]
			if !ok || target.Status != StatusCommitted {
				return Event{}, issueFor(record, "uncommitted_reference", fmt.Sprintf("reference %s is not committed", ref))
			}
			resolved[path] = append(resolved[path], ResolvedReference{Target: ref, Revision: target.Revision})
		}
	}
	entity.Status = StatusCommitted
	entity.Committed = append(entity.Committed, Revision{
		Number: entity.Revision, Fields: cloneFields(entity.Fields), References: cloneReferences(resolved),
	})
	m.entities[entity.ID] = entity
	return Event{
		Kind: "commit.accepted", EntityID: entity.ID, Revision: entity.Revision, Effect: rule.Effect, References: resolved,
	}, nil
}

func (m *Materializer) rejectRecord(record Record, code, message string) Event {
	issue := issueFor(record, code, message)
	return m.appendEvent(Event{Kind: "op.rejected", Record: canonicalRecord(record), Diagnostic: issue, EntityID: entityIDFromTarget(record.Target)})
}

func (m *Materializer) appendEvent(event Event) Event {
	event.Sequence = uint64(len(m.events) + 1)
	m.events = append(m.events, event)
	return event
}

func canonicalRecord(record Record) *Record {
	copy := record
	copy.Raw = ""
	copy.Normalizations = slices.Clone(record.Normalizations)
	return &copy
}

func entityIDFromTarget(target string) string {
	entityID, _, _ := splitPath(target)
	return entityID
}

func issueFor(record Record, code, message string) *Diagnostic {
	return &Diagnostic{Code: code, Message: message, Line: record.Line}
}

func valueAllowed(rule FieldRule, kind ValueKind) bool {
	return slices.Contains(rule.Kinds, kind)
}

func cloneEntity(entity Entity) Entity {
	entity.Fields = cloneFields(entity.Fields)
	entity.Committed = slices.Clone(entity.Committed)
	for index := range entity.Committed {
		entity.Committed[index].Fields = cloneFields(entity.Committed[index].Fields)
		entity.Committed[index].References = cloneReferences(entity.Committed[index].References)
	}
	return entity
}

func cloneFields(fields map[string]Field) map[string]Field {
	cloned := make(map[string]Field, len(fields))
	for path, field := range fields {
		if field.Scalar != nil {
			value := *field.Scalar
			field.Scalar = &value
		}
		field.References = slices.Clone(field.References)
		cloned[path] = field
	}
	return cloned
}

func cloneReferences(refs map[string][]ResolvedReference) map[string][]ResolvedReference {
	if refs == nil {
		return nil
	}
	cloned := make(map[string][]ResolvedReference, len(refs))
	for path, values := range refs {
		cloned[path] = slices.Clone(values)
	}
	return cloned
}

func cloneEvents(events []Event) []Event {
	cloned := slices.Clone(events)
	for index := range cloned {
		if cloned[index].Record != nil {
			record := *cloned[index].Record
			if record.Value != nil {
				value := *record.Value
				record.Value = &value
			}
			cloned[index].Record = &record
		}
		if cloned[index].Diagnostic != nil {
			diagnostic := *cloned[index].Diagnostic
			cloned[index].Diagnostic = &diagnostic
		}
		cloned[index].References = cloneReferences(cloned[index].References)
	}
	return cloned
}
