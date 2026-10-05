package server

import (
	"log/slog"
	"net/http"
	"time"
)

// requestRecord is what the handlers add to a request's log record.
type requestRecord struct {
	// route is the configured pattern that matched, never the raw path,
	// so parameter values stay out of the logs (SPEC.md §9).
	route string
	// messageID is the id of the message queued, if any.
	messageID string
}

type requestRecordKey struct{}

// logged returns the log record of a request that ServeHTTP routed. A
// request that did not come through ServeHTTP gets a record no one logs.
func logged(r *http.Request) *requestRecord {
	if rec, ok := r.Context().Value(requestRecordKey{}).(*requestRecord); ok {
		return rec
	}
	return &requestRecord{}
}

// statusWriter remembers the status of the response.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// logRequest writes the one record of a request (SPEC.md §9). It holds no
// secret, body or parameter value: the route is the configured pattern,
// and the trace id is only the id from a valid traceparent.
func (s *Server) logRequest(r *http.Request, rec *requestRecord, status int, d time.Duration) {
	if status == 0 {
		status = http.StatusOK
	}
	attrs := []slog.Attr{
		slog.String("method", r.Method),
		slog.String("route", rec.route),
		slog.Int("status", status),
		slog.Float64("duration_ms", float64(d.Microseconds())/1000),
	}
	if rec.messageID != "" {
		attrs = append(attrs, slog.String("message_id", rec.messageID))
	}
	if tp, ok := traceparent(r.Header); ok {
		attrs = append(attrs, slog.String("trace_id", traceID(tp)))
	}
	s.Logger.LogAttrs(r.Context(), slog.LevelInfo, "request", attrs...)
}

// traceID is the trace-id field of a valid traceparent.
func traceID(traceparent string) string {
	return traceparentRE.FindStringSubmatch(traceparent)[2]
}

// LogRoutes writes one startup record per route: its method, its pattern
// and the machine it emits to (SPEC.md §9).
func (s *Server) LogRoutes() {
	for _, e := range s.endpoints {
		s.Logger.Info("route", "method", http.MethodPost, "route", e.route, "machine", e.machine)
	}
}
