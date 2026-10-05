package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jtmckay/decree-api/internal/decreetest"
)

func TestRunStatus(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	rec := f.do(t, req{method: "GET", path: "/runs/20261005T043125Z-0a1b2c", bearer: secret})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s; want 200", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != decreetest.RunStatus {
		t.Errorf("body %q, want decree's document unchanged %q", got, decreetest.RunStatus)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	calls := f.stub.Invocations(t, "status")
	if len(calls) != 1 {
		t.Fatalf("%d status calls, want 1", len(calls))
	}
	want := []string{"status", "20261005T043125Z-0a1b2c", "--format", "json"}
	if got := calls[0].Args; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv %q, want %q", got, want)
	}
	if calls[0].Dir != f.proj {
		t.Errorf("cwd %q, want the project %q", calls[0].Dir, f.proj)
	}
}

func TestRunStatusStatuses(t *testing.T) {
	long := strings.Repeat("a", 129)
	cases := []struct {
		name   string
		setup  func(t *testing.T, f *fixture)
		req    req
		status int
		msg    string
		allow  string
		runs   bool
	}{
		{name: "found", req: req{method: "GET", path: "/runs/r.w3", bearer: secret}, status: 200, runs: true},
		{name: "HEAD", req: req{method: "HEAD", path: "/runs/r", bearer: secret}, status: 200, runs: true},
		{name: "trailing slash", req: req{method: "GET", path: "/runs/r/", bearer: secret}, status: 200, runs: true},
		{name: "id at the limit", req: req{method: "GET", path: "/runs/" + long[1:], bearer: secret}, status: 200, runs: true},
		{name: "unknown id", setup: func(t *testing.T, f *fixture) {
			f.stub.SetStatus(t, 1, "", "error: no run nope in .decree/runs/\n")
		}, req: req{method: "GET", path: "/runs/nope", bearer: secret}, status: 404, msg: "no run nope in .decree/runs/", runs: true},
		{name: "exit 1, no stderr", setup: func(t *testing.T, f *fixture) {
			f.stub.SetStatus(t, 1, "", "")
		}, req: req{method: "GET", path: "/runs/nope", bearer: secret}, status: 404, msg: "no such run", runs: true},
		{name: "exit 2", setup: func(t *testing.T, f *fixture) {
			f.stub.SetStatus(t, 2, "", "error: secret detail\n")
		}, req: req{method: "GET", path: "/runs/r", bearer: secret}, status: 500, msg: statusFailed, runs: true},
		{name: "unreadable output", setup: func(t *testing.T, f *fixture) {
			f.stub.SetStatus(t, 0, "Usage: decree status [OPTIONS] [ID]\n", "")
		}, req: req{method: "GET", path: "/runs/-h", bearer: secret}, status: 500, msg: statusFailed, runs: true},
		{name: "timeout", setup: func(t *testing.T, f *fixture) {
			f.stub.SetSleep(t, "status", "5")
			f.srv.EmitTimeout = 200 * time.Millisecond
		}, req: req{method: "GET", path: "/runs/r", bearer: secret}, status: 500, msg: statusFailed, runs: true},
		{name: "no bearer", req: req{method: "GET", path: "/runs/r"}, status: 401, msg: "unauthorized"},
		{name: "an endpoint's own secret", req: req{method: "GET", path: "/runs/r", bearer: comfySecret}, status: 401, msg: "unauthorized"},
		{name: "id too long", req: req{method: "GET", path: "/runs/" + long, bearer: secret}, status: 400, msg: "parameter id does not match its pattern"},
		{name: "id outside its pattern", req: req{method: "GET", path: "/runs/a%20b", bearer: secret}, status: 400, msg: "parameter id does not match its pattern"},
		{name: "POST", req: req{path: "/runs/r", bearer: secret}, status: 405, msg: "method not allowed", allow: "GET, HEAD"},
		{name: "too many segments", req: req{method: "GET", path: "/runs/r/x", bearer: secret}, status: 404, msg: "not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, exampleConfig(t))
			if c.setup != nil {
				c.setup(t, f)
			}
			rec := f.do(t, c.req)
			if rec.Code != c.status {
				t.Fatalf("status %d, body %s; want %d", rec.Code, rec.Body, c.status)
			}
			if c.msg != "" {
				if msg := errorBody(t, rec); msg != c.msg {
					t.Errorf("error %q, want %q", msg, c.msg)
				}
			}
			if got := rec.Header().Get("Allow"); got != c.allow {
				t.Errorf("Allow = %q, want %q", got, c.allow)
			}
			if n := len(f.stub.Invocations(t, "status")); (n > 0) != c.runs {
				t.Errorf("decree status ran %d times", n)
			}
		})
	}
}

func TestRunStatusDisabled(t *testing.T) {
	f := newFixture(t, strings.Replace(exampleConfig(t), "status: true", "status: false", 1))
	rec := f.do(t, req{method: "GET", path: "/runs/r", bearer: secret})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

// TestAcceptanceReply is the first acceptance criterion of 05.
func TestAcceptanceReply(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	const waitID = "20261005T043125Z-0a1b2c.w3"
	rec := f.do(t, req{path: "/runs/" + waitID + "/replies/approve", bearer: secret, body: "looks good"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s; want 201", rec.Code, rec.Body)
	}
	calls := f.stub.Invocations(t, "event")
	if len(calls) != 1 {
		t.Fatalf("%d event calls, want 1", len(calls))
	}
	want := []string{"event", waitID, "approve", "-m=looks good", "--format", "json"}
	if got := calls[0].Args; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv %q, want %q", got, want)
	}
	if calls[0].Dir != f.proj {
		t.Errorf("cwd %q, want the project %q", calls[0].Dir, f.proj)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "20261005T043200Z-3d4e5f" || got["path"] != ".decree/inbox/20261005T043200Z-3d4e5f.md" || len(got) != 2 {
		t.Errorf("body %v, want decree's {id, path}", got)
	}
}

// TestReplyNote: the note reaches decree as one argv entry, -m=<body>,
// unchanged, and no -m is passed for an empty body.
func TestReplyNote(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{"empty", "", []string{"event", "w", "approve", "--format", "json"}},
		{"plain", "ok", []string{"event", "w", "approve", "-m=ok", "--format", "json"}},
		{"starts with -", "-x note", []string{"event", "w", "approve", "-m=-x note", "--format", "json"}},
		{"an option", "--format", []string{"event", "w", "approve", "-m=--format", "--format", "json"}},
		{"=", "=x", []string{"event", "w", "approve", "-m==x", "--format", "json"}},
		{"no newline added", "two\nlines", []string{"event", "w", "approve", "-m=two\nlines", "--format", "json"}},
		{"trailing newline kept", "line\n", []string{"event", "w", "approve", "-m=line\n", "--format", "json"}},
		{"whitespace is a note", " ", []string{"event", "w", "approve", "-m= ", "--format", "json"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, exampleConfig(t))
			rec := f.do(t, req{path: "/runs/w/replies/approve", bearer: secret, body: c.body})
			if rec.Code != http.StatusCreated {
				t.Fatalf("status %d, body %s; want 201", rec.Code, rec.Body)
			}
			calls := f.stub.Invocations(t, "event")
			if len(calls) != 1 {
				t.Fatalf("%d event calls, want 1", len(calls))
			}
			if got := calls[0].Args; strings.Join(got, "\x00") != strings.Join(c.want, "\x00") {
				t.Errorf("argv %q, want %q", got, c.want)
			}
			if len(calls[0].Stdin) != 0 {
				t.Errorf("stdin %q, want none", calls[0].Stdin)
			}
		})
	}
}

func TestReplyStatuses(t *testing.T) {
	cfg := strings.Replace(exampleConfig(t), "max_body_bytes: 262144", "max_body_bytes: 16", 1)
	long := strings.Repeat("a", 129)
	cases := []struct {
		name   string
		setup  func(t *testing.T, f *fixture)
		req    req
		status int
		msg    string
		allow  string
		runs   bool
	}{
		{name: "queued", req: req{path: "/runs/r.w3/replies/approve", bearer: secret, body: "x"}, status: 201, runs: true},
		{name: "no note", req: req{path: "/runs/r/replies/reject", bearer: secret}, status: 201, runs: true},
		{name: "note at the cap", req: req{path: "/runs/r/replies/approve", bearer: secret, body: strings.Repeat("x", 16)}, status: 201, runs: true},
		{name: "trailing slash", req: req{path: "/runs/r/replies/approve/", bearer: secret}, status: 201, runs: true},
		{name: "not waiting", setup: func(t *testing.T, f *fixture) {
			f.stub.SetEvent(t, 1, "", "error: run r is not waiting\n")
		}, req: req{path: "/runs/r/replies/approve", bearer: secret}, status: 409, msg: "run r is not waiting", runs: true},
		{name: "exit 1, no stderr", setup: func(t *testing.T, f *fixture) {
			f.stub.SetEvent(t, 1, "", "")
		}, req: req{path: "/runs/r/replies/approve", bearer: secret}, status: 409, msg: "the run does not accept this reply", runs: true},
		{name: "exit 2", setup: func(t *testing.T, f *fixture) {
			f.stub.SetEvent(t, 2, "", "error: secret detail\n")
		}, req: req{path: "/runs/r/replies/approve", bearer: secret}, status: 500, msg: replyFailed, runs: true},
		{name: "unreadable output", setup: func(t *testing.T, f *fixture) {
			f.stub.SetEvent(t, 0, "Usage: decree event\n", "")
		}, req: req{path: "/runs/r/replies/approve", bearer: secret}, status: 500, msg: replyFailed, runs: true},
		{name: "output without id", setup: func(t *testing.T, f *fixture) {
			f.stub.SetEvent(t, 0, `{"path": ".decree/inbox/x.md"}`, "")
		}, req: req{path: "/runs/r/replies/approve", bearer: secret}, status: 500, msg: replyFailed, runs: true},
		{name: "timeout", setup: func(t *testing.T, f *fixture) {
			f.stub.SetSleep(t, "event", "5")
			f.srv.EmitTimeout = 200 * time.Millisecond
		}, req: req{path: "/runs/r/replies/approve", bearer: secret}, status: 500, msg: replyFailed, runs: true},
		{name: "note too large", req: req{path: "/runs/r/replies/approve", bearer: secret, body: strings.Repeat("x", 17)}, status: 413, msg: "the body is larger than 16 bytes"},
		{name: "NUL in the note", req: req{path: "/runs/r/replies/approve", bearer: secret, body: "a\x00b"}, status: 400, msg: "the note must not contain a NUL byte"},
		{name: "wait_id too long", req: req{path: "/runs/" + long + "/replies/approve", bearer: secret}, status: 400, msg: "parameter wait_id does not match its pattern"},
		{name: "event outside its pattern", req: req{path: "/runs/r/replies/a%2Fb", bearer: secret}, status: 400, msg: "parameter event does not match its pattern"},
		{name: "no bearer", req: req{path: "/runs/r/replies/approve"}, status: 401, msg: "unauthorized"},
		{name: "an endpoint's own secret", req: req{path: "/runs/r/replies/approve", bearer: comfySecret}, status: 401, msg: "unauthorized"},
		{name: "GET", req: req{method: "GET", path: "/runs/r/replies/approve", bearer: secret}, status: 405, msg: "method not allowed", allow: "POST"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, cfg)
			if c.setup != nil {
				c.setup(t, f)
			}
			rec := f.do(t, c.req)
			if rec.Code != c.status {
				t.Fatalf("status %d, body %s; want %d", rec.Code, rec.Body, c.status)
			}
			if c.msg != "" {
				if msg := errorBody(t, rec); msg != c.msg {
					t.Errorf("error %q, want %q", msg, c.msg)
				}
			}
			if got := rec.Header().Get("Allow"); got != c.allow {
				t.Errorf("Allow = %q, want %q", got, c.allow)
			}
			if n := len(f.stub.Invocations(t, "event")); (n > 0) != c.runs {
				t.Errorf("decree event ran %d times", n)
			}
		})
	}
}

func TestRepliesDisabled(t *testing.T) {
	f := newFixture(t, strings.Replace(exampleConfig(t), "replies: true", "replies: false", 1))
	rec := f.do(t, req{path: "/runs/r/replies/approve", bearer: secret})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

// builtinRequests are a request to each built-in under /runs/ that
// decree answers with success.
var builtinRequests = []struct {
	cmd, route string
	req        req
	status     int
}{
	{"status", "/runs/{id}", req{method: "GET", path: "/runs/r", bearer: secret}, http.StatusOK},
	{"event", "/runs/{wait_id}/replies/{event}", req{path: "/runs/r.w1/replies/approve", bearer: secret, body: "note"}, http.StatusCreated},
}

// TestBuiltinsBudgets: the built-ins spend from the same budgets as the
// configured endpoints (SPEC.md §6).
func TestBuiltinsBudgets(t *testing.T) {
	for _, b := range builtinRequests {
		t.Run(b.cmd, func(t *testing.T) {
			f := newFixture(t, limitsConfig(t, 2, 2))
			f.clock()
			// Failed authentication spends the failure budget...
			bad := b.req
			bad.bearer = wrongBearer
			for i, want := range []int{401, 401, 429} {
				if rec := f.do(t, bad); rec.Code != want {
					t.Fatalf("bad bearer %d: status %d, want %d", i+1, rec.Code, want)
				}
			}
			// ...which does not lock out a caller with the secret, who
			// spends rate_max together with the configured endpoints.
			if rec := f.do(t, b.req); rec.Code != b.status {
				t.Fatalf("status %d, body %s; want %d", rec.Code, rec.Body, b.status)
			}
			if rec := f.do(t, req{path: "/notify/backup", bearer: secret, body: "x"}); rec.Code != http.StatusCreated {
				t.Fatalf("endpoint: status %d, want 201", rec.Code)
			}
			rec := f.do(t, b.req)
			if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
				t.Fatalf("rate_max spent: status %d, Retry-After %q; want 429 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
			}
		})
	}
}

// TestBuiltins400SpendsRequestBudget: a parameter outside its pattern from
// an authenticated caller spends rate_max, not the failure budget.
func TestBuiltins400SpendsRequestBudget(t *testing.T) {
	f := newFixture(t, limitsConfig(t, 60, 1))
	f.clock()
	for i := 0; i < 3; i++ {
		if rec := f.do(t, req{method: "GET", path: "/runs/a%20b", bearer: secret}); rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d, want 400", rec.Code)
		}
	}
	if rec := f.do(t, req{method: "GET", path: "/runs/r", bearer: wrongBearer}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401: the failure budget is unspent", rec.Code)
	}
}

// TestBuiltinsEnvironment: decree status and event get the environment of
// SPEC.md §4 step 6: no DECREE_*, and the request's valid trace context.
func TestBuiltinsEnvironment(t *testing.T) {
	const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	for _, b := range builtinRequests {
		t.Run(b.cmd, func(t *testing.T) {
			f := newFixture(t, exampleConfig(t))
			t.Setenv("DECREE_RUN_ID", "20261005T000000Z-abcdef")
			t.Setenv("TRACEPARENT", "00-11111111111111111111111111111111-2222222222222222-01")
			t.Setenv("KEPT_VAR", "yes")
			r := b.req
			r.header = http.Header{"Traceparent": {tp}, "Tracestate": {"a=1"}}
			if rec := f.do(t, r); rec.Code != b.status {
				t.Fatalf("status %d, want %d", rec.Code, b.status)
			}
			r.header = http.Header{"Traceparent": {"garbage"}}
			if rec := f.do(t, r); rec.Code != b.status {
				t.Fatalf("status %d, want %d", rec.Code, b.status)
			}
			calls := f.stub.Invocations(t, b.cmd)
			if len(calls) != 2 {
				t.Fatalf("%d calls, want 2", len(calls))
			}
			for _, c := range calls {
				for _, kv := range c.Env {
					if strings.HasPrefix(kv, "DECREE_") {
						t.Errorf("decree saw %s", kv)
					}
				}
				if v, _ := c.Getenv("KEPT_VAR"); v != "yes" {
					t.Errorf("KEPT_VAR = %q, want the rest of the environment kept", v)
				}
			}
			parent, _ := calls[0].Getenv("TRACEPARENT")
			state, _ := calls[0].Getenv("TRACESTATE")
			if parent != tp || state != "a=1" {
				t.Errorf("valid: TRACEPARENT %q, TRACESTATE %q", parent, state)
			}
			if parent, ok := calls[1].Getenv("TRACEPARENT"); ok {
				t.Errorf("invalid: TRACEPARENT %q passed", parent)
			}
		})
	}
}

// TestBuiltinsLogged: one request record each, with the route pattern,
// never the id, the event or the note.
func TestBuiltinsLogged(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	logs := captureLogs(f.srv)
	const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	f.do(t, req{method: "GET", path: "/runs/secret-run-id", bearer: secret, header: http.Header{"Traceparent": {tp}}})
	f.do(t, req{path: "/runs/secret-wait-id/replies/secret-event", bearer: secret, body: "secret note"})
	f.do(t, req{method: "GET", path: "/openapi.json"})
	recs := requestRecords(t, logs)
	if len(recs) != 3 {
		t.Fatalf("%d request records, want 3:\n%s", len(recs), logs)
	}
	for i, want := range []struct {
		method, route string
		status        float64
	}{
		{"GET", "/runs/{id}", 200},
		{"POST", "/runs/{wait_id}/replies/{event}", 201},
		{"GET", "/openapi.json", 200},
	} {
		m := recs[i]
		if m["method"] != want.method || m["route"] != want.route || m["status"] != want.status {
			t.Errorf("record %d: %v, want %+v", i, m, want)
		}
	}
	if recs[0]["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace_id %v", recs[0]["trace_id"])
	}
	if recs[1]["message_id"] != "20261005T043200Z-3d4e5f" {
		t.Errorf("message_id %v, want the reply's id", recs[1]["message_id"])
	}
	if strings.Contains(logs.String(), "secret") {
		t.Errorf("the logs hold a parameter or the note:\n%s", logs)
	}
}

// TestWildcardsBesideBuiltins: configured paths of parameters only, which
// share no prefix with the built-ins but overlap them as net/http
// patterns, are served beside them, and a wrong method on either is a 405.
func TestWildcardsBesideBuiltins(t *testing.T) {
	f := newFixture(t, `project: .
endpoints:
  - path: /{a}
    body: optional
    message: { machine: notify, params: { title: '{{a}}' } }
  - path: /{a}/x
    body: optional
    message: { machine: notify, params: { title: '{{a}}' } }
  - path: /{a}/{b}
    body: optional
    message: { machine: notify, params: { title: '{{a}}{{b}}' } }
`)
	cases := []struct {
		req    req
		status int
		allow  string
	}{
		{req{path: "/hello", bearer: secret}, 201, ""},
		{req{path: "/runs/x", bearer: secret}, 201, ""},
		{req{method: "GET", path: "/runs/x", bearer: secret}, 200, ""},
		{req{path: "/runs/w/replies/approve", bearer: secret}, 201, ""},
		{req{method: "GET", path: "/openapi.json"}, 200, ""},
		{req{method: "GET", path: "/hello", bearer: secret}, 405, "POST"},
		{req{method: "GET", path: "/hello/x", bearer: secret}, 405, "POST"},
		{req{method: "PUT", path: "/runs/x", bearer: secret}, 405, "GET, HEAD, POST"},
		{req{method: "DELETE", path: "/runs/w/replies/approve", bearer: secret}, 405, "POST"},
	}
	for _, c := range cases {
		rec := f.do(t, c.req)
		if rec.Code != c.status {
			t.Errorf("%s %s: status %d, body %s; want %d", c.req.method, c.req.path, rec.Code, rec.Body, c.status)
		}
		if got := rec.Header().Get("Allow"); got != c.allow {
			t.Errorf("%s %s: Allow %q, want %q", c.req.method, c.req.path, got, c.allow)
		}
	}
}
