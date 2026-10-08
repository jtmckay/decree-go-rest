//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jtmckay/decree-go-rest/internal/config"
	"github.com/jtmckay/decree-go-rest/internal/daemon"
	"github.com/jtmckay/decree-go-rest/internal/decreetest"
)

// syncBuffer is a stderr that a test may read while decree-go-rest writes it.
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

// instance is a decree-go-rest running in the test process.
type instance struct {
	addr   string
	stderr *syncBuffer
	exited chan int
}

// serve runs decree-go-rest on the config at path and waits until /healthz
// answers.
func serve(t *testing.T, path string) *instance {
	t.Helper()
	in := &instance{addr: freeAddr(t), stderr: &syncBuffer{}, exited: make(chan int, 1)}
	t.Setenv("DECREE_GO_REST_LISTEN", in.addr)
	go func() { in.exited <- run([]string{"-config", path}, &bytes.Buffer{}, in.stderr) }()
	waitFor(t, "decree-go-rest to listen", func() bool {
		select {
		case code := <-in.exited:
			t.Fatalf("decree-go-rest exited %d: %s", code, in.stderr)
		default:
		}
		resp, err := http.Get("http://" + in.addr + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	})
	return in
}

// health returns the status and body of GET /healthz.
func (in *instance) health(t *testing.T) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get("http://" + in.addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

// terminate sends decree-go-rest SIGTERM and returns its exit code.
func (in *instance) terminate(t *testing.T, within time.Duration) int {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	return in.wait(t, within)
}

func (in *instance) wait(t *testing.T, within time.Duration) int {
	t.Helper()
	select {
	case code := <-in.exited:
		return code
	case <-time.After(within):
		t.Fatalf("decree-go-rest did not exit within %v; stderr:\n%s", within, in.stderr)
		return -1
	}
}

// daemonPID waits for the stub daemon to record its start and returns its
// pid.
func daemonPID(t *testing.T, stub *decreetest.Stub) int {
	t.Helper()
	waitFor(t, "the daemon to start", func() bool { return len(stub.DaemonEvents(t, "start")) > 0 })
	return stub.DaemonEvents(t, "start")[0].PID
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// TestAcceptanceDaemonRestarts is the first acceptance criterion: with a
// stub daemon that exits after one second, decree-go-rest restarts it with
// growing delays, and /healthz is 503 while it is down.
func TestAcceptanceDaemonRestarts(t *testing.T) {
	path, stub := exampleProject(t)
	stub.SetDaemonLife(t, time.Second)
	in := serve(t, path)

	// Poll /healthz until the third start: up 1 s, down 1 s, up 1 s,
	// down 2 s, up.
	statuses := map[int]int{}
	var downBody map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for len(stub.DaemonEvents(t, "start")) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d daemon starts", len(stub.DaemonEvents(t, "start")))
		}
		code, body := in.health(t)
		statuses[code]++
		if code == http.StatusServiceUnavailable {
			downBody = body
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code := in.terminate(t, 10*time.Second); code != 0 {
		t.Errorf("exit %d; stderr:\n%s", code, in.stderr)
	}

	if statuses[http.StatusOK] == 0 || statuses[http.StatusServiceUnavailable] == 0 {
		t.Errorf("healthz statuses %v, want both 200 and 503", statuses)
	}
	if downBody != nil {
		d := downBody["daemon"].(map[string]any)
		if d["running"] != false || d["pid"] != nil || downBody["ok"] != false {
			t.Errorf("503 body %v", downBody)
		}
	}

	starts, exits := stub.DaemonEvents(t, "start"), stub.DaemonEvents(t, "exit")
	if len(starts) < 3 || len(exits) < 2 {
		t.Fatalf("starts %v, exits %v", starts, exits)
	}
	first := starts[1].Time.Sub(exits[0].Time)
	second := starts[2].Time.Sub(exits[1].Time)
	t.Logf("restart delays: %v, %v", first, second)
	if first < 900*time.Millisecond || first > 1700*time.Millisecond {
		t.Errorf("first restart after %v, want about 1s", first)
	}
	if second < 1900*time.Millisecond || second > 2700*time.Millisecond {
		t.Errorf("second restart after %v, want about 2s", second)
	}
	for _, c := range stub.Calls(t) {
		if strings.Contains(c, " daemon ") && !strings.HasSuffix(c, " daemon --interval 2s") {
			t.Errorf("daemon call %q", c)
		}
	}
	logs := in.stderr.String()
	for _, w := range []string{`"msg":"daemon exited"`, `"status":"exit status 3"`, `"msg":"daemon restarted"`, `"source":"daemon","stream":"stdout"`, `"source":"daemon","stream":"stderr"`} {
		if !strings.Contains(logs, w) {
			t.Errorf("stderr lacks %s", w)
		}
	}
}

// TestAcceptanceShutdownOrder is the first half of the second acceptance
// criterion: on SIGTERM, the daemon gets SIGTERM only after the in-flight
// request has finished, and is not killed when it exits on it.
func TestAcceptanceShutdownOrder(t *testing.T) {
	path, stub := exampleProject(t)
	stub.SetEmitSleep(t, "1")
	in := serve(t, path)
	pid := daemonPID(t, stub)

	status := make(chan int, 1)
	go func() {
		r, _ := http.NewRequest("POST", "http://"+in.addr+"/notify/backup", strings.NewReader("x"))
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Errorf("in-flight request: %v", err)
			status <- 0
			return
		}
		resp.Body.Close()
		status <- resp.StatusCode
	}()
	waitFor(t, "decree emit to start", func() bool { return len(stub.Emits(t)) == 1 })
	if code := in.terminate(t, 10*time.Second); code != 0 {
		t.Errorf("exit %d; stderr:\n%s", code, in.stderr)
	}
	if got := <-status; got != http.StatusCreated {
		t.Errorf("in-flight request: %d, want 201", got)
	}

	var order []string
	for _, e := range stub.Events(t) {
		if e.Kind != "start" {
			order = append(order, e.Kind)
		}
	}
	if strings.Join(order, " ") != "emit-done term" {
		t.Errorf("events %q, want the emit to finish before the daemon's SIGTERM", order)
	}
	if terms := stub.DaemonEvents(t, "term"); len(terms) != 1 || terms[0].PID != pid {
		t.Errorf("SIGTERMs %+v, want one to %d", terms, pid)
	}
	if alive(pid) {
		t.Error("the daemon outlived decree-go-rest")
	}
	if logs := in.stderr.String(); strings.Contains(logs, "killing it") {
		t.Errorf("killed a daemon that exited on SIGTERM:\n%s", logs)
	}
}

// TestAcceptanceShutdownKillsAfter15s is the second half: a daemon that
// ignores SIGTERM gets SIGKILL only after 15 s.
func TestAcceptanceShutdownKillsAfter15s(t *testing.T) {
	if testing.Short() {
		t.Skip("takes 15 s")
	}
	path, stub := exampleProject(t)
	stub.SetDaemonIgnoreTerm(t, true)
	in := serve(t, path)
	pid := daemonPID(t, stub)

	sent := time.Now()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	time.Sleep(14 * time.Second)
	if !alive(pid) {
		t.Fatalf("the daemon was killed before 15 s: %+v\n%s", stub.Events(t), in.stderr)
	}
	select {
	case code := <-in.exited:
		t.Fatalf("decree-go-rest exited %d before 15 s", code)
	default:
	}
	if code := in.wait(t, 5*time.Second); code != 0 {
		t.Errorf("exit %d; stderr:\n%s", code, in.stderr)
	}
	if took := time.Since(sent); took < 15*time.Second {
		t.Errorf("exited after %v, want at least 15s", took)
	}
	if alive(pid) {
		t.Error("the daemon is alive after SIGKILL")
	}
	logs := in.stderr.String()
	for _, w := range []string{"killing it", `"status":"signal: killed"`} {
		if !strings.Contains(logs, w) {
			t.Errorf("stderr lacks %s:\n%s", w, logs)
		}
	}
}

// TestSecondInstanceRefused: a second decree-go-rest for the same project
// exits 1 with an error naming the lock, and starts no daemon.
func TestSecondInstanceRefused(t *testing.T) {
	path, stub := exampleProject(t)
	in := serve(t, path)
	daemonPID(t, stub)

	code, _, errOut := runCLI("-config", path)
	lock := filepath.Join(filepath.Dir(path), ".decree", daemon.LockName)
	if code != 1 || !strings.Contains(errOut, lock) || !strings.Contains(errOut, "another decree-go-rest is running") {
		t.Errorf("second instance: exit %d, stderr %q; want 1 naming %s", code, errOut, lock)
	}
	if n := len(stub.DaemonEvents(t, "start")); n != 1 {
		t.Errorf("%d daemon starts, want 1", n)
	}
	if code := in.terminate(t, 10*time.Second); code != 0 {
		t.Errorf("first instance: exit %d", code)
	}
	// The lock is released on exit.
	l, err := daemon.Acquire(filepath.Dir(path))
	if err != nil {
		t.Fatalf("lock after exit: %v", err)
	}
	l.Unlock()
}

// TestDaemonDisabled: with daemon.enabled false no daemon runs, and
// /healthz is 200.
func TestDaemonDisabled(t *testing.T) {
	path, stub := exampleProject(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, strings.Replace(string(raw), "enabled: true ", "enabled: false ", 1))
	in := serve(t, path)
	if code, body := in.health(t); code != http.StatusOK || body["daemon"].(map[string]any)["enabled"] != false {
		t.Errorf("healthz %d %v", code, body)
	}
	if code := in.terminate(t, 10*time.Second); code != 0 {
		t.Errorf("exit %d", code)
	}
	if n := len(stub.DaemonEvents(t, "start")); n != 0 {
		t.Errorf("%d daemon starts, want none", n)
	}
}

// TestDaemonDisabledReadOnly: without the daemon, decree-go-rest takes no
// lock and so starts on a read-only .decree/, as the container mounts it.
func TestDaemonDisabledReadOnly(t *testing.T) {
	path, _ := exampleProject(t)
	t.Setenv(config.DaemonEnv, "false")
	decreeDir := filepath.Join(filepath.Dir(path), ".decree")
	if err := os.Chmod(decreeDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(decreeDir, 0o755) })
	in := serve(t, path)
	if code, body := in.health(t); code != http.StatusOK || body["daemon"].(map[string]any)["enabled"] != false {
		t.Errorf("healthz %d %v", code, body)
	}
	if code := in.terminate(t, 10*time.Second); code != 0 {
		t.Errorf("exit %d", code)
	}
	if _, err := os.Stat(daemon.LockFile(filepath.Dir(path))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("lock file: %v, want none", err)
	}
}

// TestHealthcheckExitCodes: -healthcheck exits 0 while /healthz is 200,
// and 1 when it is 503, nothing listens, or the config cannot be read.
func TestHealthcheckExitCodes(t *testing.T) {
	path, stub := exampleProject(t)
	in := serve(t, path)
	daemonPID(t, stub)

	if code, out, errOut := runCLI("-healthcheck", "-config", path); code != 0 || !strings.Contains(out, `"ok":true`) {
		t.Errorf("healthy: exit %d, stdout %q, stderr %q; want 0", code, out, errOut)
	}
	// While the daemon is down, between its exit and the restart 1 s on.
	stub.ExitDaemon(t, 1)
	var errOut string
	waitFor(t, "-healthcheck to fail", func() bool {
		var code int
		code, _, errOut = runCLI("-healthcheck", "-config", path)
		return code == 1
	})
	if !strings.Contains(errOut, "503") {
		t.Errorf("stderr %q, want the 503", errOut)
	}
	waitFor(t, "-healthcheck to pass again", func() bool {
		code, _, _ := runCLI("-healthcheck", "-config", path)
		return code == 0
	})
	if code := in.terminate(t, 10*time.Second); code != 0 {
		t.Errorf("exit %d", code)
	}

	if code, _, errOut := runCLI("-healthcheck", "-config", path); code != 1 || !strings.Contains(errOut, "healthcheck") {
		t.Errorf("nothing listening: exit %d, stderr %q; want 1", code, errOut)
	}
	if code, _, _ := runCLI("-healthcheck", "-config", filepath.Join(t.TempDir(), "missing.yml")); code != 1 {
		t.Errorf("missing config: exit %d, want 1", code)
	}
	if code, _, _ := runCLI("-healthcheck", "-check", "-config", path); code != 2 {
		t.Errorf("-healthcheck with -check: exit %d, want 2", code)
	}
}

func TestHealthURL(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:8801": "http://127.0.0.1:8801/healthz",
		":8801":          "http://127.0.0.1:8801/healthz",
		"0.0.0.0:8801":   "http://127.0.0.1:8801/healthz",
		"[::]:8801":      "http://[::1]:8801/healthz",
		"[::1]:9":        "http://[::1]:9/healthz",
		"localhost:80":   "http://localhost:80/healthz",
	} {
		got, err := healthURL(in)
		if err != nil || got != want {
			t.Errorf("healthURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := healthURL("8801"); err == nil {
		t.Error("healthURL(8801) succeeded")
	}
}
