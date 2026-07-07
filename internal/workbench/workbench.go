package workbench

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type InitOptions struct {
	Name string
}

type InitResult struct {
	Root       string
	ConfigPath string
	Created    bool
	Config     Config
}

type StatusResult struct {
	Root       string
	ConfigPath string
	Config     Config
}

func Init(root string, opts InitOptions) (InitResult, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return InitResult{}, err
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return InitResult{}, err
	}

	layout := NewLayout(absRoot)
	for _, dir := range layout.Dirs() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return InitResult{}, err
		}
	}

	created := false
	cfg, err := ReadConfig(layout.Config)
	if errors.Is(err, os.ErrNotExist) {
		name := opts.Name
		if name == "" {
			name = filepath.Base(absRoot)
		}
		cfg = defaultConfig(name)
		if err := WriteConfig(layout.Config, cfg); err != nil {
			return InitResult{}, err
		}
		created = true
	} else if err != nil {
		return InitResult{}, err
	}

	return InitResult{
		Root:       absRoot,
		ConfigPath: layout.Config,
		Created:    created,
		Config:     cfg,
	}, nil
}

func Status(start string) (StatusResult, error) {
	root, err := FindRoot(start)
	if err != nil {
		return StatusResult{}, err
	}
	layout := NewLayout(root)
	cfg, err := ReadConfig(layout.Config)
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{Root: root, ConfigPath: layout.Config, Config: cfg}, nil
}

func FindRoot(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	dir := abs
	if !info.IsDir() {
		dir = filepath.Dir(abs)
	}
	for {
		candidate := filepath.Join(dir, DirName, "workbench.toml")
		if _, err := os.Stat(candidate); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("workbench not found from %s", start)
}

func ReadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Repos == nil {
		cfg.Repos = map[string]RepoConfig{}
	}
	return cfg, nil
}

func WriteConfig(path string, cfg Config) error {
	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
