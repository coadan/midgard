package workbench

type Config struct {
	Version int                   `toml:"version"`
	Name    string                `toml:"name"`
	Repos   map[string]RepoConfig `toml:"repos,omitempty"`
	Forge   ForgeConfig           `toml:"forge,omitempty"`
}

type RepoConfig struct {
	Path    string `toml:"path"`
	MainRef string `toml:"main_ref"`
}

type ForgeConfig struct {
	ReadinessGates bool   `toml:"readiness_gates,omitempty"`
	MaxSnapshotAge string `toml:"max_snapshot_age,omitempty"`
}

func defaultConfig(name string) Config {
	return Config{
		Version: 1,
		Name:    name,
		Repos:   map[string]RepoConfig{},
	}
}
