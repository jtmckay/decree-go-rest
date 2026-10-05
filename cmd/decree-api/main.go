// Command decree-api is the HTTP front door of a decree project (SPEC.md).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/jtmckay/decree-api/internal/config"
	"github.com/jtmckay/decree-api/internal/server"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without the process: it returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("decree-api", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", config.DefaultConfigPath, "the config file")
	check := fs.Bool("check", false, "validate the config and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "decree-api: unexpected arguments: %v\n", fs.Args())
		return 2
	}
	if !*check {
		return runServe(*configPath, stderr)
	}
	return runCheck(*configPath, stdout, stderr)
}

// runCheck is `decree-api -check`: SPEC.md §3, Validation.
func runCheck(path string, stdout, stderr io.Writer) int {
	c := loadValid(path, stderr)
	if c == nil {
		return 1
	}
	fmt.Fprintf(stdout, "config ok: %d endpoints\n", len(c.Endpoints))
	return 0
}

// loadValid loads and validates the config. On failure it lists every
// error on stderr and returns nil.
func loadValid(path string, stderr io.Writer) *config.Config {
	c, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "config invalid:\n  %v\n", err)
		return nil
	}
	if errs := config.Validate(c, config.Options{}); len(errs) > 0 {
		fmt.Fprintf(stderr, "config invalid: %d error(s)\n", len(errs))
		for _, e := range errs {
			fmt.Fprintf(stderr, "  %v\n", e)
		}
		return nil
	}
	return c
}

// runServe validates the config and serves it until SIGTERM or SIGINT,
// then finishes in-flight requests (SPEC.md §4, §5 Shutdown).
func runServe(path string, stderr io.Writer) int {
	// Before anything can fail: nothing serves until the config is valid.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	c := loadValid(path, stderr)
	if c == nil {
		return 1
	}
	server.SetUmask()
	srv, err := server.New(c)
	if err != nil {
		fmt.Fprintf(stderr, "decree-api: %v\n", err)
		return 1
	}
	ln, err := net.Listen("tcp", c.Listen)
	if err != nil {
		fmt.Fprintf(stderr, "decree-api: %v\n", err)
		return 1
	}
	slog.Info("listening", "addr", ln.Addr().String(), "project", c.ProjectDir)
	if err := server.Serve(ctx, ln, srv); err != nil {
		fmt.Fprintf(stderr, "decree-api: %v\n", err)
		return 1
	}
	slog.Info("stopped")
	return 0
}
