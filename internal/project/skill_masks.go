package project

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SkillMasks stores reversible per-project catalog exclusions separately from
// installed skill content and from the event-sourced session kernel.
type SkillMasks struct{ Path string }

func OpenSkillMasks() (SkillMasks, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return SkillMasks{}, err
	}
	return SkillMasks{Path: filepath.Join(base, "midgard", "skill-masks.json")}, nil
}

func (s SkillMasks) Disabled(projectID string) ([]string, error) {
	values, err := s.read()
	if err != nil {
		return nil, err
	}
	return append([]string(nil), values[projectID]...), nil
}

func (s SkillMasks) Set(projectID string, disabled []string) error {
	if strings.TrimSpace(s.Path) == "" {
		return errors.New("project skill settings are unavailable")
	}
	if strings.TrimSpace(projectID) == "" {
		return errors.New("project id is required")
	}
	values, err := s.read()
	if err != nil {
		return err
	}
	unique := map[string]bool{}
	for _, name := range disabled {
		name = strings.TrimSpace(name)
		if name != "" {
			unique[name] = true
		}
	}
	disabled = disabled[:0]
	for name := range unique {
		disabled = append(disabled, name)
	}
	sort.Strings(disabled)
	values[projectID] = disabled
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	return writeAtomic(s.Path, values)
}

func (s SkillMasks) read() (map[string][]string, error) {
	values := map[string][]string{}
	if strings.TrimSpace(s.Path) == "" {
		return values, nil
	}
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	return values, nil
}
