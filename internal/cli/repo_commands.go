package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"midgard/internal/gitrepo"
	"midgard/internal/workbench"
)

func runWorkbenchAddRepo(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("midgard workbench add-repo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "workbench root or search start")
	id := fs.String("id", "", "repo id")
	path := fs.String("path", "", "source checkout path")
	mainRef := fs.String("main-ref", "main", "main ref")
	if err := fs.Parse(args); err != nil {
		return err
	}
	start, err := rootOrCWD(*root)
	if err != nil {
		return err
	}
	if *path != "" {
		if err := gitrepo.IsRepo(ctx, *path); err != nil {
			return err
		}
	}
	if _, err := workbench.AddRepo(start, workbench.AddRepoOptions{ID: *id, Path: *path, MainRef: *mainRef}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "repo: %s\n", *id)
	fmt.Fprintf(stdout, "path: %s\n", *path)
	fmt.Fprintf(stdout, "main_ref: %s\n", *mainRef)
	return nil
}
