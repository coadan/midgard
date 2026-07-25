package task

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	maxRepositoryGuidanceBytes = 32 << 10
	maxGuidanceFileBytes       = 24 << 10
)

func repositoryGuidance(worktrees []WorktreeStatus) string {
	var b strings.Builder
	remaining := maxRepositoryGuidanceBytes
	for _, wt := range worktrees {
		if remaining <= 0 {
			break
		}
		path := filepath.Join(wt.Path, "AGENTS.md")
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		limit := min(len(data), maxGuidanceFileBytes, remaining)
		fmt.Fprintf(&b, "repo:%s file:AGENTS.md\n", wt.RepoID)
		b.Write(data[:limit])
		if limit < len(data) {
			fmt.Fprintf(&b, "\n[AGENTS.md truncated; %d bytes omitted]\n", len(data)-limit)
		} else if data[len(data)-1] != '\n' {
			b.WriteByte('\n')
		}
		remaining -= limit
	}
	return b.String()
}

func availableToolContext() string {
	path, err := exec.LookPath("heimdal")
	if err != nil {
		return "tool:heimdal available:false install_hint:https://github.com/vegard/breyta-heimdal\n"
	}
	return fmt.Sprintf(
		"tool:heimdal available:true command:%s purpose:Playwright_browser_QA\n",
		filepath.Base(path),
	)
}
