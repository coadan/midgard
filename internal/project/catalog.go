// Package project owns the local catalog of logical projects. Projects are
// named sets of repository mounts and deliberately have no filesystem root.
package project

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Repository struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Project struct {
	Version      int          `json:"version"`
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	StatePath    string       `json:"state_path,omitempty"`
	Repositories []Repository `json:"repositories"`
	Implicit     bool         `json:"-"`
}

type Catalog struct{ Directory string }

type ChoiceRequiredError struct {
	Repository string
	Projects   []Project
}

func (e *ChoiceRequiredError) Error() string {
	var names []string
	for _, project := range e.Projects {
		names = append(names, project.Name)
	}
	return fmt.Sprintf("repository %s belongs to multiple Midgard projects (%s); choose one with -project NAME", e.Repository, strings.Join(names, ", "))
}

func OpenCatalog() (Catalog, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return Catalog{}, err
	}
	return Catalog{Directory: filepath.Join(base, "midgard", "projects")}, nil
}

func (c Catalog) List() ([]Project, error) {
	entries, err := os.ReadDir(c.Directory)
	if errors.Is(err, os.ErrNotExist) {
		return []Project{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project catalog: %w", err)
	}
	var projects []Project
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		project, err := read(filepath.Join(c.Directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	sort.Slice(projects, func(a, b int) bool { return projects[a].Name < projects[b].Name })
	return projects, nil
}

func (c Catalog) Resolve(repository, requested string) (Project, Repository, error) {
	root, err := repositoryRoot(repository)
	if err != nil {
		return Project{}, Repository{}, err
	}
	projects, err := c.List()
	if err != nil {
		return Project{}, Repository{}, err
	}
	if requested != "" {
		for _, current := range projects {
			if current.ID == requested || current.Name == requested {
				mount, ok := current.mountFor(root)
				if !ok {
					return Project{}, Repository{}, fmt.Errorf("repository %s is not mounted in project %q", root, current.Name)
				}
				return current, mount, nil
			}
		}
		return Project{}, Repository{}, fmt.Errorf("Midgard project %q is not configured", requested)
	}
	var matches []Project
	var mounts []Repository
	for _, current := range projects {
		if mount, ok := current.mountFor(root); ok {
			matches, mounts = append(matches, current), append(mounts, mount)
		}
	}
	if len(matches) == 0 {
		implicit := implicitProject(root)
		return implicit, implicit.Repositories[0], nil
	}
	if remembered := rememberedProject(root); remembered != "" {
		for index, current := range matches {
			if current.ID == remembered {
				return current, mounts[index], nil
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], mounts[0], nil
	}
	return Project{}, Repository{}, &ChoiceRequiredError{Repository: root, Projects: matches}
}

func (c Catalog) Create(name string, repositories []Repository) (Project, error) {
	name, err := validateName("project", name)
	if err != nil {
		return Project{}, err
	}
	project := Project{Version: 1, ID: randomID("project"), Name: name, Repositories: repositories}
	if err := c.Save(project); err != nil {
		return Project{}, err
	}
	return project, nil
}

// Upgrade persists an implicit project under the same stable ID and optionally
// adds another repository. Existing sessions can retain their project ID.
func (c Catalog) Upgrade(project Project, name string, additional *Repository) (Project, error) {
	if !project.Implicit {
		return Project{}, errors.New("only an implicit project can be upgraded")
	}
	name, err := validateName("project", name)
	if err != nil {
		return Project{}, err
	}
	project.Version, project.Name, project.Implicit = 1, name, false
	if additional != nil {
		project.Repositories = append(project.Repositories, *additional)
	}
	if err := c.Save(project); err != nil {
		return Project{}, err
	}
	return project, nil
}

func (c Catalog) AddRepository(projectID string, repository Repository) (Project, error) {
	projects, err := c.List()
	if err != nil {
		return Project{}, err
	}
	for _, current := range projects {
		if current.ID != projectID && current.Name != projectID {
			continue
		}
		canonical, err := repositoryRoot(repository.Path)
		if err != nil {
			return Project{}, err
		}
		for _, existing := range current.Repositories {
			if existing.Path == canonical {
				return current, nil
			}
		}
		repository.Path = canonical
		current.Repositories = append(current.Repositories, repository)
		if err := c.Save(current); err != nil {
			return Project{}, err
		}
		return current, nil
	}
	return Project{}, fmt.Errorf("Midgard project %q is not configured", projectID)
}

// RepositoryMount returns a canonical, project-unique mount for a repository.
// The path remains the identity; a numeric suffix only resolves display-name
// collisions between repositories with the same directory name.
func RepositoryMount(current Project, path string) (Repository, error) {
	root, err := repositoryRoot(path)
	if err != nil {
		return Repository{}, err
	}
	for _, existing := range current.Repositories {
		if existing.Path == root {
			return existing, nil
		}
	}
	base := safeName(filepath.Base(root))
	name := base
	for suffix := 2; ; suffix++ {
		available := true
		for _, existing := range current.Repositories {
			if existing.Name == name {
				available = false
				break
			}
		}
		if available {
			return Repository{Name: name, Path: root}, nil
		}
		name = fmt.Sprintf("%s-%d", base, suffix)
	}
}

func (c Catalog) Save(project Project) error {
	if project.Version != 1 || project.ID == "" {
		return errors.New("project version and id are required")
	}
	name, err := validateName("project", project.Name)
	if err != nil {
		return err
	}
	project.Name = name
	existingProjects, err := c.List()
	if err != nil {
		return err
	}
	for _, existing := range existingProjects {
		if existing.ID != project.ID && existing.Name == project.Name {
			return fmt.Errorf("project name %q is already in use", project.Name)
		}
	}
	if len(project.Repositories) == 0 {
		return errors.New("project requires at least one repository")
	}
	seenNames, seenPaths := map[string]bool{}, map[string]bool{}
	for index := range project.Repositories {
		repository := &project.Repositories[index]
		repository.Name, err = validateName("repository", repository.Name)
		if err != nil {
			return err
		}
		repository.Path, err = repositoryRoot(repository.Path)
		if err != nil {
			return fmt.Errorf("repository %q: %w", repository.Name, err)
		}
		if seenNames[repository.Name] || seenPaths[repository.Path] {
			return fmt.Errorf("project has duplicate repository name or path for %q", repository.Name)
		}
		seenNames[repository.Name], seenPaths[repository.Path] = true, true
	}
	sort.Slice(project.Repositories, func(a, b int) bool { return project.Repositories[a].Name < project.Repositories[b].Name })
	if err := os.MkdirAll(c.Directory, 0o700); err != nil {
		return err
	}
	path := filepath.Join(c.Directory, project.ID+".json")
	return writeAtomic(path, project)
}

func Remember(repository, projectID string) error {
	root, err := repositoryRoot(repository)
	if err != nil {
		return err
	}
	command := exec.Command("git", "config", "--local", "midgard.project", projectID)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("remember project for repository: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (p Project) mountFor(root string) (Repository, bool) {
	for _, repository := range p.Repositories {
		if repository.Path == root {
			return repository, true
		}
	}
	return Repository{}, false
}

func implicitProject(root string) Project {
	digest := sha256.Sum256([]byte(root))
	id := "project_" + hex.EncodeToString(digest[:8])
	name := safeName(filepath.Base(root))
	return Project{Version: 1, ID: id, Name: name, Implicit: true,
		Repositories: []Repository{{Name: name, Path: root}}}
}

func rememberedProject(root string) string {
	command := exec.Command("git", "config", "--local", "--get", "midgard.project")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func repositoryRoot(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	command := exec.Command("git", "rev-parse", "--show-toplevel")
	command.Dir = absolute
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s is not a Git repository; initialize it with `git init -b main` and create an initial commit", absolute)
	}
	return filepath.EvalSymlinks(strings.TrimSpace(string(output)))
}

func read(path string) (Project, error) {
	handle, err := os.Open(path)
	if err != nil {
		return Project{}, err
	}
	defer handle.Close()
	decoder := json.NewDecoder(handle)
	decoder.DisallowUnknownFields()
	var project Project
	if err := decoder.Decode(&project); err != nil {
		return Project{}, fmt.Errorf("decode project %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Project{}, fmt.Errorf("decode project %s: multiple JSON values", path)
		}
		return Project{}, fmt.Errorf("decode project %s: %w", path, err)
	}
	if project.Version != 1 || project.ID == "" || project.Name == "" || len(project.Repositories) == 0 {
		return Project{}, fmt.Errorf("project %s is incomplete", path)
	}
	for index := range project.Repositories {
		project.Repositories[index].Path, err = repositoryRoot(project.Repositories[index].Path)
		if err != nil {
			return Project{}, fmt.Errorf("project %s repository %q: %w", path, project.Repositories[index].Name, err)
		}
	}
	return project, nil
}

func writeAtomic(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".project-*.json")
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

func safeName(value string) string {
	if name, err := validateName("repository", value); err == nil {
		return name
	}
	return "repository"
}

func randomID(prefix string) string {
	var data [8]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(data[:])
}
