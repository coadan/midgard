package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"midgard/internal/command"
	"midgard/internal/policy"
	"midgard/internal/state"
	midgardtask "midgard/internal/task"
	"midgard/internal/workbench"
)

func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printCommandUsage(stdout)
		return nil
	}
	switch args[0] {
	case "run":
		return runCommandRun(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command subcommand %q", args[0])
	}
}

func runCommandRun(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("midgard command run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "workbench root or search start")
	taskID := fs.String("task", "", "task id")
	repoID := fs.String("repo", "", "repo id")
	cwdFlag := fs.String("cwd", "", "command cwd, absolute or repo-relative")
	timeout := fs.Duration("timeout", 30*time.Second, "command timeout")
	maxStdout := fs.Int64("max-stdout", 64<<10, "stdout byte cap")
	maxStderr := fs.Int64("max-stderr", 64<<10, "stderr byte cap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	commandLine := strings.Join(fs.Args(), " ")
	start, err := rootOrCWD(*root)
	if err != nil {
		return err
	}
	wbStatus, err := workbench.Status(start)
	if err != nil {
		return err
	}
	if *taskID == "" {
		return fmt.Errorf("task id is required")
	}
	taskStatus, err := midgardtask.Status(ctx, wbStatus.Root, *taskID)
	if err != nil {
		return err
	}
	wt, err := selectWorktree(taskStatus.Worktrees, *repoID)
	if err != nil {
		return err
	}
	cwd := wt.Path
	if *cwdFlag != "" {
		if filepath.IsAbs(*cwdFlag) {
			cwd = *cwdFlag
		} else {
			cwd = filepath.Join(wt.Path, *cwdFlag)
		}
	}
	layout := workbench.NewLayout(wbStatus.Root)
	artifactDir := filepath.Join(layout.Artifacts, *taskID)
	commandPolicy := policy.DefaultCommandPolicy(wt.Path, artifactDir)
	commandPolicy.Limits.Timeout = *timeout
	commandPolicy.Limits.MaxStdoutBytes = *maxStdout
	commandPolicy.Limits.MaxStderrBytes = *maxStderr
	executor := command.NewExecutor(commandPolicy)
	result, err := executor.Run(ctx, command.Request{
		TaskID:      *taskID,
		RepoID:      wt.RepoID,
		Command:     commandLine,
		CWD:         cwd,
		ArtifactDir: artifactDir,
	})
	if err != nil {
		return err
	}
	if err := persistCommandEvents(ctx, wbStatus.Root, result); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "command: %s\n", result.ID)
	fmt.Fprintf(stdout, "exit: %d\n", result.ExitCode)
	fmt.Fprintf(stdout, "timeout: %t\n", result.TimedOut)
	fmt.Fprintf(stdout, "stdout: %s\n", result.StdoutPath)
	fmt.Fprintf(stdout, "stderr: %s\n", result.StderrPath)
	fmt.Fprintf(stdout, "result: %s\n", result.ResultPath)
	if len(result.TouchedFiles) == 0 {
		fmt.Fprintln(stdout, "touched: none")
	} else {
		fmt.Fprintf(stdout, "touched: %s\n", strings.Join(result.TouchedFiles, ","))
	}
	return nil
}

func runCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printCheckUsage(stdout)
		return nil
	}
	switch args[0] {
	case "record":
		fs := flag.NewFlagSet("midgard check record", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		taskID := fs.String("task", "", "task id")
		id := fs.String("id", "", "check id")
		statusValue := fs.String("status", "", "check status")
		summary := fs.String("summary", "", "check summary")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *taskID == "" || *id == "" || *statusValue == "" {
			return fmt.Errorf("task, id, and status are required")
		}
		start, err := rootOrCWD(*root)
		if err != nil {
			return err
		}
		wbStatus, err := workbench.Status(start)
		if err != nil {
			return err
		}
		layout := workbench.NewLayout(wbStatus.Root)
		db, err := state.Open(ctx, layout.State)
		if err != nil {
			return err
		}
		defer db.Close()
		payload, err := json.Marshal(map[string]string{"id": *id, "status": *statusValue, "summary": *summary})
		if err != nil {
			return err
		}
		eventID, err := db.InsertEvent(ctx, state.Event{TaskID: *taskID, Type: "check.recorded", Payload: string(payload)})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "check: %s\n", *id)
		fmt.Fprintf(stdout, "event: %s\n", strconv.FormatInt(eventID, 10))
		return nil
	default:
		return fmt.Errorf("unknown check command %q", args[0])
	}
}

func persistCommandEvents(ctx context.Context, root string, result command.Result) error {
	layout := workbench.NewLayout(root)
	db, err := state.Open(ctx, layout.State)
	if err != nil {
		return err
	}
	defer db.Close()
	events, err := command.Events(result)
	if err != nil {
		return err
	}
	for _, event := range events {
		if _, err := db.InsertEvent(ctx, state.Event{TaskID: result.TaskID, Type: event.Type, Payload: event.Payload}); err != nil {
			return err
		}
	}
	return nil
}

func selectWorktree(worktrees []midgardtask.WorktreeStatus, repoID string) (midgardtask.WorktreeStatus, error) {
	if repoID == "" {
		if len(worktrees) == 1 {
			return worktrees[0], nil
		}
		return midgardtask.WorktreeStatus{}, fmt.Errorf("repo id is required when task has %d worktrees", len(worktrees))
	}
	for _, wt := range worktrees {
		if wt.RepoID == repoID {
			return wt, nil
		}
	}
	return midgardtask.WorktreeStatus{}, fmt.Errorf("repo %q not found for task", repoID)
}

func printCommandUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midgard command <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  run  run an audited command")
}

func printCheckUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midgard check <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  record  record a check result")
}
