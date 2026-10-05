// Package daemon keeps `decree daemon` running for a project, and holds
// the lock that makes decree-go-rest the only one doing so (SPEC.md §5).
package daemon

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Timings of SPEC.md §5.
const (
	// MinBackoff is the first delay before a restart.
	MinBackoff = time.Second
	// MaxBackoff caps the doubling delay.
	MaxBackoff = 60 * time.Second
	// ResetAfter is how long the daemon must run for the back-off to
	// start again at MinBackoff.
	ResetAfter = 5 * time.Minute
	// StopTimeout is how long the daemon has to exit after SIGTERM before
	// it gets SIGKILL.
	StopTimeout = 15 * time.Second
)

// drainTimeout bounds how long, after the daemon exits, its last output
// is waited for. A child that outlives it may hold the pipes open.
const drainTimeout = time.Second

// Clock is the supervisor's time source; tests replace it.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// State is what /healthz reports of the daemon (SPEC.md §7).
type State struct {
	Running bool
	// PID is the daemon's process id while it runs, otherwise 0.
	PID int
	// Restarts counts the restarts since decree-go-rest started.
	Restarts int
	// Since is when the daemon last started or stopped.
	Since time.Time
}

// Supervisor runs `decree daemon --interval <interval>` in a project and
// restarts it when it exits.
type Supervisor struct {
	// Logger receives the daemon's output, one record per line, and the
	// supervisor's own records.
	Logger *slog.Logger
	// Clock times the back-off and the stop timeout.
	Clock Clock
	// StopTimeout is the wait between SIGTERM and SIGKILL.
	StopTimeout time.Duration

	bin  string
	args []string
	dir  string

	mu      sync.Mutex
	state   State
	started bool

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// New supervises `<bin> daemon --interval <interval>` run in dir.
// interval is in decree's duration format, such as 2s.
func New(bin, dir, interval string) *Supervisor {
	return &Supervisor{
		Logger:      slog.Default(),
		Clock:       realClock{},
		StopTimeout: StopTimeout,
		bin:         bin,
		args:        []string{"daemon", "--interval", interval},
		dir:         dir,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
}

// Command is the daemon's command line: the binary, then its arguments.
func (s *Supervisor) Command() []string {
	return append([]string{s.bin}, s.args...)
}

// State returns the daemon's current state.
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Start starts the daemon, then supervises it in the background until
// Stop. An error means the daemon could not be started at all.
func (s *Supervisor) Start() error {
	p, err := s.spawn()
	if err != nil {
		return err
	}
	s.Logger.Info("daemon started", "pid", p.pid, "command", s.Command(), "dir", s.dir)
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	go s.supervise(p)
	return nil
}

// Stop sends the daemon SIGTERM, waits up to StopTimeout for it to exit,
// then sends SIGKILL to it and its process group. It returns once the
// daemon has exited and no restart is pending. Stop before Start returns
// at once.
func (s *Supervisor) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if started {
		<-s.done
	}
}

// proc is one run of the daemon.
type proc struct {
	cmd     *exec.Cmd
	pid     int
	started time.Time
	// exited is closed once the process has exited and err is set.
	exited chan struct{}
	err    error
}

// spawn starts the daemon and records it as running. Its output is
// logged line by line.
func (s *Supervisor) spawn() (*proc, error) {
	cmd := exec.Command(s.bin, s.args...)
	cmd.Dir = s.dir
	// Its own process group: a terminal's Ctrl-C reaches decree-go-rest only,
	// which stops the daemon after finishing in-flight requests.
	setProcessGroup(cmd)
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, fmt.Errorf("start daemon: %w", err)
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	err = cmd.Start()
	outW.Close()
	errW.Close()
	if err != nil {
		outR.Close()
		errR.Close()
		return nil, fmt.Errorf("start daemon: %w", err)
	}
	p := &proc{cmd: cmd, pid: cmd.Process.Pid, started: s.Clock.Now(), exited: make(chan struct{})}
	var drained sync.WaitGroup
	drained.Add(2)
	go func() { defer drained.Done(); s.logLines(outR, "stdout") }()
	go func() { defer drained.Done(); s.logLines(errR, "stderr") }()
	go func() {
		err := cmd.Wait()
		// The last lines are logged before the exit is, unless a child
		// that outlived the daemon holds the pipes open.
		waitTimeout(&drained, drainTimeout)
		p.err = err
		close(p.exited)
	}()

	s.mu.Lock()
	s.state.Running = true
	s.state.PID = p.pid
	s.state.Since = p.started
	s.mu.Unlock()
	return p, nil
}

func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	c := make(chan struct{})
	go func() { wg.Wait(); close(c) }()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-c:
	case <-t.C:
	}
}

// maxLine caps one logged line; a longer one is logged in pieces.
const maxLine = 64 << 10

// logLines logs each line of r as one record, tagged with the stream
// (SPEC.md §5, Logs). It closes r at EOF.
func (s *Supervisor) logLines(r io.ReadCloser, stream string) {
	defer r.Close()
	br := bufio.NewReaderSize(r, maxLine)
	for {
		line, err := br.ReadSlice('\n')
		if len(line) > 0 {
			n := len(line)
			if line[n-1] == '\n' {
				n--
				if n > 0 && line[n-1] == '\r' {
					n--
				}
			}
			s.Logger.Info(string(line[:n]), "source", "daemon", "stream", stream)
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return
		}
	}
}

// supervise waits for p to exit and restarts it after the back-off, until
// Stop.
func (s *Supervisor) supervise(p *proc) {
	defer close(s.done)
	backoff := MinBackoff
	// next returns the delay before the restart that follows a run of
	// length ran, and doubles the back-off for the one after.
	next := func(ran time.Duration) time.Duration {
		if ran >= ResetAfter {
			backoff = MinBackoff
		}
		d := backoff
		backoff = min(2*backoff, MaxBackoff)
		return d
	}
	var status string
	for {
		var delay time.Duration
		if p != nil {
			select {
			case <-s.stop:
				s.terminate(p)
				return
			case <-p.exited:
			}
			now := s.Clock.Now()
			ran := now.Sub(p.started)
			status = exitStatus(p.err)
			s.mu.Lock()
			s.state.Running = false
			s.state.PID = 0
			s.state.Since = now
			s.mu.Unlock()
			delay = next(ran)
			s.Logger.Warn("daemon exited", "pid", p.pid, "status", status, "ran", ran.String(), "restart_in", delay.String())
		} else {
			delay = next(0)
		}

		select {
		case <-s.stop:
			return
		case <-s.Clock.After(delay):
		}
		np, err := s.spawn()
		if err != nil {
			// p stays nil: the next round backs off further.
			s.Logger.Error("daemon restart failed", "error", err.Error())
			p = nil
			continue
		}
		p = np
		s.mu.Lock()
		s.state.Restarts++
		restarts := s.state.Restarts
		s.mu.Unlock()
		s.Logger.Info("daemon restarted", "pid", p.pid, "restarts", restarts, "previous_status", status)
	}
}

// terminate stops p: SIGTERM, then SIGKILL after StopTimeout (SPEC.md §5,
// Shutdown). decree interrupts its running script on SIGTERM.
func (s *Supervisor) terminate(p *proc) {
	s.Logger.Info("stopping daemon", "pid", p.pid, "signal", "SIGTERM")
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		s.Logger.Error("signal daemon", "pid", p.pid, "error", err.Error())
	}
	select {
	case <-p.exited:
	case <-s.Clock.After(s.StopTimeout):
		s.Logger.Warn("daemon did not exit after SIGTERM; killing it", "pid", p.pid, "timeout", s.StopTimeout.String())
		if err := killGroup(p.cmd); err != nil {
			s.Logger.Error("kill daemon", "pid", p.pid, "error", err.Error())
		}
		<-p.exited
	}
	s.mu.Lock()
	s.state.Running = false
	s.state.PID = 0
	s.state.Since = s.Clock.Now()
	s.mu.Unlock()
	s.Logger.Info("daemon stopped", "pid", p.pid, "status", exitStatus(p.err))
}

// exitStatus describes how a process ended: "exit status 1",
// "signal: killed", or "exit status 0".
func exitStatus(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}
