package app

import (
	"context"
	"fmt"
	"io"

	"midgard/internal/cli"
	"midgard/internal/config"
)

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := config.LoadDotEnv(".env"); err != nil {
		fmt.Fprintf(stderr, "midgard: load .env: %v\n", err)
		return 1
	}
	if err := cli.Run(ctx, args, stdin, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "midgard: %v\n", err)
		return 1
	}
	return 0
}
