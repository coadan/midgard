package benchmark

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"midgard/internal/cost"
	"midgard/internal/gitrepo"
	"midgard/internal/model"
	"midgard/internal/model/providers/fake"
	"midgard/internal/state"
	midgardtask "midgard/internal/task"
	"midgard/internal/workbench"
)

type suiteBlockingProvider struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int64
	text    string
}

func (p *suiteBlockingProvider) ID() string { return "fake" }

func (p *suiteBlockingProvider) Stream(ctx context.Context, _ model.Packet, emit func(model.Delta) error) (model.Usage, error) {
	p.calls.Add(1)
	p.once.Do(func() { close(p.entered) })
	select {
	case <-ctx.Done():
		return model.Usage{}, context.Cause(ctx)
	case <-p.release:
	}
	if err := emit(model.Delta{Text: p.text}); err != nil {
		return model.Usage{}, err
	}
	return model.Usage{InputTokens: 10, OutputTokens: 5}, nil
}

func TestRunSuiteRejectsConcurrentBenchmarkExecutionBeforeProviderCall(t *testing.T) {
	ctx := context.Background()
	repo := initBenchmarkRepo(t)
	base, err := gitrepo.CurrentCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	manifest := resumeManifest("resume-contention", repo, base, 1, false)
	root := t.TempDir()
	planner := &suiteBlockingProvider{
		entered: make(chan struct{}), release: make(chan struct{}),
		text: "@report plan.mdx\n# Plan\n\nReady.\n@result status:ready artifact:plan.mdx checks:none\n",
	}
	firstProviders := map[string]suiteProviders{}
	firstDone := make(chan error, 1)
	go func() {
		_, err := RunSuite(ctx, root, manifest, resumeSuiteOptions(firstProviders, func(Item) model.Provider { return planner }))
		firstDone <- err
	}()
	select {
	case <-planner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first benchmark execution did not reach provider")
	}

	secondProviders := map[string]suiteProviders{}
	_, err = RunSuite(ctx, root, manifest, resumeSuiteOptions(secondProviders, nil))
	var heldErr state.ExecutionLeaseHeldError
	if !errors.As(err, &heldErr) || heldErr.Lease.ResourceType != state.LeaseResourceBenchmark {
		t.Fatalf("concurrent benchmark error = %v, want benchmark lease held", err)
	}
	if secondProviders[manifest.Items[0].ID].calls() != 0 {
		t.Fatalf("second benchmark provider calls = %d, want 0", secondProviders[manifest.Items[0].ID].calls())
	}
	close(planner.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	usage, _ := suiteRecordCounts(t, root)
	if usage != 3 || planner.calls.Load() != 1 {
		t.Fatalf("usage/planner calls = %d/%d, want 3/1", usage, planner.calls.Load())
	}
}

type suiteProviders struct {
	planner     model.Provider
	implementer *fake.Provider
	reviewer    *fake.Provider
}

func (p suiteProviders) roles() midgardtask.RoleProviders {
	return midgardtask.RoleProviders{
		model.RolePlanner: p.planner, model.RoleImplementer: p.implementer, model.RoleReviewer: p.reviewer,
	}
}

func (p suiteProviders) calls() int {
	calls := p.implementer.Calls() + p.reviewer.Calls()
	if planner, ok := p.planner.(*fake.Provider); ok {
		calls += planner.Calls()
	}
	if planner, ok := p.planner.(*cancelProvider); ok {
		calls += planner.calls
	}
	return calls
}

type cancelProvider struct {
	cancel context.CancelFunc
	calls  int
}

func (p *cancelProvider) ID() string { return "fake" }

func (p *cancelProvider) Stream(ctx context.Context, _ model.Packet, _ func(model.Delta) error) (model.Usage, error) {
	p.calls++
	p.cancel()
	return model.Usage{}, ctx.Err()
}

func TestRunSuiteReusesCompletedItemsWithoutProviderCalls(t *testing.T) {
	ctx := context.Background()
	repo := initBenchmarkRepo(t)
	base, err := gitrepo.CurrentCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	manifest := resumeManifest("resume-completed", repo, base, 2, false)
	root := t.TempDir()
	firstProviders := map[string]suiteProviders{}
	first, err := RunSuite(ctx, root, manifest, resumeSuiteOptions(firstProviders, nil))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(first.Report.Path)
	if err != nil {
		t.Fatal(err)
	}
	usageBefore, eventsBefore := suiteRecordCounts(t, root)

	secondProviders := map[string]suiteProviders{}
	second, err := RunSuite(ctx, root, manifest, resumeSuiteOptions(secondProviders, nil))
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(second.Report.Path)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID != second.RunID || second.RunStatus != "completed" {
		t.Fatalf("run identity/status = %s %s, want %s completed", second.RunID, second.RunStatus, first.RunID)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("completed resume changed deterministic report\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, item := range manifest.Items {
		if secondProviders[item.ID].calls() != 0 {
			t.Fatalf("item %s provider calls = %d, want 0", item.ID, secondProviders[item.ID].calls())
		}
	}
	for _, taskRun := range second.TaskRuns {
		if taskRun.Action != "reused" {
			t.Fatalf("task run = %#v, want reused", taskRun)
		}
	}
	usageAfter, eventsAfter := suiteRecordCounts(t, root)
	if usageAfter != usageBefore || eventsAfter != eventsBefore {
		t.Fatalf("resume duplicated records: usage %d -> %d, events %d -> %d", usageBefore, usageAfter, eventsBefore, eventsAfter)
	}
}

func TestRunSuiteResumesAfterInterruptionWithoutRepeatingCompletedItems(t *testing.T) {
	ctx := context.Background()
	repo := initBenchmarkRepo(t)
	base, err := gitrepo.CurrentCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	manifest := resumeManifest("resume-interrupted", repo, base, 10, false)
	root := t.TempDir()
	firstProviders := map[string]suiteProviders{}
	interruptedCtx, cancel := context.WithCancel(ctx)
	_, err = RunSuite(interruptedCtx, root, manifest, resumeSuiteOptions(firstProviders, func(item Item) model.Provider {
		if item.ID == "item-05" {
			return &cancelProvider{cancel: cancel}
		}
		return nil
	}))
	if err != context.Canceled {
		t.Fatalf("interrupted run error = %v, want context canceled", err)
	}
	run, items := loadBenchmarkRun(t, root, manifest.ID)
	if run.Status != "interrupted" {
		t.Fatalf("run status = %s, want interrupted", run.Status)
	}
	for i := 0; i < 4; i++ {
		if items[i].Status != "completed" {
			t.Fatalf("item %d status = %s, want completed", i+1, items[i].Status)
		}
	}
	if items[4].Status != "interrupted" {
		t.Fatalf("item 5 status = %s, want interrupted", items[4].Status)
	}
	assertNoActiveExecutionLeases(t, root)
	usageBefore, _ := suiteRecordCounts(t, root)

	resumeProviders := map[string]suiteProviders{}
	resumed, err := RunSuite(ctx, root, manifest, resumeSuiteOptions(resumeProviders, nil))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.RunStatus != "completed" || len(resumed.Report.Results) != 10 {
		t.Fatalf("resumed run = status %s, results %d", resumed.RunStatus, len(resumed.Report.Results))
	}
	for i := 0; i < 4; i++ {
		itemID := manifest.Items[i].ID
		if resumeProviders[itemID].calls() != 0 || resumed.TaskRuns[i].Action != "reused" {
			t.Fatalf("completed item %s calls/action = %d/%s", itemID, resumeProviders[itemID].calls(), resumed.TaskRuns[i].Action)
		}
	}
	usageAfter, _ := suiteRecordCounts(t, root)
	if usageAfter-usageBefore != 18 {
		t.Fatalf("new usage records = %d, want 18 for six unfinished items", usageAfter-usageBefore)
	}

	cleanRoot := t.TempDir()
	cleanProviders := map[string]suiteProviders{}
	clean, err := RunSuite(ctx, cleanRoot, manifest, resumeSuiteOptions(cleanProviders, nil))
	if err != nil {
		t.Fatal(err)
	}
	resumedReport, err := os.ReadFile(resumed.Report.Path)
	if err != nil {
		t.Fatal(err)
	}
	cleanReport, err := os.ReadFile(clean.Report.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(resumedReport, cleanReport) {
		t.Fatalf("resumed report differs from uninterrupted report\nresumed:\n%s\nclean:\n%s", resumedReport, cleanReport)
	}
}

func TestRunSuiteRerunsOnlyStaleAcceptanceEvidence(t *testing.T) {
	ctx := context.Background()
	repo := initBenchmarkRepo(t)
	base, err := gitrepo.CurrentCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	manifest := resumeManifest("resume-stale-acceptance", repo, base, 1, true)
	root := t.TempDir()
	firstProviders := map[string]suiteProviders{}
	first, err := RunSuite(ctx, root, manifest, resumeSuiteOptions(firstProviders, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Report.Results[0].Evidence.AcceptanceValid {
		t.Fatalf("first acceptance evidence = %#v", first.Report.Results[0].Evidence)
	}
	acceptancePath := filepath.Join(root, ".midgard", "artifacts", manifest.Items[0].TaskID, filepath.FromSlash(first.Report.Results[0].Evidence.AcceptancePath))
	if err := os.WriteFile(acceptancePath, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runsBefore := acceptanceRunCount(t, root)

	resumeProviders := map[string]suiteProviders{}
	resumed, err := RunSuite(ctx, root, manifest, resumeSuiteOptions(resumeProviders, nil))
	if err != nil {
		t.Fatal(err)
	}
	if resumeProviders[manifest.Items[0].ID].calls() != 0 || resumed.TaskRuns[0].Action != "resumed" {
		t.Fatalf("stale acceptance calls/action = %d/%s", resumeProviders[manifest.Items[0].ID].calls(), resumed.TaskRuns[0].Action)
	}
	if acceptanceRunCount(t, root) != runsBefore+1 || !resumed.Report.Results[0].Evidence.AcceptanceValid {
		t.Fatalf("acceptance was not refreshed: %#v", resumed.Report.Results[0].Evidence)
	}

	reuseProviders := map[string]suiteProviders{}
	_, err = RunSuite(ctx, root, manifest, resumeSuiteOptions(reuseProviders, nil))
	if err != nil {
		t.Fatal(err)
	}
	if acceptanceRunCount(t, root) != runsBefore+1 || reuseProviders[manifest.Items[0].ID].calls() != 0 {
		t.Fatal("valid acceptance evidence was rerun")
	}
}

func TestRunSuiteRejectsDriftBeforeProviderCalls(t *testing.T) {
	ctx := context.Background()
	repo := initBenchmarkRepo(t)
	base, err := gitrepo.CurrentCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("manifest", func(t *testing.T) {
		manifest := resumeManifest("resume-manifest-drift", repo, base, 1, false)
		root := t.TempDir()
		if _, err := RunSuite(ctx, root, manifest, resumeSuiteOptions(map[string]suiteProviders{}, nil)); err != nil {
			t.Fatal(err)
		}
		manifest.Items[0].Objective = "a changed objective"
		providers := map[string]suiteProviders{}
		_, err := RunSuite(ctx, root, manifest, resumeSuiteOptions(providers, nil))
		if err == nil || !strings.Contains(err.Error(), "manifest drift") || providers[manifest.Items[0].ID].calls() != 0 {
			t.Fatalf("manifest drift error/calls = %v/%d", err, providers[manifest.Items[0].ID].calls())
		}
	})

	t.Run("execution", func(t *testing.T) {
		manifest := resumeManifest("resume-execution-drift", repo, base, 1, false)
		root := t.TempDir()
		if _, err := RunSuite(ctx, root, manifest, resumeSuiteOptions(map[string]suiteProviders{}, nil)); err != nil {
			t.Fatal(err)
		}
		providers := map[string]suiteProviders{}
		opts := resumeSuiteOptions(providers, nil)
		originalFactory := opts.ProviderFactory
		opts.ProviderFactory = func(item Item) (midgardtask.RoleProviders, string, cost.Pricing, error) {
			roles, _, pricing, err := originalFactory(item)
			return roles, "different-model", pricing, err
		}
		_, err := RunSuite(ctx, root, manifest, opts)
		if err == nil || !strings.Contains(err.Error(), "provider/model/options drift") || providers[manifest.Items[0].ID].calls() != 0 {
			t.Fatalf("execution drift error/calls = %v/%d", err, providers[manifest.Items[0].ID].calls())
		}
	})

	t.Run("provider options", func(t *testing.T) {
		manifest := resumeManifest("resume-provider-options-drift", repo, base, 1, false)
		root := t.TempDir()
		initial := resumeSuiteOptions(map[string]suiteProviders{}, nil)
		initial.ProviderOptions = "reasoning_effort=high"
		if _, err := RunSuite(ctx, root, manifest, initial); err != nil {
			t.Fatal(err)
		}
		providers := map[string]suiteProviders{}
		changed := resumeSuiteOptions(providers, nil)
		changed.ProviderOptions = "reasoning_effort=max"
		_, err := RunSuite(ctx, root, manifest, changed)
		if err == nil || !strings.Contains(err.Error(), "provider/model/options drift") || providers[manifest.Items[0].ID].calls() != 0 {
			t.Fatalf("provider options drift error/calls = %v/%d", err, providers[manifest.Items[0].ID].calls())
		}
	})

	t.Run("base commit", func(t *testing.T) {
		manifest := resumeManifest("resume-base-drift", repo, "origin/main", 1, false)
		root := t.TempDir()
		if _, err := RunSuite(ctx, root, manifest, resumeSuiteOptions(map[string]suiteProviders{}, nil)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "NEXT.md"), []byte("next\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := gitrepo.Run(ctx, repo, "add", "NEXT.md"); err != nil {
			t.Fatal(err)
		}
		if _, err := gitrepo.Run(ctx, repo, "commit", "-m", "advance base"); err != nil {
			t.Fatal(err)
		}
		providers := map[string]suiteProviders{}
		_, err := RunSuite(ctx, root, manifest, resumeSuiteOptions(providers, nil))
		if err == nil || !strings.Contains(err.Error(), "base commit drift") || providers[manifest.Items[0].ID].calls() != 0 {
			t.Fatalf("base drift error/calls = %v/%d", err, providers[manifest.Items[0].ID].calls())
		}
	})
}

func TestRunSuiteClearsItemErrorAfterSuccessfulResume(t *testing.T) {
	ctx := context.Background()
	repo := initBenchmarkRepo(t)
	base, err := gitrepo.CurrentCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	manifest := resumeManifest("resume-error", repo, base, 1, false)
	root := t.TempDir()
	bad := fake.Response{Text: "not a Midgard stream", Usage: model.Usage{InputTokens: 10, OutputTokens: 5}}
	first, err := RunSuite(ctx, root, manifest, SuiteOptions{ProviderFactory: func(Item) (midgardtask.RoleProviders, string, cost.Pricing, error) {
		return midgardtask.RoleProviders{
			model.RolePlanner: fake.New(bad, bad, bad), model.RoleImplementer: fake.New(), model.RoleReviewer: fake.New(),
		}, "fake-model", cost.Pricing{ID: "test", InputUSDPerMillion: 1, OutputUSDPerMillion: 1}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if first.RunStatus != "incomplete" || first.Report.Results[0].Evidence.RunError == "" {
		t.Fatalf("failed run = %#v", first)
	}

	resumed, err := RunSuite(ctx, root, manifest, resumeSuiteOptions(map[string]suiteProviders{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.RunStatus != "completed" || resumed.Report.Results[0].Score != ScorePass || resumed.Report.Results[0].Evidence.RunError != "" {
		t.Fatalf("resumed result = %#v", resumed)
	}
	_, items := loadBenchmarkRun(t, root, manifest.ID)
	if items[0].Error != "" || items[0].ErrorClass != "" || items[0].Status != "completed" {
		t.Fatalf("durable item error was not cleared: %#v", items[0])
	}
	report, err := os.ReadFile(resumed.Report.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(report), "run_error:") {
		t.Fatalf("resumed report retained old error:\n%s", report)
	}
}

func resumeManifest(id, repo, base string, itemCount int, acceptance bool) Manifest {
	items := make([]Item, 0, itemCount)
	for i := 1; i <= itemCount; i++ {
		item := Item{
			ID: fmt.Sprintf("item-%02d", i), TaskID: fmt.Sprintf("task_%s_%02d", strings.ReplaceAll(id, "-", "_"), i),
			Objective: "change README heading", RepoIDs: []string{"repo1"}, ExpectedTouchedFiles: []string{"README.md"},
		}
		if acceptance {
			item.AcceptanceChecks = []AcceptanceCheck{{ID: "diff-check", RepoID: "repo1", Command: "git diff --check"}}
		}
		items = append(items, item)
	}
	return Manifest{ID: id, Repos: []RepoSource{{ID: "repo1", Path: repo, CheckoutRef: base}}, Items: items}
}

func resumeSuiteOptions(providers map[string]suiteProviders, plannerOverride func(Item) model.Provider) SuiteOptions {
	return SuiteOptions{ProviderFactory: func(item Item) (midgardtask.RoleProviders, string, cost.Pricing, error) {
		planner := model.Provider(fake.New(fake.Response{
			Text:  "@report plan.mdx\n# Plan\n\nReady.\n@result status:ready artifact:plan.mdx checks:none\n",
			Usage: model.Usage{InputTokens: 10, OutputTokens: 5},
		}))
		if plannerOverride != nil {
			if override := plannerOverride(item); override != nil {
				planner = override
			}
		}
		implementation := strings.Join([]string{
			"@report implementation.mdx",
			"# Implementation",
			"@payload begin type:patch path:patches/readme.diff",
			"--- a/README.md",
			"+++ b/README.md",
			"@@ -1 +1 @@",
			"-# benchmark fixture",
			"+# durable benchmark",
			"@payload end",
			"@edit file:README.md action:modify mode:patch content:artifact:patches/readme.diff reason:benchmark repo:repo1",
			"@result status:ready artifact:implementation.mdx checks:none",
			"",
		}, "\n")
		configured := suiteProviders{
			planner:     planner,
			implementer: fake.New(fake.Response{Text: implementation, Usage: model.Usage{InputTokens: 20, OutputTokens: 10}}),
			reviewer: fake.New(fake.Response{
				Text:  "@report review.mdx\n# Review\n\nApproved.\n@result status:approved artifact:review.mdx findings:none\n",
				Usage: model.Usage{InputTokens: 15, OutputTokens: 5},
			}),
		}
		providers[item.ID] = configured
		return configured.roles(), "fake-model", cost.Pricing{ID: "test", InputUSDPerMillion: 1, OutputUSDPerMillion: 1}, nil
	}}
}

func loadBenchmarkRun(t *testing.T, root, manifestID string) (state.BenchmarkRun, []state.BenchmarkRunItem) {
	t.Helper()
	db, err := state.Open(context.Background(), workbench.NewLayout(root).State)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run, err := db.BenchmarkRunByManifest(context.Background(), manifestID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := db.BenchmarkRunItems(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run, items
}

func suiteRecordCounts(t *testing.T, root string) (usage, events int) {
	t.Helper()
	db, err := state.Open(context.Background(), workbench.NewLayout(root).State)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	return usage, events
}

func acceptanceRunCount(t *testing.T, root string) int {
	t.Helper()
	db, err := state.Open(context.Background(), workbench.NewLayout(root).State)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM benchmark_acceptance_runs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertNoActiveExecutionLeases(t *testing.T, root string) {
	t.Helper()
	db, err := state.Open(context.Background(), workbench.NewLayout(root).State)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var active int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM execution_leases WHERE state = 'active'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("active execution leases after cancellation = %d", active)
	}
}
