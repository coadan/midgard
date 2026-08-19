package policy

import (
	"encoding/json"
)

// Skill describes optional operating guidance available to the coding policy.
// Full instructions are loaded only through a committed skill.read action.
type Skill struct {
	Name        string
	Description string
	Group       string
}

type SkillCatalog interface {
	Summaries() []Skill
	Search(json.RawMessage) (json.RawMessage, error)
	Read(json.RawMessage) (json.RawMessage, error)
}
