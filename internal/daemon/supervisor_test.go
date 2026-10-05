//go:build unix

package daemon

import (
	"bytes"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jtmckay/decree-api/internal/decreetest"
)

// fakeClock is a Clock whose timers fire only when a test fires them.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	d time.Duration
	c chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 5, 4, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &fakeTimer{d: d, c: make(chan time.Time, 1)}
	c.timers = append(c.timers, tm)
	return tm.c
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// next waits for the supervisor to start a timer and returns it.
func (c *fakeClock) next(t *testing.T) *fakeTimer {
	t.Helper()
	var tm *fakeTimer
	waitFor(t, "a timer", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if len(c.timers) == 0 {
			return false
		}
		tm, c.timers = c.timers[0], c.timers[1:]
		return true
	})
	return tm
}

// fire advances the clock by the timer's duration and fires it.
func (c *fakeClock) fire(tm *fakeTimer) {
	c.Advance(tm.d)
	tm.c <- c.Now()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// syncBuffer is a log destination safe for concurrent writers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type fixture struct {
	stub  *decreetest.Stub
	dir   string
	sup   *Supervisor
	clock *fakeClock
	logs  *syncBuffer
}

// newFixture supervises the stub daemon in a temp project with a fake
// clock.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{stub: decreetest.New(t), dir: t.TempDir(), clock: newFakeClock(), logs: &syncBuffer{}}
	f.sup = New(f.stub.Path, f.dir, "2s")
	f.sup.Clock = f.clock
	f.sup.Logger = slog.New(slog.NewJSONHandler(f.logs, nil))
	t.Cleanup(func() {
		done := make(chan struct{})
		go func() { f.sup.Stop(); close(done) }()
		// Stop may wait on a fake timer; fire whatever it starts.
		for {
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
				f.clock.mu.Lock()
				ts := f.clock.timers
				f.clock.timers = nil
				f.clock.mu.Unlock()
				for _, tm := range ts {
					tm.c <- time.Time{}
				}
			}
		}
	})
	return f
}

func (f *fixture) starts(t *testing.T) []decreetest.Event {
	return f.stub.DaemonEvents(t, "start")
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// TestStartArguments: the daemon runs as `decree daemon --interval <i>`
// in the project, and its output is logged line by line, tagged.
func TestStartArguments(t *testing.T) {
	f := newFixture(t)
	if err := f.sup.Start(); err != nil {
		t.Fatal(err)
	}
	want := "cwd=" + f.dir + " daemon --interval 2s"
	waitFor(t, "the daemon to start", func() bool { return len(f.stub.Calls(t)) == 1 })
	if got := f.stub.Calls(t)[0]; got != want {
		t.Errorf("daemon call %q, want %q", got, want)
	}
	if got, want := f.sup.Command(), []string{f.stub.Path, "daemon", "--interval", "2s"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Command() = %q, want %q", got, want)
	}
	st := f.sup.State()
	if !st.Running || st.PID <= 0 || st.Restarts != 0 || !st.Since.Equal(f.clock.Now()) {
		t.Errorf("state %+v, want running with a pid, no restarts, since now", st)
	}
	waitFor(t, "both output lines", func() bool { return strings.Count(f.logs.String(), `"source":"daemon"`) == 2 })
	logs := f.logs.String()
	for _, w := range []string{
		`"msg":"daemon ` + strconv.Itoa(st.PID) + ` polling","source":"daemon","stream":"stdout"`,
		`"msg":"daemon ` + strconv.Itoa(st.PID) + ` warning","source":"daemon","stream":"stderr"`,
	} {
		if !strings.Contains(logs, w) {
			t.Errorf("logs lack %s:\n%s", w, logs)
		}
	}
}

// TestRestartBackoff: each exit is followed by a restart after a back-off
// that doubles from 1 s to at most 60 s, and starts again at 1 s after a
// run of 5 minutes.
func TestRestartBackoff(t *testing.T) {
	f := newFixture(t)
	if err := f.sup.Start(); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		w *= time.Second
		waitFor(t, "the daemon to run", func() bool { return len(f.starts(t)) == i+1 })
		f.stub.ExitDaemon(t, 1)
		tm := f.clock.next(t)
		if tm.d != w {
			t.Fatalf("restart %d: back-off %v, want %v", i+1, tm.d, w)
		}
		st := f.sup.State()
		if st.Running || st.PID != 0 || st.Restarts != i {
			t.Errorf("down after exit %d: state %+v", i+1, st)
		}
		f.clock.fire(tm)
	}
	waitFor(t, "the last restart", func() bool { return len(f.starts(t)) == len(want)+1 })
	waitFor(t, "the state", func() bool { return f.sup.State().Running })
	if st := f.sup.State(); st.Restarts != len(want) {
		t.Errorf("restarts %d, want %d", st.Restarts, len(want))
	}

	// 5 minutes up resets the back-off.
	f.clock.Advance(ResetAfter)
	f.stub.ExitDaemon(t, 1)
	if tm := f.clock.next(t); tm.d != time.Second {
		t.Errorf("after 5 minutes up: back-off %v, want 1s", tm.d)
	} else {
		f.clock.fire(tm)
	}
	waitFor(t, "a restart", func() bool { return len(f.starts(t)) == len(want)+2 })
	// And it doubles again from there.
	f.stub.ExitDaemon(t, 1)
	if tm := f.clock.next(t); tm.d != 2*time.Second {
		t.Errorf("after the reset: back-off %v, want 2s", tm.d)
	}

	logs := f.logs.String()
	for _, w := range []string{`"msg":"daemon exited"`, `"status":"exit status 1"`, `"msg":"daemon restarted"`, `"previous_status":"exit status 1"`, `"restart_in":"1m0s"`} {
		if !strings.Contains(logs, w) {
			t.Errorf("logs lack %s", w)
		}
	}
}

// TestStopGraceful: Stop sends SIGTERM, and the daemon that exits on it
// is not killed.
func TestStopGraceful(t *testing.T) {
	f := newFixture(t)
	if err := f.sup.Start(); err != nil {
		t.Fatal(err)
	}
	pid := f.sup.State().PID
	waitFor(t, "the daemon to run", func() bool { return len(f.starts(t)) == 1 })
	f.sup.Stop()
	if terms := f.stub.DaemonEvents(t, "term"); len(terms) != 1 || terms[0].PID != pid {
		t.Errorf("SIGTERMs %+v, want one to %d", terms, pid)
	}
	if alive(pid) {
		t.Error("the daemon is still alive")
	}
	if st := f.sup.State(); st.Running {
		t.Errorf("state %+v after Stop", st)
	}
	logs := f.logs.String()
	if strings.Contains(logs, "killing it") {
		t.Errorf("killed a daemon that exited on SIGTERM:\n%s", logs)
	}
	if !strings.Contains(logs, `"msg":"daemon stopped"`) || !strings.Contains(logs, `"status":"exit status 0"`) {
		t.Errorf("logs lack the stop:\n%s", logs)
	}
	if len(f.starts(t)) != 1 {
		t.Error("restarted after Stop")
	}
}

// TestStopKillsAfterTimeout: a daemon that ignores SIGTERM is killed only
// when the 15 s stop timeout runs out.
func TestStopKillsAfterTimeout(t *testing.T) {
	f := newFixture(t)
	f.stub.SetDaemonIgnoreTerm(t, true)
	if err := f.sup.Start(); err != nil {
		t.Fatal(err)
	}
	pid := f.sup.State().PID
	waitFor(t, "the daemon to run", func() bool { return len(f.starts(t)) == 1 })
	stopped := make(chan struct{})
	go func() { f.sup.Stop(); close(stopped) }()
	tm := f.clock.next(t)
	if tm.d != 15*time.Second {
		t.Errorf("stop timeout %v, want 15s", tm.d)
	}
	time.Sleep(300 * time.Millisecond)
	select {
	case <-stopped:
		t.Fatal("Stop returned before the timeout")
	default:
	}
	if !alive(pid) {
		t.Fatal("the daemon died before the timeout")
	}
	f.clock.fire(tm)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the timeout")
	}
	if alive(pid) {
		t.Error("the daemon is alive after SIGKILL")
	}
	logs := f.logs.String()
	if !strings.Contains(logs, "killing it") || !strings.Contains(logs, `"status":"signal: killed"`) {
		t.Errorf("logs lack the kill:\n%s", logs)
	}
}

// TestStopDuringBackoff: Stop while a restart is pending returns without
// starting another daemon.
func TestStopDuringBackoff(t *testing.T) {
	f := newFixture(t)
	if err := f.sup.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the daemon to run", func() bool { return len(f.starts(t)) == 1 })
	f.stub.ExitDaemon(t, 2)
	f.clock.next(t)
	done := make(chan struct{})
	go func() { f.sup.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
	if n := len(f.starts(t)); n != 1 {
		t.Errorf("%d starts, want 1", n)
	}
}

// TestStartFails: a daemon that cannot be started is an error, and Stop
// still returns.
func TestStartFails(t *testing.T) {
	s := New("/nonexistent/decree", t.TempDir(), "2s")
	if err := s.Start(); err == nil || !strings.Contains(err.Error(), "start daemon") {
		t.Errorf("Start: %v, want an error", err)
	}
	s.Stop()
	if st := s.State(); st.Running {
		t.Errorf("state %+v", st)
	}
}
