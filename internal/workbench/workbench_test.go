package workbench

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestInitCreatesWorkbenchLayout(t *testing.T) {
	root := t.TempDir()
	result, err := Init(root, InitOptions{Name: "testbench"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created {
		t.Fatal("first init did not report created")
	}
	if result.Config.Name != "testbench" {
		t.Fatalf("name = %q", result.Config.Name)
	}

	layout := NewLayout(root)
	for _, dir := range layout.Dirs() {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("directory %s missing or not dir: %v", dir, err)
		}
	}
	if _, err := os.Stat(layout.Config); err != nil {
		t.Fatalf("config missing: %v", err)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	root := t.TempDir()
	first, err := Init(root, InitOptions{Name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Init(root, InitOptions{Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || second.Created {
		t.Fatalf("created flags = %v, %v", first.Created, second.Created)
	}
	if diff := cmp.Diff(first.Config, second.Config); diff != "" {
		t.Fatalf("config changed on second init (-first +second):\n%s", diff)
	}
}

func TestStatusFindsParentWorkbench(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, InitOptions{Name: "parent"}); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "nested", "deeper")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	status, err := Status(child)
	if err != nil {
		t.Fatal(err)
	}
	if status.Root != root {
		t.Fatalf("root = %q, want %q", status.Root, root)
	}
	if status.Config.Name != "parent" {
		t.Fatalf("name = %q", status.Config.Name)
	}
}
