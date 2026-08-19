package featuredelivery

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"midgard/internal/policy"
)

const (
	maxSkillReadBytes      = 24 << 10
	maxSkillExcerptBytes   = 16 << 10
	maxSkillSearchBytes    = 1 << 20
	maxSkillMatches        = 8
	maxSkillCatalogMatches = 8
	maxSkillRangeLines     = 120
	maxSkillExcerptLine    = 300
	maxSkillScanDepth      = 6
	maxSkillScanDirs       = 2000
)

type skillEntry struct {
	summary policy.Skill
	dir     string
}

// SkillCatalog is an allowlist built from installed SKILL.md files. Reads are
// resolved from this immutable catalog rather than from model-authored paths.
type SkillCatalog struct {
	entries map[string]skillEntry
}

func (c SkillCatalog) WithGroups(groups map[string][]string) SkillCatalog {
	for group, names := range groups {
		for _, name := range names {
			if entry, ok := c.entries[name]; ok {
				entry.summary.Group = group
				c.entries[name] = entry
			}
		}
	}
	return c
}

type MaskedSkillCatalog struct {
	Source   policy.SkillCatalog
	disabled map[string]bool
}

func MaskSkills(source policy.SkillCatalog, names []string) MaskedSkillCatalog {
	disabled := make(map[string]bool, len(names))
	for _, name := range names {
		disabled[name] = true
	}
	return MaskedSkillCatalog{Source: source, disabled: disabled}
}

func (c MaskedSkillCatalog) Summaries() []policy.Skill {
	if c.Source == nil {
		return nil
	}
	var visible []policy.Skill
	for _, summary := range c.Source.Summaries() {
		if !c.disabled[summary.Name] {
			visible = append(visible, summary)
		}
	}
	return visible
}

func (c MaskedSkillCatalog) Search(raw json.RawMessage) (json.RawMessage, error) {
	return searchSkillSummaries(c.Summaries(), raw)
}

func (c MaskedSkillCatalog) Read(raw json.RawMessage) (json.RawMessage, error) {
	var arguments struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return nil, err
	}
	if c.Source == nil || c.disabled[arguments.Name] {
		return nil, fmt.Errorf("skill %q is not available in this project", arguments.Name)
	}
	return c.Source.Read(raw)
}

// DiscoverInstalledSkills builds Midgard's layered skill catalog. Repository
// skills override user-installed Midgard skills, which override the optional
// Codex compatibility directory.
func DiscoverInstalledSkills(repositoryRoot string) (SkillCatalog, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return SkillCatalog{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return SkillCatalog{}, err
	}
	// First match wins. Project sources override personal sources; native and
	// cross-client Agent Skills locations precede compatibility locations.
	roots := []string{
		filepath.Join(repositoryRoot, ".midgard", "skills"),
		filepath.Join(repositoryRoot, ".agents", "skills"),
		filepath.Join(repositoryRoot, ".github", "skills"),
		filepath.Join(repositoryRoot, ".claude", "skills"),
		filepath.Join(repositoryRoot, "skills"),
		filepath.Join(config, "midgard", "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".copilot", "skills"),
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".codex", "skills"),
	}
	merged := SkillCatalog{entries: map[string]skillEntry{}}
	for _, root := range roots {
		catalog, err := DiscoverSkills(root)
		if err != nil {
			return SkillCatalog{}, err
		}
		for name, entry := range catalog.entries {
			if _, claimed := merged.entries[name]; !claimed {
				merged.entries[name] = entry
			}
		}
	}
	return merged, nil
}

func DiscoverSkills(root string) (SkillCatalog, error) {
	catalog := SkillCatalog{entries: map[string]skillEntry{}}
	if strings.TrimSpace(root) == "" {
		return catalog, nil
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return SkillCatalog{}, err
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return catalog, nil
	} else if err != nil {
		return SkillCatalog{}, err
	}
	directories := 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			directories++
			if directories > maxSkillScanDirs {
				return fmt.Errorf("skill directory scan exceeds %d directories", maxSkillScanDirs)
			}
			relative, _ := filepath.Rel(root, path)
			depth := 0
			if relative != "." {
				depth = len(strings.Split(relative, string(filepath.Separator)))
			}
			if depth > maxSkillScanDepth || (path != root && (entry.Name() == ".git" || entry.Name() == "node_modules")) {
				return filepath.SkipDir
			}
		}
		if entry.IsDir() || entry.Name() != "SKILL.md" {
			return nil
		}
		data, err := readLimited(path)
		if err != nil {
			return err
		}
		name, description, err := skillFrontmatter(data)
		if err != nil {
			return fmt.Errorf("read skill metadata from %s: %w", path, err)
		}
		if _, duplicate := catalog.entries[name]; duplicate {
			return fmt.Errorf("duplicate installed skill %q", name)
		}
		catalog.entries[name] = skillEntry{summary: policy.Skill{Name: name, Description: description}, dir: filepath.Dir(path)}
		return nil
	})
	return catalog, err
}

func (c SkillCatalog) Summaries() []policy.Skill {
	summaries := make([]policy.Skill, 0, len(c.entries))
	for _, entry := range c.entries {
		summaries = append(summaries, entry.summary)
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })
	return summaries
}

// Search returns a small, masked catalog slice so a model can discover a
// relevant skill without receiving every installed description in its prompt.
func (c SkillCatalog) Search(raw json.RawMessage) (json.RawMessage, error) {
	return searchSkillSummaries(c.Summaries(), raw)
}

func searchSkillSummaries(summaries []policy.Skill, raw json.RawMessage) (json.RawMessage, error) {
	var arguments struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return nil, err
	}
	query := strings.TrimSpace(arguments.Query)
	if query == "" {
		return nil, errors.New("query is required")
	}
	terms := strings.Fields(strings.ToLower(query))
	matches := make([]policy.Skill, 0, min(len(summaries), maxSkillCatalogMatches))
	for _, summary := range summaries {
		haystack := strings.ToLower(summary.Name + " " + summary.Group + " " + summary.Description)
		matched := true
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		matches = append(matches, summary)
		if len(matches) == maxSkillCatalogMatches {
			break
		}
	}
	return json.Marshal(map[string]any{
		"query": query, "matches": matches,
		"truncated": len(matches) == maxSkillCatalogMatches,
	})
}

func (c SkillCatalog) Read(raw json.RawMessage) (json.RawMessage, error) {
	var arguments struct {
		Name      string `json:"name"`
		Resource  string `json:"resource"`
		Query     string `json:"query"`
		StartLine int    `json:"start_line"`
		LineCount int    `json:"line_count"`
	}
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return nil, err
	}
	entry, ok := c.entries[arguments.Name]
	if !ok {
		return nil, fmt.Errorf("skill %q is not installed", arguments.Name)
	}
	if strings.TrimSpace(arguments.Query) != "" {
		return c.search(entry, arguments.Resource, arguments.Query)
	}
	resource := strings.TrimSpace(arguments.Resource)
	if resource == "" {
		resource = "SKILL.md"
	}
	resolved, relative, err := resolveSkillResource(entry.dir, resource)
	if err != nil {
		return nil, err
	}
	if filepath.ToSlash(relative) == "SKILL.md" {
		if arguments.StartLine != 0 || arguments.LineCount != 0 {
			return nil, errors.New("SKILL.md must be read completely; omit line bounds")
		}
		data, err := readAtMost(resolved, maxSkillReadBytes)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"name": entry.summary.Name, "resource": "SKILL.md", "start_line": 1, "content": string(data), "has_more": false})
	}
	if arguments.StartLine <= 0 || arguments.LineCount <= 0 || arguments.LineCount > maxSkillRangeLines {
		return nil, fmt.Errorf("reference reads require start_line and line_count between 1 and %d; use query to locate relevant sections", maxSkillRangeLines)
	}
	content, end, hasMore, err := readLineRange(resolved, arguments.StartLine, arguments.LineCount)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"name": entry.summary.Name, "resource": filepath.ToSlash(relative), "start_line": arguments.StartLine, "end_line": end, "content": content, "has_more": hasMore})
}

func readLimited(path string) ([]byte, error) {
	return readAtMost(path, maxSkillSearchBytes)
}

func readAtMost(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("skill resource exceeds %d bytes", limit)
	}
	return data, nil
}

type skillMatch struct {
	Resource  string `json:"resource"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Excerpt   string `json:"excerpt"`
}

func (c SkillCatalog) search(entry skillEntry, resource, query string) (json.RawMessage, error) {
	query = strings.TrimSpace(query)
	var paths []string
	if strings.TrimSpace(resource) != "" {
		path, _, err := resolveSkillResource(entry.dir, resource)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	} else {
		err := filepath.WalkDir(entry.dir, func(path string, item os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if item.Type()&os.ModeSymlink != 0 {
				if item.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !item.IsDir() && item.Type().IsRegular() && item.Name() != "SKILL.md" {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(paths)
	needle := strings.ToLower(query)
	var matches []skillMatch
	for _, path := range paths {
		data, err := readAtMost(path, maxSkillSearchBytes)
		if err != nil {
			continue
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for index, line := range lines {
			if !strings.Contains(strings.ToLower(line), needle) {
				continue
			}
			start := max(0, index-2)
			end := min(len(lines), index+3)
			relative, _ := filepath.Rel(entry.dir, path)
			excerptLines := make([]string, 0, end-start)
			for _, excerptLine := range lines[start:end] {
				excerptLines = append(excerptLines, boundedSkillLine(excerptLine))
			}
			matches = append(matches, skillMatch{Resource: filepath.ToSlash(relative), StartLine: start + 1, EndLine: end, Excerpt: strings.Join(excerptLines, "\n")})
			if len(matches) == maxSkillMatches {
				break
			}
		}
		if len(matches) == maxSkillMatches {
			break
		}
	}
	return json.Marshal(map[string]any{"name": entry.summary.Name, "mode": "navigation", "query": query, "matches": matches, "truncated": len(matches) == maxSkillMatches, "complete": false})
}

func readLineRange(path string, start, count int) (string, int, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxSkillSearchBytes)
	var selected []string
	line := 0
	bytesUsed := 0
	hasMore := false
	for scanner.Scan() {
		line++
		if line < start {
			continue
		}
		if len(selected) == count {
			hasMore = true
			break
		}
		value := boundedSkillLine(scanner.Text())
		bytesUsed += len(value) + 1
		if bytesUsed > maxSkillExcerptBytes {
			hasMore = true
			break
		}
		selected = append(selected, value)
	}
	if err := scanner.Err(); err != nil {
		return "", 0, false, err
	}
	if len(selected) == 0 {
		return "", 0, false, fmt.Errorf("start_line %d is outside the resource", start)
	}
	return strings.Join(selected, "\n"), start + len(selected) - 1, hasMore, nil
}

func boundedSkillLine(line string) string {
	if len(line) <= maxSkillExcerptLine {
		return line
	}
	return line[:maxSkillExcerptLine] + "…"
}

func resolveSkillResource(root, resource string) (string, string, error) {
	clean := filepath.Clean(strings.TrimSpace(resource))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", errors.New("skill resource must stay inside the installed skill")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, clean))
	if err != nil {
		return "", "", err
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(canonicalRoot, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("skill resource resolved outside the installed skill")
	}
	return resolved, relative, nil
}

func skillFrontmatter(data []byte) (string, string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return "", "", errors.New("missing YAML frontmatter")
	}
	var name, description string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "name":
			name = strings.Trim(strings.TrimSpace(value), `"'`)
		case "description":
			description = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	if name == "" || description == "" {
		return "", "", errors.New("frontmatter requires name and description")
	}
	return name, description, nil
}
