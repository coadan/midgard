package project_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"midgard/internal/project"
)

func TestSkillMasksAreDurableSortedAndProjectScoped(t *testing.T) {
	store := project.SkillMasks{Path: filepath.Join(t.TempDir(), "skill-masks.json")}
	if err := store.Set("project-a", []string{"zeta", "alpha", "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("project-b", []string{"beta"}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Disabled("project-a"); err != nil || !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("project-a masks = %#v, %v", got, err)
	}
	if got, err := store.Disabled("project-b"); err != nil || !reflect.DeepEqual(got, []string{"beta"}) {
		t.Fatalf("project-b masks = %#v, %v", got, err)
	}
}
