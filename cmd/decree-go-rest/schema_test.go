package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/jtmckay/decree-go-rest/internal/config"
)

const schemaFile = "../../decree-go-rest.schema.json"

// compileSchema compiles decree-go-rest.schema.json. Its draft 2020-12
// metaschema is built into the validator, so nothing is fetched.
func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(schemaFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatalf("%s: %v", schemaFile, err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("decree-go-rest.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("decree-go-rest.schema.json")
	if err != nil {
		t.Fatalf("%s: %v", schemaFile, err)
	}
	return s
}

// yamlInstance reads a YAML config as the JSON value the schema checks.
func yamlInstance(t *testing.T, src string) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal([]byte(src), &v); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

// TestAcceptanceExampleMatchesSchema is the second acceptance criterion:
// decree-go-rest.example.yml passes decree-go-rest.schema.json.
func TestAcceptanceExampleMatchesSchema(t *testing.T) {
	example, err := os.ReadFile("../../decree-go-rest.example.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(example), schemaLine) {
		t.Errorf("decree-go-rest.example.yml does not start with %q", schemaLine)
	}
	if err := compileSchema(t).Validate(yamlInstance(t, string(example))); err != nil {
		t.Errorf("decree-go-rest.example.yml does not match the schema:\n%v", err)
	}
}

// TestSchemaRejects: the schema catches the shape errors that config.Load
// or config.Validate reject.
func TestSchemaRejects(t *testing.T) {
	s := compileSchema(t)
	ok := "endpoints: [{ path: /a, message: { machine: m } }]\n"
	if err := s.Validate(yamlInstance(t, ok)); err != nil {
		t.Fatalf("minimal config rejected: %v", err)
	}
	for name, src := range map[string]string{
		"no endpoints":     "listen: 127.0.0.1:1\n",
		"empty endpoints":  "endpoints: []\n",
		"unknown key":      "colour: red\n" + ok,
		"unknown daemon":   "daemon: { restart: true }\n" + ok,
		"bad duration":     "daemon: { interval: 1.5s }\n" + ok,
		"zero duration":    "limits: { rate_window: 0s }\n" + ok,
		"zero rate":        "limits: { rate_max: 0 }\n" + ok,
		"string limit":     "limits: { max_body_bytes: lots }\n" + ok,
		"no machine":       "endpoints: [{ path: /a, message: {} }]\n",
		"no path":          "endpoints: [{ message: { machine: m } }]\n",
		"bad path":         "endpoints: [{ path: 'a b', message: { machine: m } }]\n",
		"bad body":         "endpoints: [{ path: /a, body: maybe, message: { machine: m } }]\n",
		"machine template": "endpoints: [{ path: '/{m}', message: { machine: '{{m}}' } }]\n",
		"list param":       "endpoints: [{ path: /a, message: { machine: m, params: { x: [1] } } }]\n",
		"unknown endpoint": "endpoints: [{ path: /a, method: GET, message: { machine: m } }]\n",
		"bad secret_env":   "secret_env: 'NOT A NAME'\n" + ok,
	} {
		if err := s.Validate(yamlInstance(t, src)); err == nil {
			t.Errorf("%s: accepted:\n%s", name, src)
		}
	}
}

// TestSchemaDescribesEveryKey: every key the schema declares has a
// description, and the keys are exactly those config.Config decodes, so
// the schema cannot drift from the code.
func TestSchemaDescribesEveryKey(t *testing.T) {
	raw, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	defs, _ := root["$defs"].(map[string]any)
	resolve := func(n map[string]any) map[string]any {
		if ref, ok := n["$ref"].(string); ok {
			d, _ := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
			return d
		}
		return n
	}
	var walk func(where string, n map[string]any, typ reflect.Type)
	walk = func(where string, n map[string]any, typ reflect.Type) {
		n = resolve(n)
		props, _ := n["properties"].(map[string]any)
		var got []string
		for k, p := range props {
			got = append(got, k)
			pm := p.(map[string]any)
			if d, _ := pm["description"].(string); strings.TrimSpace(d) == "" {
				t.Errorf("%s%s: no description", where, k)
			}
		}
		var want []string
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			tag := typ.Field(i).Tag.Get("yaml")
			if tag == "" || tag == "-" {
				continue
			}
			want = append(want, tag)
			fields[tag] = typ.Field(i).Type
		}
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: schema keys %v, config keys %v", where, got, want)
		}
		for k, p := range props {
			ft := fields[k]
			if ft == nil {
				continue
			}
			pm := resolve(p.(map[string]any))
			if ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct {
				items, _ := pm["items"].(map[string]any)
				walk(where+k+"[].", items, ft.Elem())
			} else if ft.Kind() == reflect.Struct && pm["properties"] != nil {
				walk(where+k+".", pm, ft)
			}
		}
	}
	walk("", root, reflect.TypeOf(config.Config{}))
}
