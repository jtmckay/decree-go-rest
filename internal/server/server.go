// Package server serves the configured endpoints (SPEC.md §4).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/jtmckay/decree-go-rest/internal/config"
)

// ShutdownTimeout bounds how long in-flight requests may finish after
// SIGTERM or SIGINT (SPEC.md §5).
const ShutdownTimeout = 10 * time.Second

// Server is the route table of one config. It is an http.Handler.
type Server struct {
	mux *http.ServeMux
	// endpoints are the configured endpoints, in config order.
	endpoints []*endpoint
	// decree is the decree binary, resolved on PATH.
	decree string
	// projectDir is where decree runs.
	projectDir string
	// maxBody is limits.max_body_bytes.
	maxBody int64
	// opts are what the environment set at startup; a reload keeps them.
	opts Options
	// openapi is the document GET /openapi.json serves, when it is on.
	openapi []byte
	// reserved are the paths served besides the endpoints, /healthz first.
	reserved []string
	// EmitTimeout bounds each `decree emit` (SPEC.md §4 step 6).
	EmitTimeout time.Duration
	// Environ returns the service's environment; os.Environ by default.
	Environ func() []string
	// Logger receives the request records and emit failures; the default
	// logger unless a test replaces it.
	Logger *slog.Logger
	// budgets are the rate budgets, shared with the Servers of later
	// reloads.
	budgets *budgets
	// health reports for /healthz; the Live serving this Server sets it.
	health func() health
}

// Options are the settings of the environment, read once at startup.
type Options struct {
	// OpenAPI serves GET /openapi.json: config.OpenAPIEnv.
	OpenAPI bool
}

// New builds the route table of a validated config. Secrets are read from
// the environment now.
func New(c *config.Config, opts Options) (*Server, error) {
	return newServer(c, newBudgets(c.Limits), opts)
}

// newServer is New with the rate budgets given, so that a reload keeps
// what the old config's requests spent.
func newServer(c *config.Config, b *budgets, opts Options) (*Server, error) {
	bin, err := resolveDecree(c)
	if err != nil {
		return nil, err
	}
	s := &Server{
		mux:         http.NewServeMux(),
		decree:      bin,
		projectDir:  c.ProjectDir,
		maxBody:     c.Limits.MaxBodyBytes,
		EmitTimeout: DefaultEmitTimeout,
		Environ:     os.Environ,
		Logger:      slog.Default(),
		budgets:     b,
		opts:        opts,
	}
	for _, ce := range c.Endpoints {
		e, err := newEndpoint(c, ce)
		if err != nil {
			return nil, fmt.Errorf("endpoint %s: %w", ce.Path, err)
		}
		s.endpoints = append(s.endpoints, e)
		if err := handle(s.mux, http.MethodPost+" "+ce.Path, func(w http.ResponseWriter, r *http.Request) {
			logged(r).route = e.route
			s.serveEndpoint(w, r, e)
		}); err != nil {
			return nil, fmt.Errorf("endpoint %s: %w", ce.Path, err)
		}
	}
	if err := s.handleReserved(c); err != nil {
		return nil, err
	}
	if err := handle(s.mux, "/", s.notFound); err != nil {
		return nil, err
	}
	return s, nil
}

// Decree is the decree binary the server runs, resolved on PATH.
func (s *Server) Decree() string { return s.decree }

// resolveDecree finds the decree binary, as `decree --version` did during
// validation.
func resolveDecree(c *config.Config) (string, error) {
	bin := c.DecreeBinary()
	p, err := lookPath(bin)
	if err != nil {
		return "", fmt.Errorf("cannot find %s: %w", bin, err)
	}
	return p, nil
}

// handle registers a pattern, turning the panic net/http raises for a bad
// or conflicting pattern into an error. Validation rejects those already.
func handle(mux *http.ServeMux, pattern string, h http.HandlerFunc) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	mux.HandleFunc(pattern, h)
	return nil
}

// ServeHTTP routes a request (SPEC.md §4 step 1) and logs it (§9).
// Exactly one trailing slash is ignored; a path that is not in canonical
// form is unknown, rather than redirected.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &requestRecord{}
	sw := &statusWriter{ResponseWriter: w}
	defer func() { s.logRequest(r, rec, sw.status, time.Since(start)) }()

	u := *r.URL
	if ep := u.EscapedPath(); len(ep) > 1 && strings.HasSuffix(ep, "/") {
		u.Path = strings.TrimSuffix(u.Path, "/")
		if u.RawPath != "" {
			u.RawPath = strings.TrimSuffix(u.RawPath, "/")
		}
	}
	r2 := r.WithContext(context.WithValue(r.Context(), requestRecordKey{}, rec))
	r2.URL = &u
	if ep := u.EscapedPath(); ep == "" || path.Clean(ep) != ep {
		s.notFound(sw, r2)
		return
	}
	// A known path with another method is a 405 naming the allowed ones.
	// Every pattern but the catch-all has a method: one without would
	// conflict with a configured path such as /{name}, which no
	// validation rule forbids.
	if _, pattern := s.mux.Handler(r2); pattern == "/" {
		if route, allow := s.allowed(r2); allow != "" {
			s.methodNotAllowed(route, allow)(sw, r2)
			return
		}
	}
	s.mux.ServeHTTP(sw, r2)
}

// allowed returns the route that r's path matches with another method,
// and the methods that route allows, or "" when no route has the path.
func (s *Server) allowed(r *http.Request) (route, allow string) {
	var methods []string
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		probe := *r
		probe.Method = m
		_, pattern := s.mux.Handler(&probe)
		if pattern == "/" {
			continue
		}
		_, route, _ = strings.Cut(pattern, " ")
		methods = append(methods, m)
		if m == http.MethodGet {
			methods = append(methods, http.MethodHead)
		}
	}
	return route, strings.Join(methods, ", ")
}

// handleReserved registers GET /healthz, always, and GET /openapi.json
// when it is on (SPEC.md §7). Off, /openapi.json is a 404 like any
// unknown path.
func (s *Server) handleReserved(c *config.Config) error {
	if err := handle(s.mux, http.MethodGet+" "+config.HealthPath, s.serveHealth); err != nil {
		return fmt.Errorf("%s: %w", config.HealthPath, err)
	}
	s.reserved = []string{config.HealthPath}
	if !s.opts.OpenAPI {
		return nil
	}
	doc, err := openAPI(c)
	if err != nil {
		return err
	}
	s.openapi = doc
	if err := handle(s.mux, http.MethodGet+" "+config.OpenAPIPath, s.serveOpenAPI); err != nil {
		return fmt.Errorf("%s: %w", config.OpenAPIPath, err)
	}
	s.reserved = append(s.reserved, config.OpenAPIPath)
	return nil
}

// methodNotAllowed answers a known route with the wrong method: a 405
// naming the allowed ones, charged to the failure budget.
func (s *Server) methodNotAllowed(route, allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logged(r).route = route
		s.reject(w, http.StatusMethodNotAllowed, "method not allowed", func(h http.Header) {
			h.Set("Allow", allow)
		})
	}
}

func (s *Server) notFound(w http.ResponseWriter, _ *http.Request) {
	s.reject(w, http.StatusNotFound, "not found", nil)
}

// reject answers a request that failed routing, the method check or
// authentication. It spends from the failure budget, and is a 429 instead
// once that is exhausted (SPEC.md §6). header sets the headers of the
// rejection itself, which a 429 does not carry.
func (s *Server) reject(w http.ResponseWriter, status int, msg string, header func(http.Header)) {
	if ok, retry := s.budgets.fail.take(s.budgets.now()); !ok {
		tooManyRequests(w, retry)
		return
	}
	if header != nil {
		header(w.Header())
	}
	writeError(w, status, msg)
}

// writeError writes {"error": msg}, as every error response is.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":"internal error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	w.Write(append(body, '\n'))
}

// Serve serves h on ln until ctx is done. It then stops accepting
// connections and lets in-flight requests finish, for up to
// ShutdownTimeout, before it closes the rest.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	err := srv.Shutdown(sctx)
	if serr := <-errc; serr != nil && !errors.Is(serr, http.ErrServerClosed) {
		return serr
	}
	if err != nil {
		srv.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
