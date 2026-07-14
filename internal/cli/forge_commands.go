package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	midgardforge "midgard/internal/forge"
	midgardtask "midgard/internal/task"
)

func runForge(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printForgeUsage(stdout)
		return nil
	}
	switch args[0] {
	case "auth":
		if len(args) < 2 || args[1] != "status" {
			return fmt.Errorf("usage: midgard forge auth status [args]")
		}
		fs := flag.NewFlagSet("midgard forge auth status", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root")
		forgeID := fs.String("account", "", "forge account id")
		baseURL := fs.String("base-url", "", "GitHub base URL")
		authProfile := fs.String("auth-profile", "", "local auth profile reference")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		start := ""
		if *root != "" {
			var err error
			start, err = rootOrCWD(*root)
			if err != nil {
				return err
			}
		}
		status, err := midgardforge.GitHubAuthenticationStatus(ctx, midgardforge.AuthStatusOptions{
			Root: start, ForgeID: *forgeID, BaseURL: *baseURL, AuthProfile: *authProfile,
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, "forge: github")
		fmt.Fprintf(stdout, "host: %s\n", status.Host)
		fmt.Fprintf(stdout, "authenticated: %t\n", status.Authenticated)
		fmt.Fprintf(stdout, "source: %s\n", status.Source)
		if status.Account != "" {
			fmt.Fprintf(stdout, "account: %s\n", status.Account)
		}
		if status.RateLimit > 0 {
			fmt.Fprintf(stdout, "rate: %d/%d remaining\n", status.RateRemaining, status.RateLimit)
		}
		return nil
	case "repo":
		if len(args) < 2 {
			printForgeRepoUsage(stdout)
			return nil
		}
		switch args[1] {
		case "link":
			fs := flag.NewFlagSet("midgard forge repo link", flag.ContinueOnError)
			fs.SetOutput(stderr)
			root := fs.String("root", "", "workbench root or search start")
			repoID := fs.String("repo", "", "registered repo id")
			forgeID := fs.String("account", "", "forge account id")
			kind := fs.String("forge", "github", "forge kind")
			remote := fs.String("remote", "", "forge remote, such as owner/name or URL")
			baseURL := fs.String("base-url", "", "forge base URL")
			defaultBranch := fs.String("default-branch", "", "forge default branch")
			authProfile := fs.String("auth-profile", "", "local auth profile reference")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			start, err := rootOrCWD(*root)
			if err != nil {
				return err
			}
			repo, err := midgardforge.LinkRepo(ctx, midgardforge.RepoLinkOptions{
				Root:          start,
				RepoID:        *repoID,
				ForgeID:       *forgeID,
				Kind:          *kind,
				Remote:        *remote,
				BaseURL:       *baseURL,
				DefaultBranch: *defaultBranch,
				AuthProfile:   *authProfile,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "repo: %s\n", repo.RepoID)
			fmt.Fprintf(stdout, "forge: %s\n", repo.ForgeID)
			fmt.Fprintf(stdout, "remote: %s/%s\n", repo.Owner, repo.Name)
			fmt.Fprintf(stdout, "url: %s\n", repo.URL)
			return nil
		default:
			return fmt.Errorf("unknown forge repo command %q", args[1])
		}
	default:
		return fmt.Errorf("unknown forge command %q", args[0])
	}
}

func runTaskPR(ctx context.Context, args []string, stdout, stderr io.Writer) (retErr error) {
	if len(args) == 0 {
		printTaskPRUsage(stdout)
		return nil
	}
	switch args[0] {
	case "link":
		fs := flag.NewFlagSet("midgard task pr link", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		taskID := fs.String("task", "", "task id")
		repoID := fs.String("repo", "", "registered repo id")
		forgeID := fs.String("account", "", "forge account id")
		pr := fs.String("pr", "", "pull request number or URL")
		groupID := fs.String("group", "", "optional PR group id")
		baseBranch := fs.String("base", "", "PR base branch")
		headBranch := fs.String("head", "", "PR head branch")
		headSHA := fs.String("head-sha", "", "PR head SHA")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		execution, err := midgardtask.AcquireExecution(ctx, start, *taskID)
		if err != nil {
			return err
		}
		defer func() {
			if err := execution.Close(); retErr == nil && err != nil {
				retErr = err
			}
		}()
		ctx = execution.Context
		link, err := midgardforge.LinkTaskPR(ctx, midgardforge.TaskPRLinkOptions{
			Root:       start,
			TaskID:     *taskID,
			RepoID:     *repoID,
			ForgeID:    *forgeID,
			PR:         *pr,
			GroupID:    *groupID,
			BaseBranch: *baseBranch,
			HeadBranch: *headBranch,
			HeadSHA:    *headSHA,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "task: %s\n", link.TaskID)
		fmt.Fprintf(stdout, "repo: %s\n", link.RepoID)
		fmt.Fprintf(stdout, "pr: %s#%d\n", link.ForgeID, link.Number)
		fmt.Fprintf(stdout, "url: %s\n", link.URL)
		return nil
	case "list":
		fs := flag.NewFlagSet("midgard task pr list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		taskID := fs.String("task", "", "task id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		links, err := midgardforge.TaskPRLinks(ctx, start, *taskID)
		if err != nil {
			return err
		}
		for _, link := range links {
			fmt.Fprintf(stdout, "repo:%s pr:%s#%d group:%s head:%s url:%s\n", link.RepoID, link.ForgeID, link.Number, firstCLIValue(link.GroupID, "none"), firstCLIValue(link.HeadSHA, "unknown"), link.URL)
		}
		return nil
	case "unlink":
		fs := flag.NewFlagSet("midgard task pr unlink", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		taskID := fs.String("task", "", "task id")
		repoID := fs.String("repo", "", "registered repo id")
		forgeID := fs.String("account", "", "forge account id")
		pr := fs.String("pr", "", "pull request number or URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		number, err := midgardforge.ParsePRNumber(*pr)
		if err != nil {
			return err
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		execution, err := midgardtask.AcquireExecution(ctx, start, *taskID)
		if err != nil {
			return err
		}
		defer func() {
			if err := execution.Close(); retErr == nil && err != nil {
				retErr = err
			}
		}()
		ctx = execution.Context
		link, err := midgardforge.UnlinkTaskPR(ctx, midgardforge.UnlinkOptions{
			Root: start, TaskID: *taskID, RepoID: *repoID, ForgeID: *forgeID, Number: number,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "task: %s\nrepo: %s\npr: %s#%d\nunlinked: true\n", link.TaskID, link.RepoID, link.ForgeID, link.Number)
		return nil
	case "refresh":
		fs := flag.NewFlagSet("midgard task pr refresh", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		taskID := fs.String("task", "", "task id")
		repoID := fs.String("repo", "", "registered repo id")
		forgeID := fs.String("account", "", "forge account id")
		pr := fs.String("pr", "", "pull request number or URL")
		snapshot := fs.String("snapshot", "", "optional offline snapshot JSON file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		execution, err := midgardtask.AcquireExecution(ctx, start, *taskID)
		if err != nil {
			return err
		}
		defer func() {
			if err := execution.Close(); retErr == nil && err != nil {
				retErr = err
			}
		}()
		ctx = execution.Context
		number := 0
		if strings.TrimSpace(*pr) != "" {
			number, err = midgardforge.ParsePRNumber(*pr)
			if err != nil {
				return err
			}
		}
		var results []midgardforge.RefreshResult
		if *snapshot != "" {
			result, err := midgardforge.RefreshFromSnapshot(ctx, midgardforge.RefreshOptions{
				Root: start, TaskID: *taskID, RepoID: *repoID, ForgeID: *forgeID,
				Number: number, SnapshotPath: *snapshot,
			})
			if err != nil {
				return err
			}
			results = []midgardforge.RefreshResult{result}
		} else {
			results, err = midgardforge.RefreshFromGitHub(ctx, midgardforge.LiveRefreshOptions{
				Root: start, TaskID: *taskID, RepoID: *repoID, ForgeID: *forgeID, Number: number,
			})
			if err != nil {
				return err
			}
		}
		for _, result := range results {
			fmt.Fprintf(stdout, "task: %s\n", result.Link.TaskID)
			fmt.Fprintf(stdout, "pr: %s#%d\n", result.Link.ForgeID, result.Link.Number)
			fmt.Fprintf(stdout, "snapshot: %s\n", result.SnapshotID)
			fmt.Fprintf(stdout, "artifact: %s\n", result.Artifact)
			fmt.Fprintf(stdout, "checks: %d\n", result.Checks)
			fmt.Fprintf(stdout, "threads: %d\n", result.Threads)
		}
		return nil
	case "checks", "threads":
		commandName := args[0]
		fs := flag.NewFlagSet("midgard task pr "+commandName, flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		taskID := fs.String("task", "", "task id")
		repoID := fs.String("repo", "", "registered repo id")
		forgeID := fs.String("account", "", "forge account id")
		pr := fs.String("pr", "", "pull request number or URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		number, err := midgardforge.ParsePRNumber(*pr)
		if err != nil {
			return err
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		status, err := midgardforge.Inspect(ctx, start, *taskID)
		if err != nil {
			return err
		}
		entry, err := selectCLIStatusEntry(status.Entries, *repoID, *forgeID, number)
		if err != nil {
			return err
		}
		if commandName == "checks" {
			if entry.Snapshot != nil {
				fmt.Fprintf(stdout, "artifact: %s\n", entry.Snapshot.ChecksArtifactRef)
			}
			for _, check := range entry.Checks {
				fmt.Fprintf(stdout, "check: %s status:%s conclusion:%s url:%s\n", check.Name, check.Status, check.Conclusion, check.URL)
			}
			return nil
		}
		if entry.Snapshot != nil {
			fmt.Fprintf(stdout, "artifact: %s\n", entry.Snapshot.ThreadsArtifactRef)
			fmt.Fprintf(stdout, "complete: %t\n", entry.Snapshot.ReviewThreadsComplete)
		}
		for _, thread := range entry.Threads {
			fmt.Fprintf(stdout, "thread: %s path:%s line:%d resolved:%t author:%s\n", thread.ThreadID, thread.Path, thread.Line, thread.Resolved, thread.LastAuthor)
		}
		return nil
	case "status":
		fs := flag.NewFlagSet("midgard task pr status", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		taskID := fs.String("task", "", "task id")
		forAgent := fs.Bool("for-agent", false, "print compact agent digest")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		status, err := midgardforge.Inspect(ctx, start, *taskID)
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, midgardforge.FormatTaskPRStatus(status, *forAgent))
		return nil
	default:
		return fmt.Errorf("unknown task pr command %q", args[0])
	}
}

func printForgeUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midgard forge <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  auth status  show GitHub authentication without exposing tokens")
	fmt.Fprintln(w, "  repo link  link a registered repo to a forge remote")
}

func printForgeRepoUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midgard forge repo <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  link  link a registered repo to a forge remote")
}

func printTaskPRUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midgard task pr <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  link     link a task to a pull request")
	fmt.Fprintln(w, "  list     list linked pull requests")
	fmt.Fprintln(w, "  unlink   remove a task pull request link")
	fmt.Fprintln(w, "  refresh  refresh live GitHub evidence or import an offline snapshot")
	fmt.Fprintln(w, "  status   show linked pull request state")
	fmt.Fprintln(w, "  checks   show stored check evidence for one pull request")
	fmt.Fprintln(w, "  threads  show stored review threads for one pull request")
}

func selectCLIStatusEntry(entries []midgardforge.StatusEntry, repoID, forgeID string, number int) (midgardforge.StatusEntry, error) {
	var matches []midgardforge.StatusEntry
	for _, entry := range entries {
		if repoID != "" && entry.Link.RepoID != repoID {
			continue
		}
		if forgeID != "" && entry.Link.ForgeID != forgeID {
			continue
		}
		if entry.Link.Number != number {
			continue
		}
		matches = append(matches, entry)
	}
	switch len(matches) {
	case 0:
		return midgardforge.StatusEntry{}, fmt.Errorf("linked PR was not found")
	case 1:
		return matches[0], nil
	default:
		return midgardforge.StatusEntry{}, fmt.Errorf("multiple linked PRs match; specify --repo and --account")
	}
}

func firstCLIValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
