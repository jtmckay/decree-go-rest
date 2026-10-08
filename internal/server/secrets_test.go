package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jtmckay/decree-go-rest/internal/config"
)

// withDefaultSecrets is the example config with its event endpoint on the
// default secret.
func withDefaultSecrets(t *testing.T) string {
	t.Helper()
	cfg := strings.Replace(exampleConfig(t), "    secret_env: DECREE_GO_REST_APPROVE_SECRET\n", "", 1)
	if cfg == exampleConfig(t) {
		t.Fatal("the example config gives its event endpoint no secret of its own")
	}
	return cfg
}

// TestAcceptanceApproveOwnSecret: the example's event endpoint,
// /approve/{wait_id}, takes its own secret: the top-level one is a 401,
// its own a 201, and its own opens no other endpoint.
func TestAcceptanceApproveOwnSecret(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	ep := f.cfg.Endpoints[len(f.cfg.Endpoints)-1]
	if ep.Path != "/approve/{wait_id}" || ep.Action != config.ActionEvent || ep.SecretEnv != "DECREE_GO_REST_APPROVE_SECRET" {
		t.Fatalf("the example's last endpoint is %+v", ep)
	}
	rec := f.do(t, req{path: "/approve/r.w3", bearer: secret, body: "ok"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("the top-level secret: status %d, body %s; want 401", rec.Code, rec.Body)
	}
	if n := len(f.stub.Invocations(t, "event")); n != 0 {
		t.Errorf("decree event ran %d times on a rejected reply", n)
	}
	rec = f.do(t, req{path: "/approve/r.w3", bearer: approveSecret, body: "ok"})
	if rec.Code != http.StatusCreated {
		t.Errorf("its own secret: status %d, body %s; want 201", rec.Code, rec.Body)
	}
	if rec := f.do(t, req{path: "/notify/backup", bearer: approveSecret, body: "x"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /notify/backup with the approve secret: %d, want 401", rec.Code)
	}
}

// TestEventFallsBackToDefaultSecret: an event endpoint without its own
// secret_env takes the top-level secret, and nothing else.
func TestEventFallsBackToDefaultSecret(t *testing.T) {
	f := newFixture(t, withDefaultSecrets(t))
	for _, c := range []struct {
		r      req
		status int
	}{
		{req{path: "/approve/r", bearer: secret}, http.StatusCreated},
		{req{path: "/approve/r", bearer: approveSecret}, http.StatusUnauthorized},
		{req{path: "/approve/r", bearer: comfySecret}, http.StatusUnauthorized},
	} {
		if rec := f.do(t, c.r); rec.Code != c.status {
			t.Errorf("POST %s with %.1s…: status %d, body %s; want %d", c.r.path, c.r.bearer, rec.Code, rec.Body, c.status)
		}
	}

	// The same for a top-level secret_env other than the default name.
	t.Setenv("MY_SECRET", otherDefault)
	cfg := strings.Replace(withDefaultSecrets(t), "secret_env: DECREE_GO_REST_SECRET", "secret_env: MY_SECRET", 1)
	g := newFixture(t, cfg)
	if rec := g.do(t, req{path: "/approve/r", bearer: otherDefault}); rec.Code != http.StatusCreated {
		t.Errorf("reply with the top-level MY_SECRET: %d, want 201", rec.Code)
	}
}

// otherDefault is the value of a top-level secret_env other than
// DECREE_GO_REST_SECRET.
var otherDefault = strings.Repeat("e", 32)

// TestNewRefusesMissingSecret: New, which runs after validation, still
// refuses an event endpoint whose secret is missing or short rather than
// serve it with no secret.
func TestNewRefusesMissingSecret(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	for name, val := range map[string]string{"unset": "", "short": strings.Repeat("x", config.MinSecretLen-1)} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DECREE_GO_REST_APPROVE_SECRET", val)
			_, err := New(f.cfg, Options{})
			if err == nil || !strings.Contains(err.Error(), "DECREE_GO_REST_APPROVE_SECRET") {
				t.Errorf("New: %v, want an error naming DECREE_GO_REST_APPROVE_SECRET", err)
			}
		})
	}
}

// TestOpenAPISecurity: the document shows each operation's security:
// bearer for the endpoints, saying whether the secret is the default or
// their own, and none for /healthz and /openapi.json.
func TestOpenAPISecurity(t *testing.T) {
	cases := []struct {
		name, cfg   string
		approveDesc string
	}{
		{"example", exampleConfig(t), "its own bearer secret"},
		{"default", withDefaultSecrets(t), "the default bearer secret"},
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
				{"/notify/{title}", "post", true, "the default bearer secret"},
				{"/comfy/{type}/{name}", "post", true, "its own bearer secret"},
				{"/approve/{wait_id}", "post", true, c.approveDesc},
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
			for _, env := range []string{"DECREE_GO_REST_APPROVE_SECRET", "COMFY_SECRET", "DECREE_GO_REST_SECRET"} {
				if strings.Contains(raw, env) {
					t.Errorf("the document names %s", env)
				}
			}
		})
	}
}
