package project_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"midgard/internal/project"
)

func TestSkillGroupsAssignOneTechnologyAndMaskPerProject(t *testing.T) {
	groups := project.SkillGroups{Path: filepath.Join(t.TempDir(), "groups.json")}
	if err := groups.Assign("spacetimedb", []string{"concepts", "rust-server"}); err != nil {
		t.Fatal(err)
	}
	if err := groups.Assign("xtdb", []string{"xtdb-query-and-transact", "concepts"}); err != nil {
		t.Fatal(err)
	}
	got, err := groups.Groups()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["spacetimedb"], []string{"rust-server"}) || !reflect.DeepEqual(got["xtdb"], []string{"concepts", "xtdb-query-and-transact"}) {
		t.Fatalf("groups = %#v", got)
	}
	if err := groups.SetEnabled("project-a", "xtdb", false); err != nil {
		t.Fatal(err)
	}
	disabled, err := groups.Disabled("project-a")
	if err != nil || !reflect.DeepEqual(disabled, []string{"xtdb"}) {
		t.Fatalf("disabled = %#v, %v", disabled, err)
	}
	disabled, err = groups.Disabled("project-b")
	if err != nil || len(disabled) != 0 {
		t.Fatalf("other project disabled = %#v, %v", disabled, err)
	}
}
