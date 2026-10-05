package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtmckay/decree-api/internal/config"
	"github.com/jtmckay/decree-api/internal/decreetest"
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

// exampleProject copies decree-api.example.yml into a temp project that
// holds the machines it names, puts a stub decree on PATH, sets the
// secrets, and returns the config path and the stub.
func exampleProject(t *testing.T) (string, *decreetest.Stub) {
	t.Helper()
	example, err := os.ReadFile("../../decree-api.example.yml")
	if err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	write(t, config.MachineFile(proj, "notify"), notifyMachine)
	write(t, config.MachineFile(proj, "comfy_image"), comfyMachine)
	path := filepath.Join(proj, "decree-api.yml")
	write(t, path, string(example))
	stub := decreetest.New(t)
	stub.OnPath(t)
	t.Setenv(config.ListenEnv, "")
	t.Setenv("DECREE_API_SECRET", strings.Repeat("a", 64))
	t.Setenv("COMFY_SECRET", strings.Repeat("b", 32))
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
	if code != 0 || out != "config ok: 3 endpoints\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and \"config ok: 3 endpoints\"", code, out, errOut)
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
	if code, out, errOut := runCLI("-check"); code != 0 || out != "config ok: 3 endpoints\n" {
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
	path := filepath.Join(dir, "decree-api.yml")
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

// TestExampleMatchesSpec keeps decree-api.example.yml the example of
// SPEC.md §3.
func TestExampleMatchesSpec(t *testing.T) {
	spec, err := os.ReadFile("../../SPEC.md")
	if err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile("../../decree-api.example.yml")
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
	if s != string(example) {
		t.Errorf("decree-api.example.yml differs from SPEC.md §3's example")
	}
}

// TestStartupRejectsPathUnderBuiltin: a configured path under an enabled
// built-in's prefix is a startup error, for -check and for serving alike
// (SPEC.md §7).
func TestStartupRejectsPathUnderBuiltin(t *testing.T) {
	path, _ := exampleProject(t)
	write(t, path, `project: .
daemon: { enabled: false }
endpoints:
  - path: /runs/{title}
    message: { machine: notify, params: { title: '{{title}}' } }
  - path: /openapi.json
    message: { machine: notify }
  - path: /healthz/x
    message: { machine: notify }
`)
	t.Setenv(config.ListenEnv, "127.0.0.1:0")
	wants := []string{
		"endpoint /runs/{title}: path: is under /runs/, which the enabled built-in uses",
		"endpoint /openapi.json: path: is under /openapi.json, which the enabled built-in uses",
		"endpoint /healthz/x: path: is under /healthz, which the enabled built-in uses",
	}
	for _, args := range [][]string{{"-check", "-config", path}, {"-config", path}} {
		done := make(chan struct{})
		var code int
		var errOut string
		go func() { defer close(done); code, _, errOut = runCLI(args...) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%q: still running; it should refuse to start", args)
		}
		if code != 1 {
			t.Errorf("%q: exit %d, want 1", args, code)
		}
		for _, w := range wants {
			if !strings.Contains(errOut, w) {
				t.Errorf("%q: stderr lacks %q:\n%s", args, w, errOut)
			}
		}
	}
}
