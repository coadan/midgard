package project

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestImplicitProjectUpgradesWithoutChangingIdentity(t *testing.T) {
	catalog := Catalog{Directory: t.TempDir()}
	first := createRepo(t, "midgard")
	second := createRepo(t, "bragi")
	implicit, mount, err := catalog.Resolve(first, "")
	if err != nil || !implicit.Implicit || mount.Path != first {
		t.Fatalf("implicit resolve = %#v, %#v, %v", implicit, mount, err)
	}
	upgraded, err := catalog.Upgrade(implicit, "midgard-development", &Repository{Name: "bragi", Path: second})
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.ID != implicit.ID || upgraded.Implicit || len(upgraded.Repositories) != 2 {
		t.Fatalf("upgraded = %#v", upgraded)
	}
	resolved, _, err := catalog.Resolve(second, "midgard-development")
	if err != nil || resolved.ID != implicit.ID {
		t.Fatalf("resolve upgraded = %#v, %v", resolved, err)
	}
}

func TestRepositoryCanBelongToSeveralProjectsAndRememberChoice(t *testing.T) {
	catalog := Catalog{Directory: t.TempDir()}
	repo := createRepo(t, "shared")
	first, err := catalog.Create("first", []Repository{{Name: "shared", Path: repo}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := catalog.Create("second", []Repository{{Name: "shared", Path: repo}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = catalog.Resolve(repo, "")
	var choice *ChoiceRequiredError
	if !errors.As(err, &choice) || len(choice.Projects) != 2 {
		t.Fatalf("choice error = %v", err)
	}
	if err := Remember(repo, second.ID); err != nil {
		t.Fatal(err)
	}
	selected, _, err := catalog.Resolve(repo, "")
	if err != nil || selected.ID != second.ID || selected.ID == first.ID {
		t.Fatalf("selected = %#v, %v", selected, err)
	}
}

func TestCatalogRejectsDuplicateMounts(t *testing.T) {
	catalog := Catalog{Directory: t.TempDir()}
	repo := createRepo(t, "repo")
	_, err := catalog.Create("bad", []Repository{{Name: "one", Path: repo}, {Name: "two", Path: repo}})
	if err == nil {
		t.Fatal("expected duplicate path rejection")
	}
}

func createRepo(t *testing.T, name string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "init", "-b", "main")
	run(t, repo, "git", "config", "user.email", "test@example.com")
	run(t, repo, "git", "config", "user.name", "Test")
	run(t, repo, "git", "commit", "--allow-empty", "-m", "initial")
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func run(t *testing.T, directory string, argv ...string) {
	t.Helper()
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v: %s", argv, err, output)
	}
}
