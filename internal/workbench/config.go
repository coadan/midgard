package workbench

type Config struct {
	Version int                   `toml:"version"`
	Name    string                `toml:"name"`
	Repos   map[string]RepoConfig `toml:"repos,omitempty"`
}

type RepoConfig struct {
	Path    string `toml:"path"`
	MainRef string `toml:"main_ref"`
}

func defaultConfig(name string) Config {
	return Config{
		Version: 1,
		Name:    name,
		Repos:   map[string]RepoConfig{},
	}
}
