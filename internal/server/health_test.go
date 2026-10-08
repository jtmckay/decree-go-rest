package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDaemon is a DaemonState a test sets.
type fakeDaemon struct {
	mu sync.Mutex
	st DaemonState
}

func (d *fakeDaemon) set(st DaemonState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st = st
}

func (d *fakeDaemon) state() DaemonState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st
}

// getHealth requests GET path with no Authorization, and decodes the body
// into a generic document, so the JSON names are checked too.
func getHealth(t *testing.T, l *Live, method, path string) (int, http.Header, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	l.ServeHTTP(rec, httptest.NewRequest(method, "http://decree-go-rest.test"+path, nil))
	var body map[string]any
	if method != http.MethodHead {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s %s: body %q: %v", method, path, rec.Body, err)
		}
	}
	return rec.Code, rec.Header(), body
}

func field(t *testing.T, doc map[string]any, path string) any {
	t.Helper()
	var v any = doc
	for _, k := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%s: not an object at %s", path, k)
		}
		if v, ok = m[k]; !ok {
			t.Fatalf("the body lacks %s", path)
		}
	}
	return v
}

// TestHealthDaemonRunning: 200 with the daemon's state and the config's,
// for a caller with no secret.
func TestHealthDaemonRunning(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	l := f.live(t)
	since := time.Date(2026, 10, 5, 4, 0, 0, 0, time.UTC)
	d := &fakeDaemon{}
	d.set(DaemonState{Running: true, PID: 4242, Restarts: 2, Since: since})
	l.Daemon = d.state

	code, h, body := getHealth(t, l, http.MethodGet, "/healthz")
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200; body %v", code, body)
	}
	if ct := h.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
	loadedAt, _ := l.ConfigState()
	for path, want := range map[string]any{
		"ok":               true,
		"daemon.enabled":   true,
		"daemon.running":   true,
		"daemon.pid":       float64(4242),
		"daemon.restarts":  float64(2),
		"daemon.since":     "2026-10-05T04:00:00Z",
		"config.loaded_at": loadedAt.UTC().Format(time.RFC3339Nano),
		"config.error":     nil,
	} {
		if got := field(t, body, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if code, _, _ := getHealth(t, l, http.MethodGet, "/healthz/"); code != http.StatusOK {
		t.Errorf("/healthz/: %d, want 200", code)
	}
	if code, _, _ := getHealth(t, l, http.MethodHead, "/healthz"); code != http.StatusOK {
		t.Errorf("HEAD: %d, want 200", code)
	}
}

// TestHealthDaemonDown: 503 with the same body while the daemon is not
// running, and 200 again once it is.
func TestHealthDaemonDown(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	l := f.live(t)
	d := &fakeDaemon{}
	d.set(DaemonState{Running: false, Restarts: 1, Since: time.Now()})
	l.Daemon = d.state

	code, _, body := getHealth(t, l, http.MethodGet, "/healthz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", code)
	}
	for path, want := range map[string]any{
		"ok": false, "daemon.enabled": true, "daemon.running": false,
		"daemon.pid": nil, "daemon.restarts": float64(1), "config.error": nil,
	} {
		if got := field(t, body, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	d.set(DaemonState{Running: true, PID: 7, Restarts: 1, Since: time.Now()})
	if code, _, _ := getHealth(t, l, http.MethodGet, "/healthz"); code != http.StatusOK {
		t.Errorf("after the restart: %d, want 200", code)
	}

	// Not reporting at all counts as down when the daemon is enabled.
	l.Daemon = nil
	if code, _, _ := getHealth(t, l, http.MethodGet, "/healthz"); code != http.StatusServiceUnavailable {
		t.Errorf("no daemon state: %d, want 503", code)
	}
}

// TestHealthDaemonDisabled: with daemon.enabled false, a daemon that is
// not running is healthy.
func TestHealthDaemonDisabled(t *testing.T) {
	cfg := strings.Replace(exampleConfig(t), "enabled: true ", "enabled: false ", 1)
	f := newFixture(t, cfg)
	l := f.live(t)
	code, _, body := getHealth(t, l, http.MethodGet, "/healthz")
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	for path, want := range map[string]any{
		"ok": true, "daemon.enabled": false, "daemon.running": false,
		"daemon.pid": nil, "daemon.since": nil, "daemon.restarts": float64(0),
	} {
		if got := field(t, body, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
}

// TestHealthAfterFailedReload: 503 with the error while the last reload
// failed, and 200 once a valid config loads.
func TestHealthAfterFailedReload(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	l := f.live(t)
	d := &fakeDaemon{}
	d.set(DaemonState{Running: true, PID: 7, Since: time.Now()})
	l.Daemon = d.state

	invalid := strings.Replace(exampleConfig(t), "machine: comfy_image", "machine: missing", 1)
	replaceFile(t, f.path, invalid, time.Now().Add(time.Minute))
	if err := l.Reload(); err == nil {
		t.Fatal("the invalid reload succeeded")
	}
	code, _, body := getHealth(t, l, http.MethodGet, "/healthz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("after a failed reload: %d, want 503", code)
	}
	if msg, _ := field(t, body, "config.error").(string); !strings.Contains(msg, "machine missing") {
		t.Errorf("config.error = %q, want the validation error", msg)
	}
	if field(t, body, "daemon.running") != true || field(t, body, "ok") != false {
		t.Errorf("body %v", body)
	}

	replaceFile(t, f.path, exampleConfig(t)+extraEndpoint, time.Now().Add(2*time.Minute))
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	code, _, body = getHealth(t, l, http.MethodGet, "/healthz")
	if code != http.StatusOK || field(t, body, "config.error") != nil {
		t.Errorf("after a valid reload: %d %v, want 200", code, body)
	}
}

// TestHealthExemptFromBudgets: /healthz is served when the failure budget
// is exhausted, and spends from neither budget.
func TestHealthExemptFromBudgets(t *testing.T) {
	cfg := strings.Replace(exampleConfig(t), "rate_fail_max: 10", "rate_fail_max: 1 ", 1)
	cfg = strings.Replace(cfg, "rate_max: 60 ", "rate_max: 1  ", 1)
	f := newFixture(t, cfg)
	l := f.live(t)
	d := &fakeDaemon{}
	d.set(DaemonState{Running: true, PID: 7, Since: time.Now()})
	l.Daemon = d.state

	for i := 0; i < 20; i++ {
		if code, _, _ := getHealth(t, l, http.MethodGet, "/healthz"); code != http.StatusOK {
			t.Fatalf("healthz %d: %d, want 200", i, code)
		}
	}
	// Both budgets are untouched: one failure and one authenticated
	// request still get their own answers.
	if got := liveDo(t, l, req{method: http.MethodGet, path: "/nope"}); got != http.StatusNotFound {
		t.Errorf("first failure: %d, want 404", got)
	}
	if got := liveDo(t, l, req{path: "/notify", bearer: secret, body: "x"}); got != http.StatusCreated {
		t.Errorf("first authenticated request: %d, want 201", got)
	}
	// The failure budget is now spent, and /healthz is still served.
	if got := liveDo(t, l, req{method: http.MethodGet, path: "/nope"}); got != http.StatusTooManyRequests {
		t.Errorf("second failure: %d, want 429", got)
	}
	if code, _, _ := getHealth(t, l, http.MethodGet, "/healthz"); code != http.StatusOK {
		t.Errorf("healthz with the budgets spent: %d, want 200", code)
	}
}

// TestHealthWrongMethod: another method on /healthz is a 405 with Allow,
// charged to the failure budget as on any known path.
func TestHealthWrongMethod(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	l := f.live(t)
	rec := httptest.NewRecorder()
	l.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://decree-go-rest.test/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST /healthz: %d, Allow %q; want 405 and GET, HEAD", rec.Code, rec.Header().Get("Allow"))
	}
}

// TestHealthLogged: a /healthz request is logged under its route.
func TestHealthLogged(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	logs := captureLogs(f.srv)
	l := f.live(t)
	getHealth(t, l, http.MethodGet, "/healthz")
	recs := requestRecords(t, logs)
	if len(recs) != 1 || recs[0]["route"] != "/healthz" || recs[0]["status"] != float64(503) {
		t.Errorf("request records %v", recs)
	}
}
