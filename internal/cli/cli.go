package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"midgard/internal/workbench"
)

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = ctx
	_ = stdin
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	case "workbench":
		return runWorkbench(ctx, args[1:], stdout, stderr)
	case "task":
		return runTask(ctx, args[1:], stdout, stderr)
	case "command":
		return runCommand(ctx, args[1:], stdout, stderr)
	case "check":
		return runCheck(ctx, args[1:], stdout, stderr)
	case "serve":
		return runServe(ctx, args[1:], stdout, stderr)
	case "ui":
		return runUI(ctx, args[1:], stdout, stderr)
	case "artifact":
		return runArtifact(ctx, args[1:], stdout, stderr)
	case "benchmark":
		return runBenchmark(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runWorkbench(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printWorkbenchUsage(stdout)
		return nil
	}

	switch args[0] {
	case "init":
		fs := flag.NewFlagSet("midgard workbench init", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", ".", "workbench root")
		name := fs.String("name", "", "workbench name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		result, err := workbench.Init(*root, workbench.InitOptions{Name: *name})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "workbench: %s\n", result.Root)
		fmt.Fprintf(stdout, "config: %s\n", result.ConfigPath)
		if result.Created {
			fmt.Fprintln(stdout, "status: created")
		} else {
			fmt.Fprintln(stdout, "status: exists")
		}
		return nil
	case "status":
		fs := flag.NewFlagSet("midgard workbench status", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		start := *root
		if start == "" {
			var err error
			start, err = os.Getwd()
			if err != nil {
				return err
			}
		}
		status, err := workbench.Status(start)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "workbench: %s\n", status.Root)
		fmt.Fprintf(stdout, "config: %s\n", status.ConfigPath)
		fmt.Fprintf(stdout, "name: %s\n", status.Config.Name)
		fmt.Fprintf(stdout, "version: %d\n", status.Config.Version)
		return nil
	case "add-repo":
		return runWorkbenchAddRepo(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown workbench command %q", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midgard <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  workbench init     initialize a workbench")
	fmt.Fprintln(w, "  workbench status   show current workbench")
	fmt.Fprintln(w, "  workbench add-repo register a source checkout")
	fmt.Fprintln(w, "  task create        create a task worktree")
	fmt.Fprintln(w, "  task status        show task state")
	fmt.Fprintln(w, "  task diff          show task diff")
	fmt.Fprintln(w, "  command run        run an audited command")
	fmt.Fprintln(w, "  check record       record a check result")
	fmt.Fprintln(w, "  serve              start local HTTP server")
	fmt.Fprintln(w, "  ui                 start browser UI server")
	fmt.Fprintln(w, "  artifact list      list task artifacts")
	fmt.Fprintln(w, "  artifact show      show a task artifact")
	fmt.Fprintln(w, "  benchmark import-pr import a merged GitHub PR benchmark")
	fmt.Fprintln(w, "  benchmark run      execute benchmark items with --provider")
	fmt.Fprintln(w, "  benchmark report   regenerate benchmark report")
}

func printWorkbenchUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midgard workbench <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  init      initialize a workbench")
	fmt.Fprintln(w, "  status    show current workbench")
	fmt.Fprintln(w, "  add-repo  register a source checkout")
}
