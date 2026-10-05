package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// logBuffer collects log output; it is safe for concurrent use.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// records decodes every JSON line logged so far.
func (b *logBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func captureLogs(s *Server) *logBuffer {
	b := &logBuffer{}
	s.Logger = slog.New(slog.NewJSONHandler(b, nil))
	return b
}

func requestRecords(t *testing.T, b *logBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, m := range b.records(t) {
		if m["msg"] == "request" {
			out = append(out, m)
		}
	}
	return out
}

// TestRequestLogRecords: one record per request, with the fields of
// SPEC.md §9 and the route pattern rather than the path.
func TestRequestLogRecords(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	logs := captureLogs(f.srv)
	const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	cases := []struct {
		r       req
		route   string
		status  float64
		message bool
		trace   string
	}{
		{req{path: "/notify/backup", bearer: secret, body: "x", header: http.Header{"Traceparent": {tp}}}, "/notify/{title}", 201, true, "4bf92f3577b34da6a3ce929d0e0e4736"},
		{req{path: "/notify/backup/", bearer: secret, body: "x", header: http.Header{"Traceparent": {"garbage"}}}, "/notify/{title}", 201, true, ""},
		{req{path: "/comfy/a/b", bearer: secret, body: "x"}, "/comfy/{type}/{name}", 401, false, ""},
		{req{method: "GET", path: "/notify/backup", bearer: secret}, "/notify/{title}", 405, false, ""},
		{req{path: "/nope", bearer: secret, body: "x"}, "", 404, false, ""},
		{req{path: "/notify/a$b", bearer: secret, body: "x"}, "/notify/{title}", 400, false, ""},
	}
	for _, c := range cases {
		f.do(t, c.r)
	}
	recs := requestRecords(t, logs)
	if len(recs) != len(cases) {
		t.Fatalf("%d request records, want %d:\n%s", len(recs), len(cases), logs)
	}
	for i, c := range cases {
		m := recs[i]
		method := c.r.method
		if method == "" {
			method = "POST"
		}
		if m["method"] != method || m["route"] != c.route || m["status"] != c.status {
			t.Errorf("%s: record %v; want method %s, route %q, status %v", c.r.path, m, method, c.route, c.status)
		}
		if _, ok := m["duration_ms"].(float64); !ok {
			t.Errorf("%s: no duration_ms in %v", c.r.path, m)
		}
		id, hasID := m["message_id"].(string)
		if hasID != c.message || (hasID && !strings.HasPrefix(id, "20261005T043125Z-")) {
			t.Errorf("%s: message_id %q, want one: %v", c.r.path, id, c.message)
		}
		if got, _ := m["trace_id"].(string); got != c.trace {
			t.Errorf("%s: trace_id %q, want %q", c.r.path, got, c.trace)
		}
	}
}

// TestLogsHoldNoSecretsBodiesOrParameters asserts on the captured output
// of every kind of request, including the route records of startup.
func TestLogsHoldNoSecretsBodiesOrParameters(t *testing.T) {
	f := newFixture(t, strings.Replace(exampleConfig(t), "max_body_bytes: 262144", "max_body_bytes: 64", 1))
	logs := captureLogs(f.srv)
	f.srv.LogRoutes()

	const (
		param  = "Zq7paramvalue"
		body   = "Zq7bodytext"
		wrong  = "Zq7wrongsecretZq7wrongsecretZq7wrongsecret"
		bad    = "Zq7bad$value"
		query  = "Zq7queryvalue"
		state  = "Zq7state"
		bigBdy = "Zq7bigbody"
	)
	for _, r := range []req{
		{path: "/notify/" + param + "?q=" + query, bearer: secret, body: body, header: http.Header{"Tracestate": {"k=" + state}}},
		{path: "/comfy/x/" + param, bearer: comfySecret, body: body},
		{path: "/notify/" + param, bearer: wrong, body: body},
		{path: "/notify/" + bad, bearer: secret, body: body},
		{path: "/notify/" + param, bearer: secret, body: strings.Repeat(bigBdy, 10)},
		{method: "GET", path: "/notify/" + param, bearer: secret},
		{path: "/" + param, bearer: secret, body: body},
	} {
		f.do(t, r)
	}
	f.stub.SetEmit(t, 1, "error: rejected\n")
	f.do(t, req{path: "/notify/" + param, bearer: secret, body: body})

	out := logs.String()
	if len(requestRecords(t, logs)) != 8 {
		t.Fatalf("want 8 request records:\n%s", out)
	}
	for _, leak := range []string{secret, comfySecret, wrong, param, body, bad, query, state, bigBdy, "Zq7"} {
		if strings.Contains(out, leak) {
			t.Errorf("the logs contain %q:\n%s", leak, out)
		}
	}
}

func TestLogRoutes(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	logs := captureLogs(f.srv)
	f.srv.LogRoutes()
	var got []string
	for _, m := range logs.records(t) {
		if m["msg"] == "route" {
			got = append(got, m["method"].(string)+" "+m["route"].(string)+" -> "+m["machine"].(string))
		}
	}
	want := []string{
		"POST /notify -> notify",
		"POST /notify/{title} -> notify",
		"POST /comfy/{type}/{name} -> comfy_image",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("route records:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
