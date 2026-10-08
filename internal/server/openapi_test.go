package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// getOpenAPI requests GET /openapi.json from h, with no secret.
func getOpenAPI(t *testing.T, h http.Handler) (raw string, doc map[string]any) {
	t.Helper()
	hr := httptest.NewRequest(http.MethodGet, "http://decree-go-rest.test/openapi.json", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, hr)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s; want 200", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	return rec.Body.String(), doc
}

// TestAcceptanceOpenAPIGolden is the second acceptance criterion of 05:
// the document of the example config equals testdata/openapi.golden.json.
func TestAcceptanceOpenAPIGolden(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	raw, _ := getOpenAPI(t, f.srv)
	golden(t, "openapi.golden.json", raw)
}

// TestOpenAPIShape checks the document is OpenAPI 3.1 in shape, with
// every configured endpoint, its parameters and their patterns, and the
// bearer scheme.
func TestOpenAPIShape(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	_, doc := getOpenAPI(t, f.srv)
	if doc["openapi"] != "3.1.0" {
		t.Errorf("openapi = %v, want 3.1.0", doc["openapi"])
	}
	info, _ := doc["info"].(map[string]any)
	if info["title"] == nil || info["version"] == nil {
		t.Errorf("info %v lacks title or version", info)
	}
	scheme := field(t, doc, "components.securitySchemes.bearer").(map[string]any)
	if scheme["type"] != "http" || scheme["scheme"] != "bearer" {
		t.Errorf("bearer scheme %v", scheme)
	}
	paths := doc["paths"].(map[string]any)

	type op struct {
		path, method string
		params       map[string]string
		auth         bool
		body         string // "required", "optional" or "" for none
	}
	want := []op{
		{"/notify", "post", map[string]string{}, true, "required"},
		{"/notify/{title}", "post", map[string]string{"title": "^(?:[A-Za-z0-9 _.-]{1,80})$"}, true, "required"},
		{"/comfy/{type}/{name}", "post", map[string]string{"type": "^(?:[a-z0-9-]+)$", "name": "^(?:[A-Za-z0-9_.-]+)$"}, true, "required"},
		{"/approve/{wait_id}", "post", map[string]string{"wait_id": "^(?:[A-Za-z0-9._-]{1,128})$"}, true, "optional"},
		{"/openapi.json", "get", map[string]string{}, false, ""},
		{"/healthz", "get", map[string]string{}, false, ""},
	}
	if len(paths) != len(want) {
		t.Errorf("%d paths, want %d", len(paths), len(want))
	}
	for _, w := range want {
		item, ok := paths[w.path].(map[string]any)
		if !ok {
			t.Errorf("%s missing", w.path)
			continue
		}
		o, ok := item[w.method].(map[string]any)
		if !ok || len(item) != 1 {
			t.Errorf("%s: operations %v, want only %s", w.path, item, w.method)
			continue
		}
		got := map[string]string{}
		params, _ := o["parameters"].([]any)
		for _, p := range params {
			p := p.(map[string]any)
			schema := p["schema"].(map[string]any)
			if p["in"] != "path" || p["required"] != true || schema["type"] != "string" {
				t.Errorf("%s: parameter %v", w.path, p)
			}
			got[p["name"].(string)] = schema["pattern"].(string)
		}
		if len(got) != len(w.params) {
			t.Errorf("%s: parameters %v, want %v", w.path, got, w.params)
		}
		for name, pat := range w.params {
			if got[name] != pat {
				t.Errorf("%s: parameter %s pattern %q, want %q", w.path, name, got[name], pat)
			}
		}
		security, _ := o["security"].([]any)
		if auth := len(security) == 1 && security[0].(map[string]any)["bearer"] != nil; auth != w.auth {
			t.Errorf("%s: security %v, want bearer %v", w.path, o["security"], w.auth)
		}
		body, _ := o["requestBody"].(map[string]any)
		switch {
		case w.body == "" && body != nil:
			t.Errorf("%s: a request body, want none", w.path)
		case w.body != "" && (body == nil || body["required"] != (w.body == "required")):
			t.Errorf("%s: request body %v, want %s", w.path, body, w.body)
		case body != nil && body["content"].(map[string]any)["text/plain"] == nil:
			t.Errorf("%s: request body %v is not text/plain", w.path, body)
		}
	}
}

// TestOpenAPIOff: with GET /openapi.json off, as by default, the path is
// a 404 like any unknown path, charged to the failure budget.
func TestOpenAPIOff(t *testing.T) {
	f := newFixtureWith(t, limitsConfig(t, 60, 2), Options{})
	f.clock()
	for i, want := range []int{404, 404, 429} {
		rec := f.do(t, req{method: "GET", path: "/openapi.json"})
		if rec.Code != want {
			t.Fatalf("request %d: status %d, want %d", i+1, rec.Code, want)
		}
		if want == 404 && errorBody(t, rec) != "not found" {
			t.Errorf("request %d: not the unknown-path 404", i+1)
		}
	}
	if rec := f.do(t, req{path: "/notify/backup", bearer: secret, body: "x"}); rec.Code != http.StatusCreated {
		t.Errorf("an endpoint: status %d, want 201", rec.Code)
	}
}

// TestOpenAPIDescribesActions: an emit endpoint is described by the
// message it queues, an event endpoint by the reply it sends, with the
// responses of each.
func TestOpenAPIDescribesActions(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	_, doc := getOpenAPI(t, f.srv)
	emit := field(t, doc, "paths./notify/{title}.post").(map[string]any)
	event := field(t, doc, "paths./approve/{wait_id}.post").(map[string]any)
	if d := emit["description"].(string); !strings.Contains(d, "decree emit --machine notify") {
		t.Errorf("emit description %q", d)
	}
	if d := event["description"].(string); !strings.Contains(d, "decree event {{wait_id}} approve [-m=<note>] --format json") {
		t.Errorf("event description %q", d)
	}
	codes := func(op map[string]any) string {
		var out []string
		for code := range op["responses"].(map[string]any) {
			out = append(out, code)
		}
		sort.Strings(out)
		return strings.Join(out, " ")
	}
	if got := codes(emit); got != "201 400 401 413 429 500" {
		t.Errorf("emit responses %s", got)
	}
	if got := codes(event); got != "201 400 401 409 413 429 500" {
		t.Errorf("event responses %s", got)
	}
	if ref := field(t, event, "responses.201.content.application/json.schema.$ref"); ref != "#/components/schemas/Reply" {
		t.Errorf("event 201 schema %v", ref)
	}
	reply := field(t, doc, "components.schemas.Reply.required").([]any)
	if fmt.Sprint(reply) != "[id path to event]" {
		t.Errorf("Reply requires %v", reply)
	}
}

func TestOpenAPIMethodsAndBudgets(t *testing.T) {
	f := newFixture(t, limitsConfig(t, 1, 1))
	f.clock()
	// No secret, and no budget spent, however often it is read.
	for i := 0; i < 3; i++ {
		getOpenAPI(t, f.srv)
	}
	if rec := f.do(t, req{method: "HEAD", path: "/openapi.json"}); rec.Code != http.StatusOK {
		t.Errorf("HEAD: status %d, want 200", rec.Code)
	}
	if rec := f.do(t, req{path: "/notify/backup", bearer: secret, body: "x"}); rec.Code != http.StatusCreated {
		t.Errorf("rate_max spent by /openapi.json: status %d", rec.Code)
	}
	rec := f.do(t, req{path: "/openapi.json"})
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: status %d, Allow %q; want 405 with GET, HEAD", rec.Code, rec.Header().Get("Allow"))
	}
	if rec := f.do(t, req{path: "/openapi.json"}); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the 405 did not spend the failure budget: status %d", rec.Code)
	}
}

// TestOpenAPIFollowsReload: a reload that adds an endpoint changes the
// document.
func TestOpenAPIFollowsReload(t *testing.T) {
	f := newFixture(t, exampleConfig(t))
	l := f.live(t)
	before, doc := getOpenAPI(t, l)
	if _, ok := doc["paths"].(map[string]any)["/extra"]; ok {
		t.Fatal("/extra in the document before the reload")
	}
	cfg := strings.Replace(exampleConfig(t), "  - path: /notify\n", extraEndpoint+"\n  - path: /notify\n", 1)
	replaceFile(t, f.path, cfg, time.Now().Add(time.Minute))
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	after, doc := getOpenAPI(t, l)
	if after == before {
		t.Fatal("the document did not change")
	}
	item, ok := doc["paths"].(map[string]any)["/extra"].(map[string]any)
	if !ok {
		t.Fatalf("/extra missing after the reload")
	}
	if field(t, item, "post.requestBody.required") != false {
		t.Errorf("/extra's optional body: %v", item)
	}
}
