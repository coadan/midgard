package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"midgard/internal/cost"
	"midgard/internal/model"
	codexprovider "midgard/internal/model/providers/codex"
	"midgard/internal/model/providers/deepseek"
	"midgard/internal/model/providers/fake"
	"midgard/internal/state"
	"midgard/internal/stream"
	midgardtask "midgard/internal/task"
	"midgard/internal/workbench"
)

func runTask(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printTaskUsage(stdout)
		return nil
	}

	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("midgard task create", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		id := fs.String("id", "", "task id")
		objective := fs.String("objective", "", "task objective")
		repos := fs.String("repos", "", "comma-separated repo ids")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		result, err := midgardtask.Create(ctx, start, midgardtask.CreateOptions{
			ID:        *id,
			Objective: *objective,
			RepoIDs:   splitCSV(*repos),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "task: %s\n", result.Task.ID)
		fmt.Fprintf(stdout, "state: %s\n", result.Task.State)
		fmt.Fprintf(stdout, "report: %s\n", result.ReportPath)
		for _, wt := range result.Worktrees {
			fmt.Fprintf(stdout, "repo: %s\n", wt.RepoID)
			fmt.Fprintf(stdout, "worktree: %s\n", wt.Path)
			fmt.Fprintf(stdout, "dirty: %t\n", wt.Dirty)
		}
		return nil
	case "status":
		fs := flag.NewFlagSet("midgard task status", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		id := fs.String("task", "", "task id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		status, err := midgardtask.Status(ctx, start, *id)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "task: %s\n", status.Task.ID)
		fmt.Fprintf(stdout, "state: %s\n", status.Task.State)
		fmt.Fprintf(stdout, "objective: %s\n", status.Task.Objective)
		for _, wt := range status.Worktrees {
			fmt.Fprintf(stdout, "repo: %s\n", wt.RepoID)
			fmt.Fprintf(stdout, "worktree: %s\n", wt.Path)
			fmt.Fprintf(stdout, "dirty: %t\n", wt.Dirty)
		}
		if status.ForgeGates {
			if status.ForgeReady {
				fmt.Fprintln(stdout, "forge_readiness: ready")
			} else {
				fmt.Fprintf(stdout, "forge_readiness: blocked %s\n", strings.Join(status.ForgeBlockers, ","))
			}
		} else if len(status.ForgeWarnings) > 0 {
			fmt.Fprintf(stdout, "forge_readiness: disabled warnings:%s\n", strings.Join(status.ForgeWarnings, ","))
		}
		fmt.Fprintf(stdout, "next: %s\n", status.NextAction)
		return nil
	case "diff":
		fs := flag.NewFlagSet("midgard task diff", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		id := fs.String("task", "", "task id")
		repoID := fs.String("repo", "", "repo id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		diff, err := midgardtask.Diff(ctx, start, *id, *repoID)
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, diff)
		return nil
	case "stream":
		fs := flag.NewFlagSet("midgard task stream", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		id := fs.String("task", "", "task id")
		url := fs.String("url", "", "server base URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("task id is required")
		}
		if *url != "" {
			resp, err := http.Get(strings.TrimRight(*url, "/") + "/api/events/stream?task=" + *id)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			_, err = io.Copy(stdout, resp.Body)
			return err
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		wbStatus, err := workbench.Status(start)
		if err != nil {
			return err
		}
		db, err := state.Open(ctx, filepath.Join(wbStatus.Root, ".midgard", "state.sqlite"))
		if err != nil {
			return err
		}
		defer db.Close()
		events, err := db.EventsForTask(ctx, *id)
		if err != nil {
			return err
		}
		for _, event := range events {
			fmt.Fprintf(stdout, "event: %d %s %s\n", event.ID, event.Type, event.Payload)
		}
		return nil
	case "feedback":
		fs := flag.NewFlagSet("midgard task feedback", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		id := fs.String("task", "", "task id")
		status := fs.String("status", "changes-requested", "feedback status: changes-requested or note")
		source := fs.String("source", "external", "feedback source")
		message := fs.String("message", "", "feedback message")
		messageFile := fs.String("message-file", "", "path to feedback message file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("task id is required")
		}
		text := *message
		if *messageFile != "" {
			data, err := os.ReadFile(*messageFile)
			if err != nil {
				return err
			}
			text = string(data)
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		if err := midgardtask.RecordFeedback(ctx, start, *id, midgardtask.FeedbackInput{
			Status:  *status,
			Source:  *source,
			Message: text,
		}); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "task: %s\n", *id)
		fmt.Fprintf(stdout, "feedback: %s\n", *status)
		return nil
	case "pr":
		return runTaskPR(ctx, args[1:], stdout, stderr)
	case "step":
		fs := flag.NewFlagSet("midgard task step", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		id := fs.String("task", "", "task id")
		roleName := fs.String("role", "planner", "planner, implementer, or reviewer")
		providerName := fs.String("provider", "fake", "fake, deepseek, or codex")
		fakeStream := fs.String("fake-stream", "", "fake provider stream file")
		modelID := fs.String("model", "", "model id")
		deepseekReasoningEffort := fs.String("deepseek-reasoning-effort", "", "DeepSeek reasoning effort: high or max")
		maxOutputTokens := fs.Int("max-output-tokens", 4096, "provider max output tokens")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("task id is required")
		}
		role, err := parseRole(*roleName)
		if err != nil {
			return err
		}
		provider, err := roleProviderWithOptions(*providerName, *fakeStream, providerOptions{DeepSeekReasoningEffort: *deepseekReasoningEffort})
		if err != nil {
			return err
		}
		resolvedModelID := resolveModelID(*modelID, *providerName)
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		run, err := midgardtask.RunRole(ctx, start, *id, role, midgardtask.RunnerOptions{
			ModelID: resolvedModelID,
			Providers: midgardtask.RoleProviders{
				role: provider,
			},
			Budget:  streamBudget(*maxOutputTokens),
			Pricing: pricingForProvider(*providerName, resolvedModelID),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "role: %s\n", run.Role)
		fmt.Fprintf(stdout, "status: %s\n", run.Status)
		fmt.Fprintf(stdout, "artifact: %s\n", run.Artifact)
		fmt.Fprintf(stdout, "attempts: %d\n", run.Attempts)
		fmt.Fprintf(stdout, "usage: in=%d out=%d\n", run.InputTokens, run.OutputTokens)
		return nil
	case "run":
		fs := flag.NewFlagSet("midgard task run", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		id := fs.String("task", "", "task id")
		providerName := fs.String("provider", "fake", "fake, deepseek, or codex")
		plannerStream := fs.String("planner-stream", "", "fake planner stream file")
		implementerStream := fs.String("implementer-stream", "", "fake implementer stream file")
		reviewerStream := fs.String("reviewer-stream", "", "fake reviewer stream file")
		modelID := fs.String("model", "", "model id")
		deepseekReasoningEffort := fs.String("deepseek-reasoning-effort", "", "DeepSeek reasoning effort: high or max")
		maxOutputTokens := fs.Int("max-output-tokens", 4096, "provider max output tokens")
		maxReviewCycles := fs.Int("max-review-cycles", 4, "maximum implement/review cycles")
		maxSourceEditRepairs := fs.Int("max-source-edit-repairs", 2, "maximum source edit apply repair attempts")
		maxCommandTurns := fs.Int("max-command-turns", 16, "maximum command continuation turns per role")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("task id is required")
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		providers, err := loopProvidersWithOptions(*providerName, map[model.Role]string{
			model.RolePlanner:     *plannerStream,
			model.RoleImplementer: *implementerStream,
			model.RoleReviewer:    *reviewerStream,
		}, providerOptions{DeepSeekReasoningEffort: *deepseekReasoningEffort})
		if err != nil {
			return err
		}
		resolvedModelID := resolveModelID(*modelID, *providerName)
		result, err := midgardtask.RunLoop(ctx, start, *id, midgardtask.RunnerOptions{
			ModelID:              resolvedModelID,
			Providers:            providers,
			Budget:               streamBudget(*maxOutputTokens),
			Pricing:              pricingForProvider(*providerName, resolvedModelID),
			MaxReviewCycles:      *maxReviewCycles,
			MaxSourceEditRepairs: *maxSourceEditRepairs,
			MaxCommandTurns:      *maxCommandTurns,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "task: %s\n", result.TaskID)
		fmt.Fprintf(stdout, "state: %s\n", result.State)
		fmt.Fprintf(stdout, "patch: %s\n", result.PatchPath)
		printRunCost(stdout, result.CostUSD, result.CostCaveats)
		for _, run := range result.RoleRuns {
			fmt.Fprintf(stdout, "role: %s status:%s artifact:%s attempts:%d usage:in=%d,out=%d\n", run.Role, run.Status, run.Artifact, run.Attempts, run.InputTokens, run.OutputTokens)
		}
		return nil
	case "cleanup":
		fs := flag.NewFlagSet("midgard task cleanup", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		id := fs.String("task", "", "task id")
		worktrees := fs.Bool("worktrees", true, "remove task worktrees")
		artifacts := fs.Bool("artifacts", true, "remove task artifacts")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("task id is required")
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		result, err := midgardtask.Cleanup(ctx, start, *id, midgardtask.CleanupOptions{Worktrees: *worktrees, Artifacts: *artifacts})
		if err != nil {
			return err
		}
		for _, path := range result.RemovedWorktrees {
			fmt.Fprintf(stdout, "removed_worktree: %s\n", path)
		}
		if result.RemovedArtifacts != "" {
			fmt.Fprintf(stdout, "removed_artifacts: %s\n", result.RemovedArtifacts)
		}
		return nil
	default:
		return fmt.Errorf("unknown task command %q", args[0])
	}
}

func printTaskUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midgard task <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  create  create a task worktree")
	fmt.Fprintln(w, "  status  show task state")
	fmt.Fprintln(w, "  diff    show task diff")
	fmt.Fprintln(w, "  stream  stream task events")
	fmt.Fprintln(w, "  pr      link and inspect task pull requests")
	fmt.Fprintln(w, "  step    advance one role")
	fmt.Fprintln(w, "  run     run planner, implementer, and reviewer")
	fmt.Fprintln(w, "  cleanup remove task runtime files")
}

func rootOrCWD(root string) (string, error) {
	if root != "" {
		return root, nil
	}
	return os.Getwd()
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseRole(value string) (model.Role, error) {
	switch value {
	case model.RolePlanner.String():
		return model.RolePlanner, nil
	case model.RoleImplementer.String():
		return model.RoleImplementer, nil
	case model.RoleReviewer.String():
		return model.RoleReviewer, nil
	default:
		return "", fmt.Errorf("unknown role %q", value)
	}
}

type providerOptions struct {
	DeepSeekReasoningEffort string
}

func roleProvider(providerName, fakeStream string) (model.Provider, error) {
	return roleProviderWithOptions(providerName, fakeStream, providerOptions{})
}

func roleProviderWithOptions(providerName, fakeStream string, opts providerOptions) (model.Provider, error) {
	switch providerName {
	case "fake":
		if fakeStream == "" {
			return nil, fmt.Errorf("fake-stream is required for fake provider")
		}
		data, err := os.ReadFile(fakeStream)
		if err != nil {
			return nil, err
		}
		return fake.New(fake.Response{Text: string(data), Usage: model.Usage{Caveat: "fake provider"}}), nil
	case "deepseek":
		key := os.Getenv("DEEPSEEK_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("DEEPSEEK_API_KEY is required")
		}
		client := deepseek.New(key)
		client.ReasoningEffort = resolvedDeepSeekReasoningEffort(opts.DeepSeekReasoningEffort)
		return client, nil
	case "codex":
		provider, err := codexprovider.NewFromLocalAuth()
		if err != nil {
			return nil, err
		}
		if baseURL := strings.TrimSpace(os.Getenv("MIDGARD_CODEX_BASE_URL")); baseURL != "" {
			provider.BaseURL = baseURL
		}
		return provider, nil
	default:
		return nil, fmt.Errorf("unknown provider %q", providerName)
	}
}

func resolvedDeepSeekReasoningEffort(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(deepseek.ReasoningEffortEnvName))
}

func loopProviders(providerName string, fakeStreams map[model.Role]string) (midgardtask.RoleProviders, error) {
	return loopProvidersWithOptions(providerName, fakeStreams, providerOptions{})
}

func loopProvidersWithOptions(providerName string, fakeStreams map[model.Role]string, opts providerOptions) (midgardtask.RoleProviders, error) {
	providers := midgardtask.RoleProviders{}
	for _, role := range []model.Role{model.RolePlanner, model.RoleImplementer, model.RoleReviewer} {
		provider, err := roleProviderWithOptions(providerName, fakeStreams[role], opts)
		if err != nil {
			return nil, fmt.Errorf("%s provider: %w", role, err)
		}
		providers[role] = provider
	}
	return providers, nil
}

func resolveModelID(modelID, providerName string) string {
	if strings.TrimSpace(modelID) != "" {
		return modelID
	}
	switch providerName {
	case "deepseek":
		return deepseek.DefaultModel
	case "codex":
		if modelID := strings.TrimSpace(os.Getenv("MIDGARD_CODEX_MODEL")); modelID != "" {
			return modelID
		}
		if modelID, err := codexprovider.ConfiguredModel(); err == nil && modelID != "" {
			return modelID
		}
		return codexprovider.DefaultModel
	case "fake":
		return "fake-model"
	default:
		return modelID
	}
}

func pricingForProvider(providerName, modelID string) cost.Pricing {
	switch providerName {
	case "deepseek":
		switch modelID {
		case "deepseek-v4-pro":
			return cost.Pricing{
				ID:                  "deepseek-v4-pro-2026-07-07-cache-miss",
				ProviderID:          "deepseek",
				ModelID:             modelID,
				InputUSDPerMillion:  0.435,
				OutputUSDPerMillion: 0.87,
				Currency:            "USD",
				Source:              "https://api-docs.deepseek.com/quick_start/pricing",
			}
		case "deepseek-v4-flash":
			return cost.Pricing{
				ID:                  "deepseek-v4-flash-2026-07-07-cache-miss",
				ProviderID:          "deepseek",
				ModelID:             modelID,
				InputUSDPerMillion:  0.14,
				OutputUSDPerMillion: 0.28,
				Currency:            "USD",
				Source:              "https://api-docs.deepseek.com/quick_start/pricing",
			}
		}
	case "fake":
		return cost.Pricing{
			ID:         "fake-zero",
			ProviderID: "fake",
			ModelID:    modelID,
			Currency:   "USD",
			Source:     "synthetic test provider",
		}
	}
	return cost.Pricing{
		ID:                   "manual",
		ProviderID:           providerName,
		ModelID:              modelID,
		Currency:             "USD",
		MissingPricingCaveat: "pricing not configured; usage is recorded but cost is unknown",
	}
}

func printRunCost(w io.Writer, amount float64, caveats []string) {
	if len(caveats) > 0 {
		fmt.Fprintf(w, "cost: unknown (%s)\n", strings.Join(caveats, "; "))
		return
	}
	fmt.Fprintf(w, "cost: $%.6f\n", amount)
}

func streamBudget(maxOutputTokens int) stream.Budget {
	budget := stream.DefaultBudget()
	if maxOutputTokens > 0 {
		budget.ProviderMaxTokens = maxOutputTokens
	}
	return budget
}
