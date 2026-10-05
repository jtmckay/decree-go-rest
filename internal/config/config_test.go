package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv(ListenEnv, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "decree-go-rest.yml")
	writeFile(t, path, "endpoints:\n  - path: /notify\n    message: { machine: notify }\n")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"project", c.Project, "."},
		{"project dir", c.ProjectDir, dir},
		{"listen", c.Listen, "127.0.0.1:8801"},
		{"secret_env", c.SecretEnv, "DECREE_GO_REST_SECRET"},
		{"decree", c.Decree, "decree"},
		{"daemon.enabled", c.Daemon.Enabled, true},
		{"daemon.interval", c.Daemon.Interval.Std(), 2 * time.Second},
		{"limits.max_body_bytes", c.Limits.MaxBodyBytes, int64(262144)},
		{"limits.rate_window", c.Limits.RateWindow.Std(), 60 * time.Second},
		{"limits.rate_max", c.Limits.RateMax, 60},
		{"limits.rate_fail_max", c.Limits.RateFailMax, 10},
		{"endpoints[0].secret_env", c.Endpoints[0].SecretEnv, ""},
		{"endpoints[0] effective secret_env", c.Endpoints[0].EffectiveSecretEnv(c), "DECREE_GO_REST_SECRET"},
		{"endpoints[0].action", c.Endpoints[0].Action, "emit"},
		{"endpoints[0].body", c.Endpoints[0].Body, "required"},
		{"endpoints[0].patterns", len(c.Endpoints[0].Patterns), 0},
		{"endpoints[0].message.params", len(c.Endpoints[0].Message.Params), 0},
	}
	for _, ch := range checks {
		if ch.got != ch.want {
			t.Errorf("%s = %v, want %v", ch.name, ch.got, ch.want)
		}
	}
}

func TestLoadAllKeys(t *testing.T) {
	t.Setenv(ListenEnv, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "conf", "decree-go-rest.yml")
	writeFile(t, path, `project: ../proj
listen: 0.0.0.0:9000
secret_env: MY_SECRET
decree: /opt/decree
daemon: { enabled: false, interval: 1m }
limits: { max_body_bytes: 10, rate_window: 1h, rate_max: 5, rate_fail_max: 2 }
endpoints:
  - path: /a/{x}
    secret_env: OTHER
    patterns: { x: '[a-z]+' }
    body: none
    message:
      machine: m
      params: { z: '{{x}}', a: 1, m: true }
`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.ProjectDir != filepath.Join(dir, "proj") {
		t.Errorf("project dir = %s", c.ProjectDir)
	}
	if c.Listen != "0.0.0.0:9000" || c.SecretEnv != "MY_SECRET" || c.Decree != "/opt/decree" {
		t.Errorf("top-level keys: %+v", c)
	}
	if c.Daemon.Enabled || c.Daemon.Interval.Std() != time.Minute {
		t.Errorf("daemon = %+v", c.Daemon)
	}
	if c.Limits != (Limits{10, Duration(time.Hour), 5, 2}) {
		t.Errorf("limits = %+v", c.Limits)
	}
	e := c.Endpoints[0]
	if e.Path != "/a/{x}" || e.SecretEnv != "OTHER" || e.Patterns["x"] != "[a-z]+" || e.Body != "none" || e.Message.Machine != "m" {
		t.Errorf("endpoint = %+v", e)
	}
	var keys []string
	for _, p := range e.Message.Params {
		keys = append(keys, p.Key)
	}
	if got := strings.Join(keys, ","); got != "z,a,m" {
		t.Errorf("params order = %s, want z,a,m (config order)", got)
	}
}

// TestEventEndpointDefaults: an event endpoint's body defaults to
// optional, and a parameter it uses in reply.to defaults to the wait-id
// pattern, since wait ids contain dots.
func TestEventEndpointDefaults(t *testing.T) {
	c, err := Parse([]byte(`endpoints:
  - path: /approve/{wait_id}/{ev}/{x}
    action: event
    patterns: { x: '[a-z]' }
    reply: { to: '{{wait_id}}', event: '{{ev}}{{x}}' }
  - path: /strict/{w}
    action: event
    body: required
    reply: { to: 'r.{{w}}', event: approve }
  - path: /emit/{w}
    message: { machine: m, params: { t: '{{w}}' } }
`))
	if err != nil {
		t.Fatal(err)
	}
	ev, strict, emit := c.Endpoints[0], c.Endpoints[1], c.Endpoints[2]
	checks := []struct {
		name      string
		got, want any
	}{
		{"event action", ev.Action, ActionEvent},
		{"event body", ev.Body, BodyOptional},
		{"event reply.to", ev.Reply.To, "{{wait_id}}"},
		{"event message", ev.Message == nil, true},
		{"pattern of a parameter in to", ev.ParamPattern("wait_id"), WaitIDPattern},
		{"pattern of a parameter in event", ev.ParamPattern("ev"), DefaultParamPattern},
		{"configured pattern", ev.ParamPattern("x"), "[a-z]"},
		{"explicit body", strict.Body, BodyRequired},
		{"pattern of a parameter inside to", strict.ParamPattern("w"), WaitIDPattern},
		{"emit body", emit.Body, BodyRequired},
		{"emit pattern", emit.ParamPattern("w"), DefaultParamPattern},
		{"emit reply", emit.Reply == nil, true},
	}
	for _, ch := range checks {
		if ch.got != ch.want {
			t.Errorf("%s = %v, want %v", ch.name, ch.got, ch.want)
		}
	}
}

func TestListenEnvOverride(t *testing.T) {
	t.Setenv(ListenEnv, "127.0.0.1:9999")
	c, err := Parse([]byte("listen: 1.2.3.4:5\nendpoints: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9999" {
		t.Errorf("listen = %s, want the %s override", c.Listen, ListenEnv)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"unknown top-level key": "endpoints: []\nlisten_on: x\n",
		"unknown nested key":    "daemon: { enable: true }\nendpoints: []\n",
		"unknown endpoint key":  "endpoints:\n  - path: /a\n    methods: [GET]\n    message: { machine: m }\n",
		"unknown message key":   "endpoints:\n  - path: /a\n    message: { machine: m, routine: x }\n",
		"unknown reply key":     "endpoints:\n  - path: /a\n    action: event\n    reply: { to: r, event: e, note: x }\n",
		"reply not a mapping":   "endpoints:\n  - path: /a\n    action: event\n    reply: approve\n",
		"bad duration":          "daemon: { interval: 2 }\nendpoints: []\n",
		"go duration":           "limits: { rate_window: 1m30s }\nendpoints: []\n",
		"duplicate param":       "endpoints:\n  - path: /a\n    message: { machine: m, params: { a: 1, a: 2 } }\n",
		"params not a mapping":  "endpoints:\n  - path: /a\n    message: { machine: m, params: [a] }\n",
		"empty":                 "",
		"two documents":         "endpoints: []\n---\nendpoints: []\n",
		"wrong type":            "limits: { rate_max: lots }\nendpoints: []\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err == nil {
				t.Errorf("Parse succeeded, want an error")
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yml")); err == nil {
		t.Error("Load of a missing file succeeded")
	}
}

func TestParseDuration(t *testing.T) {
	good := map[string]time.Duration{
		"0s": 0, "2s": 2 * time.Second, "60s": time.Minute, "5m": 5 * time.Minute,
		"3h": 3 * time.Hour, "1d": 24 * time.Hour, "007s": 7 * time.Second,
	}
	for in, want := range good {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "s", "2", "2ms", "1.5s", "-1s", "+1s", " 1s", "1 s", "1m30s", "2w", "99999999999999999999d"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) succeeded, want an error", in)
		}
	}
}

func TestDurationString(t *testing.T) {
	for in, want := range map[time.Duration]string{
		0: "0s", 2 * time.Second: "2s", 90 * time.Second: "90s", time.Minute: "1m",
		5 * time.Minute: "5m", 3 * time.Hour: "3h", 48 * time.Hour: "2d", 25 * time.Hour: "25h",
	} {
		got := Duration(in).String()
		if got != want {
			t.Errorf("Duration(%v).String() = %q, want %q", in, got, want)
		}
		if back, err := ParseDuration(got); err != nil || back != in {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", got, back, err, in)
		}
	}
}

// TestRemovedKey: the key of the old built-ins is an error naming the
// endpoint action form and the OpenAPI variable, in any form it takes.
func TestRemovedKey(t *testing.T) {
	for name, doc := range map[string]string{
		"objects":  RemovedKey + ":\n  replies: { enabled: true }\nendpoints: []\n",
		"empty":    "endpoints: []\n" + RemovedKey + ": {}\n",
		"a scalar": RemovedKey + ": false\nendpoints: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(doc))
			if err == nil {
				t.Fatal("Parse succeeded")
			}
			for _, want := range []string{RemovedKey + " is no longer accepted", "action: event", "reply: { to:", OpenAPIEnv + "=true"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not hold %q", err, want)
				}
			}
		})
	}
	// Nested, it is an ordinary unknown key.
	if _, err := Parse([]byte("daemon: { " + RemovedKey + ": {} }\nendpoints: []\n")); err == nil || strings.Contains(err.Error(), "no longer accepted") {
		t.Errorf("nested: %v", err)
	}
}
