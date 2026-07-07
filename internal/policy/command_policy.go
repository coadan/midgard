package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type CommandPolicy struct {
	AllowedRoots []string
	EnvAllowlist []string
	Limits       OutputLimits
}

func DefaultCommandPolicy(allowedRoots ...string) CommandPolicy {
	return CommandPolicy{
		AllowedRoots: allowedRoots,
		EnvAllowlist: []string{"PATH", "HOME", "TMPDIR", "TEMP", "TMP"},
		Limits:       DefaultOutputLimits(),
	}
}

func (p CommandPolicy) ValidateCWD(cwd string) error {
	if cwd == "" {
		return fmt.Errorf("cwd is required")
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return err
	}
	for _, root := range p.AllowedRoots {
		inside, err := isInside(root, absCWD)
		if err != nil {
			return err
		}
		if inside {
			return nil
		}
	}
	return fmt.Errorf("cwd %q is outside allowed roots", cwd)
}

func (p CommandPolicy) Environment(extra map[string]string) []string {
	allowed := map[string]bool{}
	for _, key := range p.EnvAllowlist {
		allowed[key] = true
	}
	env := make([]string, 0, len(allowed)+len(extra))
	for _, pair := range os.Environ() {
		key, _, ok := strings.Cut(pair, "=")
		if ok && allowed[key] {
			env = append(env, pair)
		}
	}
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if allowed[key] {
			env = append(env, key+"="+extra[key])
		}
	}
	return env
}

func isInside(root, path string) (bool, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(absRoot, path)
	if err != nil {
		return false, err
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."), nil
}
