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
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jtmckay/decree-api/internal/config"
	"github.com/jtmckay/decree-api/internal/daemon"
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
	healthcheck := fs.Bool("healthcheck", false, "request GET /healthz on the listen address; exit 0 on 200, 1 otherwise")
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
	switch {
	case *check && *healthcheck:
		fmt.Fprintln(stderr, "decree-api: -check and -healthcheck cannot be combined")
		return 2
	case *check:
		return runCheck(*configPath, stdout, stderr)
	case *healthcheck:
		return runHealthcheck(*configPath, stdout, stderr)
	}
	return runServe(*configPath, stderr)
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

// runServe validates the config, takes the project's lock, starts the
// daemon and serves until SIGTERM or SIGINT. It then finishes in-flight
// requests, and only then stops the daemon (SPEC.md §4, §5).
func runServe(path string, stderr io.Writer) int {
	// Before anything can fail: nothing serves until the config is valid.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	c := loadValid(path, stderr)
	if c == nil {
		return 1
	}
	server.SetUmask()
	// One decree-api, so one daemon, per project (SPEC.md §5). A second
	// instance stops here, before it touches anything process-wide.
	lock, err := daemon.Acquire(c.ProjectDir)
	if err != nil {
		fmt.Fprintf(stderr, "decree-api: %v\n", err)
		return 1
	}
	defer lock.Unlock()
	// Logs are JSON lines on stderr (SPEC.md §9).
	slog.SetDefault(slog.New(slog.NewJSONHandler(stderr, nil)))
	srv, err := server.New(c)
	if err != nil {
		fmt.Fprintf(stderr, "decree-api: %v\n", err)
		return 1
	}
	live := server.NewLive(path, c, srv)
	srv.LogRoutes()

	// The daemon starts after validation and before listening.
	var sup *daemon.Supervisor
	if c.Daemon.Enabled {
		sup = daemon.New(srv.Decree(), c.ProjectDir, c.Daemon.Interval.String())
		if err := sup.Start(); err != nil {
			fmt.Fprintf(stderr, "decree-api: %v\n", err)
			return 1
		}
		live.Daemon = func() server.DaemonState {
			st := sup.State()
			return server.DaemonState{Running: st.Running, PID: st.PID, Restarts: st.Restarts, Since: st.Since}
		}
	} else {
		slog.Info("daemon disabled")
	}
	code := listenAndServe(ctx, c, live, stderr)
	if sup != nil {
		sup.Stop()
	}
	if code == 0 {
		slog.Info("stopped")
	}
	return code
}

// listenAndServe serves live, reloading the config on change (SPEC.md
// §8), until ctx is done and in-flight requests have finished.
func listenAndServe(ctx context.Context, c *config.Config, live *server.Live, stderr io.Writer) int {
	ln, err := net.Listen("tcp", c.Listen)
	if err != nil {
		fmt.Fprintf(stderr, "decree-api: %v\n", err)
		return 1
	}
	slog.Info("listening", "addr", ln.Addr().String(), "project", c.ProjectDir)
	reloads := make(chan struct{})
	rctx, stopReloads := context.WithCancel(ctx)
	go func() { defer close(reloads); live.Run(rctx) }()
	err = server.Serve(ctx, ln, live)
	stopReloads()
	<-reloads
	if err != nil {
		fmt.Fprintf(stderr, "decree-api: %v\n", err)
		return 1
	}
	return 0
}

// HealthcheckTimeout bounds the request of -healthcheck.
const HealthcheckTimeout = 5 * time.Second

// runHealthcheck is `decree-api -healthcheck` (SPEC.md §11): GET /healthz
// on the configured listen address, exit 0 on 200 and 1 otherwise. It
// prints the body, so a container's health log shows the state.
func runHealthcheck(path string, stdout, stderr io.Writer) int {
	c, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "decree-api: %v\n", err)
		return 1
	}
	u, err := healthURL(c.Listen)
	if err != nil {
		fmt.Fprintf(stderr, "decree-api: %v\n", err)
		return 1
	}
	client := &http.Client{Timeout: HealthcheckTimeout}
	resp, err := client.Get(u)
	if err != nil {
		fmt.Fprintf(stderr, "decree-api: healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(stdout, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "decree-api: healthcheck: %s\n", resp.Status)
		return 1
	}
	return 0
}

// healthURL is the /healthz URL of a listen address. A wildcard host is
// reached on loopback.
func healthURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("listen %q: %w", listen, err)
	}
	switch ip := net.ParseIP(host); {
	case host == "":
		host = "127.0.0.1"
	case ip != nil && ip.IsUnspecified() && ip.To4() != nil:
		host = "127.0.0.1"
	case ip != nil && ip.IsUnspecified():
		host = "::1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: server.HealthRoute}).String(), nil
}
