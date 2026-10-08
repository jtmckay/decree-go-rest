// Package config loads and validates decree-go-rest.yml (SPEC.md §3).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults of SPEC.md §3.
const (
	DefaultConfigPath = "./decree-go-rest.yml"
	// DefaultProject is the config's directory, or its parent when the
	// config is in .decree/ (see Load).
	DefaultProject      = "."
	DefaultListen       = "127.0.0.1:8801"
	DefaultSecretEnv    = "DECREE_GO_REST_SECRET"
	DefaultDecree       = "decree"
	DefaultInterval     = 2 * time.Second
	DefaultMaxBodyBytes = 262144
	DefaultRateWindow   = 60 * time.Second
	DefaultRateMax      = 60
	DefaultRateFailMax  = 10
	DefaultBody         = BodyRequired
	// DefaultEventBody is the body rule of an event endpoint: the body is
	// an optional note.
	DefaultEventBody = BodyOptional
	DefaultAction    = ActionEmit

	// ListenEnv overrides `listen` when set.
	ListenEnv = "DECREE_GO_REST_LISTEN"
	// OpenAPIEnv switches GET /openapi.json on (true) or off (false, the
	// default). It is read at startup.
	OpenAPIEnv = "DECREE_GO_REST_OPENAPI"
	// DaemonEnv overrides `daemon.enabled` when set: true or false. The
	// container image sets it to false.
	DaemonEnv = "DECREE_GO_REST_DAEMON"
)

// Body rules of an endpoint.
const (
	BodyRequired = "required"
	BodyOptional = "optional"
	BodyNone     = "none"
)

// Actions of an endpoint.
const (
	// ActionEmit queues a new message with `decree emit`.
	ActionEmit = "emit"
	// ActionEvent replies to a run waiting in a person state with
	// `decree event`.
	ActionEvent = "event"
)

// Config is a loaded decree-go-rest.yml.
type Config struct {
	// File is the absolute path of the config file.
	File string `yaml:"-"`
	// Dir is the directory of the config file.
	Dir string `yaml:"-"`
	// ProjectDir is Project resolved against Dir.
	ProjectDir string `yaml:"-"`

	Project   string     `yaml:"project"`
	Listen    string     `yaml:"listen"`
	SecretEnv string     `yaml:"secret_env"`
	Decree    string     `yaml:"decree"`
	Daemon    Daemon     `yaml:"daemon"`
	Limits    Limits     `yaml:"limits"`
	Endpoints []Endpoint `yaml:"endpoints"`
}

// Daemon configures the supervised `decree daemon`.
type Daemon struct {
	Enabled  bool     `yaml:"enabled"`
	Interval Duration `yaml:"interval"`
}

// Limits are the request body cap and the two rate budgets (SPEC.md §6).
type Limits struct {
	MaxBodyBytes int64    `yaml:"max_body_bytes"`
	RateWindow   Duration `yaml:"rate_window"`
	RateMax      int      `yaml:"rate_max"`
	RateFailMax  int      `yaml:"rate_fail_max"`
}

// Paths served besides the endpoints (SPEC.md §7). A configured endpoint
// may use neither.
const (
	HealthPath  = "/healthz"
	OpenAPIPath = "/openapi.json"
)

// Endpoint is one configured endpoint.
type Endpoint struct {
	Path string `yaml:"path"`
	// Action is ActionEmit or ActionEvent.
	Action    string            `yaml:"action"`
	SecretEnv string            `yaml:"secret_env"`
	Patterns  map[string]string `yaml:"patterns"`
	Body      string            `yaml:"body"`
	// Message is what an emit endpoint queues; nil when the key is absent.
	Message *Message `yaml:"message"`
	// Reply is what an event endpoint sends; nil when the key is absent.
	Reply *Reply `yaml:"reply"`
}

// ParamPattern is the pattern of the path parameter name, unanchored: the
// endpoint's own, else WaitIDPattern for a parameter an event endpoint
// uses in reply.to, else DefaultParamPattern.
func (e Endpoint) ParamPattern(name string) string {
	if p, ok := e.Patterns[name]; ok {
		return p
	}
	if e.Action == ActionEvent && e.Reply != nil && placeholderUses(e.Reply.To, name) {
		return WaitIDPattern
	}
	return DefaultParamPattern
}

// EffectiveSecretEnv is the endpoint's secret_env, or the top-level one.
func (e Endpoint) EffectiveSecretEnv(c *Config) string {
	if e.SecretEnv != "" {
		return e.SecretEnv
	}
	return c.SecretEnv
}

// Message is what an endpoint emits.
type Message struct {
	Machine string `yaml:"machine"`
	Params  Params `yaml:"params"`
}

// Reply is what an event endpoint sends: `decree event <to> <event>`.
// Both may hold {{name}} placeholders for path parameters.
type Reply struct {
	// To is the wait id of the run waiting in a person state.
	To string `yaml:"to"`
	// Event is the event the run takes.
	Event string `yaml:"event"`
}

// Param is one `params` entry, kept in the order of the config.
type Param struct {
	Key string
	// Value is the YAML value; validation checks it is a scalar.
	Value *yaml.Node
}

// Params are the `params` of a message, in config order.
type Params []Param

// UnmarshalYAML reads a mapping, keeping its order.
func (p *Params) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: params must be a mapping", n.Line)
	}
	out := make(Params, 0, len(n.Content)/2)
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return fmt.Errorf("line %d: a params key must be a string", k.Line)
		}
		if seen[k.Value] {
			return fmt.Errorf("line %d: duplicate params key %q", k.Line, k.Value)
		}
		seen[k.Value] = true
		out = append(out, Param{Key: k.Value, Value: v})
	}
	*p = out
	return nil
}

// checkRemovedKeys reports a top-level RemovedKey. A document that does
// not parse is left to the decoder, which says why.
func checkRemovedKeys(data []byte) error {
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if k := root.Content[i]; k.Value == RemovedKey {
			return fmt.Errorf("line %d: %s %s", k.Line, RemovedKey, removedKeyError)
		}
	}
	return nil
}

func defaults() Config {
	return Config{
		// Empty means DefaultProject, which Load resolves.
		Project:   "",
		Listen:    DefaultListen,
		SecretEnv: DefaultSecretEnv,
		Decree:    DefaultDecree,
		Daemon:    Daemon{Enabled: true, Interval: Duration(DefaultInterval)},
		Limits: Limits{
			MaxBodyBytes: DefaultMaxBodyBytes,
			RateWindow:   Duration(DefaultRateWindow),
			RateMax:      DefaultRateMax,
			RateFailMax:  DefaultRateFailMax,
		},
	}
}

// Load reads the config file at path and applies the defaults and the
// DECREE_GO_REST_LISTEN and DECREE_GO_REST_DAEMON overrides. It does not validate beyond the YAML: unknown
// keys and malformed values are errors, everything else is Validate's job.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.File = abs
	c.Dir = filepath.Dir(abs)
	c.ProjectDir = c.Project
	switch {
	case c.Project == "" && filepath.Base(c.Dir) == ".decree":
		// The config lives in the project's .decree/, as in the container.
		c.ProjectDir = filepath.Dir(c.Dir)
	case c.Project == "":
		c.ProjectDir = c.Dir
	case !filepath.IsAbs(c.ProjectDir):
		c.ProjectDir = filepath.Join(c.Dir, c.ProjectDir)
	}
	return c, nil
}

// OpenAPIEnabled reads OpenAPIEnv: true or false, and false when it is
// unset or empty. Any other value is an error.
func OpenAPIEnabled() (bool, error) {
	switch v := os.Getenv(OpenAPIEnv); v {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s is %q; it must be true or false", OpenAPIEnv, v)
	}
}

// RemovedKey is the top-level key that configured the built-in endpoints
// before every endpoint was configured. It is built from parts so that a
// search of the repository for it finds only history.
const RemovedKey = "built" + "ins"

// removedKeyError names what replaced RemovedKey.
const removedKeyError = "is no longer accepted: every endpoint is configured. " +
	"Reply to a run waiting in a person state with an endpoint of `action: event` " +
	"(its own path, secret_env, patterns and reply: { to: '{{wait_id}}', event: approve }), " +
	"and serve GET " + OpenAPIPath + " by setting " + OpenAPIEnv + "=true"

// Parse decodes a config document. Unknown keys are an error, and
// RemovedKey an error that names the endpoint `action` form.
func Parse(data []byte) (*Config, error) {
	if err := checkRemovedKeys(data); err != nil {
		return nil, err
	}
	c := defaults()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the config is empty")
		}
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("the config must be a single YAML document")
	}
	// Defaults inside endpoints are applied after decoding: a struct-level
	// UnmarshalYAML would lose the decoder's KnownFields check.
	for i := range c.Endpoints {
		e := &c.Endpoints[i]
		if e.Action == "" {
			e.Action = DefaultAction
		}
		if e.Body == "" {
			e.Body = DefaultBody
			if e.Action == ActionEvent {
				e.Body = DefaultEventBody
			}
		}
	}
	if v, ok := os.LookupEnv(ListenEnv); ok && v != "" {
		c.Listen = v
	}
	switch v := os.Getenv(DaemonEnv); v {
	case "":
	case "true", "false":
		c.Daemon.Enabled = v == "true"
	default:
		return nil, fmt.Errorf("%s is %q; it must be true or false", DaemonEnv, v)
	}
	return &c, nil
}
