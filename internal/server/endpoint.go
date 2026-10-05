package server

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/jtmckay/decree-go-rest/internal/config"
)

// MaxParamBytes is the longest accepted path parameter, after decoding
// (SPEC.md §4 step 4).
const MaxParamBytes = 200

// endpoint is one configured endpoint, ready to serve.
type endpoint struct {
	// route is the configured path, the only form of it that is logged.
	route  string
	secret []byte
	params []pathParam
	body   string
	// machine and message are what it emits.
	machine string
	message []messageParam
}

type pathParam struct {
	name string
	re   *regexp.Regexp
}

type messageParam struct {
	key, text string
	// template is whether text holds placeholders.
	template bool
}

func newEndpoint(c *config.Config, ce config.Endpoint) (*endpoint, error) {
	env := ce.EffectiveSecretEnv(c)
	secret := os.Getenv(env)
	if len(secret) < config.MinSecretLen {
		return nil, fmt.Errorf("secret %s is not set or shorter than %d characters", env, config.MinSecretLen)
	}
	e := &endpoint{
		route:   ce.Path,
		secret:  []byte(secret),
		body:    ce.Body,
		machine: ce.Message.Machine,
	}
	for _, name := range config.PathParams(ce.Path) {
		pat, ok := ce.Patterns[name]
		if !ok {
			pat = config.DefaultParamPattern
		}
		re, err := regexp.Compile(`^(?:` + pat + `)$`)
		if err != nil {
			return nil, fmt.Errorf("pattern of %s: %w", name, err)
		}
		e.params = append(e.params, pathParam{name: name, re: re})
	}
	for _, p := range ce.Message.Params {
		text, template, err := config.ParamText(p.Value)
		if err != nil {
			return nil, fmt.Errorf("params.%s: %w", p.Key, err)
		}
		e.message = append(e.message, messageParam{key: p.Key, text: text, template: template})
	}
	return e, nil
}

// serveEndpoint is SPEC.md §4 steps 2–7 for a routed request.
func (s *Server) serveEndpoint(w http.ResponseWriter, r *http.Request, e *endpoint) {
	if !authorized(r, e.secret) {
		s.reject(w, http.StatusUnauthorized, "unauthorized", func(h http.Header) {
			h.Set("WWW-Authenticate", "Bearer")
		})
		return
	}
	// Only now, authenticated, does the request spend from rate_max; a 400
	// below is the caller's bug, not an attack (SPEC.md §6).
	if ok, retry := s.budgets.all.take(s.budgets.now()); !ok {
		tooManyRequests(w, retry)
		return
	}

	values, msg := e.pathValues(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	body, status, msg := s.readBody(w, r, e.body)
	if status != 0 {
		writeError(w, status, msg)
		return
	}

	args := []string{"emit", "--machine", e.machine}
	for _, p := range e.message {
		v := p.text
		if p.template {
			v = config.Substitute(v, values)
		}
		args = append(args, "--param", p.key+"="+v)
	}
	args = append(args, "--format", "json")
	s.emit(w, r, e, args, body)
}

// authorized checks `Authorization: Bearer <secret>` (SPEC.md §4 step 2):
// lengths first, then the bytes in constant time.
func authorized(r *http.Request, secret []byte) bool {
	h := r.Header.Values("Authorization")
	if len(h) != 1 {
		return false
	}
	scheme, token, ok := strings.Cut(h[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	got := []byte(token)
	if len(got) != len(secret) {
		return false
	}
	return subtle.ConstantTimeCompare(got, secret) == 1
}

// pathValues validates the path parameters (SPEC.md §4 step 4). net/http
// has URL-decoded each once. It returns the values, or why one is
// rejected; the message never repeats the value.
func (e *endpoint) pathValues(r *http.Request) (map[string]string, string) {
	values := make(map[string]string, len(e.params))
	for _, p := range e.params {
		v := r.PathValue(p.name)
		if len(v) > MaxParamBytes {
			return nil, fmt.Sprintf("parameter %s is longer than %d bytes", p.name, MaxParamBytes)
		}
		if !p.re.MatchString(v) {
			return nil, fmt.Sprintf("parameter %s does not match its pattern", p.name)
		}
		values[p.name] = v
	}
	return values, ""
}

// readBody is SPEC.md §4 step 5. On failure it returns the status and
// message of the response.
func (s *Server) readBody(w http.ResponseWriter, r *http.Request, rule string) ([]byte, int, string) {
	body, status, msg := s.readCapped(w, r)
	if status != 0 {
		return nil, status, msg
	}
	switch rule {
	case config.BodyRequired:
		if strings.TrimSpace(string(body)) == "" {
			return nil, http.StatusBadRequest, "a body is required"
		}
	case config.BodyNone:
		if len(body) > 0 {
			return nil, http.StatusBadRequest, "this endpoint takes no body"
		}
	}
	if len(body) > 0 && body[len(body)-1] != '\n' {
		body = append(body, '\n')
	}
	return body, 0, ""
}

// readCapped reads the body, at most max_body_bytes. On failure it returns
// the status and message of the response.
func (s *Server) readCapped(w http.ResponseWriter, r *http.Request) ([]byte, int, string) {
	tooLarge := fmt.Sprintf("the body is larger than %d bytes", s.maxBody)
	if r.ContentLength > s.maxBody {
		return nil, http.StatusRequestEntityTooLarge, tooLarge
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxBody))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, http.StatusRequestEntityTooLarge, tooLarge
		}
		return nil, http.StatusBadRequest, "cannot read the body"
	}
	return body, 0, ""
}
