package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"midgard/internal/artifact"
	"midgard/internal/workbench"
)

func runArtifact(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	_ = ctx
	if len(args) == 0 {
		printArtifactUsage(stdout)
		return nil
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("midgard artifact list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		taskID := fs.String("task", "", "task id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		artifactRoot, err := artifactRoot(*root, *taskID)
		if err != nil {
			return err
		}
		return filepath.WalkDir(artifactRoot, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(artifactRoot, path)
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, filepath.ToSlash(rel))
			return nil
		})
	case "show":
		fs := flag.NewFlagSet("midgard artifact show", flag.ContinueOnError)
		fs.SetOutput(stderr)
		root := fs.String("root", "", "workbench root or search start")
		taskID := fs.String("task", "", "task id")
		path := fs.String("path", "", "artifact path")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := artifact.ValidatePath(*path); err != nil {
			return err
		}
		artifactRoot, err := artifactRoot(*root, *taskID)
		if err != nil {
			return err
		}
		file, err := os.Open(filepath.Join(artifactRoot, filepath.FromSlash(*path)))
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(stdout, file)
		return err
	default:
		return fmt.Errorf("unknown artifact command %q", args[0])
	}
}

func artifactRoot(root, taskID string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("task id is required")
	}
	start, err := rootOrCWD(root)
	if err != nil {
		return "", err
	}
	status, err := workbench.Status(start)
	if err != nil {
		return "", err
	}
	return filepath.Join(status.Root, ".midgard", "artifacts", taskID), nil
}

func printArtifactUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midgard artifact <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  list  list task artifacts")
	fmt.Fprintln(w, "  show  show a task artifact")
}
