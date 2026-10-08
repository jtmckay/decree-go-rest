//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtmckay/decree-go-rest/internal/config"
)

// echoParamsMachine records the run's data, so the test sees the params
// decree-go-rest queued, as decree typed them.
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

// approvalMachine waits in a person state; its ask script records the
// wait id, and once approved it records the reply it received.
const approvalMachine = `# yaml-language-server: $schema=../schema/v1/machine.schema.json
name: approval
description: Wait for a person's approval, then record the reply.
initial: ask
states:
  ask:
    invoke:
      person:
        question: Approve?
        ask: ask
    transitions:
      approve: { target: approved, description: Go ahead. }
      reject:  { target: failed, description: Stop. }
  approved:
    invoke: record
    transitions: { done: done }
  done:   { final: true }
  failed: { final: true }
`

// askScript writes the wait id to wait_id, in the project root.
const askScript = `#!/bin/sh
set -e
printf '%s' "$DECREE_WAIT_ID" > "$DECREE_PROJECT_ROOT/wait_id.tmp"
mv "$DECREE_PROJECT_ROOT/wait_id.tmp" "$DECREE_PROJECT_ROOT/wait_id"
`

// recordReplyScript copies the reply the run received to received.md.
const recordReplyScript = `#!/bin/sh
set -e
cp "$DECREE_RECEIVED" "$DECREE_PROJECT_ROOT/received.md"
`

const e2eConfig = `project: .
decree: decree
daemon: { enabled: true, interval: 1s }
endpoints:
  - path: /echo/{title}
    message:
      machine: echo_params
      params: { title: '{{title}}', priority: high, count: 3 }
  - path: /start/approval
    body: none
    message: { machine: approval }
  - path: /approve/{wait_id}
    action: event
    secret_env: DECREE_GO_REST_APPROVE_SECRET
    patterns: { wait_id: '[A-Za-z0-9._-]{1,128}' }
    body: optional
    reply:
      to: '{{wait_id}}'
      event: approve
`

// E2ETimeout bounds the wait for the run to finish.
const E2ETimeout = 30 * time.Second

// TestEndToEnd is SPEC.md §12's end-to-end test: with the real decree and
// its daemon, a POST becomes a finished run whose script saw the
// configured params; then a run waiting in a person state is approved
// through an event endpoint on its own secret, and finishes (the first
// acceptance criterion of 08). It needs decree on PATH.
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
	approveSecret := strings.Repeat("f", 64)
	t.Setenv(config.DefaultSecretEnv, secret)
	t.Setenv("DECREE_GO_REST_APPROVE_SECRET", approveSecret)
	t.Setenv(config.OpenAPIEnv, "")

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
	write(t, config.MachineFile(proj, "approval"), approvalMachine)
	for name, body := range map[string]string{"ask": askScript, "record": recordReplyScript} {
		script := filepath.Join(proj, ".decree", "scripts", "approval", name)
		write(t, script, body)
		if err := os.Chmod(script, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(proj, "decree-go-rest.yml")
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

	// Wait until decree reports the run finished in `done`.
	run := waitFinished(t, decree, proj, queued.ID, in)
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

	// A run waits in a person state; its ask script records the wait id.
	resp = do(t, "POST", base+"/start/approval", secret, "")
	var started struct{ ID string }
	decodeJSON(t, resp, http.StatusCreated, &started)
	waitIDFile := filepath.Join(proj, "wait_id")
	var waitID string
	deadline := time.Now().Add(E2ETimeout)
	for {
		if b, err := os.ReadFile(waitIDFile); err == nil && len(b) > 0 {
			waitID = string(b)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s did not ask within %v; decree-go-rest stderr:\n%s", started.ID, E2ETimeout, in.stderr)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.HasPrefix(waitID, started.ID+".") {
		t.Fatalf("wait id %q is not of run %s", waitID, started.ID)
	}
	if st := runStatus(t, decree, proj, started.ID); st == nil || st.Status != "waiting" {
		t.Fatalf("run %s: %+v, want waiting", started.ID, st)
	}

	// The top-level secret cannot approve it; the event endpoint's own can,
	// with a note that starts with -.
	resp = do(t, "POST", base+"/approve/"+waitID, secret, "Looks good.")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("approve with the top-level secret: %d, want 401", resp.StatusCode)
	}
	const note = "-m looks good."
	resp = do(t, "POST", base+"/approve/"+waitID, approveSecret, note)
	var replied struct{ ID, Path, To, Event string }
	decodeJSON(t, resp, http.StatusCreated, &replied)
	if replied.ID == "" || replied.To != waitID || replied.Event != "approve" {
		t.Fatalf("approve answered %+v", replied)
	}

	run = waitFinished(t, decree, proj, started.ID, in)
	if run.State != "done" || run.Machine != "approval" {
		t.Fatalf("run %s finished as %+v, want state done of approval", started.ID, run)
	}
	received, err := os.ReadFile(filepath.Join(proj, "received.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"to: " + waitID, "event: approve", note} {
		if !strings.Contains(string(received), want) {
			t.Errorf("the reply the run received lacks %q:\n%s", want, received)
		}
	}

	// The run no longer waits: decree's exit 1 is a 409 with its message.
	resp = do(t, "POST", base+"/approve/"+waitID, approveSecret, "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "is not waiting") {
		t.Errorf("approve a finished run: %d %s, want 409 saying it is not waiting", resp.StatusCode, body)
	}

	// Shut down: decree-go-rest exits 0, and so has the daemon.
	stopped = true
	if code := in.terminate(t, 30*time.Second); code != 0 {
		t.Fatalf("decree-go-rest exited %d; stderr:\n%s", code, in.stderr)
	}
	if alive(pid) {
		t.Errorf("daemon %d still running after decree-go-rest exited", pid)
	}
	if _, err := os.Stat(filepath.Join(proj, ".decree", "inbox", queued.ID+".md")); !os.IsNotExist(err) {
		t.Errorf("message %s still in the inbox: %v", queued.ID, err)
	}
}

// runState is the part of `decree status <id> --format json` the test
// reads.
type runState struct{ Status, State, Machine string }

// runStatus runs `decree status <id> --format json` in the project. It
// returns nil while decree knows no such run (exit 1), as before the
// daemon claims its message.
func runStatus(t *testing.T, decree, proj, id string) *runState {
	t.Helper()
	cmd := exec.Command(decree, "status", id, "--format", "json")
	cmd.Dir = proj
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil
		}
		t.Fatalf("decree status %s: %v\n%s", id, err, stderr.String())
	}
	var st runState
	if err := json.Unmarshal(stdout.Bytes(), &st); err != nil {
		t.Fatalf("decree status %s: %v\n%s", id, err, stdout.String())
	}
	return &st
}

// waitFinished waits until run id is finished, at most E2ETimeout.
func waitFinished(t *testing.T, decree, proj, id string, in *instance) runState {
	t.Helper()
	deadline := time.Now().Add(E2ETimeout)
	var last *runState
	for {
		if last = runStatus(t, decree, proj, id); last != nil && last.Status == "finished" {
			return *last
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s not finished within %v (last: %+v); decree-go-rest stderr:\n%s", id, E2ETimeout, last, in.stderr)
		}
		time.Sleep(200 * time.Millisecond)
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
