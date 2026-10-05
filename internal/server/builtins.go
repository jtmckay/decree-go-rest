package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/jtmckay/decree-api/internal/config"
)

// Bodies of the built-ins' 500s; the details are logged.
const (
	statusFailed = "could not read the run"
	replyFailed  = "could not queue the reply"
)

var runIDRE = regexp.MustCompile(`^(?:` + config.RunIDPattern + `)$`)

// admit authenticates a request to a built-in under /runs/ with the
// default secret, then spends from rate_max, as SPEC.md §4 steps 2–3 do
// for a configured endpoint. It answers the request when it returns false.
func (s *Server) admit(w http.ResponseWriter, r *http.Request) bool {
	if !authorized(r, s.secret) {
		s.reject(w, http.StatusUnauthorized, "unauthorized", func(h http.Header) {
			h.Set("WWW-Authenticate", "Bearer")
		})
		return false
	}
	if ok, retry := s.budgets.all.take(s.budgets.now()); !ok {
		tooManyRequests(w, retry)
		return false
	}
	return true
}

// runParams validates the named path parameters of a built-in against
// RunIDPattern. It returns why one is rejected, never repeating the value,
// or "".
func runParams(r *http.Request, names ...string) string {
	for _, name := range names {
		if !runIDRE.MatchString(r.PathValue(name)) {
			return "parameter " + name + " does not match its pattern"
		}
	}
	return ""
}

// serveStatus is GET /runs/{id} (SPEC.md §7): decree's run document
// unchanged, or 404 when decree does not know the run.
func (s *Server) serveStatus(w http.ResponseWriter, r *http.Request) {
	logged(r).route = config.StatusPath
	if !s.admit(w, r) {
		return
	}
	if msg := runParams(r, "id"); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	res := s.runDecree(r, []string{"status", r.PathValue("id"), "--format", "json"}, nil)
	fail := func(reason string, attrs ...any) {
		attrs = append([]any{"route", config.StatusPath, "reason", reason,
			"stderr", strings.TrimSpace(res.stderr.String())}, attrs...)
		s.Logger.Error("decree status failed", attrs...)
		writeError(w, http.StatusInternalServerError, statusFailed)
	}
	switch {
	case res.timedOut:
		fail("timed out after " + s.EmitTimeout.String())
	case res.exitCode() == 1:
		writeError(w, http.StatusNotFound, decreeMessage(res.stderr.String(), "no such run"))
	case res.err != nil:
		fail(res.err.Error())
	case !json.Valid(res.stdout.Bytes()):
		// decree printed something other than its document, such as the
		// help of an id that reads as an option.
		fail("unreadable output")
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		w.Write(res.stdout.Bytes())
	}
}

// serveReply is POST /runs/{wait_id}/replies/{event} (SPEC.md §7): it
// queues a reply to a run waiting in a person state. The body is the
// optional note.
func (s *Server) serveReply(w http.ResponseWriter, r *http.Request) {
	logged(r).route = config.RepliesPath
	if !s.admit(w, r) {
		return
	}
	if msg := runParams(r, "wait_id", "event"); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	note, status, msg := s.readCapped(w, r)
	if status != 0 {
		writeError(w, status, msg)
		return
	}
	if bytes.IndexByte(note, 0) >= 0 {
		// An argv entry cannot hold a NUL byte, so decree could not run.
		writeError(w, http.StatusBadRequest, "the note must not contain a NUL byte")
		return
	}
	args := []string{"event", r.PathValue("wait_id"), r.PathValue("event")}
	if len(note) > 0 {
		// One argv entry, so a note that starts with - is never read as an
		// option; the body unchanged, with no newline added.
		args = append(args, "-m="+string(note))
	}
	args = append(args, "--format", "json")
	res := s.runDecree(r, args, nil)
	fail := func(reason string, attrs ...any) {
		attrs = append([]any{"route", config.RepliesPath, "reason", reason,
			"stderr", strings.TrimSpace(res.stderr.String())}, attrs...)
		s.Logger.Error("decree event failed", attrs...)
		writeError(w, http.StatusInternalServerError, replyFailed)
	}
	switch {
	case res.timedOut:
		fail("timed out after " + s.EmitTimeout.String())
	case res.exitCode() == 1:
		writeError(w, http.StatusConflict, decreeMessage(res.stderr.String(), "the run does not accept this reply"))
	case res.err != nil:
		fail(res.err.Error())
	default:
		var out emitResult
		if jerr := json.Unmarshal(res.stdout.Bytes(), &out); jerr != nil || out.ID == "" || out.Path == "" {
			fail("unreadable output", "stdout", strings.TrimSpace(res.stdout.String()))
			return
		}
		logged(r).messageID = out.ID
		writeJSON(w, http.StatusCreated, map[string]string{"id": out.ID, "path": out.Path})
	}
}
