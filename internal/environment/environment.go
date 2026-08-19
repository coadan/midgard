// Package environment owns reusable runtime environment metadata, immutable
// revisions, project bindings, and OS-keyring secret references. Secret bytes
// are never serialized by this package.
package environment

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	keyring "github.com/zalando/go-keyring"
)

const keyringService = "dev.midgard.environment"

type Variable struct {
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Secret        bool   `json:"secret"`
	Value         string `json:"value,omitempty"`
	SecretAccount string `json:"secret_account,omitempty"`
}

type Environment struct {
	Version   int        `json:"version"`
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Revision  int        `json:"revision"`
	ParentID  string     `json:"parent_id,omitempty"`
	Variables []Variable `json:"variables,omitempty"`
}

type Reference struct {
	EnvironmentID string `json:"environment_id"`
	Revision      int    `json:"revision"`
}

type VariableSource struct {
	EnvironmentID   string `json:"environment_id"`
	EnvironmentName string `json:"environment_name"`
	Revision        int    `json:"revision"`
}

type Snapshot struct {
	ID        string                    `json:"id"`
	Name      string                    `json:"name"`
	Revisions []Reference               `json:"revisions"`
	Variables []Variable                `json:"variables"`
	Sources   map[string]VariableSource `json:"sources"`
}

// VariableInfo is the secret-safe view used to explain what a child process
// will receive. It deliberately excludes values and keyring account names.
type VariableInfo struct {
	Name              string `json:"name"`
	Description       string `json:"description,omitempty"`
	Kind              string `json:"kind"`
	State             string `json:"state"`
	SourceEnvironment string `json:"source_environment"`
	SourceRevision    int    `json:"source_revision"`
	Inherited         bool   `json:"inherited"`
}

type Catalog struct{ Directory string }

func OpenCatalog() (Catalog, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return Catalog{}, err
	}
	return Catalog{Directory: filepath.Join(base, "midgard", "environments")}, nil
}

func (c Catalog) Create(name, parent string) (Environment, error) {
	name, err := validateName("environment", name)
	if err != nil {
		return Environment{}, err
	}
	if _, err := c.Current(name); err == nil {
		return Environment{}, fmt.Errorf("environment %q already exists", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Environment{}, err
	}
	current := Environment{Version: 1, ID: randomID("environment"), Name: name, Revision: 1}
	if strings.TrimSpace(parent) != "" {
		parentEnvironment, err := c.Current(parent)
		if err != nil {
			return Environment{}, fmt.Errorf("parent environment: %w", err)
		}
		current.ParentID = parentEnvironment.ID
	}
	return current, c.saveRevision(current)
}

func (c Catalog) Current(name string) (Environment, error) {
	name, err := validateName("environment", name)
	if err != nil {
		return Environment{}, err
	}
	raw, err := os.ReadFile(filepath.Join(c.Directory, name+".json"))
	if err != nil {
		return Environment{}, err
	}
	var current Environment
	if err := json.Unmarshal(raw, &current); err != nil {
		return Environment{}, fmt.Errorf("decode environment %q: %w", name, err)
	}
	return current, validateEnvironment(current)
}

func (c Catalog) List() ([]Environment, error) {
	entries, err := os.ReadDir(c.Directory)
	if errors.Is(err, os.ErrNotExist) {
		return []Environment{}, nil
	}
	if err != nil {
		return nil, err
	}
	var environments []Environment
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" || entry.Name() == "bindings.json" {
			continue
		}
		current, err := c.Current(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		environments = append(environments, current)
	}
	sort.Slice(environments, func(i, j int) bool { return environments[i].Name < environments[j].Name })
	return environments, nil
}

func (c Catalog) SetPlain(name, key, value, description string) (Environment, error) {
	return c.revise(name, Variable{Name: key, Description: description, Value: value})
}

func (c Catalog) SetSecret(name, key, description, secret string, store SecretStore) (Environment, error) {
	if secret == "" {
		return Environment{}, errors.New("secret environment value is empty")
	}
	current, err := c.Current(name)
	if err != nil {
		return Environment{}, err
	}
	key, err = validateKey(key)
	if err != nil {
		return Environment{}, err
	}
	account := fmt.Sprintf("%s/%d/%s/%s", current.ID, current.Revision+1, strings.ToLower(key), randomID("secret"))
	if err := store.Set(account, secret); err != nil {
		return Environment{}, err
	}
	return c.reviseCurrent(current, Variable{Name: key, Description: description, Secret: true, SecretAccount: account})
}

func (c Catalog) Unset(name, key string) (Environment, error) {
	current, err := c.Current(name)
	if err != nil {
		return Environment{}, err
	}
	key, err = validateKey(key)
	if err != nil {
		return Environment{}, err
	}
	variables := make([]Variable, 0, len(current.Variables))
	found := false
	for _, variable := range current.Variables {
		if variable.Name == key {
			found = true
			continue
		}
		variables = append(variables, variable)
	}
	if !found {
		return Environment{}, fmt.Errorf("environment %s does not define %s", current.Name, key)
	}
	current.Revision++
	current.Variables = variables
	return current, c.saveRevision(current)
}

func (c Catalog) revise(name string, variable Variable) (Environment, error) {
	current, err := c.Current(name)
	if err != nil {
		return Environment{}, err
	}
	variable.Name, err = validateKey(variable.Name)
	if err != nil {
		return Environment{}, err
	}
	return c.reviseCurrent(current, variable)
}

func (c Catalog) reviseCurrent(current Environment, variable Variable) (Environment, error) {
	variables := append([]Variable(nil), current.Variables...)
	replaced := false
	for index := range variables {
		if variables[index].Name == variable.Name {
			variables[index], replaced = variable, true
		}
	}
	if !replaced {
		variables = append(variables, variable)
	}
	sort.Slice(variables, func(i, j int) bool { return variables[i].Name < variables[j].Name })
	current.Revision++
	current.Variables = variables
	return current, c.saveRevision(current)
}

func (c Catalog) Snapshot(name string) (Snapshot, error) {
	current, err := c.Current(name)
	if err != nil {
		return Snapshot{}, err
	}
	selectedName := current.Name
	seen := map[string]bool{}
	var chain []Environment
	for {
		if seen[current.ID] {
			return Snapshot{}, errors.New("environment parent cycle")
		}
		seen[current.ID] = true
		chain = append(chain, current)
		if current.ParentID == "" {
			break
		}
		current, err = c.currentByID(current.ParentID)
		if err != nil {
			return Snapshot{}, err
		}
	}
	values := map[string]Variable{}
	sources := map[string]VariableSource{}
	var references []Reference
	for index := len(chain) - 1; index >= 0; index-- {
		item := chain[index]
		references = append(references, Reference{EnvironmentID: item.ID, Revision: item.Revision})
		for _, variable := range item.Variables {
			values[variable.Name] = variable
			sources[variable.Name] = VariableSource{
				EnvironmentID: item.ID, EnvironmentName: item.Name, Revision: item.Revision,
			}
		}
	}
	variables := make([]Variable, 0, len(values))
	for _, variable := range values {
		variables = append(variables, variable)
	}
	sort.Slice(variables, func(i, j int) bool { return variables[i].Name < variables[j].Name })
	raw, _ := json.Marshal(struct {
		Name       string
		References []Reference
		Variables  []Variable
	}{selectedName, references, variables})
	digest := sha256.Sum256(raw)
	return Snapshot{ID: "env_" + hex.EncodeToString(digest[:16]), Name: selectedName, Revisions: references, Variables: variables, Sources: sources}, nil
}

func (s Snapshot) Inspect() []VariableInfo {
	variables := make([]VariableInfo, 0, len(s.Variables))
	for _, variable := range s.Variables {
		source := s.Sources[variable.Name]
		kind, state := "plain", "configured"
		if variable.Secret {
			kind, state = "secret", "OS keyring reference"
		}
		variables = append(variables, VariableInfo{
			Name: variable.Name, Description: variable.Description,
			Kind: kind, State: state,
			SourceEnvironment: source.EnvironmentName, SourceRevision: source.Revision,
			Inherited: source.EnvironmentName != "" && source.EnvironmentName != s.Name,
		})
	}
	return variables
}

func (c Catalog) currentByID(id string) (Environment, error) {
	environments, err := c.List()
	if err != nil {
		return Environment{}, err
	}
	for _, current := range environments {
		if current.ID == id {
			return current, nil
		}
	}
	return Environment{}, fmt.Errorf("environment id %q is not configured", id)
}

func (c Catalog) saveRevision(current Environment) error {
	if err := validateEnvironment(current); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(c.Directory, "revisions", current.ID), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	if err := writeRevision(filepath.Join(c.Directory, "revisions", current.ID, fmt.Sprintf("%d.json", current.Revision)), raw); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(c.Directory, current.Name+".json"), raw)
}

func validateEnvironment(current Environment) error {
	if current.Version != 1 || current.ID == "" || current.Name == "" || current.Revision <= 0 {
		return errors.New("environment record is incomplete")
	}
	for _, variable := range current.Variables {
		if _, err := validateKey(variable.Name); err != nil {
			return err
		}
		if variable.Secret && (variable.SecretAccount == "" || variable.Value != "") || !variable.Secret && variable.SecretAccount != "" {
			return fmt.Errorf("environment variable %s has invalid storage metadata", variable.Name)
		}
	}
	return nil
}

type SecretStore interface {
	Set(account, secret string) error
	Get(account string) (string, error)
}

type NativeSecretStore struct{}

func (NativeSecretStore) Set(account, secret string) error {
	if err := keyring.Set(keyringService, account, secret); err != nil {
		return fmt.Errorf("store environment secret in OS keyring: %w", err)
	}
	return nil
}

func (NativeSecretStore) Get(account string) (string, error) {
	value, err := keyring.Get(keyringService, account)
	if err != nil {
		return "", fmt.Errorf("read environment secret from OS keyring: %w", err)
	}
	if value == "" {
		return "", errors.New("environment secret is empty")
	}
	return value, nil
}

type Resolved struct {
	SnapshotID string
	Values     map[string]string
	Secrets    map[string]string
}

type Resolver struct {
	Snapshot Snapshot
	Secrets  SecretStore
}

func (r Resolver) Resolve(_ context.Context, snapshotID string) (Resolved, error) {
	if snapshotID == "" || snapshotID != r.Snapshot.ID {
		return Resolved{}, errors.New("environment revision does not match the committed action")
	}
	resolved := Resolved{SnapshotID: snapshotID, Values: map[string]string{}, Secrets: map[string]string{}}
	for _, variable := range r.Snapshot.Variables {
		value := variable.Value
		if variable.Secret {
			if r.Secrets == nil {
				return Resolved{}, fmt.Errorf("secret %s is unavailable", variable.Name)
			}
			var err error
			value, err = r.Secrets.Get(variable.SecretAccount)
			if err != nil {
				return Resolved{}, fmt.Errorf("secret %s is unavailable: %w", variable.Name, err)
			}
			if value == "" {
				return Resolved{}, fmt.Errorf("secret %s is unavailable: stored value is empty", variable.Name)
			}
			resolved.Secrets[variable.Name] = value
		}
		resolved.Values[variable.Name] = value
	}
	return resolved, nil
}

type Bindings struct{ Path string }

func OpenBindings() (Bindings, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return Bindings{}, err
	}
	return Bindings{Path: filepath.Join(base, "midgard", "environment-bindings.json")}, nil
}

func (b Bindings) Get(projectID string) (string, error) {
	values, err := b.read()
	return values[projectID], err
}

func (b Bindings) Set(projectID, environmentName string) error {
	if strings.TrimSpace(projectID) == "" {
		return errors.New("project id is required")
	}
	environmentName, err := validateName("environment", environmentName)
	if err != nil {
		return err
	}
	values, err := b.read()
	if err != nil {
		return err
	}
	values[projectID] = environmentName
	raw, _ := json.MarshalIndent(map[string]any{"projects": values}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(b.Path), 0o700); err != nil {
		return err
	}
	return writeAtomic(b.Path, raw)
}

func (b Bindings) read() (map[string]string, error) {
	raw, err := os.ReadFile(b.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var document struct {
		Projects map[string]string `json:"projects"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	if document.Projects == nil {
		document.Projects = map[string]string{}
	}
	return document.Projects, nil
}

func validateName(kind, value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", fmt.Errorf("%s name is required", kind)
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || (index > 0 && (char == '-' || char == '_' || char == '.')) {
			continue
		}
		return "", fmt.Errorf("invalid %s name %q", kind, value)
	}
	return value, nil
}

func validateKey(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return "", errors.New("environment variable name is required")
	}
	for index, char := range value {
		if (char >= 'A' && char <= 'Z') || char == '_' || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return "", fmt.Errorf("invalid environment variable name %q", value)
	}
	return value, nil
}

func writeAtomic(path string, raw []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".environment-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(raw, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func writeRevision(path string, raw []byte) error {
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.TrimSpace(string(existing)) == strings.TrimSpace(string(raw)) {
			return nil
		}
		return fmt.Errorf("environment revision %s already exists with different content", path)
	}
	if err != nil {
		return err
	}
	if _, err := handle.Write(append(raw, '\n')); err != nil {
		handle.Close()
		return err
	}
	return handle.Close()
}

func randomID(prefix string) string {
	var data [8]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(data[:])
}
