package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"midgard/internal/gitrepo"
	"midgard/internal/model/providers/deepseek"
)

func TestWorkbenchInitAndStatus(t *testing.T) {
	root := t.TempDir()

	var out bytes.Buffer
	var errOut bytes.Buffer
	if err := Run(context.Background(), []string{"workbench", "init", "--root", root, "--name", "cli-test"}, nil, &out, &errOut); err != nil {
		t.Fatalf("init failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "status: created") {
		t.Fatalf("init output missing created status:\n%s", out.String())
	}

	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errOut.Reset()
	if err := Run(context.Background(), []string{"workbench", "status", "--root", nested}, nil, &out, &errOut); err != nil {
		t.Fatalf("status failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "name: cli-test") {
		t.Fatalf("status output missing workbench name:\n%s", out.String())
	}
}

func TestTaskCommandsCreateStatusAndDiff(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := initCLITestRepo(t)

	var out bytes.Buffer
	var errOut bytes.Buffer
	if err := Run(ctx, []string{"workbench", "init", "--root", root, "--name", "cli-task-test"}, nil, &out, &errOut); err != nil {
		t.Fatalf("init failed: %v\nstderr:\n%s", err, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"workbench", "add-repo", "--root", root, "--id", "repo1", "--path", repo, "--main-ref", "main"}, nil, &out, &errOut); err != nil {
		t.Fatalf("add-repo failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "repo: repo1") {
		t.Fatalf("add-repo output:\n%s", out.String())
	}

	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "create", "--root", root, "--id", "task_cli", "--objective", "exercise CLI lifecycle"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task create failed: %v\nstderr:\n%s", err, errOut.String())
	}
	createOut := out.String()
	if !strings.Contains(createOut, "task: task_cli") || !strings.Contains(createOut, "dirty: false") {
		t.Fatalf("task create output:\n%s", createOut)
	}

	fakePlan := filepath.Join(root, "planner.stream")
	if err := os.WriteFile(fakePlan, []byte("@report plan.mdx\n# Plan\n\nReady.\n@result status:ready artifact:plan.mdx checks:none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "step", "--root", root, "--task", "task_cli", "--role", "planner", "--provider", "fake", "--fake-stream", fakePlan, "--max-output-tokens", "256"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task step failed: %v\nstderr:\n%s", err, errOut.String())
	}
	stepOut := out.String()
	if !strings.Contains(stepOut, "role: planner") || !strings.Contains(stepOut, "status: ready") {
		t.Fatalf("task step output:\n%s", stepOut)
	}

	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "status", "--root", root, "--task", "task_cli"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task status failed: %v\nstderr:\n%s", err, errOut.String())
	}
	statusOut := out.String()
	if !strings.Contains(statusOut, "repo: repo1") || !strings.Contains(statusOut, "next: implement") {
		t.Fatalf("task status output:\n%s", statusOut)
	}

	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "diff", "--root", root, "--task", "task_cli", "--repo", "repo1"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task diff failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if out.String() != "" {
		t.Fatalf("task diff = %q, want empty", out.String())
	}

	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"command", "run", "--root", root, "--task", "task_cli", "--repo", "repo1", "--", "printf '\ncli change\n' >> README.md"}, nil, &out, &errOut); err != nil {
		t.Fatalf("command run failed: %v\nstderr:\n%s", err, errOut.String())
	}
	commandOut := out.String()
	if !strings.Contains(commandOut, "exit: 0") || !strings.Contains(commandOut, "touched: README.md") {
		t.Fatalf("command output:\n%s", commandOut)
	}
	resultPath := valueForPrefix(commandOut, "result: ")
	if resultPath == "" {
		t.Fatalf("command output missing result path:\n%s", commandOut)
	}

	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"artifact", "list", "--root", root, "--task", "task_cli"}, nil, &out, &errOut); err != nil {
		t.Fatalf("artifact list failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), resultPath) {
		t.Fatalf("artifact list output:\n%s", out.String())
	}

	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"artifact", "show", "--root", root, "--task", "task_cli", "--path", resultPath}, nil, &out, &errOut); err != nil {
		t.Fatalf("artifact show failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), `"TouchedFiles"`) {
		t.Fatalf("artifact show output:\n%s", out.String())
	}

	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"check", "record", "--root", root, "--task", "task_cli", "--id", "unit", "--status", "passed", "--summary", "cli smoke"}, nil, &out, &errOut); err != nil {
		t.Fatalf("check record failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "check: unit") {
		t.Fatalf("check output:\n%s", out.String())
	}

	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "stream", "--root", root, "--task", "task_cli"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task stream failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "command.finished") || !strings.Contains(out.String(), "check.recorded") {
		t.Fatalf("task stream output:\n%s", out.String())
	}

	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "create", "--root", root, "--id", "task_loop", "--objective", "complete loop"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task_loop create failed: %v\nstderr:\n%s", err, errOut.String())
	}
	loopArtifactDir := filepath.Join(root, ".midgard", "artifacts", "task_loop")
	plannerStream := filepath.Join(root, "loop-planner.stream")
	implementerStream := filepath.Join(root, "loop-implementer.stream")
	reviewerStream := filepath.Join(root, "loop-reviewer.stream")
	if err := os.WriteFile(plannerStream, []byte("@report plan.mdx\n# Plan\n\nReady.\n@result status:ready artifact:plan.mdx checks:none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	implementer := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"@payload begin type:script path:scripts/edit.py lang:python",
		"from pathlib import Path",
		"Path('README.md').write_text('# loop done\\n')",
		"@payload end",
		"@edit file:README.md action:modify mode:script content:artifact:scripts/edit.py reason:loop",
		"@cmd repo:repo1 -- python3 " + filepath.Join(loopArtifactDir, "scripts", "edit.py"),
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	if err := os.WriteFile(implementerStream, []byte(implementer), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reviewerStream, []byte("@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{
		"task", "run",
		"--root", root,
		"--task", "task_loop",
		"--provider", "fake",
		"--planner-stream", plannerStream,
		"--implementer-stream", implementerStream,
		"--reviewer-stream", reviewerStream,
		"--max-output-tokens", "512",
	}, nil, &out, &errOut); err != nil {
		t.Fatalf("task run failed: %v\nstderr:\n%s", err, errOut.String())
	}
	runOut := out.String()
	if !strings.Contains(runOut, "state: completed") ||
		!strings.Contains(runOut, "patch: patch.diff") ||
		!strings.Contains(runOut, "cost: unknown (fake provider)") {
		t.Fatalf("task run output:\n%s", runOut)
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"artifact", "show", "--root", root, "--task", "task_loop", "--path", "plan.mdx"}, nil, &out, &errOut); err != nil {
		t.Fatalf("show loop plan failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "provider_fingerprint: sha256:") {
		t.Fatalf("loop plan missing provenance:\n%s", out.String())
	}

	manifest := filepath.Join(root, "benchmark.json")
	if err := os.WriteFile(manifest, []byte(`{
  "id": "cli-bench",
  "title": "CLI Bench",
  "items": [
    {
      "id": "cli-item",
      "title": "CLI item",
      "objective": "exercise benchmark command",
      "task_id": "task_cli",
      "repo_ids": ["repo1"],
      "hidden_reference_prs": [
        {"forge":"github","repo":"private/ref","number":1,"url":"https://github.com/private/ref/pull/1","merged_commit":"secret"}
      ]
    }
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"benchmark", "run", "--root", root, "--manifest", manifest}, nil, &out, &errOut); err != nil {
		t.Fatalf("benchmark run failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "benchmark: cli-bench") || strings.Contains(out.String(), "secret") {
		t.Fatalf("benchmark output:\n%s", out.String())
	}

	baseCommit, err := gitrepo.CurrentCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	suitePlanner := filepath.Join(root, "suite-planner.stream")
	suiteImplementer := filepath.Join(root, "suite-implementer.stream")
	suiteReviewer := filepath.Join(root, "suite-reviewer.stream")
	if err := os.WriteFile(suitePlanner, []byte("@report plan.mdx\n# Plan\n\nReady.\n@result status:ready artifact:plan.mdx checks:none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	suitePatch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"@payload begin type:patch path:patches/readme-suite.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-# cli fixture",
		"+# cli suite done",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-suite.diff reason:suite repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	if err := os.WriteFile(suiteImplementer, []byte(suitePatch), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(suiteReviewer, []byte("@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	suiteManifest := filepath.Join(root, "suite-benchmark.json")
	if err := os.WriteFile(suiteManifest, []byte(`{
  "id": "cli-suite-bench",
  "title": "CLI Suite Bench",
  "repos": [
    {"id":"repo1","path":`+quoteJSON(repo)+`,"checkout_ref":`+quoteJSON(baseCommit)+`}
  ],
  "items": [
    {
      "id": "suite-item",
      "title": "Suite item",
      "objective": "exercise benchmark suite command",
      "task_id": "task_cli_suite",
      "repo_ids": ["repo1"],
      "checks": ["git diff --check"],
      "expected_touched_files": ["README.md"]
    }
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{
		"benchmark", "run",
		"--root", root,
		"--manifest", suiteManifest,
		"--provider", "fake",
		"--planner-stream", suitePlanner,
		"--implementer-stream", suiteImplementer,
		"--reviewer-stream", suiteReviewer,
		"--max-output-tokens", "512",
	}, nil, &out, &errOut); err != nil {
		t.Fatalf("benchmark suite failed: %v\nstderr:\n%s", err, errOut.String())
	}
	suiteOut := out.String()
	if !strings.Contains(suiteOut, "benchmark: cli-suite-bench") ||
		!strings.Contains(suiteOut, "run: benchmark_run_") ||
		!strings.Contains(suiteOut, "task: task_cli_suite item:suite-item state:completed") ||
		!strings.Contains(suiteOut, "action:created") ||
		!strings.Contains(suiteOut, "item: suite-item score:pass") ||
		!strings.Contains(suiteOut, "acceptance:passed") {
		t.Fatalf("benchmark suite output:\n%s", suiteOut)
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{
		"benchmark", "run",
		"--root", root,
		"--manifest", suiteManifest,
		"--provider", "fake",
		"--planner-stream", suitePlanner,
		"--implementer-stream", suiteImplementer,
		"--reviewer-stream", suiteReviewer,
		"--max-output-tokens", "512",
	}, nil, &out, &errOut); err != nil {
		t.Fatalf("benchmark suite resume failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "action:reused") {
		t.Fatalf("benchmark suite did not resume completed task:\n%s", out.String())
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{
		"benchmark", "verify", "--root", root, "--manifest", suiteManifest, "--acceptance-timeout", "30s",
	}, nil, &out, &errOut); err != nil {
		t.Fatalf("benchmark verify failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "item: suite-item score:pass") {
		t.Fatalf("benchmark verify output:\n%s", out.String())
	}
}

func TestForgePRCommandsLinkRefreshAndStatus(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := initCLITestRepo(t)
	var out bytes.Buffer
	var errOut bytes.Buffer
	if err := Run(ctx, []string{"workbench", "init", "--root", root, "--name", "forge-cli-test"}, nil, &out, &errOut); err != nil {
		t.Fatalf("init failed: %v\nstderr:\n%s", err, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"workbench", "add-repo", "--root", root, "--id", "repo1", "--path", repo, "--main-ref", "main"}, nil, &out, &errOut); err != nil {
		t.Fatalf("add repo failed: %v\nstderr:\n%s", err, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "create", "--root", root, "--id", "task_forge", "--objective", "track linked PR"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task create failed: %v\nstderr:\n%s", err, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"forge", "repo", "link", "--root", root, "--repo", "repo1", "--forge", "github", "--remote", "example/project"}, nil, &out, &errOut); err != nil {
		t.Fatalf("forge repo link failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "forge: github-main") || !strings.Contains(out.String(), "remote: example/project") {
		t.Fatalf("repo link output:\n%s", out.String())
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "pr", "link", "--root", root, "--task", "task_forge", "--repo", "repo1", "--pr", "42", "--head-sha", "abcdef1234567890"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task pr link failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "pr: github-main#42") || !strings.Contains(out.String(), "url: https://github.com/example/project/pull/42") {
		t.Fatalf("pr link output:\n%s", out.String())
	}
	snapshot := filepath.Join(root, "snapshot.json")
	if err := os.WriteFile(snapshot, []byte(`{
  "state": "open",
  "title": "Fix public catalog metadata",
  "author": "alice",
  "labels": ["bug"],
  "mergeable_state": "clean",
  "review_decision": "changes_requested",
  "check_conclusion": "failure",
  "unresolved_thread_count": 2,
  "head_sha": "abcdef1234567890",
  "checks": [{"name":"unit","status":"completed","conclusion":"failure"}],
  "threads": [{"id":"thread-1","path":"README.md","line":7,"resolved":false,"last_author":"bob"}]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "pr", "refresh", "--root", root, "--task", "task_forge", "--repo", "repo1", "--pr", "42", "--snapshot", snapshot}, nil, &out, &errOut); err != nil {
		t.Fatalf("task pr refresh failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "checks: 1") || !strings.Contains(out.String(), "threads: 1") || !strings.Contains(out.String(), "artifact: artifact:forge/") {
		t.Fatalf("refresh output:\n%s", out.String())
	}
	snapshots, err := filepath.Glob(filepath.Join(root, ".midgard", "artifacts", "task_forge", "forge", "task_forge_repo1_github-main_42", "forge_snap_*", "snapshot.json"))
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshot artifacts = %#v, err=%v", snapshots, err)
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "pr", "status", "--root", root, "--task", "task_forge"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task pr status failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "state: open draft:false checks:failure review:changes_requested unresolved_threads:2") ||
		!strings.Contains(out.String(), "check: unit status:completed conclusion:failure") ||
		!strings.Contains(out.String(), "thread: thread-1 path:README.md line:7 resolved:false") {
		t.Fatalf("status output:\n%s", out.String())
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "pr", "status", "--root", root, "--task", "task_forge", "--for-agent"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task pr status --for-agent failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "repo:repo1 pr:github-main#42 head:abcdef123456 state:open checks:failure review:changes_requested threads:2") ||
		!strings.Contains(out.String(), "warnings:pr-open,checks-failure,changes-requested,unresolved-threads,head-mismatch") ||
		!strings.Contains(out.String(), "refs:forge:artifact:forge/") {
		t.Fatalf("agent status output:\n%s", out.String())
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "pr", "list", "--root", root, "--task", "task_forge"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task pr list failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "repo:repo1 pr:github-main#42") {
		t.Fatalf("list output:\n%s", out.String())
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "pr", "checks", "--root", root, "--task", "task_forge", "--repo", "repo1", "--pr", "42"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task pr checks failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "artifact: artifact:forge/") || !strings.Contains(out.String(), "check: unit status:completed conclusion:failure") {
		t.Fatalf("checks output:\n%s", out.String())
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "pr", "threads", "--root", root, "--task", "task_forge", "--repo", "repo1", "--pr", "https://github.com/example/project/pull/42"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task pr threads failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "complete: true") || !strings.Contains(out.String(), "thread: thread-1 path:README.md line:7 resolved:false") {
		t.Fatalf("threads output:\n%s", out.String())
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "pr", "unlink", "--root", root, "--task", "task_forge", "--repo", "repo1", "--pr", "https://github.com/example/project/pull/42"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task pr unlink failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "unlinked: true") {
		t.Fatalf("unlink output:\n%s", out.String())
	}
	out.Reset()
	errOut.Reset()
	if err := Run(ctx, []string{"task", "pr", "list", "--root", root, "--task", "task_forge"}, nil, &out, &errOut); err != nil {
		t.Fatalf("task pr list after unlink failed: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("links remain after unlink:\n%s", out.String())
	}
}

func TestBenchmarkImportPRCommand(t *testing.T) {
	ctx := context.Background()
	repo := initCLITestRepo(t)
	baseCommit, err := gitrepo.CurrentCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# cli imported\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, repo, "commit", "-m", "import target"); err != nil {
		t.Fatal(err)
	}
	mergedCommit, err := gitrepo.CurrentCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	referenceDiff, err := gitrepo.Run(ctx, repo, "diff", baseCommit+".."+mergedCommit)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/example/project/pulls/9":
			if r.Header.Get("Accept") == "application/vnd.github.diff" {
				_, _ = w.Write([]byte(referenceDiff + "\n"))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number":           9,
				"title":            "CLI import fixture",
				"html_url":         "https://github.com/example/project/pull/9",
				"merged":           true,
				"merge_commit_sha": mergedCommit,
				"base": map[string]any{
					"sha": baseCommit,
					"repo": map[string]any{
						"full_name": "example/project",
						"clone_url": "https://github.com/example/project.git",
					},
				},
			})
		case "/repos/example/project/pulls/9/files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"filename": "README.md"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	manifest := filepath.Join(root, "imported.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	if err := Run(ctx, []string{
		"benchmark", "import-pr",
		"--repo", "example/project",
		"--pr", "9",
		"--out", manifest,
		"--clone-url", repo,
		"--github-api-url", server.URL,
		"--check", "git diff --check",
	}, nil, &out, &errOut); err != nil {
		t.Fatalf("benchmark import-pr failed: %v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "benchmark: example-project-pr-9") ||
		!strings.Contains(out.String(), "item: pr-9 task:bench_example-project-pr-9_pr-9 expected_files:1") {
		t.Fatalf("import output:\n%s", out.String())
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"hidden_reference_patch": "references/example-project-pr-9.patch"`) ||
		!strings.Contains(text, `"checkout_ref": "`+baseCommit+`"`) ||
		!strings.Contains(text, `"command": "git diff --check"`) {
		t.Fatalf("manifest:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(root, "references", "example-project-pr-9.patch")); err != nil {
		t.Fatal(err)
	}
}

func TestDeepSeekCLIProfileUsesV4ProPricingAndMaxEffort(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	t.Setenv(deepseek.ReasoningEffortEnvName, "high")
	provider, err := roleProviderWithOptions("deepseek", "", providerOptions{DeepSeekReasoningEffort: "max"})
	if err != nil {
		t.Fatal(err)
	}
	client, ok := provider.(*deepseek.Client)
	if !ok {
		t.Fatalf("provider = %T, want *deepseek.Client", provider)
	}
	if client.ReasoningEffort != "max" {
		t.Fatalf("reasoning effort = %q", client.ReasoningEffort)
	}
	if got := benchmarkProviderOptions("deepseek", "max"); got != "reasoning_effort=max" {
		t.Fatalf("explicit benchmark provider options = %q", got)
	}
	if got := benchmarkProviderOptions("deepseek", ""); got != "reasoning_effort=high" {
		t.Fatalf("environment benchmark provider options = %q", got)
	}
	pricing := pricingForProvider("deepseek", "deepseek-v4-pro")
	if pricing.ProviderID != "deepseek" ||
		pricing.ModelID != "deepseek-v4-pro" ||
		pricing.InputUSDPerMillion != 0.435 ||
		pricing.OutputUSDPerMillion != 0.87 ||
		pricing.Source == "" {
		t.Fatalf("pricing = %#v", pricing)
	}
}

func quoteJSON(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func initCLITestRepo(t *testing.T) string {
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
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# cli fixture\n"), 0o644); err != nil {
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

func TestRoleProviderCodexUsesLocalAccessToken(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_ACCESS_TOKEN", "test-token")
	t.Setenv("CODEX_HOME", codexHome)

	provider, err := roleProvider("codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if provider.ID() != "codex" {
		t.Fatalf("provider ID = %q", provider.ID())
	}
	if got := resolveModelID("", "codex"); got != "gpt-5.4" {
		t.Fatalf("codex default model = %q", got)
	}

	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("model = \"local-codex-model\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveModelID("", "codex"); got != "local-codex-model" {
		t.Fatalf("codex configured model = %q", got)
	}
}

func valueForPrefix(text, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}
