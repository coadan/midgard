package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"midgard/internal/artifact"
)

type artifactInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (api *API) handleArtifacts(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/artifacts" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	taskID := r.URL.Query().Get("task")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task is required")
		return
	}
	root := filepath.Join(api.layout.Artifacts, taskID)
	var artifacts []artifactInfo
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, artifactInfo{Path: filepath.ToSlash(rel), Size: info.Size()})
		return nil
	}); err != nil && !os.IsNotExist(err) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, artifacts)
}

func (api *API) handleArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/artifacts/")
	taskID, artifactPath, ok := strings.Cut(rest, "/")
	if !ok || taskID == "" || artifactPath == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := artifact.ValidatePath(artifactPath); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	http.ServeFile(w, r, filepath.Join(api.layout.Artifacts, taskID, filepath.FromSlash(artifactPath)))
}
