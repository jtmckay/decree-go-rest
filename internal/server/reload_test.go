package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtmckay/decree-api/internal/config"
)

const extraEndpoint = `
  - path: /extra
    body: optional
    message: { machine: flags }
`

// replaceFile replaces path as an editor does: a new file renamed over
// the old one, with a modification time that is surely different.
func replaceFile(t *testing.T, path, content string, mod time.Time) {
	t.Helper()
	tmp := filepath.Join(filepath.Dir(path), ".decree-api.yml.tmp")
	write(t, tmp, content)
	if err := os.Chtimes(tmp, mod, mod); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) live(t *testing.T) *Live {
	t.Helper()
	l := NewLive(f.path, f.cfg, f.srv)
	l.PollInterval = 20 * time.Millisecond
	l.Settle = 50 * time.Millisecond
	return l
}

// runForTest polls the config until the test ends.
func (l *Live) runForTest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); l.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func liveDo(t *testing.T, l *Live, r req) int {
	t.Helper()
	if r.method == "" {
		r.method = http.MethodPost
	}
	hr := httptest.NewRequest(r.method, "http://decree-api.test"+r.path, strings.NewReader(r.body))
	if r.bearer != "" {
		hr.Header.Set("Authorization", "Bearer "+r.bearer)
	}
	rec := httptest.NewRecorder()
	l.ServeHTTP(rec, hr)
	return rec.Code
}

// TestReloadSwapsOnValidChange: polling sees the replaced file, and after
// the settle the new route table serves.
func TestReloadSwapsOnValidChange(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	logs := captureLogs(f.srv)
	l := f.live(t)
	before, _ := l.ConfigState()
	l.runForTest(t)

	if got := liveDo(t, l, req{path: "/extra", bearer: secret}); got != http.StatusNotFound {
		t.Fatalf("/extra before the reload: %d, want 404", got)
	}
	cfg := strings.Replace(exampleConfig(t), "  - path: /notify\n", extraEndpoint+"\n  - path: /notify\n", 1)
	replaceFile(t, f.path, cfg, time.Now().Add(time.Minute))
	waitFor(t, "the reload", func() bool { return liveDo(t, l, req{path: "/extra", bearer: secret}) == http.StatusCreated })

	loadedAt, err := l.ConfigState()
	if err != nil || !loadedAt.After(before) {
		t.Errorf("ConfigState = %v, %v; want a later load and no error", loadedAt, err)
	}
	if got := liveDo(t, l, req{path: "/notify/backup", bearer: secret, body: "x"}); got != http.StatusCreated {
		t.Errorf("/notify/backup after the reload: %d, want 201", got)
	}
	if !strings.Contains(logs.String(), `"msg":"config reloaded"`) {
		t.Errorf("no reload record:\n%s", logs)
	}
	// The stub's calls: validation's --version and check at startup, and
	// the emits; a reload that changes neither decree nor project runs no
	// decree check.
	for _, c := range f.stub.Calls(t)[2:] {
		if !strings.Contains(c, " emit ") {
			t.Errorf("reload ran decree: %s", c)
		}
	}
}

// TestAcceptanceInvalidReloadKeepsOld is the second acceptance criterion
// of 03: after the config is replaced with an invalid one, requests still
// use the old routes, and the error is recorded until a valid one loads.
func TestAcceptanceInvalidReloadKeepsOld(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	logs := captureLogs(f.srv)
	l := f.live(t)
	l.runForTest(t)

	invalid := strings.Replace(exampleConfig(t), "machine: comfy_image", "machine: missing", 1) + extraEndpoint
	replaceFile(t, f.path, invalid, time.Now().Add(time.Minute))
	waitFor(t, "the failed reload", func() bool { _, err := l.ConfigState(); return err != nil })

	_, err := l.ConfigState()
	var errs config.Errors
	if !errors.As(err, &errs) || !strings.Contains(err.Error(), "machine missing") {
		t.Fatalf("recorded error %v, want the validation errors", err)
	}
	for _, c := range []struct {
		r      req
		status int
	}{
		{req{path: "/notify/backup", bearer: secret, body: "x"}, 201},
		{req{path: "/comfy/a/b", bearer: comfySecret, body: "x"}, 201},
		{req{path: "/extra", bearer: secret}, 404},
	} {
		if got := liveDo(t, l, c.r); got != c.status {
			t.Errorf("%s: %d, want %d", c.r.path, got, c.status)
		}
	}
	if e := f.stub.Emits(t)[1]; !strings.Contains(strings.Join(e.Args, " "), "--machine comfy_image") {
		t.Errorf("the old route emitted %q", e.Args)
	}
	if !strings.Contains(logs.String(), "machine missing") {
		t.Errorf("the error is not logged:\n%s", logs)
	}

	// Unparseable YAML is recorded too.
	replaceFile(t, f.path, "endpoints: [", time.Now().Add(2*time.Minute))
	waitFor(t, "the second failed reload", func() bool {
		_, err := l.ConfigState()
		return err != nil && !strings.Contains(err.Error(), "machine missing")
	})
	if got := liveDo(t, l, req{path: "/notify/backup", bearer: secret, body: "x"}); got != 201 {
		t.Errorf("after unparseable YAML: %d, want 201", got)
	}

	// A valid config clears the error.
	replaceFile(t, f.path, exampleConfig(t)+extraEndpoint, time.Now().Add(3*time.Minute))
	waitFor(t, "the valid reload", func() bool { _, err := l.ConfigState(); return err == nil })
	if got := liveDo(t, l, req{path: "/extra", bearer: secret}); got != 201 {
		t.Errorf("/extra after the valid reload: %d, want 201", got)
	}
}

// TestReloadInFlightFinishesOnOldTable: a request that started before the
// swap finishes on the route table it started on.
func TestReloadInFlightFinishesOnOldTable(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	f.stub.SetEmitSleep(t, "1")
	l := f.live(t)
	status := make(chan int, 1)
	go func() { status <- liveDo(t, l, req{path: "/comfy/a/b", bearer: comfySecret, body: "x"}) }()
	waitFor(t, "decree emit to start", func() bool { return len(f.stub.Emits(t)) == 1 })

	// The new config has no /comfy endpoint at all.
	cfg := exampleConfig(t)
	cfg = cfg[:strings.Index(cfg, "  - path: /comfy/")]
	write(t, f.path, cfg)
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := liveDo(t, l, req{path: "/comfy/a/b", bearer: comfySecret, body: "x"}); got != http.StatusNotFound {
		t.Errorf("/comfy after the reload: %d, want 404", got)
	}
	if got := <-status; got != http.StatusCreated {
		t.Errorf("in-flight /comfy: %d, want 201", got)
	}
}

// TestReloadLimitsAndBudgets: new limits apply, and what was spent stays
// spent across the reload.
func TestReloadLimitsAndBudgets(t *testing.T) {
	f := newFixture(t, limitsConfig(t, 60, 3))
	f.clock()
	l := f.live(t)
	for i := 0; i < 3; i++ {
		liveDo(t, l, req{path: "/nope"})
	}
	write(t, f.path, limitsConfig(t, 60, 4))
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := liveDo(t, l, req{path: "/nope"}); got != http.StatusNotFound {
		t.Errorf("fourth failure with rate_fail_max 4: %d, want 404", got)
	}
	if got := liveDo(t, l, req{path: "/nope"}); got != http.StatusTooManyRequests {
		t.Errorf("fifth failure: %d, want 429", got)
	}
}

// TestReloadRestartOnlyKeys: listen, project and daemon.* log a warning;
// the reload still applies the rest, and the project being served stays.
func TestReloadRestartOnlyKeys(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	logs := captureLogs(f.srv)
	l := f.live(t)

	// A second project, valid on its own, with the same machines.
	other := t.TempDir()
	for _, m := range []string{"notify", "comfy_image", "flags"} {
		raw, err := os.ReadFile(config.MachineFile(f.proj, m))
		if err != nil {
			t.Fatal(err)
		}
		write(t, config.MachineFile(other, m), string(raw))
	}
	cfg := exampleConfig(t)
	cfg = strings.Replace(cfg, "listen: 127.0.0.1:8801", "listen: 127.0.0.1:9901", 1)
	cfg = strings.Replace(cfg, "project: .", "project: "+other, 1)
	cfg = strings.Replace(cfg, "enabled: true", "enabled: false", 1)
	cfg = strings.Replace(cfg, "interval: 2s", "interval: 5s", 1)
	write(t, f.path, cfg+extraEndpoint)
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	var warned []string
	for _, m := range logs.records(t) {
		if m["level"] == "WARN" {
			warned = append(warned, fmt.Sprint(m["key"]))
		}
	}
	if got := strings.Join(warned, " "); got != "listen project daemon.enabled daemon.interval" {
		t.Errorf("warnings for %q", got)
	}
	if got := liveDo(t, l, req{path: "/extra", bearer: secret}); got != http.StatusCreated {
		t.Fatalf("/extra: %d, want 201", got)
	}
	if e := f.stub.Emits(t)[0]; e.Dir != f.proj {
		t.Errorf("decree ran in %s, want the running project %s", e.Dir, f.proj)
	}
	// The project changed, so step 7 ran: decree check in the new project.
	if calls := f.stub.Calls(t); !strings.Contains(strings.Join(calls, "\n"), "cwd="+other+" check") {
		t.Errorf("no decree check in the new project: %q", calls)
	}
}

// TestReloadRunsDecreeCheckWhenDecreeChanges: step 7 runs, and fails the
// reload, only when decree or project changed.
func TestReloadRunsDecreeCheckWhenDecreeChanges(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	l := f.live(t)
	f.stub.SetCheck(t, `{"errors":[{"file":"machines/bad.yml","line":1,"message":"bad","rule":null}],"valid":false,"warnings":[]}`, 1)

	write(t, f.path, exampleConfig(t)+extraEndpoint)
	if err := l.Reload(); err != nil {
		t.Fatalf("same decree: %v", err)
	}
	write(t, f.path, strings.Replace(exampleConfig(t), "decree: decree", "decree: "+f.stub.Path, 1))
	if err := l.Reload(); err == nil {
		t.Fatal("a changed decree whose check fails: reload succeeded")
	}
	if got := liveDo(t, l, req{path: "/extra", bearer: secret}); got != http.StatusCreated {
		t.Errorf("/extra: %d, want 201 from the kept config", got)
	}
}

// TestReloadRace: concurrent requests while the config is reloaded over
// and over. Run with -race.
func TestReloadRace(t *testing.T) {
	base := limitsConfig(t, 100000, 100000)
	f := newFixture(t, base)
	captureLogs(f.srv)
	l := f.live(t)
	l.PollInterval = 5 * time.Millisecond
	l.Settle = 5 * time.Millisecond
	l.runForTest(t)
	ts := httptest.NewServer(l)
	defer ts.Close()

	configs := []string{
		base,
		base + extraEndpoint,
		strings.Replace(base, "machine: notify", "machine: missing", 1),
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			paths := []string{"/notify/a$b", "/nope", "/extra", "/comfy/a/b"}
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				r, _ := http.NewRequest("POST", ts.URL+paths[(g+i)%len(paths)], strings.NewReader("x"))
				r.Header.Set("Authorization", "Bearer "+secret)
				resp, err := http.DefaultClient.Do(r)
				if err != nil {
					t.Error(err)
					return
				}
				resp.Body.Close()
			}
		}(g)
	}
	for i := 0; i < 30; i++ {
		replaceFile(t, f.path, configs[i%len(configs)], time.Now().Add(time.Duration(i+1)*time.Minute))
		if i%2 == 0 {
			_ = l.Reload()
		}
		l.ConfigState()
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
}
