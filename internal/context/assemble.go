package context

import (
	stdcontext "context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"midgard/internal/eventlog"
	"midgard/internal/workspace"
)

type View struct {
	SessionID  string           `json:"session_id"`
	Objective  string           `json:"objective"`
	Repository RepositoryFacts  `json:"repository"`
	Guidance   []Guidance       `json:"guidance,omitempty"`
	Recent     []eventlog.Event `json:"recent_events"`
}

type RepositoryFacts struct {
	Root            string `json:"root"`
	StartCommit     string `json:"start_commit"`
	HeadCommit      string `json:"head_commit"`
	Status          string `json:"status"`
	DefaultBranch   string `json:"default_branch"`
	LandingStrategy string `json:"landing_strategy"`
}

type Guidance struct {
	Path string `json:"path"`
}

type Assembler struct {
	Log             *eventlog.Store
	MaxRecentEvents int
	MaxBytes        int
}

func (a Assembler) Build(ctx stdcontext.Context, objective string, binding workspace.Binding) (View, error) {
	events, err := a.Log.Events(ctx, binding.SessionID, 0)
	if err != nil {
		return View{}, err
	}
	filtered := events[:0]
	for _, event := range events {
		if event.Visibility != eventlog.VisibilitySecret {
			filtered = append(filtered, event)
		}
	}
	events = filtered
	maxEvents := a.MaxRecentEvents
	if maxEvents <= 0 {
		maxEvents = 100
	}
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	maxBytes := a.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 256 << 10
	}
	for len(events) > 0 {
		raw, _ := json.Marshal(events)
		if len(raw) <= maxBytes {
			break
		}
		events = events[1:]
	}
	head, err := output(ctx, binding.WorktreeRoot, "git", "rev-parse", "HEAD")
	if err != nil {
		return View{}, err
	}
	status, err := output(ctx, binding.WorktreeRoot, "git", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return View{}, err
	}
	guidance, err := discoverGuidance(binding.WorktreeRoot)
	if err != nil {
		return View{}, err
	}
	return View{SessionID: binding.SessionID, Objective: objective,
		Repository: RepositoryFacts{Root: binding.WorktreeRoot, StartCommit: binding.StartCommit, HeadCommit: strings.TrimSpace(head), Status: status,
			DefaultBranch: binding.DefaultBranch, LandingStrategy: binding.LandingStrategy},
		Guidance: guidance, Recent: events}, nil
}

func discoverGuidance(root string) ([]Guidance, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if !entry.IsDir() && entry.Name() == "AGENTS.md" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	const maxGuidanceFiles = 128
	guidance := make([]Guidance, 0, min(len(paths), maxGuidanceFiles))
	for _, path := range paths {
		if len(guidance) == maxGuidanceFiles {
			break
		}
		relative, _ := filepath.Rel(root, path)
		guidance = append(guidance, Guidance{Path: filepath.ToSlash(relative)})
	}
	return guidance, nil
}

func output(ctx stdcontext.Context, dir string, argv ...string) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(raw)))
	}
	return string(raw), nil
}
