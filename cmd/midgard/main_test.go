package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"midgard/internal/credential"
	runtimeenv "midgard/internal/environment"
	"midgard/internal/project"
)

func TestParsedDefaultKeepsImplicitProjectWithoutCatalogLookup(t *testing.T) {
	repository := createMainTestRepository(t)
	catalog := project.Catalog{Directory: t.TempDir()}
	implicit, mount, err := catalog.Resolve(repository, "")
	if err != nil || !implicit.Implicit {
		t.Fatalf("implicit project = %#v, %v", implicit, err)
	}
	selected, selectedMount, err := resolveParsedProject(catalog, repository, "", implicit, mount)
	if err != nil || selected.ID != implicit.ID || selectedMount.Path != mount.Path {
		t.Fatalf("parsed project = %#v, %#v, %v", selected, selectedMount, err)
	}
}

func TestEnvironmentStatusExplainsProvenanceWithoutSecretStorageDetails(t *testing.T) {
	snapshot := runtimeenv.Snapshot{
		ID: "env_safe", Name: "production",
		Variables: []runtimeenv.Variable{{
			Name: "API_TOKEN", Description: "service access", Secret: true,
			SecretAccount: "private-keyring-account", Value: "must-not-print",
		}},
		Sources: map[string]runtimeenv.VariableSource{
			"API_TOKEN": {EnvironmentID: "environment_shared", EnvironmentName: "shared", Revision: 4},
		},
	}
	var output strings.Builder
	if err := writeEnvironmentStatus(&output, snapshot); err != nil {
		t.Fatal(err)
	}
	status := output.String()
	for _, expected := range []string{"API_TOKEN", "OS keyring reference", "from shared revision 4 (inherited)", "service access"} {
		if !strings.Contains(status, expected) {
			t.Fatalf("status %q does not contain %q", status, expected)
		}
	}
	if strings.Contains(status, "private-keyring-account") || strings.Contains(status, "must-not-print") {
		t.Fatalf("status exposed secret storage: %q", status)
	}
}

func TestParsedExplicitProjectStillUsesNamedCatalogSelection(t *testing.T) {
	repository := createMainTestRepository(t)
	catalog := project.Catalog{Directory: t.TempDir()}
	implicit, mount, err := catalog.Resolve(repository, "")
	if err != nil {
		t.Fatal(err)
	}
	named, err := catalog.Create("named", []project.Repository{{Name: "source", Path: repository}})
	if err != nil {
		t.Fatal(err)
	}
	selected, _, err := resolveParsedProject(catalog, repository, named.Name, implicit, mount)
	if err != nil || selected.ID != named.ID {
		t.Fatalf("explicit project = %#v, %v", selected, err)
	}
}

func TestEnvironmentCLIReusesPlainConfigurationAcrossProjectBinding(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	repository := createMainTestRepository(t)
	if err := runEnvironment([]string{"create", "shared"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvironment([]string{"set", "shared", "LOG_LEVEL", "debug", "--description", "logging verbosity"}); err != nil {
		t.Fatal(err)
	}
	if err := runEnvironment([]string{"use", "shared", "-repo", repository}); err != nil {
		t.Fatal(err)
	}
	catalog, err := runtimeenv.OpenCatalog()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("shared")
	if err != nil || len(snapshot.Variables) != 1 || snapshot.Variables[0].Value != "debug" {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	projects := project.Catalog{Directory: filepath.Join(configHome, "midgard", "projects")}
	implicit, _, err := projects.Resolve(repository, "")
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := runtimeenv.OpenBindings()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := bindings.Get(implicit.ID)
	if err != nil || selected != "shared" {
		t.Fatalf("selected environment = %q, %v", selected, err)
	}
}

func TestDefaultStatePathIsStablePerRepository(t *testing.T) {
	first, err := defaultStatePath("/repo/one")
	if err != nil {
		t.Fatal(err)
	}
	again, err := defaultStatePath("/repo/one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := defaultStatePath("/repo/two")
	if err != nil {
		t.Fatal(err)
	}
	if first != again || first == second {
		t.Fatalf("paths = %q, %q, %q", first, again, second)
	}
}

func TestReadAuthSecretFromEnvironmentForMigration(t *testing.T) {
	t.Setenv("MIDGARD_TEST_API_KEY", "secret-value")
	name := "MIDGARD_TEST_API_KEY"
	value, err := readAuthSecret(&name)
	if err != nil || value != "secret-value" {
		t.Fatalf("readAuthSecret() = %q, %v", value, err)
	}
}

func TestReadAuthSecretRejectsMissingMigrationVariable(t *testing.T) {
	name := "MIDGARD_MISSING_API_KEY"
	_, err := readAuthSecret(&name)
	if err == nil {
		t.Fatal("expected missing environment variable error")
	}
}

func TestCredentialProfilesProduceIndependentAccounts(t *testing.T) {
	first, err := (credential.Ref{Provider: "deepseek", Profile: "work", Name: credential.APIKey}).Account()
	if err != nil {
		t.Fatal(err)
	}
	second, err := (credential.Ref{Provider: "deepseek", Profile: "personal", Name: credential.APIKey}).Account()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("profile accounts collide at %q", first)
	}
}

func TestSplitAuthProviderSupportsProviderBeforeFlags(t *testing.T) {
	provider, args, err := splitAuthProvider([]string{"deepseek", "--profile", "work", "--from-env", "DEEPSEEK_API_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	if provider != "deepseek" || len(args) != 4 {
		t.Fatalf("split = %q, %#v", provider, args)
	}
}

func TestUnknownProviderAdapterDoesNotPreventCredentialStorage(t *testing.T) {
	_, err := lookupProvider("future-provider")
	if err == nil || errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("lookupProvider() error = %v", err)
	}
	if _, refErr := (credential.Ref{Provider: "future-provider", Name: credential.APIKey}).Account(); refErr != nil {
		t.Fatalf("future provider credential reference: %v", refErr)
	}
}

func createMainTestRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	commands := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "initial"},
	}
	for _, argv := range commands {
		command := exec.Command(argv[0], argv[1:]...)
		command.Dir = repository
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", argv, err, output)
		}
	}
	resolved, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(resolved); err != nil {
		t.Fatal(err)
	}
	return resolved
}
