package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

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
		var checks stringListFlag
		fs.Var(&checks, "check", "authoritative acceptance command; repeat for multiple checks")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		acceptanceChecks := make([]benchmark.AcceptanceCheck, 0, len(checks))
		for i, commandText := range checks {
			acceptanceChecks = append(acceptanceChecks, benchmark.AcceptanceCheck{
				ID: fmt.Sprintf("check-%d", i+1), RepoID: "repo1", Command: commandText,
			})
		}
		result, err := benchmark.ImportPR(ctx, benchmark.ImportPROptions{
			Repo:          *repo,
			PullNumber:    *prNumber,
			OutPath:       *outPath,
			ReferencePath: *referencePath,
			CloneURL:      *cloneURL,
			APIBaseURL:    *apiBaseURL,
			Token:         githubToken(),
			Checks:        acceptanceChecks,
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
	case "run", "report", "verify":
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
		acceptanceTimeout := fs.Duration("acceptance-timeout", 5*time.Minute, "default timeout for each authoritative acceptance check")
		acceptanceMaxStdout := fs.Int64("acceptance-max-stdout", 64<<10, "stdout byte cap for each acceptance check")
		acceptanceMaxStderr := fs.Int64("acceptance-max-stderr", 64<<10, "stderr byte cap for each acceptance check")
		resetTasks := fs.Bool("reset", false, "discard durable benchmark state and start a new run")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *manifestPath == "" {
			return fmt.Errorf("manifest is required")
		}
		if args[0] == "run" && *providerName != "" {
			return runBenchmarkSuiteWithOptions(ctx, stdout, *root, *manifestPath, *providerName, *plannerStream, *implementerStream, *reviewerStream, *modelID, *deepseekReasoningEffort, *maxOutputTokens, *maxReviewCycles, *maxSourceEditRepairs, *acceptanceTimeout, *acceptanceMaxStdout, *acceptanceMaxStderr, *resetTasks)
		}
		if args[0] != "run" && *providerName != "" {
			return fmt.Errorf("benchmark %s does not execute providers; use benchmark run --provider %s", args[0], *providerName)
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		manifest, err := benchmark.LoadManifest(*manifestPath)
		if err != nil {
			return err
		}
		var report benchmark.Report
		if args[0] == "verify" {
			report, err = benchmark.Verify(ctx, start, manifest, benchmark.AcceptanceOptions{
				DefaultTimeout: *acceptanceTimeout, MaxStdoutBytes: *acceptanceMaxStdout, MaxStderrBytes: *acceptanceMaxStderr,
			})
		} else {
			report, err = benchmark.Run(ctx, start, manifest)
		}
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
	acceptanceTimeout := fs.Duration("acceptance-timeout", 5*time.Minute, "default timeout for each authoritative acceptance check")
	acceptanceMaxStdout := fs.Int64("acceptance-max-stdout", 64<<10, "stdout byte cap for each acceptance check")
	acceptanceMaxStderr := fs.Int64("acceptance-max-stderr", 64<<10, "stderr byte cap for each acceptance check")
	resetTasks := fs.Bool("reset", false, "discard durable benchmark state and start a new run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *manifestPath == "" {
		return fmt.Errorf("manifest is required")
	}
	return runBenchmarkSuiteWithOptions(ctx, stdout, *root, *manifestPath, *providerName, *plannerStream, *implementerStream, *reviewerStream, *modelID, *deepseekReasoningEffort, *maxOutputTokens, *maxReviewCycles, *maxSourceEditRepairs, *acceptanceTimeout, *acceptanceMaxStdout, *acceptanceMaxStderr, *resetTasks)
}

func runBenchmarkSuiteWithOptions(ctx context.Context, stdout io.Writer, root, manifestPath, providerName, plannerStream, implementerStream, reviewerStream, modelID, deepseekReasoningEffort string, maxOutputTokens, maxReviewCycles, maxSourceEditRepairs int, acceptanceTimeout time.Duration, acceptanceMaxStdout, acceptanceMaxStderr int64, resetTasks bool) error {
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
		ProviderOptions:      benchmarkProviderOptions(providerName, deepseekReasoningEffort),
		Budget:               streamBudget(maxOutputTokens),
		MaxReviewCycles:      maxReviewCycles,
		MaxSourceEditRepairs: maxSourceEditRepairs,
		Acceptance: benchmark.AcceptanceOptions{
			DefaultTimeout: acceptanceTimeout, MaxStdoutBytes: acceptanceMaxStdout, MaxStderrBytes: acceptanceMaxStderr,
		},
		ResetTasks: resetTasks,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "benchmark: %s\n", result.Report.ManifestID)
	fmt.Fprintf(stdout, "run: %s status:%s\n", result.RunID, result.RunStatus)
	fmt.Fprintf(stdout, "report: %s\n", result.Report.Path)
	for _, repo := range result.PreparedRepos {
		fmt.Fprintf(stdout, "repo: %s path:%s checkout_ref:%s start_commit:%s\n", repo.ID, repo.Path, repo.CheckoutRef, repo.StartCommit)
	}
	for _, taskRun := range result.TaskRuns {
		fmt.Fprintf(stdout, "task: %s item:%s state:%s action:%s cost:$%.6f\n", taskRun.TaskID, taskRun.ItemID, taskRun.State, firstCLIValue(taskRun.Action, "unknown"), taskRun.CostUSD)
		if taskRun.Error != "" {
			fmt.Fprintf(stdout, "task_error: %s item:%s class:%s error:%s\n", taskRun.TaskID, taskRun.ItemID, firstCLIValue(taskRun.ErrorClass, "unknown"), strings.ReplaceAll(taskRun.Error, "\n", " "))
		}
	}
	for _, scored := range result.Report.Results {
		fmt.Fprintf(stdout, "item: %s score:%s task:%s patch_bytes:%d acceptance:%s cost:$%.6f\n", scored.ItemID, scored.Score, scored.Evidence.TaskID, scored.Evidence.PatchBytes, firstCLIValue(scored.Evidence.AcceptanceStatus, "none"), scored.Evidence.CostUSD)
	}
	return nil
}

func benchmarkProviderOptions(providerName, deepseekReasoningEffort string) string {
	switch providerName {
	case "deepseek":
		return "reasoning_effort=" + resolvedDeepSeekReasoningEffort(deepseekReasoningEffort)
	case "codex":
		return "base_url=" + strings.TrimSpace(os.Getenv("MIDGARD_CODEX_BASE_URL"))
	default:
		return ""
	}
}

func githubToken() string {
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token
	}
	return os.Getenv("GH_TOKEN")
}

type stringListFlag []string

func (values *stringListFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("value cannot be empty")
	}
	*values = append(*values, value)
	return nil
}

func printBenchmarkUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midgard benchmark <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  import-pr import a merged GitHub PR into a benchmark manifest")
	fmt.Fprintln(w, "  run     execute benchmark items with --provider, then score/report")
	fmt.Fprintln(w, "  report  regenerate benchmark report without executing providers")
	fmt.Fprintln(w, "  verify  rerun authoritative acceptance checks, then regenerate the report")
	fmt.Fprintln(w, "  suite   alias for run --provider fake unless --provider is supplied")
}
