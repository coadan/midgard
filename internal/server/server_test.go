package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"midgard/internal/command"
	"midgard/internal/gitrepo"
	"midgard/internal/workbench"
)

func TestServerTaskCommandArtifactsAndSSE(t *testing.T) {
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "server-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initServerRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	server, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	indexResp, err := http.Get(httpServer.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if indexResp.StatusCode != http.StatusOK {
		t.Fatalf("index status = %d body=%s", indexResp.StatusCode, readBody(t, indexResp))
	}
	if !strings.Contains(readBody(t, indexResp), `<div id="root">`) {
		t.Fatal("static index did not serve browser shell")
	}

	createResp := postJSON(t, httpServer.URL+"/api/tasks", map[string]any{
		"id":        "task_api",
		"objective": "api lifecycle",
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createResp.StatusCode, readBody(t, createResp))
	}

	statusResp, err := http.Get(httpServer.URL + "/api/tasks/task_api")
	if err != nil {
		t.Fatal(err)
	}
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", statusResp.StatusCode, readBody(t, statusResp))
	}

	commandResp := postJSON(t, httpServer.URL+"/api/commands/run", map[string]any{
		"task_id": "task_api",
		"repo_id": "repo1",
		"command": "printf '\napi change\n' >> README.md",
	})
	if commandResp.StatusCode != http.StatusOK {
		t.Fatalf("command status = %d body=%s", commandResp.StatusCode, readBody(t, commandResp))
	}
	var commandResult command.Result
	if err := json.NewDecoder(commandResp.Body).Decode(&commandResult); err != nil {
		t.Fatal(err)
	}
	if len(commandResult.TouchedFiles) != 1 || commandResult.TouchedFiles[0] != "README.md" {
		t.Fatalf("touched files = %#v", commandResult.TouchedFiles)
	}

	artifactsResp, err := http.Get(httpServer.URL + "/api/artifacts?task=task_api")
	if err != nil {
		t.Fatal(err)
	}
	if artifactsResp.StatusCode != http.StatusOK {
		t.Fatalf("artifacts status = %d body=%s", artifactsResp.StatusCode, readBody(t, artifactsResp))
	}
	body := readBody(t, artifactsResp)
	if !strings.Contains(body, commandResult.ResultPath) {
		t.Fatalf("artifact list missing result path:\n%s", body)
	}

	artifactResp, err := http.Get(httpServer.URL + "/api/artifacts/task_api/" + commandResult.ResultPath)
	if err != nil {
		t.Fatal(err)
	}
	if artifactResp.StatusCode != http.StatusOK {
		t.Fatalf("artifact status = %d body=%s", artifactResp.StatusCode, readBody(t, artifactResp))
	}
	if !strings.Contains(readBody(t, artifactResp), `"TouchedFiles"`) {
		t.Fatal("artifact hydration did not return command result JSON")
	}

	eventsResp, err := http.Get(httpServer.URL + "/api/events?task=task_api")
	if err != nil {
		t.Fatal(err)
	}
	if eventsResp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d body=%s", eventsResp.StatusCode, readBody(t, eventsResp))
	}
	if !strings.Contains(readBody(t, eventsResp), "command.finished") {
		t.Fatal("events response missing command.finished")
	}

	streamResp, err := http.Get(httpServer.URL + "/api/events/stream?task=task_api")
	if err != nil {
		t.Fatal(err)
	}
	streamBody := readBody(t, streamResp)
	if !strings.Contains(streamBody, "id: 1\n") || !strings.Contains(streamBody, "event: command.finished\n") {
		t.Fatalf("SSE body:\n%s", streamBody)
	}
}

func postJSON(t *testing.T, url string, value any) *http.Response {
	t.Helper()
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(value); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", &body)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func initServerRepo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	if _, err := gitrepo.Run(ctx, repo, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "config", "user.email", "midgard@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "config", "user.name", "Midgard Test"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# server fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "commit", "-m", "initial"); err != nil {
		t.Fatal(err)
	}
	return repo
}
