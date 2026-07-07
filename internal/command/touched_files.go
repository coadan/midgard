package command

import (
	"slices"
	"strings"
)

func touchedFiles(before, after string) []string {
	seen := map[string]bool{}
	for _, status := range []string{before, after} {
		for _, line := range strings.Split(status, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			path := statusPath(line)
			if path != "" {
				seen[path] = true
			}
		}
	}
	files := make([]string, 0, len(seen))
	for file := range seen {
		files = append(files, file)
	}
	slices.Sort(files)
	return files
}

func statusPath(line string) string {
	if len(line) < 4 {
		return ""
	}
	path := strings.TrimSpace(line[3:])
	if before, after, ok := strings.Cut(path, " -> "); ok {
		_ = before
		return after
	}
	return path
}
