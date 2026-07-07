package main

import (
	"context"
	"os"

	"midgard/internal/app"
)

func main() {
	code := app.Run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	os.Exit(code)
}
