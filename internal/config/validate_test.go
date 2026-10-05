package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtmckay/decree-go-rest/internal/config"
	"github.com/jtmckay/decree-go-rest/internal/decreetest"
)

const (
	goodSecret  = "0123456789abcdef0123456789abcdef"
	otherSecret = "fedcba9876543210fedcba9876543210"
)

// notifyMachine has data of every type.
const notifyMachine = `name: notify
description: test machine
initial: send
data:
  title:    { type: string, default: Untitled }
  priority: { type: string, default: low }
  count:    { type: int, default: 1 }
  urgent:   { type: bool, default: false }
states:
  send: { invoke: send, transitions: { done: done } }
  done: { final: true }
`

// newProject makes a temp project with the notify machine.
func newProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, config.MachineFile(dir, "notify"), notifyMachine)
	return dir
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

// load writes a config with the given body after `project` and `decree`
// lines pointing at proj and the stub, and loads it.
func load(t *testing.T, proj string, stub *decreetest.Stub, body string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "decree-go-rest.yml")
	write(t, path, "project: "+proj+"\ndecree: "+stub.Path+"\n"+body)
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// want is one expected error: its endpoint, its rule, and a part of its
// message.
type want struct {
	endpoint, rule, msg string
}

func matchErrors(t *testing.T, got config.Errors, wants []want) {
	t.Helper()
	used := make([]bool, len(got))
	for _, w := range wants {
		found := false
		for i, e := range got {
			if !used[i] && e.Endpoint == w.endpoint && e.Rule == w.rule && strings.Contains(e.Msg, w.msg) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			t.Errorf("missing error {endpoint %q, rule %q, %q}", w.endpoint, w.rule, w.msg)
		}
	}
	for i, e := range got {
		if !used[i] {
			t.Errorf("unexpected error: %v", e)
		}
	}
}

func TestValidateRules(t *testing.T) {
	ok := "endpoints:\n  - path: /notify\n    message: { machine: notify }\n"
	cases := []struct {
		name  string
		body  string
		env   map[string]string // set on top of DECREE_GO_REST_SECRET=goodSecret; "-" unsets
		setup func(t *testing.T, proj string, stub *decreetest.Stub)
		want  []want
	}{
		// Everything passes.
		{name: "valid minimal", body: ok},
		{name: "valid example-like", body: `endpoints:
  - path: /notify
    message: { machine: notify, params: { title: Untitled, priority: high, count: 3, urgent: true } }
  - path: /notify/{title}
    patterns: { title: '[A-Za-z0-9 _.-]{1,80}' }
    message: { machine: notify, params: { title: '{{title}}', priority: high } }
  - path: /n/{a}/{b}
    secret_env: OTHER_SECRET
    body: optional
    message: { machine: notify, params: { title: '{{a}}-{{b}}' } }
`, env: map[string]string{"OTHER_SECRET": otherSecret}},

		// Config values outside the numbered steps.
		{name: "config: no endpoints", body: "endpoints: []\n", want: []want{{"", config.RuleConfig, "at least one endpoint"}}},
		{name: "config: endpoints missing", body: "listen: 127.0.0.1:1\n", want: []want{{"", config.RuleConfig, "at least one endpoint"}}},
		{name: "config: bad body", body: "endpoints:\n  - path: /notify\n    body: maybe\n    message: { machine: notify }\n",
			want: []want{{"/notify", config.RuleConfig, `"maybe" is not required, optional or none`}}},
		{name: "config: body none passes", body: "endpoints:\n  - path: /notify\n    body: none\n    message: { machine: notify }\n"},
		{name: "config: zero limits", body: "daemon: { interval: 0s }\nlimits: { max_body_bytes: 0, rate_window: 0s, rate_max: 0, rate_fail_max: -1 }\n" + ok,
			want: []want{
				{"", config.RuleConfig, "daemon.interval"}, {"", config.RuleConfig, "max_body_bytes"},
				{"", config.RuleConfig, "rate_window"}, {"", config.RuleConfig, "rate_max"}, {"", config.RuleConfig, "rate_fail_max"},
			}},
		{name: "config: project without .decree", body: ok,
			setup: func(t *testing.T, proj string, _ *decreetest.Stub) {
				if err := os.RemoveAll(filepath.Join(proj, ".decree")); err != nil {
					t.Fatal(err)
				}
			},
			want: []want{{"", config.RuleConfig, "holds no .decree/"}, {"/notify", config.RuleMachine, "no file"}}},

		// Step 1: path shape.
		{name: "path: missing", body: "endpoints:\n  - message: { machine: notify }\n", want: []want{{"", config.RulePath, "path is required"}}},
		{name: "path: no leading slash", body: "endpoints:\n  - path: notify\n    message: { machine: notify }\n", want: []want{{"notify", config.RulePath, "must match"}}},
		{name: "path: bad character", body: "endpoints:\n  - path: /no tify\n    message: { machine: notify }\n", want: []want{{"/no tify", config.RulePath, "must match"}}},
		{name: "path: root only", body: "endpoints:\n  - path: /\n    message: { machine: notify }\n",
			want: []want{{"/", config.RulePath, "must match"}, {"/", config.RulePath, "empty segment"}}},
		{name: "path: dotdot", body: "endpoints:\n  - path: /a/../b\n    message: { machine: notify }\n", want: []want{{"/a/../b", config.RulePath, "must not contain .."}}},
		{name: "path: empty segment", body: "endpoints:\n  - path: /a//b\n    message: { machine: notify }\n", want: []want{{"/a//b", config.RulePath, "empty segment"}}},
		{name: "path: trailing slash", body: "endpoints:\n  - path: /a/\n    message: { machine: notify }\n", want: []want{{"/a/", config.RulePath, "empty segment"}}},
		{name: "path: partial parameter", body: "endpoints:\n  - path: /a/x{t}\n    message: { machine: notify }\n",
			want: []want{{"/a/x{t}", config.RulePath, "full segment"}}},
		{name: "path: parameter name not \\w+", body: "endpoints:\n  - path: /a/{t-x}\n    message: { machine: notify }\n",
			want: []want{{"/a/{t-x}", config.RulePath, "full segment"}}},
		{name: "path: repeated parameter", body: "endpoints:\n  - path: /a/{t}/{t}\n    message: { machine: notify, params: { title: '{{t}}' } }\n",
			want: []want{{"/a/{t}/{t}", config.RulePath, "appears twice"}}},
		{name: "path: duplicate", body: ok + "  - path: /notify\n    message: { machine: notify }\n", want: []want{{"/notify", config.RulePath, "duplicate path"}}},
		{name: "path: net/http conflict", body: `endpoints:
  - path: /a/{x}
    message: { machine: notify, params: { title: '{{x}}' } }
  - path: /a/{y}
    message: { machine: notify, params: { title: '{{y}}' } }
`, want: []want{{"/a/{y}", config.RulePath, "conflicts"}}},
		{name: "path: literal beside parameter passes", body: `endpoints:
  - path: /a/{x}
    message: { machine: notify, params: { title: '{{x}}' } }
  - path: /a/b
    message: { machine: notify }
`},
		{name: "path: /runs/ is free", body: "endpoints:\n  - path: /runs/{a}/{b}/{c}\n    action: event\n    reply: { to: '{{a}}.{{b}}', event: '{{c}}' }\n"},
		{name: "path: /healthz reserved", body: "endpoints:\n  - path: /healthz\n    message: { machine: notify }\n", want: []want{{"/healthz", config.RulePath, "under /healthz, which is reserved"}}},
		{name: "path: under /healthz reserved", body: "endpoints:\n  - path: /healthz/x\n    message: { machine: notify }\n", want: []want{{"/healthz/x", config.RulePath, "under /healthz, which is reserved"}}},
		{name: "path: /openapi.json reserved", body: "endpoints:\n  - path: /openapi.json\n    message: { machine: notify }\n", want: []want{{"/openapi.json", config.RulePath, "under /openapi.json, which is reserved"}}},
		{name: "path: /openapi.json reserved for an event endpoint", body: "endpoints:\n  - path: /openapi.json\n    action: event\n    reply: { to: r, event: approve }\n",
			want: []want{{"/openapi.json", config.RulePath, "under /openapi.json, which is reserved"}}},
		{name: "path: wildcards beside the reserved paths pass", body: `endpoints:
  - path: /{a}
    message: { machine: notify, params: { title: '{{a}}' } }
  - path: /{a}/x
    message: { machine: notify, params: { title: '{{a}}' } }
  - path: /{a}/{b}/{c}
    message: { machine: notify, params: { title: '{{a}}{{b}}{{c}}' } }
`},

		// Step 2: patterns.
		{name: "patterns: valid", body: "endpoints:\n  - path: /n/{t}\n    patterns: { t: '[a-z]+' }\n    message: { machine: notify, params: { title: '{{t}}' } }\n"},
		{name: "patterns: not a parameter", body: "endpoints:\n  - path: /n/{t}\n    patterns: { u: '[a-z]+' }\n    message: { machine: notify, params: { title: '{{t}}' } }\n",
			want: []want{{"/n/{t}", config.RulePatterns, `"u" is not a parameter`}}},
		{name: "patterns: does not compile", body: "endpoints:\n  - path: /n/{t}\n    patterns: { t: '[a-z' }\n    message: { machine: notify, params: { title: '{{t}}' } }\n",
			want: []want{{"/n/{t}", config.RulePatterns, "does not compile"}}},

		// Step 3: placeholders.
		{name: "placeholders: unknown parameter", body: "endpoints:\n  - path: /n/{t}\n    message: { machine: notify, params: { title: '{{t}}', priority: '{{p}}' } }\n",
			want: []want{{"/n/{t}", config.RulePlaceholders, "{{p}} is not a parameter"}}},
		{name: "placeholders: spaces inside braces", body: "endpoints:\n  - path: /n/{t}\n    message: { machine: notify, params: { title: '{{ t }}' } }\n",
			want: []want{{"/n/{t}", config.RulePlaceholders, "{{ t }} is not a parameter"}, {"/n/{t}", config.RulePlaceholders, "{t} is not used"}}},
		{name: "placeholders: in a non-string value", body: "endpoints:\n  - path: /n/{t}\n    message: { machine: notify, params: { title: {{t}} } }\n",
			want: []want{{"/n/{t}", config.RulePlaceholders, "must be a string, int or bool, not a mapping"}, {"/n/{t}", config.RulePlaceholders, "{t} is not used"}}},
		{name: "placeholders: list value", body: "endpoints:\n  - path: /notify\n    message: { machine: notify, params: { title: [a] } }\n",
			want: []want{{"/notify", config.RulePlaceholders, "not a sequence"}}},
		{name: "placeholders: in a key", body: "endpoints:\n  - path: /n/{t}\n    message: { machine: notify, params: { '{{t}}': x, title: '{{t}}' } }\n",
			want: []want{{"/n/{t}", config.RulePlaceholders, "placeholders appear only in string values"}, {"/n/{t}", config.RuleParams, "no data"}}},
		{name: "placeholders: parameter unused", body: "endpoints:\n  - path: /n/{t}/{u}\n    message: { machine: notify, params: { title: '{{t}}' } }\n",
			want: []want{{"/n/{t}/{u}", config.RulePlaceholders, "{u} is not used"}}},

		// Step 4: machine.
		{name: "machine: missing key", body: "endpoints:\n  - path: /notify\n    message: { params: { title: x } }\n",
			want: []want{{"/notify", config.RuleMachine, "message.machine is required"}}},
		{name: "machine: no file", body: "endpoints:\n  - path: /notify\n    message: { machine: nope }\n", want: []want{{"/notify", config.RuleMachine, "machine nope: no file"}}},
		{name: "machine: placeholder", body: "endpoints:\n  - path: /n/{t}\n    message: { machine: '{{t}}', params: { title: '{{t}}' } }\n",
			want: []want{{"/n/{t}", config.RuleMachine, "placeholder is not allowed"}}},
		{name: "machine: path traversal", body: "endpoints:\n  - path: /notify\n    message: { machine: ../notify }\n",
			want: []want{{"/notify", config.RuleMachine, "not a machine name"}}},

		// Step 5: params.
		{name: "params: not in data", body: "endpoints:\n  - path: /notify\n    message: { machine: notify, params: { colour: red } }\n",
			want: []want{{"/notify", config.RuleParams, `machine notify has no data "colour"`}}},
		{name: "params: int for string", body: "endpoints:\n  - path: /notify\n    message: { machine: notify, params: { title: 5 } }\n",
			want: []want{{"/notify", config.RuleParams, "title is string, the value is int"}}},
		{name: "params: string for int", body: "endpoints:\n  - path: /notify\n    message: { machine: notify, params: { count: 'five' } }\n",
			want: []want{{"/notify", config.RuleParams, "count is int, the value is string"}}},
		{name: "params: string for bool", body: "endpoints:\n  - path: /notify\n    message: { machine: notify, params: { urgent: 'true' } }\n",
			want: []want{{"/notify", config.RuleParams, "urgent is bool, the value is string"}}},
		{name: "params: null value", body: "endpoints:\n  - path: /notify\n    message: { machine: notify, params: { title: } }\n",
			want: []want{{"/notify", config.RuleParams, "the value is null"}}},
		{name: "params: placeholder into int", body: "endpoints:\n  - path: /n/{c}\n    message: { machine: notify, params: { count: '{{c}}' } }\n",
			want: []want{{"/n/{c}", config.RuleParams, "requires string data, and count is int"}}},
		{name: "params: machine YAML unreadable", body: "endpoints:\n  - path: /notify\n    message: { machine: broken }\n",
			setup: func(t *testing.T, proj string, _ *decreetest.Stub) {
				write(t, config.MachineFile(proj, "broken"), "data: [\n")
			},
			want: []want{{"/notify", config.RuleParams, "cannot read its data"}}},

		// Actions: emit (the default) and event, each with its required and
		// forbidden keys.
		{name: "action: emit explicit", body: "endpoints:\n  - path: /notify\n    action: emit\n    message: { machine: notify }\n"},
		{name: "action: unknown", body: "endpoints:\n  - path: /notify\n    action: reply\n    message: { machine: notify }\n",
			want: []want{{"/notify", config.RuleConfig, `action: "reply" is not emit or event`}}},
		{name: "action: emit without message", body: "endpoints:\n  - path: /notify\n",
			want: []want{{"/notify", config.RuleMachine, "action: emit requires message: { machine, params }"}}},
		{name: "action: emit with reply names the event form", body: "endpoints:\n  - path: /a/{w}\n    message: { machine: notify, params: { title: '{{w}}' } }\n    reply: { to: '{{w}}', event: approve }\n",
			want: []want{{"/a/{w}", config.RuleConfig, "reply is not allowed with action: emit, which queues message: { machine, params }; an endpoint that replies to a waiting run is action: event"}}},
		{name: "action: emit with only reply", body: "endpoints:\n  - path: /a/{w}\n    reply: { to: '{{w}}', event: approve }\n",
			want: []want{{"/a/{w}", config.RuleConfig, "an endpoint that replies to a waiting run is action: event"}, {"/a/{w}", config.RuleMachine, "action: emit requires message"}}},
		{name: "action: event valid", body: eventOK, env: map[string]string{"OTHER_SECRET": otherSecret}},
		{name: "action: event, literal to, no parameters", body: "endpoints:\n  - path: /approve\n    action: event\n    reply: { to: r.w1, event: approve }\n"},
		{name: "action: event, event from the path", body: "endpoints:\n  - path: /reply/{w}/{e}\n    action: event\n    reply: { to: '{{w}}', event: '{{e}}' }\n"},
		{name: "action: event, dotted event", body: "endpoints:\n  - path: /approve/{w}\n    action: event\n    reply: { to: '{{w}}', event: review.ok_2 }\n"},
		{name: "action: event with message names the emit form", body: "endpoints:\n  - path: /approve/{w}\n    action: event\n    message: { machine: notify, params: { title: '{{w}}' } }\n    reply: { to: '{{w}}', event: approve }\n",
			want: []want{{"/approve/{w}", config.RuleConfig, "message is not allowed with action: event, which sends reply: { to, event }; an endpoint that queues a message is action: emit (the default)"}}},
		{name: "action: event without reply", body: "endpoints:\n  - path: /approve\n    action: event\n",
			want: []want{{"/approve", config.RuleConfig, "action: event requires reply: { to: '{{wait_id}}', event: <event> }"}}},
		{name: "action: event, reply without to or event", body: "endpoints:\n  - path: /approve\n    action: event\n    reply: {}\n",
			want: []want{{"/approve", config.RuleConfig, "reply.to is required"}, {"/approve", config.RuleConfig, "reply.event is required"}}},
		{name: "action: event, bad literal event", body: "endpoints:\n  - path: /approve/{w}\n    action: event\n    reply: { to: '{{w}}', event: Approve }\n",
			want: []want{{"/approve/{w}", config.RuleConfig, `reply.event "Approve" does not match decree's event-name pattern`}}},
		{name: "action: event, event ending in a dot", body: "endpoints:\n  - path: /approve/{w}\n    action: event\n    reply: { to: '{{w}}', event: approve. }\n",
			want: []want{{"/approve/{w}", config.RuleConfig, "event-name pattern"}}},
		{name: "action: event, unknown placeholder", body: "endpoints:\n  - path: /approve/{w}\n    action: event\n    reply: { to: '{{w}}', event: '{{e}}' }\n",
			want: []want{{"/approve/{w}", config.RulePlaceholders, "reply.event: {{e}} is not a parameter of the path"}}},
		{name: "action: event, parameter unused", body: "endpoints:\n  - path: /approve/{w}/{x}\n    action: event\n    reply: { to: '{{w}}', event: approve }\n",
			want: []want{{"/approve/{w}/{x}", config.RulePlaceholders, "parameter {x} is not used in reply"}}},
		{name: "action: event, machine and params not checked", body: "endpoints:\n  - path: /approve/{w}\n    action: event\n    reply: { to: '{{w}}', event: approve }\n",
			setup: func(t *testing.T, proj string, _ *decreetest.Stub) {
				if err := os.Remove(config.MachineFile(proj, "notify")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "action: event, own secret unset", body: "endpoints:\n  - path: /approve/{w}\n    action: event\n    secret_env: APPROVE_SECRET\n    reply: { to: '{{w}}', event: approve }\n",
			env: map[string]string{"APPROVE_SECRET": "-"}, want: []want{{"/approve/{w}", config.RuleSecrets, "APPROVE_SECRET is not set"}}},
		{name: "action: event, body rule checked", body: "endpoints:\n  - path: /approve\n    action: event\n    body: maybe\n    reply: { to: r, event: approve }\n",
			want: []want{{"/approve", config.RuleConfig, `"maybe" is not required, optional or none`}}},

		// Step 6: secrets.
		{name: "secrets: default unset", body: ok, env: map[string]string{"DECREE_GO_REST_SECRET": "-"},
			want: []want{
				{"/notify", config.RuleSecrets, "DECREE_GO_REST_SECRET is not set"},
			}},
		{name: "secrets: blank", body: ok, env: map[string]string{"DECREE_GO_REST_SECRET": strings.Repeat(" ", 40)},
			want: []want{{"/notify", config.RuleSecrets, "DECREE_GO_REST_SECRET is blank"}}},
		{name: "secrets: short", body: ok, env: map[string]string{"DECREE_GO_REST_SECRET": goodSecret[:31]},
			want: []want{{"/notify", config.RuleSecrets, "is 31 characters, at least 32"}}},
		{name: "secrets: endpoint's own unset", body: "endpoints:\n  - path: /notify\n    secret_env: OTHER_SECRET\n    message: { machine: notify }\n",
			env: map[string]string{"OTHER_SECRET": "-"}, want: []want{{"/notify", config.RuleSecrets, "OTHER_SECRET is not set"}}},
		{name: "secrets: default unused needs no value", body: "endpoints:\n  - path: /notify\n    secret_env: OTHER_SECRET\n    message: { machine: notify }\n",
			env: map[string]string{"DECREE_GO_REST_SECRET": "-", "OTHER_SECRET": otherSecret}},
		{name: "secrets: custom default name", body: "secret_env: MY_SECRET\n" + ok, env: map[string]string{"DECREE_GO_REST_SECRET": "-", "MY_SECRET": otherSecret}},
		{name: "secrets: empty secret_env", body: "secret_env: ''\n" + ok,
			want: []want{{"/notify", config.RuleSecrets, "no secret_env"}}},
		{name: "secrets: event endpoint's own, default unset", body: "endpoints:\n  - path: /approve\n    action: event\n    secret_env: APPROVE_SECRET\n    reply: { to: r, event: approve }\n",
			env: map[string]string{"DECREE_GO_REST_SECRET": "-", "APPROVE_SECRET": otherSecret}},
		{name: "secrets: event endpoint's own short", body: "endpoints:\n  - path: /approve\n    action: event\n    secret_env: APPROVE_SECRET\n    reply: { to: r, event: approve }\n",
			env: map[string]string{"APPROVE_SECRET": goodSecret[:31]}, want: []want{{"/approve", config.RuleSecrets, "APPROVE_SECRET is 31 characters, at least 32"}}},

		// Step 7: decree.
		{name: "decree: newer version passes", body: ok, setup: func(t *testing.T, _ string, s *decreetest.Stub) { s.SetVersion(t, "decree 1.0.0") }},
		{name: "decree: 0.10 passes", body: ok, setup: func(t *testing.T, _ string, s *decreetest.Stub) { s.SetVersion(t, "decree 0.10.1") }},
		{name: "decree: old version", body: ok, setup: func(t *testing.T, _ string, s *decreetest.Stub) { s.SetVersion(t, "decree 0.4.9") },
			want: []want{{"", config.RuleDecree, "is version 0.4, 0.5 or newer is required"}}},
		{name: "decree: unreadable version", body: ok, setup: func(t *testing.T, _ string, s *decreetest.Stub) { s.SetVersion(t, "decree") },
			want: []want{{"", config.RuleDecree, "no version"}}},
		{name: "decree: invalid project", body: ok, setup: func(t *testing.T, _ string, s *decreetest.Stub) { s.SetCheck(t, decreetest.InvalidCheck, 1) },
			want: []want{{"", config.RuleDecree, "check: machines/bad.yml: line 1: missing field `description`"}}},
		{name: "decree: check prints no JSON", body: ok, setup: func(t *testing.T, _ string, s *decreetest.Stub) { s.SetCheck(t, "", 1) },
			want: []want{{"", config.RuleDecree, "error: decree check found errors"}}},
		{name: "decree: binary missing", body: ok, setup: func(t *testing.T, _ string, s *decreetest.Stub) {
			if err := os.Remove(s.Path); err != nil {
				t.Fatal(err)
			}
		}, want: []want{{"", config.RuleDecree, "cannot find"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(config.ListenEnv, "")
			t.Setenv("DECREE_GO_REST_SECRET", goodSecret)
			t.Setenv("OTHER_SECRET", "")
			os.Unsetenv("OTHER_SECRET")
			for k, v := range tc.env {
				t.Setenv(k, v)
				if v == "-" {
					os.Unsetenv(k)
				}
			}
			proj := newProject(t)
			stub := decreetest.New(t)
			if tc.setup != nil {
				tc.setup(t, proj, stub)
			}
			c := load(t, proj, stub, tc.body)
			matchErrors(t, config.Validate(c, config.Options{}), tc.want)
		})
	}
}

// eventOK is a valid event endpoint with its own secret, as the example's.
const eventOK = `endpoints:
  - path: /approve/{wait_id}
    action: event
    secret_env: OTHER_SECRET
    patterns: { wait_id: '[A-Za-z0-9._-]{1,128}' }
    body: optional
    reply: { to: '{{wait_id}}', event: approve }
`

func TestValidateReportsEveryErrorTogether(t *testing.T) {
	t.Setenv("DECREE_GO_REST_SECRET", "short")
	proj := newProject(t)
	stub := decreetest.New(t)
	stub.SetVersion(t, "decree 0.4.0")
	c := load(t, proj, stub, `endpoints:
  - path: /a//b
    message: { machine: notify }
  - path: /n/{t}
    patterns: { t: '(' }
    message: { machine: notify, params: { title: '{{x}}' } }
  - path: /m
    message: { machine: nope }
  - path: /p
    message: { machine: notify, params: { count: many } }
`)
	errs := config.Validate(c, config.Options{})
	matchErrors(t, errs, []want{
		{"/a//b", config.RulePath, "empty segment"},
		{"/n/{t}", config.RulePatterns, "does not compile"},
		{"/n/{t}", config.RulePlaceholders, "{{x}} is not a parameter"},
		{"/n/{t}", config.RulePlaceholders, "{t} is not used"},
		{"/m", config.RuleMachine, "no file"},
		{"/p", config.RuleParams, "count is int"},
		{"/a//b", config.RuleSecrets, "is 5 characters"},
		{"/n/{t}", config.RuleSecrets, "is 5 characters"},
		{"/m", config.RuleSecrets, "is 5 characters"},
		{"/p", config.RuleSecrets, "is 5 characters"},
		{"", config.RuleDecree, "0.5 or newer"},
	})
	for _, e := range errs {
		s := e.Error()
		if e.Endpoint != "" && !strings.HasPrefix(s, "endpoint "+e.Endpoint+": "+e.Rule+": ") {
			t.Errorf("%q does not name its endpoint and rule", s)
		}
	}
}

func TestValidateRunsDecreeInProject(t *testing.T) {
	t.Setenv("DECREE_GO_REST_SECRET", goodSecret)
	proj := newProject(t)
	stub := decreetest.New(t)
	c := load(t, proj, stub, "endpoints:\n  - path: /notify\n    message: { machine: notify }\n")
	if errs := config.Validate(c, config.Options{}); errs != nil {
		t.Fatal(errs)
	}
	calls := stub.Calls(t)
	if len(calls) != 2 || !strings.HasSuffix(calls[0], " --version") || calls[1] != "cwd="+proj+" check --format json" {
		t.Errorf("calls = %q, want --version then check --format json in %s", calls, proj)
	}
}

func TestValidateSkipDecree(t *testing.T) {
	t.Setenv("DECREE_GO_REST_SECRET", goodSecret)
	proj := newProject(t)
	stub := decreetest.New(t)
	stub.SetVersion(t, "decree 0.1.0")
	c := load(t, proj, stub, "endpoints:\n  - path: /notify\n    message: { machine: notify }\n")
	if errs := config.Validate(c, config.Options{SkipDecree: true}); errs != nil {
		t.Fatal(errs)
	}
	if calls := stub.Calls(t); len(calls) != 0 {
		t.Errorf("decree ran with SkipDecree: %q", calls)
	}
}

func TestDecreeOnPathAndRelative(t *testing.T) {
	t.Setenv("DECREE_GO_REST_SECRET", goodSecret)
	proj := newProject(t)
	stub := decreetest.New(t)
	stub.OnPath(t)
	path := filepath.Join(stub.Dir, "decree-go-rest.yml")
	ep := "endpoints:\n  - path: /notify\n    message: { machine: notify }\n"
	for _, d := range []string{"decree", "./decree"} {
		write(t, path, "project: "+proj+"\ndecree: "+d+"\n"+ep)
		c, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if errs := config.Validate(c, config.Options{}); errs != nil {
			t.Errorf("decree: %s: %v", d, errs)
		}
	}
}

func TestParseDecreeVersion(t *testing.T) {
	for in, want := range map[string][2]int{"decree 0.5.0\n": {0, 5}, "decree 0.12": {0, 12}, "decree 1.2.3-rc1": {1, 2}} {
		ma, mi, err := config.ParseDecreeVersion(in)
		if err != nil || [2]int{ma, mi} != want {
			t.Errorf("ParseDecreeVersion(%q) = %d.%d, %v", in, ma, mi, err)
		}
	}
	if _, _, err := config.ParseDecreeVersion("decree"); err == nil {
		t.Error("ParseDecreeVersion without a version succeeded")
	}
}
