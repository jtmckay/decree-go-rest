package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jtmckay/decree-go-rest/internal/config"
)

// statusSecret is the own secret of GET /runs/{id} in the tests that set
// builtins.status.secret_env to STATUS_SECRET.
var statusSecret = strings.Repeat("s", 36)

// withStatusSecret is the example config with GET /runs/{id} on its own
// secret, STATUS_SECRET.
func withStatusSecret(t *testing.T) string {
	t.Helper()
	t.Setenv("STATUS_SECRET", statusSecret)
	cfg := strings.Replace(exampleConfig(t), "status: { enabled: true }", "status: { enabled: true, secret_env: STATUS_SECRET }", 1)
	if cfg == exampleConfig(t) {
		t.Fatal("the example config has no status built-in to change")
	}
	return cfg
}

// withDefaultSecrets is the example config with neither built-in under
// /runs/ naming its own secret.
func withDefaultSecrets(t *testing.T) string {
	t.Helper()
	cfg := strings.Replace(exampleConfig(t), ", secret_env: DECREE_GO_REST_REPLY_SECRET", "", 1)
	if cfg == exampleConfig(t) {
		t.Fatal("the example config gives replies no secret of its own")
	}
	return cfg
}

// TestAcceptanceReplyOwnSecret is the second acceptance criterion of 07:
// with builtins.replies.secret_env set, a reply with the default secret
// is a 401 and one with its own a 201.
func TestAcceptanceReplyOwnSecret(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	if got := f.cfg.Builtins.Replies.SecretEnv; got != "DECREE_GO_REST_REPLY_SECRET" {
		t.Fatalf("the example's builtins.replies.secret_env = %q", got)
	}
	rec := f.do(t, req{path: "/runs/r.w3/replies/approve", bearer: secret, body: "ok"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("the default secret: status %d, body %s; want 401", rec.Code, rec.Body)
	}
	if n := len(f.stub.Invocations(t, "event")); n != 0 {
		t.Errorf("decree event ran %d times on a rejected reply", n)
	}
	rec = f.do(t, req{path: "/runs/r.w3/replies/approve", bearer: replySecret, body: "ok"})
	if rec.Code != http.StatusCreated {
		t.Errorf("its own secret: status %d, body %s; want 201", rec.Code, rec.Body)
	}
	// The reply's secret is the reply's only: it opens neither the status
	// built-in nor an endpoint.
	if rec := f.do(t, req{method: "GET", path: "/runs/r", bearer: replySecret}); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /runs/r with the reply secret: %d, want 401", rec.Code)
	}
	if rec := f.do(t, req{path: "/notify/backup", bearer: replySecret, body: "x"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /notify/backup with the reply secret: %d, want 401", rec.Code)
	}
}

// TestStatusOwnSecret: with builtins.status.secret_env set, GET /runs/{id}
// with the default secret is a 401 and with its own a 200.
func TestStatusOwnSecret(t *testing.T) {
	f := newFixture(t, withStatusSecret(t))
	if rec := f.do(t, req{method: "GET", path: "/runs/r", bearer: secret}); rec.Code != http.StatusUnauthorized {
		t.Errorf("the default secret: status %d, body %s; want 401", rec.Code, rec.Body)
	}
	if n := len(f.stub.Invocations(t, "status")); n != 0 {
		t.Errorf("decree status ran %d times on a rejected request", n)
	}
	if rec := f.do(t, req{method: "GET", path: "/runs/r", bearer: statusSecret}); rec.Code != http.StatusOK {
		t.Errorf("its own secret: status %d, body %s; want 200", rec.Code, rec.Body)
	}
	if rec := f.do(t, req{path: "/runs/r/replies/approve", bearer: statusSecret}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a reply with the status secret: %d, want 401", rec.Code)
	}
}

// TestBuiltinsFallBackToDefaultSecret: a built-in without its own
// secret_env takes the top-level secret, and nothing else.
func TestBuiltinsFallBackToDefaultSecret(t *testing.T) {
	f := newFixture(t, withDefaultSecrets(t))
	for _, c := range []struct {
		r      req
		status int
	}{
		{req{method: "GET", path: "/runs/r", bearer: secret}, http.StatusOK},
		{req{path: "/runs/r/replies/approve", bearer: secret}, http.StatusCreated},
		{req{method: "GET", path: "/runs/r", bearer: comfySecret}, http.StatusUnauthorized},
		{req{path: "/runs/r/replies/approve", bearer: replySecret}, http.StatusUnauthorized},
	} {
		if rec := f.do(t, c.r); rec.Code != c.status {
			t.Errorf("%s %s with %.1s…: status %d, body %s; want %d", c.r.method, c.r.path, c.r.bearer, rec.Code, rec.Body, c.status)
		}
	}

	// The same for a top-level secret_env other than the default name.
	t.Setenv("MY_SECRET", otherDefault)
	cfg := strings.Replace(withDefaultSecrets(t), "secret_env: DECREE_GO_REST_SECRET", "secret_env: MY_SECRET", 1)
	g := newFixture(t, cfg)
	if rec := g.do(t, req{method: "GET", path: "/runs/r", bearer: otherDefault}); rec.Code != http.StatusOK {
		t.Errorf("status with the top-level MY_SECRET: %d, want 200", rec.Code)
	}
	if rec := g.do(t, req{path: "/runs/r/replies/approve", bearer: otherDefault}); rec.Code != http.StatusCreated {
		t.Errorf("reply with the top-level MY_SECRET: %d, want 201", rec.Code)
	}
}

// otherDefault is the value of a top-level secret_env other than
// DECREE_GO_REST_SECRET.
var otherDefault = strings.Repeat("d", 32)

// TestNewRefusesMissingBuiltinSecret: New, which runs after validation,
// still refuses a built-in whose secret is missing or short rather than
// serve it with no secret.
func TestNewRefusesMissingBuiltinSecret(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	for name, val := range map[string]string{"unset": "", "short": strings.Repeat("x", config.MinSecretLen-1)} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DECREE_GO_REST_REPLY_SECRET", val)
			_, err := New(f.cfg)
			if err == nil || !strings.Contains(err.Error(), "DECREE_GO_REST_REPLY_SECRET") {
				t.Errorf("New: %v, want an error naming DECREE_GO_REST_REPLY_SECRET", err)
			}
		})
	}
}

// TestOpenAPIBuiltinSecurity: the document shows each built-in's security:
// bearer for status and replies, saying whether the secret is the
// default or their own, and none for /healthz and /openapi.json.
func TestOpenAPIBuiltinSecurity(t *testing.T) {
	cases := []struct {
		name, cfg             string
		statusDesc, replyDesc string
	}{
		{"example", exampleConfig(t), "the default bearer secret", "its own bearer secret"},
		{"both own", withStatusSecret(t), "its own bearer secret", "its own bearer secret"},
		{"both default", withDefaultSecrets(t), "the default bearer secret", "the default bearer secret"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.cfg)
			_, doc := getOpenAPI(t, f.srv)
			for _, w := range []struct {
				path, method string
				bearer       bool
				desc         string
			}{
				{config.StatusPath, "get", true, c.statusDesc},
				{config.RepliesPath, "post", true, c.replyDesc},
				{config.HealthPath, "get", false, ""},
				{config.OpenAPIPath, "get", false, ""},
			} {
				op := field(t, doc, "paths").(map[string]any)[w.path].(map[string]any)[w.method].(map[string]any)
				security, ok := op["security"].([]any)
				if !ok {
					t.Errorf("%s: no security, want it stated", w.path)
					continue
				}
				if w.bearer {
					if len(security) != 1 || security[0].(map[string]any)["bearer"] == nil {
						t.Errorf("%s: security %v, want bearer", w.path, security)
					}
					if d, _ := op["description"].(string); !strings.Contains(d, "Authenticated with "+w.desc+".") {
						t.Errorf("%s: description %q, want it to name %s", w.path, d, w.desc)
					}
				} else if len(security) != 0 {
					t.Errorf("%s: security %v, want none", w.path, security)
				}
			}
			// The document names no environment variable.
			raw, _ := getOpenAPI(t, f.srv)
			for _, env := range []string{"DECREE_GO_REST_REPLY_SECRET", "STATUS_SECRET", "DECREE_GO_REST_SECRET"} {
				if strings.Contains(raw, env) {
					t.Errorf("the document names %s", env)
				}
			}
		})
	}
}
