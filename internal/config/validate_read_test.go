package config_test

import (
	"strings"
	"testing"

	"github.com/jtmckay/decree-go-rest/internal/config"
	"github.com/jtmckay/decree-go-rest/internal/decreetest"
)

// TestValidateRead covers the rules of a read endpoint: its shape, its
// machine, read.path and its placeholders.
func TestValidateRead(t *testing.T) {
	cases := []struct {
		name, body string
		want       []want
	}{
		{name: "directory", body: `endpoints:
  - path: /leases
    action: read
    read: { machine: notify, path: status/leases }
`},
		{name: "whole state folder", body: `endpoints:
  - path: /state
    action: read
    read: { machine: notify }
`},
		{name: "placeholders", body: `endpoints:
  - path: /leases/by-id/{id}
    action: read
    patterns: { id: "[0-9TZ.-]+-[a-f0-9]{6}" }
    read: { machine: notify, path: "status/leases/{{id}}.json" }
`},
		{name: "beside a read endpoint below it", body: `endpoints:
  - path: /leases
    action: read
    read: { machine: notify, path: status/leases }
  - path: /leases/by-id/{id}
    action: read
    read: { machine: notify, path: "status/leases/{{id}}.json" }
  - path: /leases/new
    message: { machine: notify }
`},
		{name: "machine missing", body: `endpoints:
  - path: /leases
    action: read
    read: { machine: gpu_lease }
`, want: []want{{"/leases", config.RuleMachine, "machine gpu_lease: no file"}}},
		{name: "machine required", body: `endpoints:
  - path: /leases
    action: read
    read: { path: status }
`, want: []want{{"/leases", config.RuleMachine, "read.machine is required"}}},
		{name: "machine not a name", body: `endpoints:
  - path: /leases
    action: read
    read: { machine: ../notify }
`, want: []want{{"/leases", config.RuleMachine, `read.machine "../notify" is not a machine name`}}},
		{name: "read required", body: `endpoints:
  - path: /leases
    action: read
`, want: []want{{"/leases", config.RuleMachine, "action: read requires read"}}},
		{name: "dot dot in read.path", body: `endpoints:
  - path: /leases
    action: read
    read: { machine: notify, path: status/../../scripts }
`, want: []want{
			{"/leases", config.RuleConfig, `read.path segment ".."`},
			{"/leases", config.RuleConfig, `read.path segment ".."`},
		}},
		{name: "dot segment, hidden name, bad character, empty segment, absolute", body: `endpoints:
  - path: /leases
    action: read
    read: { machine: notify, path: "/./.env/a b//x" }
`, want: []want{
			{"/leases", config.RuleConfig, "must be relative"},
			{"/leases", config.RuleConfig, `read.path segment "."`},
			{"/leases", config.RuleConfig, `read.path segment ".env"`},
			{"/leases", config.RuleConfig, `read.path segment "a b"`},
			{"/leases", config.RuleConfig, `read.path segment ""`},
		}},
		{name: "placeholder not a parameter, parameter unused", body: `endpoints:
  - path: /leases/{id}
    action: read
    read: { machine: notify, path: "status/{{name}}.json" }
`, want: []want{
			{"/leases/{id}", config.RulePlaceholders, "read.path: {{name}} is not a parameter of the path"},
			{"/leases/{id}", config.RulePlaceholders, "parameter {id} is not used in read.path"},
		}},
		{name: "parameter and no read.path", body: `endpoints:
  - path: /leases/{id}
    action: read
    read: { machine: notify }
`, want: []want{{"/leases/{id}", config.RulePlaceholders, "parameter {id} is not used in read.path"}}},
		{name: "message, reply and body", body: `endpoints:
  - path: /leases
    action: read
    body: none
    message: { machine: notify }
    reply: { to: x, event: approve }
    read: { machine: notify }
`, want: []want{
			{"/leases", config.RuleConfig, "message is not allowed with action: read"},
			{"/leases", config.RuleConfig, "reply is not allowed with action: read"},
			{"/leases", config.RuleConfig, "body is not allowed with action: read"},
		}},
		{name: "same path as an emit endpoint", body: `endpoints:
  - path: /leases
    message: { machine: notify }
  - path: /leases
    action: read
    read: { machine: notify }
`, want: []want{{"/leases", config.RulePath, "duplicate path"}}},
		{name: "conflicts with another read endpoint", body: `endpoints:
  - path: /a/{x}
    action: read
    read: { machine: notify, path: "{{x}}" }
  - path: /{y}/b
    action: read
    read: { machine: notify, path: "{{y}}" }
`, want: []want{{"/{y}/b", config.RulePath, "conflicts with another endpoint"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(config.ListenEnv, "")
			t.Setenv("DECREE_GO_REST_SECRET", goodSecret)
			proj := newProject(t)
			stub := decreetest.New(t)
			c := load(t, proj, stub, tc.body)
			matchErrors(t, config.Validate(c, config.Options{}), tc.want)
		})
	}
}

// TestParseReadBody checks that a read endpoint gets no default body, so
// validation can tell a body key was set.
func TestParseReadBody(t *testing.T) {
	c, err := config.Parse([]byte("endpoints:\n  - path: /a\n    action: read\n    read: { machine: m }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if b := c.Endpoints[0].Body; b != "" {
		t.Errorf("body = %q, want none", b)
	}
}

// TestRoutes checks the net/http patterns of each action.
func TestRoutes(t *testing.T) {
	for _, tc := range []struct {
		e    config.Endpoint
		want string
	}{
		{config.Endpoint{Path: "/n", Action: config.ActionEmit}, "POST /n"},
		{config.Endpoint{Path: "/a/{id}", Action: config.ActionReply}, "POST /a/{id}"},
		{config.Endpoint{Path: "/leases", Action: config.ActionRead}, "GET /leases|GET /leases/{rest...}"},
		{config.Endpoint{Path: "/leases/{id}", Action: config.ActionRead}, "GET /leases/{id}"},
	} {
		if got := strings.Join(tc.e.Routes(), "|"); got != tc.want {
			t.Errorf("%s %s: routes %q, want %q", tc.e.Action, tc.e.Path, got, tc.want)
		}
	}
}

func TestValidSegment(t *testing.T) {
	for s, want := range map[string]bool{
		"a.json": true, "old": true, "x_y-1.2": true,
		"": false, ".": false, "..": false, ".hidden": false, "a/b": false, "a b": false, "%2e": false,
	} {
		if got := config.ValidSegment(s); got != want {
			t.Errorf("ValidSegment(%q) = %v, want %v", s, got, want)
		}
	}
}
