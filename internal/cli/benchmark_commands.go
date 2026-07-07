package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"midgard/internal/benchmark"
	"midgard/internal/cost"
	"midgard/internal/model"
	midgardtask "midgard/internal/task"
)

func runBenchmark(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printBenchmarkUsage(stdout)
		return nil
	}
	switch args[0] {
	case "import-pr":
		fs := flag.NewFlagSet("midgard benchmark import-pr", flag.ContinueOnError)
		fs.SetOutput(stderr)
		repo := fs.String("repo", "", "GitHub repository URL or owner/name")
		prNumber := fs.Int("pr", 0, "GitHub pull request number")
		outPath := fs.String("out", "", "output benchmark manifest path")
		referencePath := fs.String("reference-patch", "", "output reference patch path")
		cloneURL := fs.String("clone-url", "", "override clone URL for generating the reference patch")
		apiBaseURL := fs.String("github-api-url", "", "GitHub API base URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		result, err := benchmark.ImportPR(ctx, benchmark.ImportPROptions{
			Repo:          *repo,
			PullNumber:    *prNumber,
			OutPath:       *outPath,
			ReferencePath: *referencePath,
			CloneURL:      *cloneURL,
			APIBaseURL:    *apiBaseURL,
			Token:         githubToken(),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "benchmark: %s\n", result.Manifest.ID)
		fmt.Fprintf(stdout, "manifest: %s\n", result.ManifestPath)
		fmt.Fprintf(stdout, "reference_patch: %s\n", result.ReferencePatchPath)
		for _, item := range result.Manifest.Items {
			fmt.Fprintf(stdout, "item: %s task:%s expected_files:%d\n", item.ID, item.TaskID, len(item.ExpectedTouchedFiles))
		}
		return nil
	case "suite":
		return runBenchmarkSuite(ctx, args[1:], stdout, stderr, "fake")
	case "run", "report":
		fs := flag.NewFlagSet("midgard benchmark "+args[0], flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		manifestPath := fs.String("manifest", "", "benchmark manifest path")
		providerName := fs.String("provider", "", "execute benchmark items with provider before scoring")
		plannerStream := fs.String("planner-stream", "", "fake planner stream file")
		implementerStream := fs.String("implementer-stream", "", "fake implementer stream file")
		reviewerStream := fs.String("reviewer-stream", "", "fake reviewer stream file")
		modelID := fs.String("model", "", "model id")
		deepseekReasoningEffort := fs.String("deepseek-reasoning-effort", "", "DeepSeek reasoning effort: high or max")
		maxOutputTokens := fs.Int("max-output-tokens", 4096, "provider max output tokens")
		maxReviewCycles := fs.Int("max-review-cycles", 2, "maximum implement/review cycles")
		maxSourceEditRepairs := fs.Int("max-source-edit-repairs", 2, "maximum source edit apply repairs")
		resetTasks := fs.Bool("reset", true, "reset existing benchmark tasks before running")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *manifestPath == "" {
			return fmt.Errorf("manifest is required")
		}
		if args[0] == "run" && *providerName != "" {
			return runBenchmarkSuiteWithOptions(ctx, stdout, *root, *manifestPath, *providerName, *plannerStream, *implementerStream, *reviewerStream, *modelID, *deepseekReasoningEffort, *maxOutputTokens, *maxReviewCycles, *maxSourceEditRepairs, *resetTasks)
		}
		if args[0] == "report" && *providerName != "" {
			return fmt.Errorf("benchmark report does not execute providers; use benchmark run --provider %s", *providerName)
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		manifest, err := benchmark.LoadManifest(*manifestPath)
		if err != nil {
			return err
		}
		report, err := benchmark.Run(ctx, start, manifest)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "benchmark: %s\n", report.ManifestID)
		fmt.Fprintf(stdout, "report: %s\n", report.Path)
		for _, result := range report.Results {
			fmt.Fprintf(stdout, "item: %s score:%s task:%s\n", result.ItemID, result.Score, result.Evidence.TaskID)
		}
		return nil
	default:
		return fmt.Errorf("unknown benchmark command %q", args[0])
	}
}

func runBenchmarkSuite(ctx context.Context, args []string, stdout, stderr io.Writer, defaultProvider string) error {
	fs := flag.NewFlagSet("midgard benchmark suite", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "benchmark workbench root")
	manifestPath := fs.String("manifest", "", "benchmark manifest path")
	providerName := fs.String("provider", defaultProvider, "fake, deepseek, or codex")
	plannerStream := fs.String("planner-stream", "", "fake planner stream file")
	implementerStream := fs.String("implementer-stream", "", "fake implementer stream file")
	reviewerStream := fs.String("reviewer-stream", "", "fake reviewer stream file")
	modelID := fs.String("model", "", "model id")
	deepseekReasoningEffort := fs.String("deepseek-reasoning-effort", "", "DeepSeek reasoning effort: high or max")
	maxOutputTokens := fs.Int("max-output-tokens", 4096, "provider max output tokens")
	maxReviewCycles := fs.Int("max-review-cycles", 2, "maximum implement/review cycles")
	maxSourceEditRepairs := fs.Int("max-source-edit-repairs", 2, "maximum source edit apply repairs")
	resetTasks := fs.Bool("reset", true, "reset existing benchmark tasks before running")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *manifestPath == "" {
		return fmt.Errorf("manifest is required")
	}
	return runBenchmarkSuiteWithOptions(ctx, stdout, *root, *manifestPath, *providerName, *plannerStream, *implementerStream, *reviewerStream, *modelID, *deepseekReasoningEffort, *maxOutputTokens, *maxReviewCycles, *maxSourceEditRepairs, *resetTasks)
}

func runBenchmarkSuiteWithOptions(ctx context.Context, stdout io.Writer, root, manifestPath, providerName, plannerStream, implementerStream, reviewerStream, modelID, deepseekReasoningEffort string, maxOutputTokens, maxReviewCycles, maxSourceEditRepairs int, resetTasks bool) error {
	if providerName == "" {
		return fmt.Errorf("provider is required")
	}
	start, err := rootOrCWD(root)
	if err != nil {
		return err
	}
	manifest, err := benchmark.LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	resolvedModelID := resolveModelID(modelID, providerName)
	factory := func(benchmark.Item) (midgardtask.RoleProviders, string, cost.Pricing, error) {
		providers, err := loopProvidersWithOptions(providerName, map[model.Role]string{
			model.RolePlanner:     plannerStream,
			model.RoleImplementer: implementerStream,
			model.RoleReviewer:    reviewerStream,
		}, providerOptions{DeepSeekReasoningEffort: deepseekReasoningEffort})
		return providers, resolvedModelID, pricingForProvider(providerName, resolvedModelID), err
	}
	result, err := benchmark.RunSuite(ctx, start, manifest, benchmark.SuiteOptions{
		ProviderFactory:      factory,
		Budget:               streamBudget(maxOutputTokens),
		MaxReviewCycles:      maxReviewCycles,
		MaxSourceEditRepairs: maxSourceEditRepairs,
		ResetTasks:           resetTasks,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "benchmark: %s\n", result.Report.ManifestID)
	fmt.Fprintf(stdout, "report: %s\n", result.Report.Path)
	for _, repo := range result.PreparedRepos {
		fmt.Fprintf(stdout, "repo: %s path:%s checkout_ref:%s start_commit:%s\n", repo.ID, repo.Path, repo.CheckoutRef, repo.StartCommit)
	}
	for _, taskRun := range result.TaskRuns {
		fmt.Fprintf(stdout, "task: %s item:%s state:%s cost:$%.6f\n", taskRun.TaskID, taskRun.ItemID, taskRun.State, taskRun.CostUSD)
	}
	for _, scored := range result.Report.Results {
		fmt.Fprintf(stdout, "item: %s score:%s task:%s patch_bytes:%d cost:$%.6f\n", scored.ItemID, scored.Score, scored.Evidence.TaskID, scored.Evidence.PatchBytes, scored.Evidence.CostUSD)
	}
	return nil
}

func githubToken() string {
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token
	}
	return os.Getenv("GH_TOKEN")
}

func printBenchmarkUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midgard benchmark <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  import-pr import a merged GitHub PR into a benchmark manifest")
	fmt.Fprintln(w, "  run     execute benchmark items with --provider, then score/report")
	fmt.Fprintln(w, "  report  regenerate benchmark report without executing providers")
	fmt.Fprintln(w, "  suite   alias for run --provider fake unless --provider is supplied")
}
