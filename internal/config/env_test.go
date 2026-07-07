package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvDoesNotOverrideExistingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("MIDGARD_TEST_ENV=from_file\nOTHER='quoted'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MIDGARD_TEST_ENV", "from_env")
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}

	if got := os.Getenv("MIDGARD_TEST_ENV"); got != "from_env" {
		t.Fatalf("existing env overwritten: %q", got)
	}
	if got := os.Getenv("OTHER"); got != "quoted" {
		t.Fatalf("quoted env not loaded: %q", got)
	}
}

func TestLoadDotEnvMissingFileIsOK(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), ".env")); err != nil {
		t.Fatal(err)
	}
}
