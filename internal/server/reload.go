package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jtmckay/decree-go-rest/internal/config"
)

// Reload timings of SPEC.md §8.
const (
	// DefaultPollInterval is how often the config file's modification
	// time is checked. Polling, not watching: editors and bind mounts
	// replace the inode.
	DefaultPollInterval = 2 * time.Second
	// DefaultSettle is the wait after a change is seen, so a half-written
	// file is never read.
	DefaultSettle = 300 * time.Millisecond
)

// Live serves the route table of the current config, and swaps it when
// the config file changes (SPEC.md §8). It is an http.Handler.
type Live struct {
	// PollInterval and Settle are the reload timings; set them before Run.
	PollInterval time.Duration
	Settle       time.Duration
	// Logger receives the reload records.
	Logger *slog.Logger
	// Daemon reports the supervised daemon for /healthz; set it before
	// serving. While it is nil an enabled daemon counts as not running.
	Daemon func() DaemonState

	path string
	cur  atomic.Pointer[Server]

	// mu serializes reloads and guards the fields below.
	mu sync.Mutex
	// boot is the startup config: its listen, project and daemon.* are the
	// ones in effect until a restart.
	boot *config.Config
	// last is the last valid config, as written in the file.
	last     *config.Config
	loadedAt time.Time
	// lastErr is why the last reload failed, or nil once a config loads.
	lastErr error
	// seen is the state of the file when it was last read.
	seen fileStamp
}

// NewLive serves s, built from c, which was loaded from path.
func NewLive(path string, c *config.Config, s *Server) *Live {
	l := &Live{
		PollInterval: DefaultPollInterval,
		Settle:       DefaultSettle,
		Logger:       s.Logger,
		path:         path,
		boot:         c,
		last:         c,
		loadedAt:     time.Now(),
		seen:         stampOf(path),
	}
	s.health = l.health
	l.cur.Store(s)
	return l
}

// ServeHTTP serves r on the route table current when it arrives; a reload
// during the request does not change the table it finishes on.
func (l *Live) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.cur.Load().ServeHTTP(w, r)
}

// Server returns the current route table.
func (l *Live) Server() *Server {
	return l.cur.Load()
}

// ConfigState returns when the current config was loaded and, when the
// last reload failed, why. /healthz reports it (SPEC.md §7).
func (l *Live) ConfigState() (loadedAt time.Time, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loadedAt, l.lastErr
}

// Run polls the config file until ctx is done, and reloads it after each
// change.
func (l *Live) Run(ctx context.Context) {
	tick := time.NewTicker(l.PollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		l.mu.Lock()
		changed := stampOf(l.path) != l.seen
		l.mu.Unlock()
		if !changed {
			continue
		}
		settle := time.NewTimer(l.Settle)
		select {
		case <-ctx.Done():
			settle.Stop()
			return
		case <-settle.C:
		}
		// The error is logged and recorded by Reload.
		_ = l.Reload()
	}
}

// Reload loads and validates the config file, and on success swaps the
// route table. On failure the old one keeps serving, and the error is
// logged and recorded until a valid config loads.
func (l *Live) Reload() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = stampOf(l.path)

	c, err := config.Load(l.path)
	if err != nil {
		return l.fail(err)
	}
	// Step 7 runs decree, so only when the decree or the project changed.
	opts := config.Options{SkipDecree: c.Decree == l.last.Decree && c.ProjectDir == l.last.ProjectDir}
	if errs := config.Validate(c, opts); errs != nil {
		return l.fail(errs)
	}
	serving := c
	if c.ProjectDir != l.boot.ProjectDir {
		// project takes effect only on restart: the new config is valid
		// for its own project, and must also be for the one being served.
		cp := *c
		cp.Project, cp.ProjectDir = l.boot.Project, l.boot.ProjectDir
		if errs := config.Validate(&cp, config.Options{SkipDecree: true}); errs != nil {
			return l.fail(fmt.Errorf("in the running project %s: %w", cp.ProjectDir, errs))
		}
		serving = &cp
	}

	old := l.cur.Load()
	next, err := newServer(serving, old.budgets, old.opts)
	if err != nil {
		return l.fail(err)
	}
	next.EmitTimeout = old.EmitTimeout
	next.Environ = old.Environ
	next.Logger = old.Logger
	next.health = old.health
	old.budgets.setLimits(serving.Limits)
	l.cur.Store(next)

	l.last = c
	l.loadedAt = time.Now()
	l.lastErr = nil
	l.Logger.Info("config reloaded", "config", c.File, "endpoints", len(c.Endpoints))
	next.LogRoutes()
	l.warnRestartOnly(c)
	return nil
}

// fail records and logs every error of a failed reload.
func (l *Live) fail(err error) error {
	l.lastErr = err
	var errs config.Errors
	if errors.As(err, &errs) {
		for _, e := range errs {
			l.Logger.Error("config reload failed; keeping the old config", "config", l.path, "error", e.Error())
		}
	} else {
		l.Logger.Error("config reload failed; keeping the old config", "config", l.path, "error", err.Error())
	}
	return err
}

// warnRestartOnly warns about each key of c that differs from the one in
// effect and that takes effect only on restart (SPEC.md §8).
func (l *Live) warnRestartOnly(c *config.Config) {
	b := l.boot
	for _, k := range []struct {
		key     string
		changed bool
	}{
		{"listen", c.Listen != b.Listen},
		{"project", c.ProjectDir != b.ProjectDir},
		{"daemon.enabled", c.Daemon.Enabled != b.Daemon.Enabled},
		{"daemon.interval", c.Daemon.Interval != b.Daemon.Interval},
	} {
		if k.changed {
			l.Logger.Warn("config key changed; it takes effect only on restart", "key", k.key)
		}
	}
}

// fileStamp is what polling compares: a changed modification time or
// size, or the file appearing or disappearing, is a change.
type fileStamp struct {
	exists bool
	mod    int64
	size   int64
}

func stampOf(path string) fileStamp {
	st, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, mod: st.ModTime().UnixNano(), size: st.Size()}
}
