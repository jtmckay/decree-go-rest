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
	DefaultConfigPath   = "./decree-go-rest.yml"
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

	// ListenEnv overrides `listen` when set.
	ListenEnv = "DECREE_GO_REST_LISTEN"
)

// Body rules of an endpoint.
const (
	BodyRequired = "required"
	BodyOptional = "optional"
	BodyNone     = "none"
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
	Builtins  Builtins   `yaml:"builtins"`
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

// Builtins configures the built-in endpoints (SPEC.md §7), one object per
// key.
type Builtins struct {
	Status  Builtin        `yaml:"status"`
	Replies Builtin        `yaml:"replies"`
	OpenAPI OpenAPIBuiltin `yaml:"openapi"`
}

// OpenAPIBuiltin is GET /openapi.json, which has no auth and so takes no
// secret_env.
type OpenAPIBuiltin struct {
	Enabled bool `yaml:"enabled"`
}

// Builtin is one built-in endpoint.
type Builtin struct {
	Enabled bool `yaml:"enabled"`
	// SecretEnv is the built-in's own secret, or "" for the top-level one.
	SecretEnv string `yaml:"secret_env"`
}

// EffectiveSecretEnv is the built-in's secret_env, or the top-level one.
func (b Builtin) EffectiveSecretEnv(c *Config) string {
	if b.SecretEnv != "" {
		return b.SecretEnv
	}
	return c.SecretEnv
}

// UnmarshalYAML reads the builtins mapping. Each built-in is an object; a
// bare true or false is an error naming the object form, and openapi takes
// no secret_env. Keys left out keep their defaults.
func (b *Builtins) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: builtins must be a mapping", n.Line)
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if seen[k.Value] {
			return fmt.Errorf("line %d: builtins: duplicate key %q", k.Line, k.Value)
		}
		seen[k.Value] = true
		key := "builtins." + k.Value
		switch k.Value {
		case "status":
			if err := b.Status.unmarshal(key, v); err != nil {
				return err
			}
		case "replies":
			if err := b.Replies.unmarshal(key, v); err != nil {
				return err
			}
		case "openapi":
			o := Builtin{Enabled: b.OpenAPI.Enabled}
			if err := o.unmarshal(key, v); err != nil {
				return err
			}
			if o.SecretEnv != "" {
				return fmt.Errorf("line %d: %s: secret_env is not allowed; GET %s takes no auth", v.Line, key, OpenAPIPath)
			}
			b.OpenAPI.Enabled = o.Enabled
		default:
			return fmt.Errorf("line %d: builtins: unknown key %q (status, replies or openapi)", k.Line, k.Value)
		}
	}
	return nil
}

// unmarshal reads one built-in's object, named key in errors.
func (b *Builtin) unmarshal(key string, n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		if n.Kind == yaml.ScalarNode && n.Tag == "!!bool" {
			return fmt.Errorf("line %d: %s: write { enabled: %s }, not a bare %s", n.Line, key, n.Value, n.Value)
		}
		return fmt.Errorf("line %d: %s: must be an object, such as { enabled: true }", n.Line, key)
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if seen[k.Value] {
			return fmt.Errorf("line %d: %s: duplicate key %q", k.Line, key, k.Value)
		}
		seen[k.Value] = true
		var err error
		switch k.Value {
		case "enabled":
			err = v.Decode(&b.Enabled)
		case "secret_env":
			if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
				err = errors.New("must be a string")
			} else {
				b.SecretEnv = v.Value
			}
		default:
			return fmt.Errorf("line %d: %s: unknown key %q (enabled or secret_env)", k.Line, key, k.Value)
		}
		if err != nil {
			return fmt.Errorf("line %d: %s.%s: %w", v.Line, key, k.Value, err)
		}
	}
	return nil
}

// Paths of the built-in endpoints (SPEC.md §7).
const (
	HealthPath  = "/healthz"
	StatusPath  = "/runs/{id}"
	RepliesPath = "/runs/{wait_id}/replies/{event}"
	OpenAPIPath = "/openapi.json"
)

// RunIDPattern is the pattern of every parameter of the built-ins under
// /runs/, anchored when it is matched.
const RunIDPattern = `[A-Za-z0-9._-]{1,128}`

// Route is a method and a path, as a built-in is served.
type Route struct {
	Method, Path string
}

// BuiltinRoutes returns the built-ins c enables, /healthz first; /healthz
// is always served.
func (c *Config) BuiltinRoutes() []Route {
	out := []Route{{"GET", HealthPath}}
	if c.Builtins.Status.Enabled {
		out = append(out, Route{"GET", StatusPath})
	}
	if c.Builtins.Replies.Enabled {
		out = append(out, Route{"POST", RepliesPath})
	}
	if c.Builtins.OpenAPI.Enabled {
		out = append(out, Route{"GET", OpenAPIPath})
	}
	return out
}

// Endpoint is one configured endpoint.
type Endpoint struct {
	Path      string            `yaml:"path"`
	SecretEnv string            `yaml:"secret_env"`
	Patterns  map[string]string `yaml:"patterns"`
	Body      string            `yaml:"body"`
	Message   Message           `yaml:"message"`
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

func defaults() Config {
	return Config{
		Project:   DefaultProject,
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
		Builtins: Builtins{
			Status:  Builtin{Enabled: true},
			Replies: Builtin{Enabled: true},
			OpenAPI: OpenAPIBuiltin{Enabled: true},
		},
	}
}

// Load reads the config file at path and applies the defaults and the
// DECREE_GO_REST_LISTEN override. It does not validate beyond the YAML: unknown
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
	if !filepath.IsAbs(c.ProjectDir) {
		c.ProjectDir = filepath.Join(c.Dir, c.ProjectDir)
	}
	return c, nil
}

// Parse decodes a config document. Unknown keys are an error.
func Parse(data []byte) (*Config, error) {
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
		if c.Endpoints[i].Body == "" {
			c.Endpoints[i].Body = DefaultBody
		}
	}
	if v, ok := os.LookupEnv(ListenEnv); ok && v != "" {
		c.Listen = v
	}
	return &c, nil
}
