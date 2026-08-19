package bragi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

const ProtocolID = "bragi/1.0"

type Limits struct {
	MaxLineBytes    int `json:"max_line_bytes"`
	MaxLiteralBytes int `json:"max_literal_bytes"`
	MaxEntities     int `json:"max_entities"`
	MaxRecords      int `json:"max_records"`
}

type FieldRule struct {
	Kinds       []ValueKind `json:"kinds"`
	Cardinality string      `json:"cardinality"`
	Literal     bool        `json:"literal,omitempty"`
}

type EntityRule struct {
	Mutation          string               `json:"mutation"`
	Effect            string               `json:"effect"`
	Required          []string             `json:"required,omitempty"`
	PublicationGuards []string             `json:"publication_guards,omitempty"`
	FieldOrder        []string             `json:"field_order,omitempty"`
	Fields            map[string]FieldRule `json:"fields"`
}

type Profile struct {
	Protocol    string                `json:"protocol"`
	Name        string                `json:"name"`
	Version     string                `json:"version"`
	Fingerprint string                `json:"-"`
	Limits      Limits                `json:"limits"`
	Types       map[string]EntityRule `json:"types"`
}

func LoadProfile(reader io.Reader) (Profile, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return Profile{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var profile Profile
	if err := decoder.Decode(&profile); err != nil {
		return Profile{}, fmt.Errorf("decode profile: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Profile{}, fmt.Errorf("decode profile: trailing JSON value")
		}
		return Profile{}, fmt.Errorf("decode profile: %w", err)
	}
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	sum := sha256.Sum256(data)
	profile.Fingerprint = "sha256:" + hex.EncodeToString(sum[:])
	return profile, nil
}

func (p Profile) Validate() error {
	if p.Protocol != ProtocolID {
		return fmt.Errorf("profile protocol is %q, want %q", p.Protocol, ProtocolID)
	}
	if !validName(p.Name) || p.Version == "" {
		return fmt.Errorf("profile requires a valid name and non-empty version")
	}
	if p.Limits.MaxLineBytes <= 0 || p.Limits.MaxLiteralBytes <= 0 || p.Limits.MaxEntities <= 0 || p.Limits.MaxRecords <= 0 {
		return fmt.Errorf("profile limits must all be positive")
	}
	if len(p.Types) == 0 {
		return fmt.Errorf("profile requires at least one entity type")
	}
	for typeName, rule := range p.Types {
		if !validName(typeName) {
			return fmt.Errorf("invalid entity type %q", typeName)
		}
		if rule.Mutation != "immutable" && rule.Mutation != "revisioned" {
			return fmt.Errorf("entity type %s has invalid mutation %q", typeName, rule.Mutation)
		}
		if !slices.Contains([]string{"none", "host-action", "human-question", "completion-proposal"}, rule.Effect) {
			return fmt.Errorf("entity type %s has invalid effect %q", typeName, rule.Effect)
		}
		for field, fieldRule := range rule.Fields {
			if !validFieldPattern(field) {
				return fmt.Errorf("entity type %s has invalid field pattern %q", typeName, field)
			}
			if fieldRule.Cardinality != "scalar" && fieldRule.Cardinality != "collection" {
				return fmt.Errorf("field %s.%s has invalid cardinality", typeName, field)
			}
			if len(fieldRule.Kinds) == 0 {
				return fmt.Errorf("field %s.%s has no value kinds", typeName, field)
			}
			for _, kind := range fieldRule.Kinds {
				if !slices.Contains([]ValueKind{ValueString, ValueNumber, ValueBool, ValueNull, ValueRef}, kind) {
					return fmt.Errorf("field %s.%s has invalid value kind %q", typeName, field, kind)
				}
			}
			if fieldRule.Cardinality == "collection" && (!slices.Contains(fieldRule.Kinds, ValueRef) || len(fieldRule.Kinds) != 1) {
				return fmt.Errorf("collection field %s.%s must contain only refs", typeName, field)
			}
			if fieldRule.Literal && (fieldRule.Cardinality != "scalar" || !slices.Contains(fieldRule.Kinds, ValueString)) {
				return fmt.Errorf("literal field %s.%s must be a scalar string", typeName, field)
			}
		}
		for _, field := range append(slices.Clone(rule.Required), rule.PublicationGuards...) {
			if _, ok := rule.Fields[field]; !ok {
				return fmt.Errorf("entity type %s references undeclared exact field %q", typeName, field)
			}
		}
		seenOrder := map[string]bool{}
		for _, field := range rule.FieldOrder {
			if _, ok := rule.Fields[field]; !ok {
				return fmt.Errorf("entity type %s orders undeclared field pattern %q", typeName, field)
			}
			if seenOrder[field] {
				return fmt.Errorf("entity type %s repeats field pattern %q in field_order", typeName, field)
			}
			seenOrder[field] = true
		}
	}
	return nil
}

func (r EntityRule) fieldRule(path string) (FieldRule, bool) {
	if rule, ok := r.Fields[path]; ok {
		return rule, true
	}
	longest := ""
	var selected FieldRule
	for pattern, rule := range r.Fields {
		if !strings.HasSuffix(pattern, ".*") {
			continue
		}
		prefix := strings.TrimSuffix(pattern, ".*")
		if strings.HasPrefix(path, prefix+".") && len(prefix) > len(longest) {
			longest = prefix
			selected = rule
		}
	}
	return selected, longest != ""
}

func validFieldPattern(pattern string) bool {
	if strings.HasSuffix(pattern, ".*") {
		pattern = strings.TrimSuffix(pattern, ".*")
	}
	if pattern == "" {
		return false
	}
	for _, segment := range strings.Split(pattern, ".") {
		if !validName(segment) {
			return false
		}
	}
	return true
}
