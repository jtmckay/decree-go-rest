//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtmckay/decree-api/internal/config"
)

// echoParamsMachine records the run's data, so the test sees the params
// decree-api queued, as decree typed them.
const echoParamsMachine = `# yaml-language-server: $schema=../schema/v1/machine.schema.json
name: echo_params
description: Record the run's DECREE_DATA_* variables and its message.
initial: record
data:
  title:    { type: string, default: Untitled }
  priority: { type: string, default: low }
  count:    { type: int, default: 1 }
  colour:   { type: string, default: blue }
states:
  record: { invoke: record, transitions: { done: done } }
  done:   { final: true }
  failed: { final: true }
`

// recordScript writes DECREE_DATA_* to params.env and the message to
// message.md, in the project root.
const recordScript = `#!/bin/sh
set -e
env | grep '^DECREE_DATA_' | sort > "$DECREE_PROJECT_ROOT/params.env.tmp"
cp "$DECREE_MESSAGE" "$DECREE_PROJECT_ROOT/message.md"
mv "$DECREE_PROJECT_ROOT/params.env.tmp" "$DECREE_PROJECT_ROOT/params.env"
`

const e2eConfig = `project: .
decree: decree
daemon: { enabled: true, interval: 1s }
builtins: { status: true, replies: false, openapi: false }
endpoints:
  - path: /echo/{title}
    message:
      machine: echo_params
      params: { title: '{{title}}', priority: high, count: 3 }
`

// E2ETimeout bounds the wait for the run to finish.
const E2ETimeout = 30 * time.Second

// TestEndToEnd is SPEC.md §12's end-to-end test, and the first acceptance
// criterion: with the real decree and its daemon, a POST becomes a finished
// run whose script saw the configured params. It needs decree on PATH.
func TestEndToEnd(t *testing.T) {
	decree, err := exec.LookPath("decree")
	if err != nil {
		fmt.Println("TestEndToEnd: skipped: decree is not on PATH; install decree 0.5 to run the end-to-end test")
		t.Skip("decree is not on PATH")
	}
	// The test may itself run inside a decree run (the gate): its
	// DECREE_* variables must reach neither `decree init` nor the daemon.
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "DECREE_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	t.Setenv("TRACEPARENT", "")
	os.Unsetenv("TRACEPARENT")
	secret := strings.Repeat("e", 64)
	t.Setenv(config.DefaultSecretEnv, secret)

	proj := t.TempDir()
	initCmd := exec.Command(decree, "init")
	initCmd.Dir = proj
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("decree init: %v\n%s", err, out)
	}
	write(t, config.MachineFile(proj, "echo_params"), echoParamsMachine)
	script := filepath.Join(proj, ".decree", "scripts", "echo_params", "record")
	write(t, script, recordScript)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(proj, "decree-api.yml")
	write(t, path, e2eConfig)

	in := serve(t, path)
	stopped := false
	defer func() {
		if !stopped {
			in.terminate(t, 30*time.Second)
		}
	}()
	_, health := in.health(t)
	d, _ := health["daemon"].(map[string]any)
	pidf, _ := d["pid"].(float64)
	pid := int(pidf)
	if running, _ := d["running"].(bool); !running || pid <= 0 {
		t.Fatalf("/healthz daemon = %v, want running with a pid", d)
	}

	// One POST.
	base := "http://" + in.addr
	resp := do(t, "POST", base+"/echo/hello-world", secret, "Say hello.")
	var queued struct{ ID, Path, Machine string }
	decodeJSON(t, resp, http.StatusCreated, &queued)
	if queued.ID == "" || queued.Machine != "echo_params" {
		t.Fatalf("POST answered %+v", queued)
	}

	// Wait until GET /runs/{id} reports the run finished in `done`.
	var run struct{ Status, State, Machine string }
	deadline := time.Now().Add(E2ETimeout)
	for {
		resp := do(t, "GET", base+"/runs/"+queued.ID, secret, "")
		if resp.StatusCode == http.StatusOK {
			decodeJSON(t, resp, http.StatusOK, &run)
			if run.Status == "finished" {
				break
			}
		} else {
			// 404 until the daemon claims the message.
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("GET /runs/%s: %d %s", queued.ID, resp.StatusCode, body)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s not finished within %v (last: %+v); decree-api stderr:\n%s", queued.ID, E2ETimeout, run, in.stderr)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if run.State != "done" || run.Machine != "echo_params" {
		t.Fatalf("run %s finished as %+v, want state done of echo_params", queued.ID, run)
	}

	// The script saw the configured params, typed by decree, and the body.
	got, err := os.ReadFile(filepath.Join(proj, "params.env"))
	if err != nil {
		t.Fatal(err)
	}
	want := "DECREE_DATA_COLOUR=blue\nDECREE_DATA_COUNT=3\nDECREE_DATA_PRIORITY=high\nDECREE_DATA_TITLE=hello-world\n"
	if string(got) != want {
		t.Errorf("recorded params:\n%s\nwant:\n%s", got, want)
	}
	msg, err := os.ReadFile(filepath.Join(proj, "message.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "Say hello.\n") {
		t.Errorf("the run's message lacks the body:\n%s", msg)
	}

	// Shut down: decree-api exits 0, and so has the daemon.
	stopped = true
	if code := in.terminate(t, 30*time.Second); code != 0 {
		t.Fatalf("decree-api exited %d; stderr:\n%s", code, in.stderr)
	}
	if alive(pid) {
		t.Errorf("daemon %d still running after decree-api exited", pid)
	}
	if _, err := os.Stat(filepath.Join(proj, ".decree", "inbox", queued.ID+".md")); !os.IsNotExist(err) {
		t.Errorf("message %s still in the inbox: %v", queued.ID, err)
	}
}

// do sends one request with the bearer secret.
func do(t *testing.T, method, url, secret, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// decodeJSON checks resp's status and decodes its body into v.
func decodeJSON(t *testing.T, resp *http.Response, status int, v any) {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("%s %s: %d %s, want %d", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, body, status)
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(v); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
}
