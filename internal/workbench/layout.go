package workbench

import "path/filepath"

const DirName = ".midgard"

type Layout struct {
	Root       string
	Midgard    string
	Config     string
	State      string
	Tasks      string
	Artifacts  string
	Worktrees  string
	Benchmarks string
}

func NewLayout(root string) Layout {
	midgard := filepath.Join(root, DirName)
	return Layout{
		Root:       root,
		Midgard:    midgard,
		Config:     filepath.Join(midgard, "workbench.toml"),
		State:      filepath.Join(midgard, "state.sqlite"),
		Tasks:      filepath.Join(midgard, "tasks"),
		Artifacts:  filepath.Join(midgard, "artifacts"),
		Worktrees:  filepath.Join(midgard, "worktrees"),
		Benchmarks: filepath.Join(midgard, "benchmarks"),
	}
}

func (l Layout) Dirs() []string {
	return []string{l.Midgard, l.Tasks, l.Artifacts, l.Worktrees, l.Benchmarks}
}
