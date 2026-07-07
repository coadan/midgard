package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"

	"midgard/internal/server"
)

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("midgard serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "workbench root or search start")
	addr := fs.String("addr", "127.0.0.1:8765", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	start, err := rootOrCWD(*root)
	if err != nil {
		return err
	}
	srv, err := server.New(start)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Handler: srv}
	go func() {
		<-ctx.Done()
		_ = httpServer.Close()
	}()
	fmt.Fprintf(stdout, "server: http://%s\n", listener.Addr().String())
	return httpServer.Serve(listener)
}

func runUI(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("midgard ui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "workbench root or search start")
	addr := fs.String("addr", "127.0.0.1:8765", "listen address")
	openBrowser := fs.Bool("open", true, "open browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	start, err := rootOrCWD(*root)
	if err != nil {
		return err
	}
	srv, err := server.New(start)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	url := "http://" + listener.Addr().String()
	httpServer := &http.Server{Handler: srv}
	go func() {
		<-ctx.Done()
		_ = httpServer.Close()
	}()
	if *openBrowser {
		_ = openURL(url)
	}
	fmt.Fprintf(stdout, "ui: %s\n", url)
	return httpServer.Serve(listener)
}

func openURL(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
