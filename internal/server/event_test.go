package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// eventConfig has an emit endpoint and event endpoints: /approve/{wait_id}
// on its own secret, as the example's, and /reply/{w}/{e}, whose event
// comes from the path, on the default one.
const eventConfig = `endpoints:
  - path: /notify
    message: { machine: notify }
  - path: /approve/{wait_id}
    action: event
    secret_env: DECREE_GO_REST_APPROVE_SECRET
    patterns: { wait_id: '[A-Za-z0-9._-]{1,128}' }
    body: optional
    reply:
      to: '{{wait_id}}'
      event: approve
  - path: /reply/{w}/{e}
    action: event
    patterns: { e: '[a-z]+' }
    reply: { to: '{{w}}', event: '{{e}}' }
  - path: /strict/{w}
    action: event
    body: required
    reply: { to: '{{w}}', event: approve }
`

// TestEventArgv: an event endpoint runs decree event <to> <event>, with
// the note as one argv entry, -m=<body>, unchanged, and no -m for an
// empty body. Nothing goes to stdin.
func TestEventArgv(t *testing.T) {
	const waitID = "20261005T043125Z-0a1b2c.w3"
	cases := []struct {
		name string
		req  req
		want []string
	}{
		{"no note", req{path: "/approve/" + waitID}, []string{"event", waitID, "approve", "--format", "json"}},
		{"note", req{path: "/approve/" + waitID, body: "looks good"}, []string{"event", waitID, "approve", "-m=looks good", "--format", "json"}},
		{"starts with -", req{path: "/approve/w", body: "-x note"}, []string{"event", "w", "approve", "-m=-x note", "--format", "json"}},
		{"an option", req{path: "/approve/w", body: "--format"}, []string{"event", "w", "approve", "-m=--format", "--format", "json"}},
		{"=", req{path: "/approve/w", body: "=x"}, []string{"event", "w", "approve", "-m==x", "--format", "json"}},
		{"no newline added", req{path: "/approve/w", body: "two\nlines"}, []string{"event", "w", "approve", "-m=two\nlines", "--format", "json"}},
		{"trailing newline kept", req{path: "/approve/w", body: "line\n"}, []string{"event", "w", "approve", "-m=line\n", "--format", "json"}},
		{"whitespace is a note", req{path: "/approve/w", body: " "}, []string{"event", "w", "approve", "-m= ", "--format", "json"}},
		{"event from the path", req{path: "/reply/r.w1/reject", bearer: secret}, []string{"event", "r.w1", "reject", "--format", "json"}},
		{"trailing slash", req{path: "/approve/w/"}, []string{"event", "w", "approve", "--format", "json"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, eventConfig)
			if c.req.bearer == "" {
				c.req.bearer = approveSecret
			}
			rec := f.do(t, c.req)
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
			if calls[0].Dir != f.proj {
				t.Errorf("cwd %q, want the project %q", calls[0].Dir, f.proj)
			}
			if len(calls[0].Stdin) != 0 {
				t.Errorf("stdin %q, want none", calls[0].Stdin)
			}
			if n := len(f.stub.Emits(t)); n != 0 {
				t.Errorf("%d emits, want none", n)
			}
		})
	}
}

// TestEventCreated: a 201 carries decree's id and path, and the to and
// event replied.
func TestEventCreated(t *testing.T) {
	f := newFixture(t, eventConfig)
	rec := f.do(t, req{path: "/approve/r.w3", bearer: approveSecret, body: "ok"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s; want 201", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"id":    "20261005T043200Z-3d4e5f",
		"path":  ".decree/inbox/20261005T043200Z-3d4e5f.md",
		"to":    "r.w3",
		"event": "approve",
	}
	if len(got) != len(want) {
		t.Errorf("body %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestEventStatuses(t *testing.T) {
	cfg := "limits: { max_body_bytes: 16 }\n" + eventConfig
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
		{name: "queued", req: req{path: "/approve/r.w3", bearer: approveSecret, body: "x"}, status: 201, runs: true},
		{name: "note at the cap", req: req{path: "/approve/r", bearer: approveSecret, body: strings.Repeat("x", 16)}, status: 201, runs: true},
		{name: "wait id at its limit", req: req{path: "/approve/" + long[1:], bearer: approveSecret}, status: 201, runs: true},
		{name: "not waiting", setup: func(t *testing.T, f *fixture) {
			f.stub.SetEvent(t, 1, "", "error: run r is not waiting\n")
		}, req: req{path: "/approve/r", bearer: approveSecret}, status: 409, msg: "run r is not waiting", runs: true},
		{name: "exit 1, no stderr", setup: func(t *testing.T, f *fixture) {
			f.stub.SetEvent(t, 1, "", "")
		}, req: req{path: "/approve/r", bearer: approveSecret}, status: 409, msg: "the run does not accept this reply", runs: true},
		{name: "exit 2", setup: func(t *testing.T, f *fixture) {
			f.stub.SetEvent(t, 2, "", "error: secret detail\n")
		}, req: req{path: "/approve/r", bearer: approveSecret}, status: 500, msg: replyFailed, runs: true},
		{name: "unreadable output", setup: func(t *testing.T, f *fixture) {
			f.stub.SetEvent(t, 0, "Usage: decree event\n", "")
		}, req: req{path: "/approve/r", bearer: approveSecret}, status: 500, msg: replyFailed, runs: true},
		{name: "output without id", setup: func(t *testing.T, f *fixture) {
			f.stub.SetEvent(t, 0, `{"path": ".decree/inbox/x.md"}`, "")
		}, req: req{path: "/approve/r", bearer: approveSecret}, status: 500, msg: replyFailed, runs: true},
		{name: "timeout", setup: func(t *testing.T, f *fixture) {
			f.stub.SetSleep(t, "event", "5")
			f.srv.EmitTimeout = 200 * time.Millisecond
		}, req: req{path: "/approve/r", bearer: approveSecret}, status: 500, msg: replyFailed, runs: true},
		{name: "note too large", req: req{path: "/approve/r", bearer: approveSecret, body: strings.Repeat("x", 17)}, status: 413, msg: "the body is larger than 16 bytes"},
		{name: "NUL in the note", req: req{path: "/approve/r", bearer: approveSecret, body: "a\x00b"}, status: 400, msg: "the note must not contain a NUL byte"},
		{name: "wait_id too long", req: req{path: "/approve/" + long, bearer: approveSecret}, status: 400, msg: "parameter wait_id does not match its pattern"},
		{name: "wait_id outside its pattern", req: req{path: "/approve/a%20b", bearer: approveSecret}, status: 400, msg: "parameter wait_id does not match its pattern"},
		{name: "default wait-id pattern", req: req{path: "/strict/a%21b", bearer: secret, body: "x"}, status: 400, msg: "parameter w does not match its pattern"},
		{name: "event outside its pattern", req: req{path: "/reply/r/Approve", bearer: secret}, status: 400, msg: "parameter e does not match its pattern"},
		{name: "body required", req: req{path: "/strict/r.w1", bearer: secret, body: " "}, status: 400, msg: "a body is required"},
		{name: "body required, given", req: req{path: "/strict/r.w1", bearer: secret, body: "why"}, status: 201, runs: true},
		{name: "no bearer", req: req{path: "/approve/r"}, status: 401, msg: "unauthorized"},
		{name: "GET", req: req{method: "GET", path: "/approve/r", bearer: approveSecret}, status: 405, msg: "method not allowed", allow: "POST"},
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

// TestEventOwnSecret: an event endpoint with its own secret takes that
// secret only: the top-level one gets 401, and its own opens no other
// endpoint.
func TestEventOwnSecret(t *testing.T) {
	f := newFixture(t, eventConfig)
	for _, c := range []struct {
		r    req
		want int
	}{
		{req{path: "/approve/r.w1", bearer: approveSecret, body: "ok"}, http.StatusCreated},
		{req{path: "/approve/r.w1", bearer: secret, body: "ok"}, http.StatusUnauthorized},
		{req{path: "/notify", bearer: approveSecret, body: "x"}, http.StatusUnauthorized},
		{req{path: "/reply/r.w1/approve", bearer: approveSecret}, http.StatusUnauthorized},
		{req{path: "/reply/r.w1/approve", bearer: secret}, http.StatusCreated},
	} {
		rec := f.do(t, c.r)
		if rec.Code != c.want {
			t.Errorf("POST %s with %.4s…: %d, want %d", c.r.path, c.r.bearer, rec.Code, c.want)
		}
		if c.want == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("POST %s: no WWW-Authenticate", c.r.path)
		}
	}
	if n := len(f.stub.Invocations(t, "event")); n != 2 {
		t.Errorf("%d event calls, want 2", n)
	}
}

// TestEventEnvironment: decree event gets the environment of SPEC.md §4
// step 6: no DECREE_*, and the request's valid trace context.
func TestEventEnvironment(t *testing.T) {
	const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	f := newFixture(t, eventConfig)
	t.Setenv("DECREE_RUN_ID", "20261005T000000Z-abcdef")
	t.Setenv("TRACEPARENT", "00-11111111111111111111111111111111-2222222222222222-01")
	t.Setenv("KEPT_VAR", "yes")
	for _, h := range []http.Header{{"Traceparent": {tp}, "Tracestate": {"a=1"}}, {"Traceparent": {"garbage"}}} {
		if rec := f.do(t, req{path: "/approve/r", bearer: approveSecret, header: h}); rec.Code != http.StatusCreated {
			t.Fatalf("status %d, want 201", rec.Code)
		}
	}
	calls := f.stub.Invocations(t, "event")
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
}

// TestEventLogged: one request record with the route pattern and the
// reply's id, never the wait id, the event or the note.
func TestEventLogged(t *testing.T) {
	f := newFixture(t, eventConfig)
	logs := captureLogs(f.srv)
	f.do(t, req{path: "/reply/secret-wait-id/secretevent", bearer: secret, body: "secret note"})
	f.stub.SetEvent(t, 2, "", "")
	f.do(t, req{path: "/reply/secret-wait-id/secretevent", bearer: secret, body: "secret note"})
	recs := requestRecords(t, logs)
	if len(recs) != 2 {
		t.Fatalf("%d request records, want 2:\n%s", len(recs), logs)
	}
	if m := recs[0]; m["method"] != "POST" || m["route"] != "/reply/{w}/{e}" || m["status"] != 201.0 || m["message_id"] != "20261005T043200Z-3d4e5f" {
		t.Errorf("record %v", m)
	}
	if m := recs[1]; m["status"] != 500.0 {
		t.Errorf("record %v", m)
	}
	if !strings.Contains(logs.String(), "decree event failed") {
		t.Errorf("the failure is not logged:\n%s", logs)
	}
	if strings.Contains(logs.String(), "secret") {
		t.Errorf("the logs hold a parameter or the note:\n%s", logs)
	}
}

// TestEventBudgets: an event endpoint spends from the same budgets as an
// emit endpoint (SPEC.md §6).
func TestEventBudgets(t *testing.T) {
	f := newFixture(t, limitsConfig(t, 2, 2))
	f.clock()
	ok := req{path: "/approve/r.w1", bearer: approveSecret, body: "note"}
	// Failed authentication spends the failure budget...
	bad := ok
	bad.bearer = wrongBearer
	for i, want := range []int{401, 401, 429} {
		if rec := f.do(t, bad); rec.Code != want {
			t.Fatalf("bad bearer %d: status %d, want %d", i+1, rec.Code, want)
		}
	}
	// ...which does not lock out a caller with the secret, who spends
	// rate_max together with the emit endpoints.
	if rec := f.do(t, ok); rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s; want 201", rec.Code, rec.Body)
	}
	if rec := f.do(t, req{path: "/notify/backup", bearer: secret, body: "x"}); rec.Code != http.StatusCreated {
		t.Fatalf("emit endpoint: status %d, want 201", rec.Code)
	}
	rec := f.do(t, ok)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("rate_max spent: status %d, Retry-After %q; want 429 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// TestEvent400SpendsRequestBudget: a parameter outside its pattern from an
// authenticated caller spends rate_max, not the failure budget.
func TestEvent400SpendsRequestBudget(t *testing.T) {
	f := newFixture(t, limitsConfig(t, 60, 1))
	f.clock()
	for i := 0; i < 3; i++ {
		if rec := f.do(t, req{path: "/approve/a%20b", bearer: approveSecret}); rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d, want 400", rec.Code)
		}
	}
	if rec := f.do(t, req{path: "/approve/r", bearer: wrongBearer}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401: the failure budget is unspent", rec.Code)
	}
}

// TestWildcardsBesideReserved: configured paths of parameters only, which
// overlap /healthz and /openapi.json as net/http patterns, are served
// beside them, and a wrong method on either is a 405.
func TestWildcardsBesideReserved(t *testing.T) {
	cfg := `project: .
endpoints:
  - path: /{a}
    body: optional
    message: { machine: notify, params: { title: '{{a}}' } }
  - path: /{a}/x
    body: optional
    message: { machine: notify, params: { title: '{{a}}' } }
  - path: /{a}/{b}
    action: event
    reply: { to: '{{a}}', event: '{{b}}' }
`
	f := newFixture(t, cfg)
	cases := []struct {
		req    req
		status int
		allow  string
	}{
		{req{path: "/hello", bearer: secret}, 201, ""},
		{req{path: "/runs/x", bearer: secret}, 201, ""},
		{req{path: "/r.w1/approve", bearer: secret}, 201, ""},
		{req{method: "GET", path: "/openapi.json"}, 200, ""},
		{req{method: "GET", path: "/hello", bearer: secret}, 405, "POST"},
		{req{method: "GET", path: "/hello/x", bearer: secret}, 405, "POST"},
		{req{method: "PUT", path: "/openapi.json", bearer: secret}, 405, "GET, HEAD, POST"},
		{req{method: "DELETE", path: "/r/approve", bearer: secret}, 405, "POST"},
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
	// Off, /openapi.json is one more path of /{a}.
	g := newFixtureWith(t, cfg, Options{})
	if rec := g.do(t, req{method: "GET", path: "/openapi.json"}); rec.Code != 405 || rec.Header().Get("Allow") != "POST" {
		t.Errorf("GET /openapi.json, off: %d, Allow %q; want 405, POST", rec.Code, rec.Header().Get("Allow"))
	}
}
