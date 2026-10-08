package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/jtmckay/decree-go-rest/internal/config"
	"github.com/jtmckay/decree-go-rest/internal/decreetest"
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
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// exampleProject copies example/.decree/decree-go-rest.yml into a temp project that
// holds the machines it names, puts a stub decree on PATH, sets the
// secrets, and returns the config path and the stub.
func exampleProject(t *testing.T) (string, *decreetest.Stub) {
	t.Helper()
	example, err := os.ReadFile("../../example/.decree/decree-go-rest.yml")
	if err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	write(t, config.MachineFile(proj, "notify"), notifyMachine)
	write(t, config.MachineFile(proj, "comfy_image"), comfyMachine)
	path := filepath.Join(proj, "decree-go-rest.yml")
	// The tests supervise a stub daemon; the example leaves it to its own container.
	write(t, path, strings.Replace(string(example), "enabled: false ", "enabled: true ", 1))
	stub := decreetest.New(t)
	stub.OnPath(t)
	t.Setenv(config.ListenEnv, "")
	t.Setenv("DECREE_GO_REST_SECRET", strings.Repeat("a", 64))
	t.Setenv("COMFY_SECRET", strings.Repeat("b", 32))
	t.Setenv("DECREE_GO_REST_APPROVE_SECRET", strings.Repeat("c", 40))
	t.Setenv(config.OpenAPIEnv, "")
	t.Setenv(config.DaemonEnv, "")
	return path, stub
}

func runCLI(args ...string) (code int, stdout, stderr string) {
	var o, e bytes.Buffer
	code = run(args, &o, &e)
	return code, o.String(), e.String()
}

func TestCheckExampleOK(t *testing.T) {
	path, stub := exampleProject(t)
	code, out, errOut := runCLI("-check", "-config", path)
	if code != 0 || out != "config ok: 4 endpoints\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and \"config ok: 4 endpoints\"", code, out, errOut)
	}
	if calls := stub.Calls(t); len(calls) != 2 {
		t.Errorf("decree calls = %q, want --version and check", calls)
	}
}

func TestCheckDefaultConfigPath(t *testing.T) {
	path, _ := exampleProject(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	if code, out, errOut := runCLI("-check"); code != 0 || out != "config ok: 4 endpoints\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestCheckListsEveryError(t *testing.T) {
	path, stub := exampleProject(t)
	t.Setenv("COMFY_SECRET", "too-short")
	stub.SetCheck(t, decreetest.InvalidCheck, 1)
	if err := os.Remove(config.MachineFile(filepath.Dir(path), "comfy_image")); err != nil {
		t.Fatal(err)
	}
	write(t, path, `project: .
endpoints:
  - path: /notify
    message:
      machine: notify
      params: { title: Untitled, priority: 3 }
  - path: /notify/{title}/
    message: { machine: notify, params: { title: '{{title}}' } }
  - path: /comfy/{type}/{name}
    secret_env: COMFY_SECRET
    patterns: { type: '[a-z', kind: x }
    message:
      machine: comfy_image
      params: { workflow: '{{type}}' }
`)
	code, out, errOut := runCLI("-check", "-config", path)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout %q", code, out)
	}
	if out != "" {
		t.Errorf("stdout %q, want nothing", out)
	}
	wants := []string{
		"config invalid: 8 error(s)",
		"endpoint /notify/{title}/: path: has an empty segment",
		`endpoint /comfy/{type}/{name}: patterns: "kind" is not a parameter of the path`,
		`endpoint /comfy/{type}/{name}: patterns: "type" does not compile`,
		"endpoint /comfy/{type}/{name}: placeholders: parameter {name} is not used in params",
		"endpoint /comfy/{type}/{name}: machine: machine comfy_image: no file",
		"endpoint /notify: params: params.priority: priority is string, the value is int",
		"endpoint /comfy/{type}/{name}: secrets: COMFY_SECRET is 9 characters, at least 32 are required",
		"decree: check: machines/bad.yml: line 1: missing field `description`",
	}
	for _, w := range wants {
		if !strings.Contains(errOut, w) {
			t.Errorf("stderr lacks %q:\n%s", w, errOut)
		}
	}
}

func TestCheckUnreadableConfig(t *testing.T) {
	dir := t.TempDir()
	code, _, errOut := runCLI("-check", "-config", filepath.Join(dir, "missing.yml"))
	if code != 1 || !strings.Contains(errOut, "config invalid") {
		t.Errorf("missing file: exit %d, stderr %q", code, errOut)
	}
	path := filepath.Join(dir, "decree-go-rest.yml")
	write(t, path, "endpoints: []\nlisten_on: x\n")
	code, _, errOut = runCLI("-check", "-config", path)
	if code != 1 || !strings.Contains(errOut, "listen_on") {
		t.Errorf("unknown key: exit %d, stderr %q", code, errOut)
	}
}

func TestCommandLine(t *testing.T) {
	if code, _, _ := runCLI("-nope"); code != 2 {
		t.Errorf("unknown flag: exit %d, want 2", code)
	}
	if code, _, _ := runCLI("-check", "extra"); code != 2 {
		t.Errorf("extra argument: exit %d, want 2", code)
	}
	if code, _, errOut := runCLI("-config", filepath.Join(t.TempDir(), "missing.yml")); code != 1 || !strings.Contains(errOut, "config invalid") {
		t.Errorf("serve without a config: exit %d, stderr %q", code, errOut)
	}
}

// schemaLine is the first line of example/.decree/decree-go-rest.yml, which points
// editors at decree-go-rest.schema.json (SPEC.md §11).
const schemaLine = "# yaml-language-server: $schema=https://raw.githubusercontent.com/jtmckay/decree-go-rest/main/decree-go-rest.schema.json\n"

// TestExampleMatchesSpec keeps example/.decree/decree-go-rest.yml the example of
// SPEC.md §3, below its $schema line.
func TestExampleMatchesSpec(t *testing.T) {
	spec, err := os.ReadFile("../../SPEC.md")
	if err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile("../../example/.decree/decree-go-rest.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(spec)
	start := strings.Index(s, "A full example:\n\n```yaml\n")
	if start < 0 {
		t.Fatal("SPEC.md has no full example")
	}
	s = s[start+len("A full example:\n\n```yaml\n"):]
	s = s[:strings.Index(s, "```")]
	rest, ok := strings.CutPrefix(string(example), schemaLine)
	if !ok {
		t.Errorf("example/.decree/decree-go-rest.yml does not start with %q", schemaLine)
	}
	if s != rest {
		t.Errorf("example/.decree/decree-go-rest.yml differs from SPEC.md §3's example")
	}
}

// TestStartupRejectsReservedPaths: a configured path that is, or is
// under, /healthz or /openapi.json is a startup error, for -check and for
// serving alike, whatever DECREE_GO_REST_OPENAPI says (SPEC.md §7).
func TestStartupRejectsReservedPaths(t *testing.T) {
	path, _ := exampleProject(t)
	write(t, path, `project: .
daemon: { enabled: false }
endpoints:
  - path: /openapi.json
    message: { machine: notify }
  - path: /healthz
    action: event
    reply: { to: r, event: approve }
  - path: /healthz/x
    message: { machine: notify }
`)
	t.Setenv(config.ListenEnv, "127.0.0.1:0")
	wants := []string{
		"endpoint /openapi.json: path: is under /openapi.json, which is reserved",
		"endpoint /healthz: path: is under /healthz, which is reserved",
		"endpoint /healthz/x: path: is under /healthz, which is reserved",
	}
	for _, openapi := range []string{"", "false", "true"} {
		t.Setenv(config.OpenAPIEnv, openapi)
		for _, args := range [][]string{{"-check", "-config", path}, {"-config", path}} {
			code, errOut := runRefused(t, args...)
			if code != 1 {
				t.Errorf("%s=%q %q: exit %d, want 1", config.OpenAPIEnv, openapi, args, code)
			}
			for _, w := range wants {
				if !strings.Contains(errOut, w) {
					t.Errorf("%s=%q %q: stderr lacks %q:\n%s", config.OpenAPIEnv, openapi, args, w, errOut)
				}
			}
		}
	}
}

// runRefused runs the CLI, which must exit by itself, as it does when it
// refuses to start, and returns its exit code and stderr.
func runRefused(t *testing.T, args ...string) (int, string) {
	t.Helper()
	done := make(chan struct{})
	var code int
	var errOut string
	go func() { defer close(done); code, _, errOut = runCLI(args...) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%q: still running; it should refuse to start", args)
	}
	return code, errOut
}

// TestAcceptanceRemovedKey: a config with the old built-ins key fails to
// start, and fails -check, naming the endpoint action form.
func TestAcceptanceRemovedKey(t *testing.T) {
	path, _ := exampleProject(t)
	example, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, strings.Replace(string(example), "endpoints:\n", config.RemovedKey+":\n  replies: { enabled: true }\n\nendpoints:\n", 1))
	t.Setenv(config.ListenEnv, "127.0.0.1:0")
	for _, args := range [][]string{{"-check", "-config", path}, {"-config", path}} {
		code, errOut := runRefused(t, args...)
		if code != 1 {
			t.Errorf("%q: exit %d, want 1", args, code)
		}
		for _, w := range []string{config.RemovedKey + " is no longer accepted", "`action: event`", config.OpenAPIEnv + "=true"} {
			if !strings.Contains(errOut, w) {
				t.Errorf("%q: stderr lacks %q:\n%s", args, w, errOut)
			}
		}
	}
}

// TestCheckMixedActionKeys: an endpoint mixing message and reply fails
// -check, naming the action form it needs.
func TestCheckMixedActionKeys(t *testing.T) {
	path, _ := exampleProject(t)
	write(t, path, `project: .
endpoints:
  - path: /approve/{wait_id}
    message: { machine: notify, params: { title: '{{wait_id}}' } }
    reply: { to: '{{wait_id}}', event: approve }
  - path: /reply/{wait_id}
    action: event
    message: { machine: notify }
    reply: { to: '{{wait_id}}', event: approve }
`)
	code, errOut := runRefused(t, "-check", "-config", path)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	for _, w := range []string{
		"endpoint /approve/{wait_id}: config: reply is not allowed with action: emit",
		"is action: event",
		"endpoint /reply/{wait_id}: config: message is not allowed with action: event",
		"is action: emit (the default)",
	} {
		if !strings.Contains(errOut, w) {
			t.Errorf("stderr lacks %q:\n%s", w, errOut)
		}
	}
}

// TestVersionFlag: -version prints decree-go-rest's version and exits 0, the
// linker's version first.
func TestVersionFlag(t *testing.T) {
	code, out, _ := runCLI("-version")
	if code != 0 || !strings.HasPrefix(out, "decree-go-rest ") || strings.TrimSpace(out) == "decree-go-rest" {
		t.Errorf("-version: exit %d, stdout %q", code, out)
	}
	old := version
	version = "v1.2.3"
	defer func() { version = old }()
	if code, out, _ := runCLI("-version", "-check"); code != 0 || out != "decree-go-rest v1.2.3\n" {
		t.Errorf("-version with a linker version: exit %d, stdout %q", code, out)
	}
}

func TestVersionOf(t *testing.T) {
	info := func(v string, settings ...string) *debug.BuildInfo {
		bi := &debug.BuildInfo{Main: debug.Module{Version: v}}
		for i := 0; i+1 < len(settings); i += 2 {
			bi.Settings = append(bi.Settings, debug.BuildSetting{Key: settings[i], Value: settings[i+1]})
		}
		return bi
	}
	for _, tc := range []struct {
		info *debug.BuildInfo
		want string
	}{
		{info("v0.6.0"), "v0.6.0"},
		{info("(devel)"), "devel"},
		{info(""), "devel"},
		{info("(devel)", "vcs.revision", "85a8c1ae566f0123", "vcs.modified", "false"), "devel-85a8c1ae566f"},
		{info("(devel)", "vcs.revision", "85a8c1a", "vcs.modified", "true"), "devel-85a8c1a-dirty"},
	} {
		if got := versionOf(tc.info); got != tc.want {
			t.Errorf("versionOf(%v, %v) = %q, want %q", tc.info.Main.Version, tc.info.Settings, got, tc.want)
		}
	}
}
