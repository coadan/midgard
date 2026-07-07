package workbench

import (
	"fmt"
	"path/filepath"
)

type AddRepoOptions struct {
	ID      string
	Path    string
	MainRef string
}

func AddRepo(root string, opts AddRepoOptions) (Config, error) {
	status, err := Status(root)
	if err != nil {
		return Config{}, err
	}
	if opts.ID == "" {
		return Config{}, fmt.Errorf("repo id is required")
	}
	if opts.Path == "" {
		return Config{}, fmt.Errorf("repo path is required")
	}
	mainRef := opts.MainRef
	if mainRef == "" {
		mainRef = "main"
	}
	absPath, err := filepath.Abs(opts.Path)
	if err != nil {
		return Config{}, err
	}
	cfg := status.Config
	if cfg.Repos == nil {
		cfg.Repos = map[string]RepoConfig{}
	}
	cfg.Repos[opts.ID] = RepoConfig{Path: absPath, MainRef: mainRef}
	if err := WriteConfig(status.ConfigPath, cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
