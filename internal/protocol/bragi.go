package protocol

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	bragi "github.com/coadan/bragi"
)

//go:embed midgard-v1.json
var midgardProfile []byte

const ID = bragi.ProtocolID

type Negotiation struct {
	Protocol           string `json:"protocol"`
	Profile            string `json:"profile"`
	ProfileVersion     string `json:"profile_version"`
	ProfileFingerprint string `json:"profile_fingerprint"`
}

type Update struct {
	Event       bragi.Event
	Entity      bragi.Entity
	Exists      bool
	Publishable bool
}

type HostAction struct {
	EntityID  string
	Name      string
	Reason    string
	Arguments json.RawMessage
}

type Turn struct {
	decoder      *bragi.Decoder
	materializer *bragi.Materializer
	profile      bragi.Profile
}

func NewTurn() (*Turn, error) {
	profile, err := bragi.LoadProfile(strings.NewReader(string(midgardProfile)))
	if err != nil {
		return nil, fmt.Errorf("load Midgard model-protocol profile: %w", err)
	}
	materializer, err := bragi.NewMaterializer(profile)
	if err != nil {
		return nil, err
	}
	return &Turn{
		decoder:      bragi.NewDecoder(bragi.DecoderOptions{MaxLineBytes: profile.Limits.MaxLineBytes}),
		materializer: materializer,
		profile:      profile,
	}, nil
}

func (t *Turn) Negotiation() Negotiation {
	return Negotiation{Protocol: bragi.ProtocolID, Profile: t.profile.Name,
		ProfileVersion: t.profile.Version, ProfileFingerprint: t.profile.Fingerprint}
}

// BeginSource starts the next provider-authored Bragi source while retaining
// the materialized entities and canonical event sequence for this Midgard
// turn. Each provider response has its own decoder completion boundary.
func (t *Turn) BeginSource() {
	t.decoder = bragi.NewDecoder(bragi.DecoderOptions{MaxLineBytes: t.profile.Limits.MaxLineBytes})
}

func (t *Turn) Write(text string) []Update {
	records, diagnostics := t.decoder.Write([]byte(text))
	return t.apply(records, diagnostics)
}

func (t *Turn) FinishCompleted() []Update {
	records, diagnostics := t.decoder.FinishCompleted()
	return t.apply(records, diagnostics)
}

func (t *Turn) FinishInterrupted() []Update {
	return t.apply(nil, t.decoder.Finish())
}

func (t *Turn) apply(records []bragi.Record, diagnostics []bragi.Diagnostic) []Update {
	updates := make([]Update, 0, len(records)+len(diagnostics))
	for _, issue := range diagnostics {
		event := t.materializer.RejectSource(issue)
		updates = append(updates, Update{Event: event})
	}
	for _, record := range records {
		for _, event := range t.materializer.Apply(record) {
			update := Update{Event: event}
			if event.EntityID != "" {
				update.Entity, update.Exists = t.materializer.Entity(event.EntityID)
				update.Publishable = t.materializer.Publishable(event.EntityID)
			}
			updates = append(updates, update)
		}
	}
	return updates
}

func (t *Turn) Events() []bragi.Event { return t.materializer.Events() }

func (t *Turn) EventCount() int { return len(t.materializer.Events()) }

func (t *Turn) ValidateComplete() []bragi.Diagnostic { return t.materializer.ValidateComplete() }

func (t *Turn) HostActions() ([]HostAction, error) {
	return t.HostActionsSince(0)
}

func (t *Turn) HostActionsSince(eventIndex int) ([]HostAction, error) {
	var actions []HostAction
	events := t.materializer.Events()
	if eventIndex < 0 || eventIndex > len(events) {
		return nil, fmt.Errorf("invalid Bragi event index %d", eventIndex)
	}
	for _, event := range events[eventIndex:] {
		if event.Kind != "commit.accepted" || event.Effect != "host-action" {
			continue
		}
		entity, ok := t.materializer.Entity(event.EntityID)
		if !ok || entity.Type != "tool" || entity.Revision != event.Revision {
			return nil, fmt.Errorf("accepted host action %s has no matching tool revision", event.EntityID)
		}
		name, ok := stringField(entity, "name")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("accepted tool %s has no name", event.EntityID)
		}
		reason, _ := stringField(entity, "reason")
		arguments := map[string]any{}
		for path, field := range entity.Fields {
			if !strings.HasPrefix(path, "arguments.") {
				continue
			}
			name := strings.TrimPrefix(path, "arguments.")
			if field.Scalar != nil {
				arguments[name] = scalarValue(*field.Scalar)
			} else {
				arguments[name] = field.Literal
			}
		}
		raw, err := json.Marshal(arguments)
		if err != nil {
			return nil, err
		}
		actions = append(actions, HostAction{EntityID: event.EntityID, Name: name, Reason: reason, Arguments: raw})
	}
	return actions, nil
}

func (t *Turn) FinalMessages() []string {
	return t.FinalMessagesSince(0)
}

func (t *Turn) FinalMessagesSince(eventIndex int) []string {
	var messages []string
	events := t.materializer.Events()
	if eventIndex < 0 || eventIndex > len(events) {
		return nil
	}
	for _, event := range events[eventIndex:] {
		if event.Kind != "commit.accepted" || event.Effect != "none" {
			continue
		}
		entity, ok := t.materializer.Entity(event.EntityID)
		if !ok || entity.Type != "message" || entity.Revision != event.Revision {
			continue
		}
		speaker, _ := stringField(entity, "speaker")
		audience, _ := stringField(entity, "audience")
		channel, _ := stringField(entity, "channel")
		content, _ := stringField(entity, "content")
		if speaker == "assistant" && audience == "user" && channel == "final" && content != "" {
			messages = append(messages, content)
		}
	}
	return messages
}

func (t *Turn) CompletionProposed() bool {
	return t.CompletionProposedSince(0)
}

func (t *Turn) CompletionProposedSince(eventIndex int) bool {
	events := t.materializer.Events()
	if eventIndex < 0 || eventIndex > len(events) {
		return false
	}
	for _, event := range events[eventIndex:] {
		if event.Kind == "commit.accepted" && event.Effect == "completion-proposal" {
			return true
		}
	}
	return false
}

func stringField(entity bragi.Entity, path string) (string, bool) {
	field, ok := entity.Fields[path]
	if !ok || field.Open {
		return "", false
	}
	if field.Scalar != nil && field.Scalar.Kind == bragi.ValueString {
		return field.Scalar.String, true
	}
	if field.Scalar == nil {
		return field.Literal, true
	}
	return "", false
}

func scalarValue(value bragi.Value) any {
	switch value.Kind {
	case bragi.ValueString, bragi.ValueRef:
		return value.String
	case bragi.ValueNumber:
		number, err := strconv.ParseFloat(value.Number, 64)
		if err == nil {
			return number
		}
		return value.Number
	case bragi.ValueBool:
		return value.Bool
	case bragi.ValueNull:
		return nil
	default:
		return nil
	}
}

func EntityJSON(entity bragi.Entity) string {
	raw, _ := json.Marshal(entity)
	return string(raw)
}

func ProfileBytes() []byte { return append([]byte(nil), midgardProfile...) }
