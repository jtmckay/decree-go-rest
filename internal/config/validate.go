package config

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Rules of SPEC.md §3, Validation, used in Error.Rule.
const (
	RulePath         = "path"         // step 1
	RulePatterns     = "patterns"     // step 2
	RulePlaceholders = "placeholders" // step 3
	RuleMachine      = "machine"      // step 4
	RuleParams       = "params"       // step 5
	RuleSecrets      = "secrets"      // step 6
	RuleDecree       = "decree"       // step 7
	RuleConfig       = "config"       // values outside the seven steps: endpoints, body, limits, project
)

// MinSecretLen is the shortest accepted secret (SPEC.md §10).
const MinSecretLen = 32

// DefaultParamPattern is a path parameter's pattern when `patterns` sets none.
const DefaultParamPattern = `[A-Za-z0-9_\-!]+`

// Error is one validation failure.
type Error struct {
	// Endpoint is the path of the endpoint the error is about, or "".
	Endpoint string
	// Rule is one of the Rule constants.
	Rule string
	// Msg says what is wrong.
	Msg string
}

func (e Error) Error() string {
	if e.Endpoint != "" {
		return fmt.Sprintf("endpoint %s: %s: %s", e.Endpoint, e.Rule, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.Rule, e.Msg)
}

// Errors are every validation failure of a config, in a stable order.
type Errors []Error

func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = e.Error()
	}
	return strings.Join(lines, "\n")
}

// Options control Validate.
type Options struct {
	// SkipDecree skips step 7 (running decree), as a reload that changes
	// neither `decree` nor `project` does (SPEC.md §8).
	SkipDecree bool
}

var (
	pathShape     = regexp.MustCompile(`^/[A-Za-z0-9._\-/{}]+$`)
	paramSegment  = regexp.MustCompile(`^\{(\w+)\}$`)
	placeholderRE = regexp.MustCompile(`\{\{([^{}]*)\}\}`)
)

// PathParams returns the `{name}` parameters of a path, in order. It
// assumes the path passed step 1.
func PathParams(path string) []string {
	var out []string
	for _, seg := range strings.Split(path, "/") {
		if m := paramSegment.FindStringSubmatch(seg); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// Validate runs every rule of SPEC.md §3, steps 1–7, and returns all the
// failures, or nil.
func Validate(c *Config, opts Options) Errors {
	v := &validator{c: c, machines: map[string]*machineData{}}
	v.config()
	v.paths()
	for _, e := range c.Endpoints {
		v.endpoint(e)
	}
	v.secrets()
	if !opts.SkipDecree {
		v.decree()
	}
	if len(v.errs) == 0 {
		return nil
	}
	return v.errs
}

type validator struct {
	c        *Config
	errs     Errors
	machines map[string]*machineData
	// hasDecreeDir is whether the project holds .decree/.
	hasDecreeDir bool
}

func (v *validator) add(endpoint, rule, format string, args ...any) {
	v.errs = append(v.errs, Error{Endpoint: endpoint, Rule: rule, Msg: fmt.Sprintf(format, args...)})
}

// config checks the values no numbered step covers.
func (v *validator) config() {
	c := v.c
	if len(c.Endpoints) == 0 {
		v.add("", RuleConfig, "endpoints: at least one endpoint is required")
	}
	if st, err := os.Stat(filepath.Join(c.ProjectDir, ".decree")); err == nil && st.IsDir() {
		v.hasDecreeDir = true
	} else {
		v.add("", RuleConfig, "project: %s holds no .decree/ directory", c.ProjectDir)
	}
	if c.Listen == "" {
		v.add("", RuleConfig, "listen: must not be empty")
	}
	if c.Daemon.Interval <= 0 {
		v.add("", RuleConfig, "daemon.interval: must be positive")
	}
	if c.Limits.MaxBodyBytes <= 0 {
		v.add("", RuleConfig, "limits.max_body_bytes: must be positive")
	}
	if c.Limits.RateWindow <= 0 {
		v.add("", RuleConfig, "limits.rate_window: must be positive")
	}
	if c.Limits.RateMax <= 0 {
		v.add("", RuleConfig, "limits.rate_max: must be positive")
	}
	if c.Limits.RateFailMax <= 0 {
		v.add("", RuleConfig, "limits.rate_fail_max: must be positive")
	}
	for _, e := range c.Endpoints {
		switch e.Body {
		case BodyRequired, BodyOptional, BodyNone:
		default:
			v.add(e.Path, RuleConfig, "body: %q is not required, optional or none", e.Body)
		}
	}
}

// paths is step 1, plus the built-in reservations of SPEC.md §7.
func (v *validator) paths() {
	mux := http.NewServeMux()
	seen := map[string]bool{}
	for _, e := range v.c.Endpoints {
		p := e.Path
		if !v.pathShape(p) {
			continue
		}
		if reserved := v.reserved(p); reserved != "" {
			v.add(p, RulePath, "is under %s, which the enabled built-in uses", reserved)
		}
		if seen[p] {
			v.add(p, RulePath, "duplicate path")
			continue
		}
		seen[p] = true
		if err := v.builtinConflict(p); err != nil {
			v.add(p, RulePath, "conflicts with a built-in endpoint: %v", err)
			continue
		}
		if err := register(mux, "POST "+p); err != nil {
			v.add(p, RulePath, "conflicts with another endpoint: %v", err)
		}
	}
}

// builtinConflict reports whether POST p conflicts, as a net/http
// pattern, with an enabled built-in, which a path outside the built-ins'
// prefixes can, such as /{a}/{b}/{c}/x with POST /runs/{wait_id}/replies/{event}.
func (v *validator) builtinConflict(p string) error {
	mux := http.NewServeMux()
	for _, rt := range v.c.BuiltinRoutes() {
		if err := register(mux, rt.Method+" "+rt.Path); err != nil {
			return err
		}
	}
	return register(mux, "POST "+p)
}

// pathShape checks one path's shape and reports every problem with it.
func (v *validator) pathShape(p string) bool {
	if p == "" {
		v.add(p, RulePath, "path is required")
		return false
	}
	ok := true
	if !pathShape.MatchString(p) {
		v.add(p, RulePath, "must match %s", pathShape)
		ok = false
	}
	if strings.Contains(p, "..") {
		v.add(p, RulePath, "must not contain ..")
		ok = false
	}
	if !strings.HasPrefix(p, "/") {
		return false
	}
	params := map[string]bool{}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "" {
			v.add(p, RulePath, "has an empty segment")
			ok = false
			continue
		}
		if !strings.ContainsAny(seg, "{}") {
			continue
		}
		m := paramSegment.FindStringSubmatch(seg)
		if m == nil {
			v.add(p, RulePath, "segment %q: a parameter must be a full segment {name}, name matching \\w+", seg)
			ok = false
			continue
		}
		if params[m[1]] {
			v.add(p, RulePath, "parameter {%s} appears twice", m[1])
			ok = false
		}
		params[m[1]] = true
	}
	return ok
}

// reserved returns the built-in prefix a path falls under, or "".
func (v *validator) reserved(p string) string {
	b := v.c.Builtins
	under := func(prefix string) bool { return p == prefix || strings.HasPrefix(p, prefix+"/") }
	switch {
	case (b.Status || b.Replies) && strings.HasPrefix(p, "/runs/"):
		return "/runs/"
	case under(HealthPath):
		return HealthPath
	case b.OpenAPI && under(OpenAPIPath):
		return OpenAPIPath
	}
	return ""
}

// register adds a pattern to mux, turning the panic net/http raises for a
// bad or conflicting pattern into an error.
func register(mux *http.ServeMux, pattern string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	return nil
}

// endpoint runs steps 2–5 for one endpoint.
func (v *validator) endpoint(e Endpoint) {
	params := map[string]bool{}
	for _, name := range PathParams(e.Path) {
		params[name] = true
	}

	// Step 2: patterns.
	for _, name := range sortedKeys(e.Patterns) {
		if !params[name] {
			v.add(e.Path, RulePatterns, "%q is not a parameter of the path", name)
		}
		if _, err := regexp.Compile(`^(?:` + e.Patterns[name] + `)$`); err != nil {
			v.add(e.Path, RulePatterns, "%q does not compile: %v", name, err)
		}
	}

	// Step 3: placeholders.
	used := map[string]bool{}
	for _, p := range e.Message.Params {
		if strings.Contains(p.Key, "{{") {
			v.add(e.Path, RulePlaceholders, "params key %q: placeholders appear only in string values", p.Key)
		}
		if p.Value.Kind != yaml.ScalarNode {
			v.add(e.Path, RulePlaceholders, "params.%s: must be a string, int or bool, not a %s", p.Key, kindName(p.Value))
			continue
		}
		if p.Value.Tag != "!!str" {
			continue
		}
		for _, m := range placeholderRE.FindAllStringSubmatch(p.Value.Value, -1) {
			if !params[m[1]] {
				v.add(e.Path, RulePlaceholders, "params.%s: {{%s}} is not a parameter of the path", p.Key, m[1])
			}
			used[m[1]] = true
		}
	}
	for _, name := range PathParams(e.Path) {
		if !used[name] {
			v.add(e.Path, RulePlaceholders, "parameter {%s} is not used in params", name)
		}
	}

	// Step 4: machine.
	md := v.machine(e)
	if md == nil {
		return
	}

	// Step 5: params against the machine's data.
	for _, p := range e.Message.Params {
		spec, ok := md.Data[p.Key]
		if !ok {
			v.add(e.Path, RuleParams, "params.%s: machine %s has no data %q", p.Key, e.Message.Machine, p.Key)
			continue
		}
		if p.Value.Kind != yaml.ScalarNode {
			continue // reported in step 3
		}
		if p.Value.Tag == "!!str" && placeholderRE.MatchString(p.Value.Value) {
			if spec.Type != "string" {
				v.add(e.Path, RuleParams, "params.%s: a value with a placeholder requires string data, and %s is %s", p.Key, p.Key, spec.Type)
			}
			continue
		}
		if got := scalarType(p.Value); got != spec.Type {
			v.add(e.Path, RuleParams, "params.%s: %s is %s, the value is %s", p.Key, p.Key, spec.Type, got)
		}
	}
}

// machine is step 4. It returns the machine's data, or nil when the
// machine is missing or unreadable (and reports why).
func (v *validator) machine(e Endpoint) *machineData {
	name := e.Message.Machine
	switch {
	case name == "":
		v.add(e.Path, RuleMachine, "message.machine is required")
		return nil
	case strings.Contains(name, "{{"):
		v.add(e.Path, RuleMachine, "message.machine %q: a placeholder is not allowed in the machine", name)
		return nil
	case strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, "."):
		v.add(e.Path, RuleMachine, "message.machine %q is not a machine name", name)
		return nil
	}
	if md, ok := v.machines[name]; ok {
		if md.err != "" {
			v.add(e.Path, md.rule, "%s", md.err)
			return nil
		}
		return md
	}
	md := loadMachine(v.c.ProjectDir, name)
	v.machines[name] = md
	if md.err != "" {
		v.add(e.Path, md.rule, "%s", md.err)
		return nil
	}
	return md
}

type dataSpec struct {
	Type string `yaml:"type"`
}

type machineData struct {
	Data map[string]dataSpec
	// rule and err are set when the machine cannot be used.
	rule, err string
}

// MachineFile is the file of a machine in a project.
func MachineFile(projectDir, machine string) string {
	return filepath.Join(projectDir, ".decree", "machines", machine+".yml")
}

func loadMachine(projectDir, name string) *machineData {
	file := MachineFile(projectDir, name)
	raw, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return &machineData{rule: RuleMachine, err: fmt.Sprintf("machine %s: no file %s", name, file)}
		}
		return &machineData{rule: RuleMachine, err: fmt.Sprintf("machine %s: %v", name, err)}
	}
	var doc struct {
		Data map[string]dataSpec `yaml:"data"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return &machineData{rule: RuleParams, err: fmt.Sprintf("machine %s: cannot read its data: %v", name, err)}
	}
	return &machineData{Data: doc.Data}
}

// scalarType names the decree type of a YAML scalar.
func scalarType(n *yaml.Node) string {
	switch n.Tag {
	case "!!str":
		return "string"
	case "!!int":
		return "int"
	case "!!bool":
		return "bool"
	case "!!null":
		return "null"
	case "!!float":
		return "float"
	}
	return n.Tag
}

func kindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "mapping"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.AliasNode:
		return "alias"
	}
	return "non-scalar"
}

// secrets is step 6: every referenced variable is set, non-blank and long
// enough. The default secret is referenced by endpoints without their own
// and by the authenticated built-ins.
func (v *validator) secrets() {
	users := map[string][]string{}
	var order []string
	use := func(env, who string) {
		if _, ok := users[env]; !ok {
			order = append(order, env)
		}
		users[env] = append(users[env], who)
	}
	for _, e := range v.c.Endpoints {
		env := e.EffectiveSecretEnv(v.c)
		if env == "" {
			v.add(e.Path, RuleSecrets, "no secret_env, and the top-level secret_env is empty")
			continue
		}
		use(env, e.Path)
	}
	if b := v.c.Builtins; b.Status && v.c.SecretEnv != "" {
		use(v.c.SecretEnv, "GET /runs/{id}")
	}
	if b := v.c.Builtins; b.Replies && v.c.SecretEnv != "" {
		use(v.c.SecretEnv, "POST /runs/{wait_id}/replies/{event}")
	}
	if (v.c.Builtins.Status || v.c.Builtins.Replies) && v.c.SecretEnv == "" {
		v.add("", RuleSecrets, "secret_env is empty, and the built-ins under /runs/ need the default secret")
	}
	for _, env := range order {
		val, set := os.LookupEnv(env)
		var problem string
		switch {
		case !set:
			problem = "is not set"
		case strings.TrimSpace(val) == "":
			problem = "is blank"
		case len(val) < MinSecretLen:
			problem = fmt.Sprintf("is %d characters, at least %d are required", len(val), MinSecretLen)
		default:
			continue
		}
		for _, who := range users[env] {
			if strings.HasPrefix(who, "/") {
				v.add(who, RuleSecrets, "%s %s", env, problem)
			} else {
				v.add("", RuleSecrets, "%s %s (used by %s)", env, problem, who)
			}
		}
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Substitute replaces each {{name}} placeholder in s with values[name], in
// one pass: a substituted value is never expanded again.
func Substitute(s string, values map[string]string) string {
	return placeholderRE.ReplaceAllStringFunc(s, func(m string) string {
		return values[m[2:len(m)-2]]
	})
}

// ParamText is the text of a validated `params` value as decree's
// `--param <key>=<value>` takes it: a string as written, an int in decimal
// and a bool as true or false. template is whether it holds placeholders.
func ParamText(n *yaml.Node) (text string, template bool, err error) {
	if n.Kind != yaml.ScalarNode {
		return "", false, fmt.Errorf("not a scalar")
	}
	switch n.Tag {
	case "!!str":
		return n.Value, placeholderRE.MatchString(n.Value), nil
	case "!!int":
		var i int64
		if err := n.Decode(&i); err != nil {
			return "", false, err
		}
		return strconv.FormatInt(i, 10), false, nil
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err != nil {
			return "", false, err
		}
		return strconv.FormatBool(b), false, nil
	}
	return "", false, fmt.Errorf("a %s value is not a string, int or bool", scalarType(n))
}
