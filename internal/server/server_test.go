package server

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtmckay/decree-api/internal/config"
	"github.com/jtmckay/decree-api/internal/decreetest"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func TestMain(m *testing.M) {
	// As main does: decree inherits the service's umask.
	SetUmask()
	os.Exit(m.Run())
}

var (
	secret      = strings.Repeat("a", 64)
	comfySecret = strings.Repeat("b", 32)
)

const (
	notifyMachine = `name: notify
description: test machine
initial: send
data:
  title:    { type: string, default: Untitled }
  priority: { type: string, default: low }
states:
  send: { invoke: send, transitions: { done: done } }
  done: { final: true }
`
	comfyMachine = `name: comfy_image
description: test machine
initial: render
data:
  workflow:      { type: string, default: txt2img }
  output_prefix: { type: string, default: comfy/out }
states:
  render: { invoke: render, transitions: { done: done } }
  done: { final: true }
`
	flagMachine = `name: flags
description: test machine
initial: go
data:
  note:  { type: string, default: x }
  count: { type: int, default: 1 }
  loud:  { type: bool, default: false }
states:
  go: { invoke: go, transitions: { done: done } }
  done: { final: true }
`
)

// fixture is a temp project served by a Server, with a stub decree.
type fixture struct {
	srv  *Server
	stub *decreetest.Stub
	proj string
	// path is the config file, and cfg the config loaded from it.
	path string
	cfg  *config.Config
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exampleConfig(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../decree-api.example.yml")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// newFixture validates cfg in a temp project holding the test machines,
// as decree-api does at startup, and builds its Server.
func newFixture(t *testing.T, cfg string) *fixture {
	t.Helper()
	proj := t.TempDir()
	write(t, config.MachineFile(proj, "notify"), notifyMachine)
	write(t, config.MachineFile(proj, "comfy_image"), comfyMachine)
	write(t, config.MachineFile(proj, "flags"), flagMachine)
	path := filepath.Join(proj, "decree-api.yml")
	write(t, path, cfg)
	stub := decreetest.New(t)
	stub.OnPath(t)
	t.Setenv(config.ListenEnv, "")
	t.Setenv("DECREE_API_SECRET", secret)
	t.Setenv("COMFY_SECRET", comfySecret)
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if errs := config.Validate(c, config.Options{}); errs != nil {
		t.Fatalf("config invalid:\n%v", errs)
	}
	srv, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{srv: srv, stub: stub, proj: c.ProjectDir, path: path, cfg: c}
}

type req struct {
	method, path, bearer, body string
	header                     http.Header
}

func (f *fixture) do(t *testing.T, r req) *httptest.ResponseRecorder {
	t.Helper()
	if r.method == "" {
		r.method = http.MethodPost
	}
	hr := httptest.NewRequest(r.method, "http://decree-api.test"+r.path, strings.NewReader(r.body))
	for k, vs := range r.header {
		for _, v := range vs {
			hr.Header.Add(k, v)
		}
	}
	if r.bearer != "" {
		hr.Header.Set("Authorization", "Bearer "+r.bearer)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, hr)
	return rec
}

// errorBody checks that rec is a JSON error response and returns its message.
func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body, err)
	}
	msg, ok := body["error"].(string)
	if !ok || len(body) != 1 {
		t.Fatalf("body %q is not {\"error\": …}", rec.Body)
	}
	return msg
}

// TestAcceptanceNotifyBackup is the first acceptance criterion of 02.
func TestAcceptanceNotifyBackup(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	rec := f.do(t, req{path: "/notify/backup", bearer: secret, body: "disk 3 is full"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s; want 201", rec.Code, rec.Body)
	}
	emits := f.stub.Emits(t)
	if len(emits) != 1 {
		t.Fatalf("%d emits, want 1", len(emits))
	}
	e := emits[0]
	want := "emit --machine notify --param title=backup --param priority=high --format json"
	if got := strings.Join(e.Args, " "); got != want {
		t.Errorf("argv %q, want %q", got, want)
	}
	if e.Dir != f.proj {
		t.Errorf("cwd %q, want the project %q", e.Dir, f.proj)
	}
	if string(e.Stdin) != "disk 3 is full\n" {
		t.Errorf("stdin %q, want %q", e.Stdin, "disk 3 is full\n")
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	id := got["id"]
	if !strings.HasPrefix(id, "20261005T043125Z-") || got["path"] != ".decree/inbox/"+id+".md" || got["machine"] != "notify" || len(got) != 3 {
		t.Errorf("body %v, want the stub's id and path and machine notify", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

// TestGoldenEmits records the exact argv and stdin of `decree emit` for
// each endpoint of the example config, in testdata/emit.golden.
func TestGoldenEmits(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	cases := []req{
		{path: "/notify", bearer: secret, body: "hello"},
		{path: "/notify/backup", bearer: secret, body: "disk 3 is full"},
		{path: "/notify/disk%203.full_x-y", bearer: secret, body: "two\nlines\n"},
		{path: "/comfy/txt2img/cat_01.png", bearer: comfySecret, body: `{"prompt": "a cat"}`},
	}
	var out strings.Builder
	for i, c := range cases {
		rec := f.do(t, c)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: status %d, body %s", c.path, rec.Code, rec.Body)
		}
		e := f.stub.Emits(t)[i]
		fmt.Fprintf(&out, "POST %s\n", c.path)
		for _, a := range e.Args {
			fmt.Fprintf(&out, "  argv %q\n", a)
		}
		fmt.Fprintf(&out, "  stdin %q\n\n", e.Stdin)
	}
	golden(t, "emit.golden", out.String())
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		write(t, path, got)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to write it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs; got:\n%s\nwant:\n%s", path, got, want)
	}
}

func TestParamTypes(t *testing.T) {
	f := newFixture(t, `project: .
endpoints:
  - path: /flags/{note}
    body: optional
    message:
      machine: flags
      params: { count: 0x10, loud: True, note: 'n={{note}};{{note}}' }
`)
	rec := f.do(t, req{path: "/flags/%7B%7Bnote%7D%7D", bearer: secret})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("braces in the default pattern: status %d, want 400", rec.Code)
	}
	rec = f.do(t, req{path: "/flags/x!", bearer: secret})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	e := f.stub.Emits(t)[0]
	want := "emit --machine flags --param count=16 --param loud=true --param note=n=x!;x! --format json"
	if got := strings.Join(e.Args, " "); got != want {
		t.Errorf("argv %q, want %q", got, want)
	}
	if len(e.Stdin) != 0 {
		t.Errorf("stdin %q, want empty", e.Stdin)
	}
}

func TestStatuses(t *testing.T) {
	cfg := exampleConfig(t) + `
  - path: /quiet
    body: none
    message: { machine: notify }

  - path: /maybe
    body: optional
    message: { machine: notify }
`
	cfg = strings.Replace(cfg, "max_body_bytes: 262144", "max_body_bytes: 16", 1)
	long := strings.Repeat("x", MaxParamBytes+1)
	cases := []struct {
		name   string
		req    req
		status int
		allow  string
		emits  bool
	}{
		{"unknown path", req{path: "/nope", bearer: secret, body: "x"}, 404, "", false},
		{"unknown root", req{path: "/", bearer: secret, body: "x"}, 404, "", false},
		{"two trailing slashes", req{path: "/notify//", bearer: secret, body: "x"}, 404, "", false},
		{"dot segment", req{path: "/x/../notify", bearer: secret, body: "x"}, 404, "", false},
		{"empty parameter", req{path: "/comfy/a//b", bearer: comfySecret, body: "x"}, 404, "", false},
		{"too many segments", req{path: "/notify/a/b", bearer: secret, body: "x"}, 404, "", false},
		{"GET", req{method: "GET", path: "/notify/backup", bearer: secret}, 405, "POST", false},
		{"PUT with slash", req{method: "PUT", path: "/notify/", bearer: secret, body: "x"}, 405, "POST", false},
		{"no bearer", req{path: "/notify/backup", body: "x"}, 401, "", false},
		{"wrong bearer", req{path: "/notify/backup", bearer: strings.Repeat("c", 64), body: "x"}, 401, "", false},
		{"short bearer", req{path: "/notify/backup", bearer: secret[:63], body: "x"}, 401, "", false},
		{"long bearer", req{path: "/notify/backup", bearer: secret + "a", body: "x"}, 401, "", false},
		{"another endpoint's secret", req{path: "/comfy/a/b", bearer: secret, body: "x"}, 401, "", false},
		{"basic auth", req{path: "/notify/backup", body: "x", header: http.Header{"Authorization": {"Basic " + secret}}}, 401, "", false},
		{"no token", req{path: "/notify/backup", body: "x", header: http.Header{"Authorization": {"Bearer"}}}, 401, "", false},
		{"two headers", req{path: "/notify/backup", body: "x", header: http.Header{"Authorization": {"Bearer " + secret, "Bearer " + secret}}}, 401, "", false},
		{"bad parameter", req{path: "/notify/a$b", bearer: secret, body: "x"}, 400, "", false},
		{"parameter too long", req{path: "/comfy/a/" + long, bearer: comfySecret, body: "x"}, 400, "", false},
		{"body required, empty", req{path: "/notify/backup", bearer: secret}, 400, "", false},
		{"body required, blank", req{path: "/notify/backup", bearer: secret, body: " \n\t"}, 400, "", false},
		{"body none, given", req{path: "/quiet", bearer: secret, body: "x"}, 400, "", false},
		{"body too large", req{path: "/notify/backup", bearer: secret, body: strings.Repeat("x", 17)}, 413, "", false},
		{"body at the cap", req{path: "/notify/backup", bearer: secret, body: strings.Repeat("x", 16)}, 201, "", true},
		{"body none", req{path: "/quiet", bearer: secret}, 201, "", true},
		{"body optional", req{path: "/maybe", bearer: secret}, 201, "", true},
		{"one trailing slash", req{path: "/notify/backup/", bearer: secret, body: "x"}, 201, "", true},
		{"lower-case scheme", req{path: "/notify/backup", body: "x", header: http.Header{"Authorization": {"bearer " + secret}}}, 201, "", true},
		{"parameter at the limit", req{path: "/comfy/a/" + long[1:], bearer: comfySecret, body: "x"}, 201, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, cfg)
			rec := f.do(t, c.req)
			if rec.Code != c.status {
				t.Fatalf("status %d, body %s; want %d", rec.Code, rec.Body, c.status)
			}
			if rec.Code != http.StatusCreated {
				errorBody(t, rec)
			}
			if got := rec.Header().Get("Allow"); got != c.allow {
				t.Errorf("Allow = %q, want %q", got, c.allow)
			}
			if c.status == 401 && rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
			}
			if n := len(f.stub.Emits(t)); (n > 0) != c.emits {
				t.Errorf("decree emit ran %d times", n)
			}
		})
	}
}

// TestRejectedRequestsRunNoDecree is the second acceptance criterion of 02.
func TestRejectedRequestsRunNoDecree(t *testing.T) {
	f := newFixture(t, strings.Replace(exampleConfig(t), "max_body_bytes: 262144", "max_body_bytes: 8", 1))
	for _, c := range []struct {
		req    req
		status int
	}{
		{req{method: "GET", path: "/notify/backup", bearer: secret}, 405},
		{req{path: "/notify/backup", bearer: strings.Repeat("z", 64), body: "x"}, 401},
		{req{path: "/notify/back%2Fup", bearer: secret, body: "x"}, 400},
		{req{path: "/notify/backup", bearer: secret, body: "disk 3 is full"}, 413},
	} {
		rec := f.do(t, c.req)
		if rec.Code != c.status {
			t.Errorf("%s %s: status %d, want %d", c.req.method, c.req.path, rec.Code, c.status)
		}
		errorBody(t, rec)
	}
	if calls := f.stub.Calls(t); len(calls) != 2 {
		t.Errorf("decree calls %q, want only validation's --version and check", calls)
	}
}

// TestBodyTooLargeChunked covers a body without Content-Length.
func TestBodyTooLargeChunked(t *testing.T) {
	f := newFixture(t, strings.Replace(exampleConfig(t), "max_body_bytes: 262144", "max_body_bytes: 8", 1))
	r := httptest.NewRequest("POST", "/notify/backup", io.MultiReader(strings.NewReader("12345"), strings.NewReader("6789")))
	r.ContentLength = -1
	r.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", rec.Code)
	}
	if len(f.stub.Emits(t)) != 0 {
		t.Error("decree emit ran")
	}
}

// TestParameterOutsidePattern: a parameter is rejected, never repaired,
// and never reaches decree.
func TestParameterOutsidePattern(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	for _, p := range []string{
		"/notify/a%2Fb",                      // decodes to a/b
		"/notify/%252F",                      // decoded once: %2F
		"/notify/a;b",                        // outside [A-Za-z0-9 _.-]
		"/notify/" + strings.Repeat("x", 81), // over {1,80}
		"/notify/%C3%A9",                     // é
		"/notify/a%0Ab",                      // newline
		"/comfy/TXT/x",                       // upper case outside [a-z0-9-]
		"/comfy/a/b%20c",                     // space outside [A-Za-z0-9_.-]
	} {
		bearer := secret
		if strings.HasPrefix(p, "/comfy/") {
			bearer = comfySecret
		}
		rec := f.do(t, req{path: p, bearer: bearer, body: "x"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", p, rec.Code)
			continue
		}
		if msg := errorBody(t, rec); !strings.Contains(msg, "does not match its pattern") {
			t.Errorf("%s: error %q", p, msg)
		}
	}
	if n := len(f.stub.Emits(t)); n != 0 {
		t.Errorf("decree emit ran %d times", n)
	}
}

func TestDecreeEnvironmentNeverPassed(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	t.Setenv("DECREE_RUN_ID", "20261005T000000Z-abcdef")
	t.Setenv("DECREE_MACHINE", "develop")
	t.Setenv("DECREE_DATA_TITLE", "leak")
	t.Setenv("TRACEPARENT", "00-11111111111111111111111111111111-2222222222222222-01")
	t.Setenv("TRACESTATE", "a=b")
	t.Setenv("DECREE_API_TEST_KEPT", "no") // a DECREE_ prefix all the same
	t.Setenv("KEPT_VAR", "yes")
	if rec := f.do(t, req{path: "/notify/backup", bearer: secret, body: "x"}); rec.Code != 201 {
		t.Fatalf("status %d", rec.Code)
	}
	e := f.stub.Emits(t)[0]
	for _, kv := range e.Env {
		if strings.HasPrefix(kv, "DECREE_") || strings.HasPrefix(kv, "TRACE") {
			t.Errorf("decree saw %s", kv)
		}
	}
	if v, ok := e.Getenv("KEPT_VAR"); v != "yes" || !ok {
		t.Errorf("KEPT_VAR = %q, %v; the rest of the environment is kept", v, ok)
	}
}

func TestTraceContext(t *testing.T) {
	const valid = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	cases := []struct {
		name   string
		header http.Header
		parent string
		state  string
	}{
		{"valid", http.Header{"Traceparent": {valid}}, valid, ""},
		{"valid with state", http.Header{"Traceparent": {valid}, "Tracestate": {"rojo=00f067aa0ba902b7, congo=t61rcWkgMzE", "vendor@sys=x"}}, valid, "rojo=00f067aa0ba902b7,congo=t61rcWkgMzE,vendor@sys=x"},
		{"future version with more fields", http.Header{"Traceparent": {"cc-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-what"}}, "cc-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-what", ""},
		{"invalid state dropped", http.Header{"Traceparent": {valid}, "Tracestate": {"Bad Key=1"}}, valid, ""},
		{"duplicate state key", http.Header{"Traceparent": {valid}, "Tracestate": {"a=1,a=2"}}, valid, ""},
		{"state without parent", http.Header{"Tracestate": {"a=1"}}, "", ""},
		{"garbage", http.Header{"Traceparent": {"garbage"}, "Tracestate": {"a=1"}}, "", ""},
		{"upper case hex", http.Header{"Traceparent": {strings.ToUpper(valid)}}, "", ""},
		{"zero trace id", http.Header{"Traceparent": {"00-00000000000000000000000000000000-00f067aa0ba902b7-01"}}, "", ""},
		{"zero parent id", http.Header{"Traceparent": {"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"}}, "", ""},
		{"version ff", http.Header{"Traceparent": {"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}}, "", ""},
		{"version 00 with more fields", http.Header{"Traceparent": {valid + "-x"}}, "", ""},
		{"two parents", http.Header{"Traceparent": {valid, valid}}, "", ""},
		{"injected newline", http.Header{"Traceparent": {valid + "\nDECREE_X=1"}}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, exampleConfig(t))
			if rec := f.do(t, req{path: "/notify/backup", bearer: secret, body: "x", header: c.header}); rec.Code != 201 {
				t.Fatalf("status %d", rec.Code)
			}
			e := f.stub.Emits(t)[0]
			parent, _ := e.Getenv("TRACEPARENT")
			state, _ := e.Getenv("TRACESTATE")
			if parent != c.parent || state != c.state {
				t.Errorf("TRACEPARENT %q, TRACESTATE %q; want %q, %q", parent, state, c.parent, c.state)
			}
		})
	}
}

// TestUmask: the messages decree writes are not world-readable.
func TestUmask(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	if rec := f.do(t, req{path: "/notify/backup", bearer: secret, body: "x"}); rec.Code != 201 {
		t.Fatalf("status %d", rec.Code)
	}
	e := f.stub.Emits(t)[0]
	if e.Umask != "0027" {
		t.Errorf("decree's umask %q, want 0027", e.Umask)
	}
	st, err := os.Stat(e.StdinFile)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o640 {
		t.Errorf("a file decree creates has mode %#o, want 0640", mode)
	}
}

func TestEmitFailures(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, f *fixture)
		status int
		msg    string
	}{
		{"exit 1", func(t *testing.T, f *fixture) {
			f.stub.SetEmit(t, 1, "error: param `priority` must be of type `int`\n")
		}, 400, "param `priority` must be of type `int`"},
		{"exit 1, no stderr", func(t *testing.T, f *fixture) {
			f.stub.SetEmit(t, 1, "")
		}, 400, "decree rejected the message"},
		{"exit 2", func(t *testing.T, f *fixture) {
			f.stub.SetEmit(t, 2, "error: secret detail\n")
		}, 500, queueFailed},
		{"unreadable output", func(t *testing.T, f *fixture) {
			f.stub.SetEmitOutput(t, "not json\n")
		}, 500, queueFailed},
		{"output without id", func(t *testing.T, f *fixture) {
			f.stub.SetEmitOutput(t, `{"path": ".decree/inbox/x.md"}`)
		}, 500, queueFailed},
		{"timeout", func(t *testing.T, f *fixture) {
			f.stub.SetEmitSleep(t, "5")
			f.srv.EmitTimeout = 200 * time.Millisecond
		}, 500, queueFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, exampleConfig(t))
			c.setup(t, f)
			rec := f.do(t, req{path: "/notify/backup", bearer: secret, body: "x"})
			if rec.Code != c.status {
				t.Fatalf("status %d, body %s; want %d", rec.Code, rec.Body, c.status)
			}
			if msg := errorBody(t, rec); msg != c.msg {
				t.Errorf("error %q, want %q", msg, c.msg)
			}
		})
	}
}
