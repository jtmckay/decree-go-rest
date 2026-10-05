// Package server serves the configured endpoints (SPEC.md §4).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/jtmckay/decree-api/internal/config"
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
	// EmitTimeout bounds each `decree emit` (SPEC.md §4 step 6).
	EmitTimeout time.Duration
	// Environ returns the service's environment; os.Environ by default.
	Environ func() []string
}

// New builds the route table of a validated config. Secrets are read from
// the environment now.
func New(c *config.Config) (*Server, error) {
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
	}
	allow := map[string][]string{}
	var order []string
	for _, ce := range c.Endpoints {
		e, err := newEndpoint(c, ce)
		if err != nil {
			return nil, fmt.Errorf("endpoint %s: %w", ce.Path, err)
		}
		s.endpoints = append(s.endpoints, e)
		if err := handle(s.mux, http.MethodPost+" "+ce.Path, func(w http.ResponseWriter, r *http.Request) {
			s.serveEndpoint(w, r, e)
		}); err != nil {
			return nil, fmt.Errorf("endpoint %s: %w", ce.Path, err)
		}
		if _, ok := allow[ce.Path]; !ok {
			order = append(order, ce.Path)
		}
		allow[ce.Path] = append(allow[ce.Path], http.MethodPost)
	}
	// A known path with any other method is a 405 naming the allowed ones.
	for _, p := range order {
		methods := strings.Join(allow[p], ", ")
		if err := handle(s.mux, p, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", methods)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}); err != nil {
			return nil, fmt.Errorf("endpoint %s: %w", p, err)
		}
	}
	if err := handle(s.mux, "/", notFound); err != nil {
		return nil, err
	}
	return s, nil
}

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

// ServeHTTP routes a request (SPEC.md §4 step 1). Exactly one trailing
// slash is ignored; a path that is not in canonical form is unknown,
// rather than redirected.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := *r.URL
	if ep := u.EscapedPath(); len(ep) > 1 && strings.HasSuffix(ep, "/") {
		u.Path = strings.TrimSuffix(u.Path, "/")
		if u.RawPath != "" {
			u.RawPath = strings.TrimSuffix(u.RawPath, "/")
		}
	}
	if ep := u.EscapedPath(); ep == "" || path.Clean(ep) != ep {
		notFound(w, r)
		return
	}
	if u != *r.URL {
		r2 := new(http.Request)
		*r2 = *r
		r2.URL = &u
		r = r2
	}
	s.mux.ServeHTTP(w, r)
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not found")
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
