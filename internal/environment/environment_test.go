package environment

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memorySecrets map[string]string

func (m memorySecrets) Set(account, secret string) error   { m[account] = secret; return nil }
func (m memorySecrets) Get(account string) (string, error) { return m[account], nil }

func TestEnvironmentComposesCurrentParentWithoutPersistingSecretBytes(t *testing.T) {
	catalog := Catalog{Directory: t.TempDir()}
	secrets := memorySecrets{}
	if _, err := catalog.Create("shared", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetPlain("shared", "LOG_LEVEL", "debug", "logging verbosity"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetSecret("shared", "API_TOKEN", "shared service token", "top-secret-value", secrets); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Create("production", "shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetPlain("production", "LOG_LEVEL", "info", "production verbosity"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("production")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := (Resolver{Snapshot: snapshot, Secrets: secrets}).Resolve(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Values["LOG_LEVEL"] != "info" || resolved.Values["API_TOKEN"] != "top-secret-value" {
		t.Fatalf("resolved = %#v", resolved)
	}
	inspection := snapshot.Inspect()
	if len(inspection) != 2 {
		t.Fatalf("inspection = %#v", inspection)
	}
	byName := map[string]VariableInfo{}
	for _, variable := range inspection {
		byName[variable.Name] = variable
	}
	if source := byName["API_TOKEN"]; source.SourceEnvironment != "shared" || source.SourceRevision != 3 || !source.Inherited || source.State != "OS keyring reference" {
		t.Fatalf("API_TOKEN provenance = %#v", source)
	}
	if source := byName["LOG_LEVEL"]; source.SourceEnvironment != "production" || source.SourceRevision != 2 || source.Inherited {
		t.Fatalf("LOG_LEVEL provenance = %#v", source)
	}
	safeInspection, _ := json.Marshal(inspection)
	secretAccount := snapshot.Variables[0].SecretAccount
	if strings.Contains(string(safeInspection), "top-secret-value") || (secretAccount != "" && strings.Contains(string(safeInspection), secretAccount)) {
		t.Fatalf("inspection exposed secret storage: %s", safeInspection)
	}
	rawSnapshot, _ := json.Marshal(snapshot)
	if strings.Contains(string(rawSnapshot), "top-secret-value") {
		t.Fatalf("snapshot leaked secret: %s", rawSnapshot)
	}
	if err := filepath.WalkDir(catalog.Directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(raw), "top-secret-value") {
				t.Fatalf("%s leaked secret", path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBindingsRememberEnvironmentPerProject(t *testing.T) {
	bindings := Bindings{Path: filepath.Join(t.TempDir(), "bindings.json")}
	if err := bindings.Set("project-one", "production"); err != nil {
		t.Fatal(err)
	}
	got, err := bindings.Get("project-one")
	if err != nil || got != "production" {
		t.Fatalf("binding = %q, %v", got, err)
	}
}

func TestResolverRejectsDifferentCommittedRevision(t *testing.T) {
	snapshot := Snapshot{ID: "env_expected"}
	_, err := (Resolver{Snapshot: snapshot, Secrets: memorySecrets{}}).Resolve(context.Background(), "env_other")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("revision error = %v", err)
	}
}

type missingSecrets struct{}

func (missingSecrets) Set(string, string) error   { return nil }
func (missingSecrets) Get(string) (string, error) { return "", os.ErrNotExist }

func TestResolverStopsWhenKeyringSecretIsMissing(t *testing.T) {
	snapshot := Snapshot{ID: "env_expected", Variables: []Variable{{Name: "API_TOKEN", Secret: true, SecretAccount: "missing"}}}
	_, err := (Resolver{Snapshot: snapshot, Secrets: missingSecrets{}}).Resolve(context.Background(), snapshot.ID)
	if err == nil || !strings.Contains(err.Error(), "API_TOKEN is unavailable") {
		t.Fatalf("missing secret error = %v", err)
	}
}
