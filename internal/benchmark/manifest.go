package benchmark

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Manifest struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	Repos   []RepoSource `json:"repos"`
	Items   []Item       `json:"items"`
	BaseDir string       `json:"-"`
}

type RepoSource struct {
	ID          string `json:"id"`
	URL         string `json:"url,omitempty"`
	Path        string `json:"path,omitempty"`
	CheckoutRef string `json:"checkout_ref"`
	MainRef     string `json:"main_ref,omitempty"`
}

type Item struct {
	ID                   string        `json:"id"`
	Title                string        `json:"title"`
	Objective            string        `json:"objective"`
	TaskID               string        `json:"task_id"`
	RepoIDs              []string      `json:"repo_ids"`
	Checks               []string      `json:"checks"`
	ExpectedTouchedFiles []string      `json:"expected_touched_files"`
	HiddenReferencePRs   []ReferencePR `json:"hidden_reference_prs"`
	HiddenReferencePatch string        `json:"hidden_reference_patch"`
	ManifestBaseDir      string        `json:"-"`
}

type ReferencePR struct {
	Forge        string `json:"forge"`
	Repo         string `json:"repo"`
	Number       int    `json:"number"`
	URL          string `json:"url"`
	MergedCommit string `json:"merged_commit"`
}

type WorkerItem struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Objective string   `json:"objective"`
	TaskID    string   `json:"task_id"`
	RepoIDs   []string `json:"repo_ids"`
	Checks    []string `json:"checks"`
}

func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	manifest.BaseDir = filepath.Dir(path)
	for i := range manifest.Items {
		manifest.Items[i].ManifestBaseDir = manifest.BaseDir
	}
	return manifest, nil
}

func WorkerContext(item Item) WorkerItem {
	return WorkerItem{
		ID:        item.ID,
		Title:     item.Title,
		Objective: item.Objective,
		TaskID:    item.TaskID,
		RepoIDs:   append([]string(nil), item.RepoIDs...),
		Checks:    append([]string(nil), item.Checks...),
	}
}
