package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jtmckay/decree-go-rest/internal/config"
)

// replyFailed is the body of every 500 from an event endpoint; the details
// are logged.
const replyFailed = "could not queue the reply"

// reply is SPEC.md §4 steps 6–7 for an event endpoint: it runs
//
//	decree event <to> <event> [-m=<note>] --format json
//
// with the placeholders of to and event substituted, and the body as the
// note. The note is one argv entry, so a note that starts with - is never
// read as an option; it is the body unchanged, and an empty body passes
// no -m.
func (s *Server) reply(w http.ResponseWriter, r *http.Request, e *endpoint, values map[string]string, note []byte) {
	if bytes.IndexByte(note, 0) >= 0 {
		// An argv entry cannot hold a NUL byte, so decree could not run.
		writeError(w, http.StatusBadRequest, "the note must not contain a NUL byte")
		return
	}
	to := config.Substitute(e.to, values)
	event := config.Substitute(e.event, values)
	args := []string{"event", to, event}
	if len(note) > 0 {
		args = append(args, "-m="+string(note))
	}
	args = append(args, "--format", "json")
	res := s.runDecree(r, args, nil)
	fail := func(reason string, attrs ...any) {
		attrs = append([]any{"route", e.route, "reason", reason,
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
		writeJSON(w, http.StatusCreated, map[string]string{
			"id":    out.ID,
			"path":  out.Path,
			"to":    to,
			"event": event,
		})
	}
}
