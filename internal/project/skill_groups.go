package project

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SkillGroups is manually managed catalog metadata plus project-scoped group
// masks. Installed skill files remain owned by their original distributors.
type SkillGroups struct{ Path string }

type SkillGroupDocument struct {
	Groups            map[string][]string `json:"groups"`
	DisabledByProject map[string][]string `json:"disabled_by_project,omitempty"`
}

func OpenSkillGroups() (SkillGroups, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return SkillGroups{}, err
	}
	return SkillGroups{Path: filepath.Join(base, "midgard", "skill-groups.json")}, nil
}

func (s SkillGroups) Groups() (map[string][]string, error) {
	document, err := s.read()
	if err != nil {
		return nil, err
	}
	result := map[string][]string{}
	for group, names := range document.Groups {
		result[group] = append([]string(nil), names...)
	}
	return result, nil
}

func (s SkillGroups) Assign(group string, skills []string) error {
	group = strings.TrimSpace(group)
	if group == "" {
		return errors.New("skill group name is required")
	}
	document, err := s.read()
	if err != nil {
		return err
	}
	// A skill belongs to one group so the UI and prompt catalog stay unambiguous.
	for current, names := range document.Groups {
		filtered := names[:0]
		for _, name := range names {
			if !containsSkill(skills, name) {
				filtered = append(filtered, name)
			}
		}
		if len(filtered) == 0 {
			delete(document.Groups, current)
		} else {
			document.Groups[current] = filtered
		}
	}
	unique := map[string]bool{}
	for _, name := range skills {
		if name = strings.TrimSpace(name); name != "" {
			unique[name] = true
		}
	}
	for name := range unique {
		document.Groups[group] = append(document.Groups[group], name)
	}
	sort.Strings(document.Groups[group])
	return s.write(document)
}

func (s SkillGroups) Clear(group string) error {
	document, err := s.read()
	if err != nil {
		return err
	}
	delete(document.Groups, strings.TrimSpace(group))
	for projectID, groups := range document.DisabledByProject {
		filtered := groups[:0]
		for _, current := range groups {
			if current != group {
				filtered = append(filtered, current)
			}
		}
		document.DisabledByProject[projectID] = filtered
	}
	return s.write(document)
}

func (s SkillGroups) Disabled(projectID string) ([]string, error) {
	document, err := s.read()
	if err != nil {
		return nil, err
	}
	return append([]string(nil), document.DisabledByProject[projectID]...), nil
}

func (s SkillGroups) SetEnabled(projectID, group string, enabled bool) error {
	document, err := s.read()
	if err != nil {
		return err
	}
	if _, ok := document.Groups[group]; !ok {
		return errors.New("skill group is not configured")
	}
	values := map[string]bool{}
	for _, current := range document.DisabledByProject[projectID] {
		values[current] = true
	}
	if enabled {
		delete(values, group)
	} else {
		values[group] = true
	}
	document.DisabledByProject[projectID] = document.DisabledByProject[projectID][:0]
	for current := range values {
		document.DisabledByProject[projectID] = append(document.DisabledByProject[projectID], current)
	}
	sort.Strings(document.DisabledByProject[projectID])
	return s.write(document)
}

func (s SkillGroups) read() (SkillGroupDocument, error) {
	document := SkillGroupDocument{Groups: map[string][]string{}, DisabledByProject: map[string][]string{}}
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return document, nil
	}
	if err != nil {
		return document, err
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return document, err
	}
	if document.Groups == nil {
		document.Groups = map[string][]string{}
	}
	if document.DisabledByProject == nil {
		document.DisabledByProject = map[string][]string{}
	}
	return document, nil
}

func (s SkillGroups) write(document SkillGroupDocument) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	return writeAtomic(s.Path, document)
}

func containsSkill(skills []string, wanted string) bool {
	for _, skill := range skills {
		if strings.TrimSpace(skill) == wanted {
			return true
		}
	}
	return false
}
