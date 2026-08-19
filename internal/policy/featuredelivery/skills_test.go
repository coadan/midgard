package featuredelivery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillCatalogUsesFullPrimaryInstructionsAndBoundedReferenceRetrieval(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "heimdal")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	primary := "---\nname: heimdal\ndescription: Browser QA with compact evidence.\n---\n\nRead references/browser.md when visual evidence matters.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(primary), 0o600); err != nil {
		t.Fatal(err)
	}
	reference := "setup\nstart a managed session\nuse bounded evidence\nstop the managed session\ncleanup\n"
	if err := os.WriteFile(filepath.Join(dir, "references", "browser.md"), []byte(reference), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	if summaries := catalog.Summaries(); len(summaries) != 1 || summaries[0].Name != "heimdal" {
		t.Fatalf("summaries = %#v", summaries)
	}
	found, err := catalog.Search(json.RawMessage(`{"query":"browser evidence"}`))
	if err != nil || !strings.Contains(string(found), "heimdal") || strings.Contains(string(found), "Read references/browser.md") {
		t.Fatalf("catalog search = %s, %v", found, err)
	}
	full, err := catalog.Read(json.RawMessage(`{"name":"heimdal"}`))
	if err != nil || !strings.Contains(string(full), "Read references/browser.md") {
		t.Fatalf("primary read = %s, %v", full, err)
	}
	searched, err := catalog.Read(json.RawMessage(`{"name":"heimdal","query":"bounded evidence"}`))
	if err != nil || !strings.Contains(string(searched), `"resource":"references/browser.md"`) || !strings.Contains(string(searched), `"start_line":1`) {
		t.Fatalf("search = %s, %v", searched, err)
	}
	if _, err := catalog.Read(json.RawMessage(`{"name":"heimdal","resource":"references/browser.md"}`)); err == nil || !strings.Contains(err.Error(), "require start_line") {
		t.Fatalf("unbounded reference read error = %v", err)
	}
	ranged, err := catalog.Read(json.RawMessage(`{"name":"heimdal","resource":"references/browser.md","start_line":2,"line_count":2}`))
	if err != nil || !strings.Contains(string(ranged), "start a managed session\\nuse bounded evidence") || !strings.Contains(string(ranged), `"has_more":true`) || strings.Contains(string(ranged), "cleanup") {
		t.Fatalf("range = %s, %v", ranged, err)
	}
	if _, err := catalog.Read(json.RawMessage(`{"name":"heimdal","resource":"../outside","start_line":1,"line_count":1}`)); err == nil {
		t.Fatal("reference traversal was accepted")
	}
}

func TestInstalledSkillsPreferRepositoryThenMidgardUserThenCodexCompatibility(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	config := filepath.Join(root, "config")
	home := filepath.Join(root, "home")
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("HOME", home)
	userConfig, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	writeSkill := func(directory, name, description string) {
		t.Helper()
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + description
		if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(filepath.Join(home, ".codex", "skills", "shared"), "shared", "codex copy")
	writeSkill(filepath.Join(userConfig, "midgard", "skills", "shared"), "shared", "midgard copy")
	writeSkill(filepath.Join(repository, ".agents", "skills", "shared"), "shared", "repository copy")
	writeSkill(filepath.Join(userConfig, "midgard", "skills", "native"), "native", "native only")
	writeSkill(filepath.Join(home, ".codex", "skills", "compat"), "compat", "compatibility only")

	catalog, err := DiscoverInstalledSkills(repository)
	if err != nil {
		t.Fatal(err)
	}
	summaries := catalog.Summaries()
	if len(summaries) != 3 {
		t.Fatalf("summaries = %#v", summaries)
	}
	for _, summary := range summaries {
		if summary.Name == "shared" && summary.Description != "repository copy" {
			t.Fatalf("repository skill did not win: %#v", summary)
		}
	}
}

func TestMaskedSkillCatalogHidesMetadataAndReads(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "hidden")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: hidden\ndescription: hidden instructions\n---\n\nsecret workflow\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := DiscoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	masked := MaskSkills(catalog, []string{"hidden"})
	if summaries := masked.Summaries(); len(summaries) != 0 {
		t.Fatalf("masked summaries = %#v", summaries)
	}
	if found, err := masked.Search(json.RawMessage(`{"query":"hidden"}`)); err != nil || strings.Contains(string(found), `"Name":"hidden"`) {
		t.Fatalf("masked search = %s, %v", found, err)
	}
	if _, err := masked.Read(json.RawMessage(`{"name":"hidden"}`)); err == nil || !strings.Contains(err.Error(), "not available in this project") {
		t.Fatalf("masked read error = %v", err)
	}
}
