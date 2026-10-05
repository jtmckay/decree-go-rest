package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// DefaultEmitTimeout bounds each `decree emit` (SPEC.md §4 step 6).
const DefaultEmitTimeout = 10 * time.Second

// queueFailed is the body of every 500 from emit; the details are logged.
const queueFailed = "could not queue the message"

var lookPath = exec.LookPath

// emitResult is `decree emit --format json` (schema/v1/cli/emit.schema.json).
type emitResult struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// emit runs decree with args and the body on stdin, and responds as
// SPEC.md §4 step 7 says.
func (s *Server) emit(w http.ResponseWriter, r *http.Request, e *endpoint, args []string, body []byte) {
	// The request's context is not used: once started, an emit finishes
	// even if the client goes away, so a queued message is never cut short.
	ctx, cancel := context.WithTimeout(context.Background(), s.EmitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.decree, args...)
	cmd.Dir = s.projectDir
	cmd.Env = childEnv(s.Environ(), r.Header)
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A child that keeps the pipes open after decree is killed must not
	// hold the response.
	cmd.WaitDelay = time.Second
	err := cmd.Run()

	fail := func(reason string, attrs ...any) {
		attrs = append([]any{"route", e.route, "machine", e.machine, "reason", reason,
			"stderr", strings.TrimSpace(stderr.String())}, attrs...)
		slog.Error("decree emit failed", attrs...)
		writeError(w, http.StatusInternalServerError, queueFailed)
	}
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil:
		fail("timed out after " + s.EmitTimeout.String())
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 1:
		writeError(w, http.StatusBadRequest, decreeMessage(stderr.String()))
	case err != nil:
		fail(err.Error())
	default:
		var res emitResult
		if jerr := json.Unmarshal(stdout.Bytes(), &res); jerr != nil || res.ID == "" || res.Path == "" {
			fail("unreadable output", "stdout", strings.TrimSpace(stdout.String()))
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{
			"id":      res.ID,
			"path":    res.Path,
			"machine": e.machine,
		})
	}
}

// decreeMessage is decree's error message from its stderr, without the
// "error: " label it prints before it.
func decreeMessage(stderr string) string {
	msg := strings.TrimSpace(stderr)
	msg = strings.TrimSpace(strings.TrimPrefix(msg, "error:"))
	if msg == "" {
		return "decree rejected the message"
	}
	return msg
}

// childEnv is the environment of decree (SPEC.md §4 step 6): the service's
// without any DECREE_* variable, and with TRACEPARENT and TRACESTATE taken
// from the request's headers only when they are valid W3C Trace Context.
func childEnv(base []string, h http.Header) []string {
	env := make([]string, 0, len(base)+2)
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "DECREE_") || name == "TRACEPARENT" || name == "TRACESTATE" {
			continue
		}
		env = append(env, kv)
	}
	tp, ok := traceparent(h)
	if !ok {
		return env
	}
	env = append(env, "TRACEPARENT="+tp)
	if ts, ok := tracestate(h); ok {
		env = append(env, "TRACESTATE="+ts)
	}
	return env
}

var (
	traceparentRE = regexp.MustCompile(`^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})(-.*)?$`)
	tracestateKey = regexp.MustCompile(`^(?:[a-z0-9][_0-9a-z\-*/]{0,255}|[a-z0-9][_0-9a-z\-*/]{0,240}@[a-z][_0-9a-z\-*/]{0,13})$`)
	tracestateVal = regexp.MustCompile(`^[\x20-\x2b\x2d-\x3c\x3e-\x7e]{0,255}[\x21-\x2b\x2d-\x3c\x3e-\x7e]$`)
)

// traceparent returns the request's traceparent header when it is one
// valid W3C Trace Context value.
func traceparent(h http.Header) (string, bool) {
	vs := h.Values("Traceparent")
	if len(vs) != 1 {
		return "", false
	}
	v := strings.TrimSpace(vs[0])
	m := traceparentRE.FindStringSubmatch(v)
	if m == nil {
		return "", false
	}
	version, traceID, parentID, extra := m[1], m[2], m[3], m[5]
	switch {
	case version == "ff":
		return "", false
	case version == "00" && extra != "":
		return "", false
	case traceID == strings.Repeat("0", 32), parentID == strings.Repeat("0", 16):
		return "", false
	}
	return v, true
}

// tracestate returns the request's tracestate headers, combined, when they
// are a valid W3C list of at most 32 members.
func tracestate(h http.Header) (string, bool) {
	vs := h.Values("Tracestate")
	if len(vs) == 0 {
		return "", false
	}
	var members []string
	seen := map[string]bool{}
	for _, v := range vs {
		for _, m := range strings.Split(v, ",") {
			m = strings.Trim(m, " \t")
			if m == "" {
				continue
			}
			k, val, ok := strings.Cut(m, "=")
			if !ok || !tracestateKey.MatchString(k) || !tracestateVal.MatchString(val) || seen[k] {
				return "", false
			}
			seen[k] = true
			members = append(members, m)
		}
	}
	if len(members) == 0 || len(members) > 32 {
		return "", false
	}
	return strings.Join(members, ","), true
}
