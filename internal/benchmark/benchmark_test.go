package benchmark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"midgard/internal/cost"
	"midgard/internal/gitrepo"
	"midgard/internal/model"
	"midgard/internal/model/providers/fake"
	midgardtask "midgard/internal/task"
	"midgard/internal/workbench"
)

func TestWorkerContextOmitsHiddenReferencePRs(t *testing.T) {
	manifest, err := LoadManifest(filepath.Join("..", "..", "testdata", "benchmark", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Items[0].HiddenReferencePRs) == 0 {
		t.Fatal("fixture missing hidden reference PR")
	}
	worker := WorkerContext(manifest.Items[0])
	data, err := json.Marshal(worker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "pull/42") || strings.Contains(string(data), "abc123") {
		t.Fatalf("worker context leaked hidden reference data: %s", data)
	}
}

func TestScoreItemFailsWhenPatchArtifactIsEmpty(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "empty-patch-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initBenchmarkRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := midgardtask.Create(ctx, root, midgardtask.CreateOptions{ID: "task_empty_patch", Objective: "no source change"}); err != nil {
		t.Fatal(err)
	}
	artifactRoot := filepath.Join(root, ".midgard", "artifacts", "task_empty_patch")
	if err := os.WriteFile(filepath.Join(artifactRoot, "patch.diff"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "implementation.mdx"), []byte("# Implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ScoreItem(ctx, root, Item{ID: "item", TaskID: "task_empty_patch"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Score != ScoreFail {
		t.Fatalf("score = %s, want fail", result.Score)
	}
}

func TestPatchChangesMatchRejectsDuplicateLinePatch(t *testing.T) {
	reference := `--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-To target TOML specifically you can implement ` + "`UnmarshalTOML`" + ` TOML interface in
+To target TOML specifically you can implement the ` + "`UnmarshalTOML`" + ` interface in
`
	actual := `# repo:target
diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -1,2 +1,2 @@
-
 To target TOML specifically you can implement ` + "`UnmarshalTOML`" + ` TOML interface in
+To target TOML specifically, you can implement the ` + "`UnmarshalTOML`" + ` interface in
`
	if patchChangesMatch(actual, reference) {
		t.Fatal("duplicate-line patch matched reference changes")
	}
	if !patchChangesMatch(reference, reference) {
		t.Fatal("reference patch should match itself")
	}
}

func TestRunGeneratesEvidenceLinkedReport(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "bench-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initBenchmarkRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := midgardtask.Create(ctx, root, midgardtask.CreateOptions{ID: "task_bench", Objective: "change readme"}); err != nil {
		t.Fatal(err)
	}
	artifactDir := filepath.Join(root, ".midgard", "artifacts", "task_bench")
	scriptPath := filepath.Join(artifactDir, "scripts", "edit.py")
	_, err := midgardtask.RunLoop(ctx, root, "task_bench", midgardtask.RunnerOptions{
		ModelID: "fake-model",
		Providers: midgardtask.RoleProviders{
			model.RolePlanner: fake.New(fake.Response{Text: "@report plan.mdx\n# Plan\n\nReady.\n@result status:ready artifact:plan.mdx checks:none\n"}),
			model.RoleImplementer: fake.New(fake.Response{Text: strings.Join([]string{
				"@report implementation.mdx",
				"# Implementation",
				"@payload begin type:script path:scripts/edit.py lang:python",
				"from pathlib import Path",
				"Path('README.md').write_text('# benchmark change\\n')",
				"@payload end",
				"@edit file:README.md action:modify mode:script content:artifact:scripts/edit.py reason:bench",
				"@cmd repo:repo1 -- python3 " + scriptPath,
				"@result status:ready artifact:implementation.mdx checks:none",
				"",
			}, "\n")}),
			model.RoleReviewer: fake.New(fake.Response{Text: "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"}),
		},
		Pricing: cost.Pricing{ID: "test", InputUSDPerMillion: 1, OutputUSDPerMillion: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		ID:    "bench-local",
		Title: "private/reference secret",
		Items: []Item{{
			ID:        "item-1",
			Title:     "README change",
			Objective: "change readme",
			TaskID:    "task_bench",
			RepoIDs:   []string{"repo1"},
			HiddenReferencePRs: []ReferencePR{{
				Forge:        "github",
				Repo:         "private/reference",
				Number:       1,
				URL:          "https://github.com/private/reference/pull/1",
				MergedCommit: "secret",
			}},
		}},
	}
	report, err := Run(ctx, root, manifest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(report.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "score: pass") || !strings.Contains(text, "artifact:patch.diff") {
		t.Fatalf("report:\n%s", text)
	}
	if !strings.Contains(text, "provider_model: role:planner provider:fake model:fake-model") ||
		!strings.Contains(text, "provider_fingerprint:sha256:") {
		t.Fatalf("report missing provider/model evidence:\n%s", text)
	}
	if strings.Contains(text, "private/reference") || strings.Contains(text, "secret") {
		t.Fatalf("report leaked hidden reference:\n%s", text)
	}
	if !strings.Contains(text, "worker_context_excludes_hidden_references: true") ||
		!strings.Contains(text, "hidden_reference_evidence: bench-local-reference-evidence.json") {
		t.Fatalf("report missing reference sidecar pointer:\n%s", text)
	}
	referenceData, err := os.ReadFile(report.ReferenceEvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(referenceData), "private/reference") || !strings.Contains(string(referenceData), "secret") {
		t.Fatalf("reference sidecar missing hidden evidence:\n%s", referenceData)
	}
}

func TestBenchmarkScoresPassAfterPatchApplyRepair(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := workbench.Init(root, workbench.InitOptions{Name: "bench-repair-test"}); err != nil {
		t.Fatal(err)
	}
	repo := initBenchmarkRepo(t)
	if _, err := workbench.AddRepo(root, workbench.AddRepoOptions{ID: "repo1", Path: repo, MainRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := midgardtask.Create(ctx, root, midgardtask.CreateOptions{ID: "task_bench_repair", Objective: "change readme"}); err != nil {
		t.Fatal(err)
	}
	badPatch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"@payload begin type:patch path:patches/readme-stale.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-# stale heading",
		"+# benchmark repair",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-stale.diff reason:first-pass repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	fixedPatch := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"Repaired stale patch.",
		"@payload begin type:patch path:patches/readme-repaired.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-# benchmark fixture",
		"+# benchmark repair",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme-repaired.diff reason:repair repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	_, err := midgardtask.RunLoop(ctx, root, "task_bench_repair", midgardtask.RunnerOptions{
		ModelID: "fake-model",
		Providers: midgardtask.RoleProviders{
			model.RolePlanner: fake.New(fake.Response{Text: "@report plan.mdx\n# Plan\n\nReady.\n@result status:ready artifact:plan.mdx checks:none\n"}),
			model.RoleImplementer: fake.New(
				fake.Response{Text: badPatch},
				fake.Response{Text: fixedPatch},
			),
			model.RoleReviewer: fake.New(fake.Response{Text: "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"}),
		},
		MaxSourceEditRepairs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	score, err := ScoreItem(ctx, root, Item{ID: "repair-item", TaskID: "task_bench_repair"})
	if err != nil {
		t.Fatal(err)
	}
	if score.Score != ScorePass {
		t.Fatalf("score = %#v, want pass", score)
	}
	artifactRoot := filepath.Join(root, ".midgard", "artifacts", "task_bench_repair")
	if _, err := os.Stat(filepath.Join(artifactRoot, "source-edits", "apply-failures", "1", "stderr.txt")); err != nil {
		t.Fatalf("missing apply failure stderr artifact: %v", err)
	}
}

func TestRunSuitePreparesMergedPRBenchmark(t *testing.T) {
	ctx := context.Background()
	sourceRepo := initBenchmarkRepo(t)
	baseCommit, err := gitrepo.CurrentCommit(ctx, sourceRepo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRepo, "README.md"), []byte("# benchmark repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, sourceRepo, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, sourceRepo, "commit", "-m", "merged pr"); err != nil {
		t.Fatal(err)
	}
	mergedCommit, err := gitrepo.CurrentCommit(ctx, sourceRepo)
	if err != nil {
		t.Fatal(err)
	}
	referencePatch, err := gitrepo.Run(ctx, sourceRepo, "diff", baseCommit+".."+mergedCommit)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	referencePath := filepath.Join(root, "reference.patch")
	if err := os.WriteFile(referencePath, []byte(referencePatch+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	implementerStream := strings.Join([]string{
		"@report implementation.mdx",
		"# Implementation",
		"@payload begin type:patch path:patches/readme.diff",
		"--- a/README.md",
		"+++ b/README.md",
		"@@ -1 +1 @@",
		"-# benchmark fixture",
		"+# benchmark repair",
		"@payload end",
		"@edit file:README.md action:modify mode:patch content:artifact:patches/readme.diff reason:merged-pr repo:repo1",
		"@result status:ready artifact:implementation.mdx checks:none",
		"",
	}, "\n")
	manifest := Manifest{
		ID:    "oss-merged-pr-local",
		Title: "OSS merged PR local fixture",
		Repos: []RepoSource{{
			ID:          "repo1",
			Path:        sourceRepo,
			CheckoutRef: baseCommit,
		}},
		Items: []Item{{
			ID:                   "readme-pr",
			Title:                "README merged PR",
			Objective:            "Apply the README wording change from the merged PR.",
			TaskID:               "task_oss_readme_pr",
			RepoIDs:              []string{"repo1"},
			ExpectedTouchedFiles: []string{"README.md"},
			HiddenReferencePatch: referencePath,
			HiddenReferencePRs: []ReferencePR{{
				Forge:        "github",
				Repo:         "private/fixture",
				Number:       17,
				URL:          "https://github.com/private/fixture/pull/17",
				MergedCommit: "secret-merge",
			}},
		}},
	}
	result, err := RunSuite(ctx, root, manifest, SuiteOptions{
		ProviderFactory: func(Item) (midgardtask.RoleProviders, string, cost.Pricing, error) {
			return midgardtask.RoleProviders{
				model.RolePlanner: fake.New(fake.Response{
					Text:  "@report plan.mdx\n# Plan\n\nReady.\n@result status:ready artifact:plan.mdx checks:none\n",
					Usage: model.Usage{InputTokens: 10, OutputTokens: 5},
				}),
				model.RoleImplementer: fake.New(fake.Response{
					Text:  implementerStream,
					Usage: model.Usage{InputTokens: 20, OutputTokens: 10},
				}),
				model.RoleReviewer: fake.New(fake.Response{
					Text:  "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n",
					Usage: model.Usage{InputTokens: 15, OutputTokens: 5},
				}),
			}, "fake-model", cost.Pricing{ID: "test", InputUSDPerMillion: 1, OutputUSDPerMillion: 1}, nil
		},
		ResetTasks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.PreparedRepos) != 1 || result.PreparedRepos[0].StartCommit != baseCommit {
		t.Fatalf("prepared repos = %#v, want base commit %s", result.PreparedRepos, baseCommit)
	}
	if len(result.Report.Results) != 1 {
		t.Fatalf("results = %#v", result.Report.Results)
	}
	score := result.Report.Results[0]
	if score.Score != ScorePass ||
		!score.Evidence.ReferencePatchMatched ||
		!score.Evidence.ExpectedFilesMatched ||
		score.Evidence.CostUSD <= 0 {
		t.Fatalf("score = %#v", score)
	}
	reportData, err := os.ReadFile(result.Report.Path)
	if err != nil {
		t.Fatal(err)
	}
	reportText := string(reportData)
	for _, want := range []string{"score: pass", "reference_patch_match: true", "expected_touched_files_match: true", "cost_usd:"} {
		if !strings.Contains(reportText, want) {
			t.Fatalf("report missing %q:\n%s", want, reportText)
		}
	}
	if strings.Contains(reportText, "private/fixture") || strings.Contains(reportText, "secret-merge") {
		t.Fatalf("public report leaked hidden PR data:\n%s", reportText)
	}
	sidecar, err := os.ReadFile(result.Report.ReferenceEvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sidecar), "private/fixture") || !strings.Contains(string(sidecar), "secret-merge") {
		t.Fatalf("sidecar missing hidden evidence:\n%s", sidecar)
	}

	result2, err := RunSuite(ctx, root, manifest, SuiteOptions{
		ProviderFactory: func(Item) (midgardtask.RoleProviders, string, cost.Pricing, error) {
			return midgardtask.RoleProviders{
				model.RolePlanner:     fake.New(fake.Response{Text: "@report plan.mdx\n# Plan\n\nReady.\n@result status:ready artifact:plan.mdx checks:none\n"}),
				model.RoleImplementer: fake.New(fake.Response{Text: implementerStream}),
				model.RoleReviewer:    fake.New(fake.Response{Text: "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n"}),
			}, "fake-model", cost.Pricing{}, nil
		},
		ResetTasks: true,
	})
	if err != nil {
		t.Fatalf("rerun failed: %v", err)
	}
	if result2.Report.Results[0].Score != ScorePass {
		t.Fatalf("rerun score = %#v", result2.Report.Results[0])
	}
}

func TestImportPRWritesManifestAndReferencePatch(t *testing.T) {
	ctx := context.Background()
	sourceRepo := initBenchmarkRepo(t)
	baseCommit, err := gitrepo.CurrentCommit(ctx, sourceRepo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRepo, "README.md"), []byte("# benchmark import\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, sourceRepo, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.Run(ctx, sourceRepo, "commit", "-m", "import target"); err != nil {
		t.Fatal(err)
	}
	mergedCommit, err := gitrepo.CurrentCommit(ctx, sourceRepo)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/example/project/pulls/123":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number":           123,
				"title":            "Update benchmark README",
				"body":             "Make the benchmark wording clearer.",
				"html_url":         "https://github.com/example/project/pull/123",
				"merged":           true,
				"merge_commit_sha": mergedCommit,
				"base": map[string]any{
					"sha": baseCommit,
					"repo": map[string]any{
						"full_name":      "example/project",
						"clone_url":      "https://github.com/example/project.git",
						"default_branch": "main",
					},
				},
			})
		case "/repos/example/project/pulls/123/files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"filename": "README.md"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	outPath := filepath.Join(root, "benchmarks", "pr-123.json")
	result, err := ImportPR(ctx, ImportPROptions{
		Repo:       "https://github.com/example/project",
		PullNumber: 123,
		OutPath:    outPath,
		CloneURL:   sourceRepo,
		APIBaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.ID != "example-project-pr-123" || result.ManifestPath != outPath {
		t.Fatalf("result = %#v", result)
	}
	manifest, err := LoadManifest(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repos) != 1 ||
		manifest.Repos[0].Path != sourceRepo ||
		manifest.Repos[0].CheckoutRef != baseCommit {
		t.Fatalf("repos = %#v", manifest.Repos)
	}
	if len(manifest.Items) != 1 {
		t.Fatalf("items = %#v", manifest.Items)
	}
	item := manifest.Items[0]
	if item.TaskID != "bench_example-project-pr-123_pr-123" ||
		item.HiddenReferencePatch != "references/example-project-pr-123.patch" ||
		!strings.Contains(item.Objective, "Update benchmark README") ||
		!strings.Contains(item.Objective, "Make the benchmark wording clearer.") {
		t.Fatalf("item = %#v", item)
	}
	if len(item.ExpectedTouchedFiles) != 1 || item.ExpectedTouchedFiles[0] != "README.md" {
		t.Fatalf("expected files = %#v", item.ExpectedTouchedFiles)
	}
	if len(item.HiddenReferencePRs) != 1 ||
		item.HiddenReferencePRs[0].Repo != "example/project" ||
		item.HiddenReferencePRs[0].MergedCommit != mergedCommit {
		t.Fatalf("hidden refs = %#v", item.HiddenReferencePRs)
	}
	patch, err := os.ReadFile(result.ReferencePatchPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "-# benchmark fixture") ||
		!strings.Contains(string(patch), "+# benchmark import") {
		t.Fatalf("reference patch:\n%s", patch)
	}
}

func initBenchmarkRepo(t *testing.T) string {
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
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# benchmark fixture\n"), 0o644); err != nil {
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
