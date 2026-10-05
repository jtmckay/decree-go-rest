// Package config loads and validates decree-api.yml (SPEC.md §3).
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
	DefaultConfigPath   = "./decree-api.yml"
	DefaultProject      = "."
	DefaultListen       = "127.0.0.1:8801"
	DefaultSecretEnv    = "DECREE_API_SECRET"
	DefaultDecree       = "decree"
	DefaultInterval     = 2 * time.Second
	DefaultMaxBodyBytes = 262144
	DefaultRateWindow   = 60 * time.Second
	DefaultRateMax      = 60
	DefaultRateFailMax  = 10
	DefaultBody         = BodyRequired

	// ListenEnv overrides `listen` when set.
	ListenEnv = "DECREE_API_LISTEN"
)

// Body rules of an endpoint.
const (
	BodyRequired = "required"
	BodyOptional = "optional"
	BodyNone     = "none"
)

// Config is a loaded decree-api.yml.
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

// Builtins switches the built-in endpoints (SPEC.md §7).
type Builtins struct {
	Status  bool `yaml:"status"`
	Replies bool `yaml:"replies"`
	OpenAPI bool `yaml:"openapi"`
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
		Builtins: Builtins{Status: true, Replies: true, OpenAPI: true},
	}
}

// Load reads the config file at path and applies the defaults and the
// DECREE_API_LISTEN override. It does not validate beyond the YAML: unknown
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
