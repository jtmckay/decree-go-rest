package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtmckay/decree-go-rest/internal/config"
)

// readConfig has a read endpoint of each form, and an emit endpoint.
const readConfig = `limits:
  rate_window: 60s
  rate_max: 60
  rate_fail_max: 10
endpoints:
  - path: /leases
    action: read
    secret_env: DECREE_GO_REST_READ_SECRET
    read: { machine: gpu_lease, path: status/leases }
  - path: /leases/by-id/{id}
    action: read
    secret_env: DECREE_GO_REST_READ_SECRET
    patterns: { id: "[0-9TZ.-]+-[a-f0-9]{6}" }
    read: { machine: gpu_lease, path: "status/leases/{{id}}.json" }
  - path: /registry
    action: read
    read: { machine: gpu_lease }
  - path: /health
    action: read
    read: { machine: gpu_lease, path: health.json }
  - path: /notify
    message: { machine: notify }
`

// mtime is the modification time of the files the tests write.
var mtime = time.Date(2026, 11, 24, 17, 0, 0, 0, time.UTC)

// readFixture serves readConfig over a gpu_lease state/ folder holding
// status/leases/{a.json, .hidden, old/b.json}. It returns the fixture and
// the state folder.
func readFixture(t *testing.T, cfg string) (*fixture, string) {
	t.Helper()
	f := newFixture(t, cfg)
	state := config.StateDir(f.proj, "gpu_lease")
	writeState(t, state, "status/leases/a.json", `{"holder":"a"}`)
	writeState(t, state, "status/leases/.hidden", "secret")
	writeState(t, state, "status/leases/old/b.json", `{"holder":"b"}`)
	if err := os.Chtimes(filepath.Join(state, "status/leases/old"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return f, state
}

// writeState writes a file under the state folder, modified at mtime.
func writeState(t *testing.T, state, rel, content string) string {
	t.Helper()
	p := filepath.Join(state, filepath.FromSlash(rel))
	write(t, p, content)
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func get(path, bearer string) req {
	return req{method: http.MethodGet, path: path, bearer: bearer}
}

// decodeListing checks that rec is a 200 listing and decodes it.
func decodeListing(t *testing.T, rec interface {
	Result() *http.Response
}, body []byte) listing {
	t.Helper()
	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q, want application/json", ct)
	}
	if res.Header.Get("ETag") == "" {
		t.Error("a listing has no ETag")
	}
	var l listing
	if err := json.Unmarshal(body, &l); err != nil {
		t.Fatalf("listing %q: %v", body, err)
	}
	return l
}

// TestAcceptanceReadListing is the first acceptance criterion of 10.
func TestAcceptanceReadListing(t *testing.T) {
	f, _ := readFixture(t, readConfig)

	rec := f.do(t, get("/leases", readSecret))
	l := decodeListing(t, rec, rec.Body.Bytes())
	size := int64(len(`{"holder":"a"}`))
	want := listing{Path: "/leases", Items: []listingEntry{
		{Name: "a.json", Type: "file", Size: &size, Modified: "2026-11-24T17:00:00Z", Href: "/leases/a.json"},
		{Name: "old", Type: "directory", Modified: "2026-11-24T17:00:00Z", Href: "/leases/old/"},
	}}
	if got, w := mustJSON(t, l), mustJSON(t, want); got != w {
		t.Errorf("GET /leases:\n got %s\nwant %s", got, w)
	}
	if strings.Contains(rec.Body.String(), "hidden") {
		t.Errorf("the listing names the dotfile: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "truncated") {
		t.Errorf("a short listing says truncated: %s", rec.Body)
	}
	fmt.Printf("evidence: GET /leases -> %d %s", rec.Code, rec.Body)

	// The trailing slash of the root is ignored.
	if rec := f.do(t, get("/leases/", readSecret)); rec.Code != http.StatusOK || decodeListing(t, rec, rec.Body.Bytes()).Path != "/leases" {
		t.Errorf("GET /leases/: %d %s", rec.Code, rec.Body)
	}

	rec = f.do(t, get("/leases/old/", readSecret))
	l = decodeListing(t, rec, rec.Body.Bytes())
	size = int64(len(`{"holder":"b"}`))
	want = listing{Path: "/leases/old", Items: []listingEntry{
		{Name: "b.json", Type: "file", Size: &size, Modified: "2026-11-24T17:00:00Z", Href: "/leases/old/b.json"},
	}}
	if got, w := mustJSON(t, l), mustJSON(t, want); got != w {
		t.Errorf("GET /leases/old/:\n got %s\nwant %s", got, w)
	}
	fmt.Printf("evidence: GET /leases/old/ -> %d %s", rec.Code, rec.Body)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAcceptanceReadFileETag is the second acceptance criterion of 10.
func TestAcceptanceReadFileETag(t *testing.T) {
	f, _ := readFixture(t, readConfig)
	content := `{"holder":"a"}`
	sum := sha256.Sum256([]byte(content))
	etag := `"` + hex.EncodeToString(sum[:]) + `"`

	rec := f.do(t, get("/leases/a.json", readSecret))
	if rec.Code != http.StatusOK || rec.Body.String() != content {
		t.Fatalf("GET /leases/a.json: %d %q", rec.Code, rec.Body)
	}
	h := rec.Header()
	for k, v := range map[string]string{
		"Content-Type":           "application/json",
		"ETag":                   etag,
		"Last-Modified":          mtime.Format(http.TimeFormat),
		"Cache-Control":          "no-cache",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	fmt.Printf("evidence: GET /leases/a.json -> %d Content-Type=%s ETag=%s Last-Modified=%s body=%s\n",
		rec.Code, h.Get("Content-Type"), h.Get("ETag"), h.Get("Last-Modified"), rec.Body)

	r := get("/leases/a.json", readSecret)
	r.header = http.Header{"If-None-Match": {h.Get("ETag")}}
	rec = f.do(t, r)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("If-None-Match: %d %q, want 304 and no body", rec.Code, rec.Body)
	}
	fmt.Printf("evidence: GET /leases/a.json If-None-Match: %s -> %d, body %d bytes\n", h.Get("ETag"), rec.Code, rec.Body.Len())

	// A changed file is a new ETag, so the old one no longer matches.
	write(t, filepath.Join(config.StateDir(f.proj, "gpu_lease"), "status/leases/a.json"), `{"holder":"c"}`)
	if rec := f.do(t, r); rec.Code != http.StatusOK || rec.Body.String() != `{"holder":"c"}` {
		t.Errorf("after a change: %d %q, want 200 and the new content", rec.Code, rec.Body)
	}
}

// TestReadContentTypes: each extension's Content-Type.
func TestReadContentTypes(t *testing.T) {
	f, state := readFixture(t, readConfig)
	for name, want := range map[string]string{
		"x.json":  "application/json",
		"x.JSON":  "application/json",
		"x.md":    "text/markdown; charset=utf-8",
		"x.txt":   "text/plain; charset=utf-8",
		"x.log":   "text/plain; charset=utf-8",
		"x.yml":   "text/plain; charset=utf-8",
		"x.yaml":  "text/plain; charset=utf-8",
		"x.html":  "application/octet-stream",
		"x":       "application/octet-stream",
		"x.bin":   "application/octet-stream",
		"x.jsonl": "application/octet-stream",
	} {
		// HTML content must not be sniffed into text/html.
		writeState(t, state, "status/leases/"+name, "<html><script>x</script></html>")
		rec := f.do(t, get("/leases/"+name, readSecret))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d", name, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != want {
			t.Errorf("%s: Content-Type %q, want %q", name, got, want)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options %q", name, got)
		}
	}
}

// TestReadHeadRangeConditional: HEAD of a file and a listing, a Range, and
// If-None-Match on a listing.
func TestReadHeadRangeConditional(t *testing.T) {
	f, _ := readFixture(t, readConfig)

	rec := f.do(t, req{method: http.MethodHead, path: "/leases/a.json", bearer: readSecret})
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD file: %d %q", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Length"); got != "14" {
		t.Errorf("HEAD file: Content-Length %q, want 14", got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("HEAD file: no ETag")
	}

	r := get("/leases/a.json", readSecret)
	r.header = http.Header{"Range": {"bytes=2-7"}}
	rec = f.do(t, r)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != `holder` {
		t.Errorf("Range: %d %q, want 206 holder", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 2-7/14" {
		t.Errorf("Content-Range %q", got)
	}

	r.header = http.Header{"If-Modified-Since": {mtime.Format(http.TimeFormat)}}
	if rec := f.do(t, r); rec.Code != http.StatusNotModified {
		t.Errorf("If-Modified-Since: %d, want 304", rec.Code)
	}

	rec = f.do(t, get("/leases", readSecret))
	etag := rec.Header().Get("ETag")
	head := f.do(t, req{method: http.MethodHead, path: "/leases", bearer: readSecret})
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != etag {
		t.Errorf("HEAD listing: %d %q ETag %q, want 200, no body, %q", head.Code, head.Body, head.Header().Get("ETag"), etag)
	}
	if got := head.Header().Get("Content-Length"); got != fmt.Sprint(rec.Body.Len()) {
		t.Errorf("HEAD listing: Content-Length %q, want %d", got, rec.Body.Len())
	}
	for _, inm := range []string{etag, `"other", ` + etag, "W/" + etag, "*"} {
		r := get("/leases", readSecret)
		r.header = http.Header{"If-None-Match": {inm}}
		if rec := f.do(t, r); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || rec.Header().Get("ETag") != etag {
			t.Errorf("listing If-None-Match %s: %d %q", inm, rec.Code, rec.Body)
		}
	}
	r = get("/leases", readSecret)
	r.header = http.Header{"If-None-Match": {`"other"`}}
	if rec := f.do(t, r); rec.Code != http.StatusOK {
		t.Errorf("listing If-None-Match other: %d, want 200", rec.Code)
	}
	// A new file changes the listing, and its ETag.
	writeState(t, config.StateDir(f.proj, "gpu_lease"), "status/leases/c.json", "{}")
	r.header = http.Header{"If-None-Match": {etag}}
	if rec := f.do(t, r); rec.Code != http.StatusOK {
		t.Errorf("listing after a change: %d, want 200", rec.Code)
	}
}

// TestAcceptanceReadMethodAndAuth is the fifth acceptance criterion of
// 10: a POST is a 405 with Allow: GET, HEAD and a GET without the secret
// a 401, both charged to the failure budget.
func TestAcceptanceReadMethodAndAuth(t *testing.T) {
	cfg := strings.Replace(readConfig, "rate_fail_max: 10", "rate_fail_max: 2", 1)
	f, _ := readFixture(t, cfg)
	f.clock()

	rec := f.do(t, req{method: http.MethodPost, path: "/leases", bearer: readSecret, body: "x"})
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST /leases: %d Allow %q, want 405 GET, HEAD", rec.Code, rec.Header().Get("Allow"))
	}
	fmt.Printf("evidence: POST /leases -> %d Allow: %s\n", rec.Code, rec.Header().Get("Allow"))
	rec = f.do(t, get("/leases/a.json", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET without the secret: %d, want 401", rec.Code)
	}
	fmt.Printf("evidence: GET /leases/a.json without the secret -> %d\n", rec.Code)
	// Both spent the failure budget of 2: the next rejection is a 429,
	rec = f.do(t, req{method: http.MethodDelete, path: "/leases/a.json", bearer: readSecret})
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("third rejection: %d, want 429", rec.Code)
	}
	fmt.Printf("evidence: DELETE /leases/a.json after both, with rate_fail_max 2 -> %d\n", rec.Code)
	// while an authenticated request is unaffected.
	if rec := f.do(t, get("/leases/a.json", readSecret)); rec.Code != http.StatusOK {
		t.Errorf("GET with the secret: %d, want 200", rec.Code)
	}
	// Another endpoint's secret is not this one's.
	if rec := f.do(t, get("/leases/a.json", secret)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("GET with the default secret: %d, want 429 (a 401 over budget)", rec.Code)
	}
}

// TestReadMethodNotAllowedBelow: a POST below a read endpoint's path is a
// 405 too, logged as the endpoint's route.
func TestReadMethodNotAllowedBelow(t *testing.T) {
	f, _ := readFixture(t, readConfig)
	logs := captureLogs(f.srv)
	rec := f.do(t, req{method: http.MethodPut, path: "/leases/old/b.json", bearer: readSecret})
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("PUT below: %d Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
	recs := requestRecords(t, logs)
	if len(recs) != 1 || recs[0]["route"] != "/leases" {
		t.Errorf("log records %v, want route /leases", recs)
	}
}

// TestReadBudgetOrder: authenticated requests, a 404 and a 400 among
// them, spend rate_max and not the failure budget.
func TestReadBudgetOrder(t *testing.T) {
	cfg := strings.Replace(readConfig, "rate_max: 60", "rate_max: 3", 1)
	cfg = strings.Replace(cfg, "rate_fail_max: 10", "rate_fail_max: 1", 1)
	f, _ := readFixture(t, cfg)
	f.clock()
	for i, tc := range []struct {
		path string
		want int
	}{
		{"/leases/missing.json", http.StatusNotFound},
		{"/leases/a%20b", http.StatusBadRequest},
		{"/leases/a.json", http.StatusOK},
		{"/leases/a.json", http.StatusTooManyRequests},
	} {
		if rec := f.do(t, get(tc.path, readSecret)); rec.Code != tc.want {
			t.Errorf("request %d, %s: %d, want %d", i+1, tc.path, rec.Code, tc.want)
		}
	}
	// The failure budget is untouched: its one rejection is a 401.
	if rec := f.do(t, get("/leases/a.json", wrongBearer)); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong bearer: %d, want 401", rec.Code)
	}
}

// TestReadNotFound: a missing file, a dotfile reached through a symlink,
// a symlink out of state/, a FIFO, and what lies below a file are 404s.
func TestReadNotFound(t *testing.T) {
	f, state := readFixture(t, readConfig)
	outside := filepath.Join(f.proj, ".decree", "machines", "gpu_lease", "scripts")
	write(t, filepath.Join(outside, "run.sh"), "#!/bin/sh\necho secret\n")
	write(t, filepath.Join(f.proj, ".decree", "machines", "gpu_lease", ".env"), "TOKEN=secret\n")
	leases := filepath.Join(state, "status", "leases")
	for name, target := range map[string]string{
		"out.json":     filepath.Join(outside, "run.sh"),
		"scripts":      outside,
		"rel.json":     "../../../scripts/run.sh",
		"env.json":     "../../../.env",
		"hidden.json":  ".hidden",
		"dangling.txt": "nothing-here",
		"in.json":      "a.json", // inside: served
		"olddir":       "old",    // inside: listed and served
	} {
		if err := os.Symlink(target, filepath.Join(leases, name)); err != nil {
			t.Fatal(err)
		}
	}
	// A dot directory, entered through a symlink.
	writeState(t, state, ".private/k.json", "{}")
	if err := os.Symlink("../../.private", filepath.Join(leases, "priv")); err != nil {
		t.Fatal(err)
	}
	writeState(t, state, "health.json", `{"ok":true}`)
	makeFIFO(t, filepath.Join(leases, "pipe.json"))

	for _, p := range []string{
		"/leases/missing.json",
		"/leases/old/missing.json",
		"/leases/out.json",
		"/leases/scripts",
		"/leases/scripts/run.sh",
		"/leases/rel.json",
		"/leases/env.json",
		"/leases/hidden.json",
		"/leases/dangling.txt",
		"/leases/priv",
		"/leases/priv/k.json",
		"/leases/pipe.json",
		"/leases/a.json/x",
		"/health/x",
	} {
		bearer := readSecret
		if strings.HasPrefix(p, "/health") {
			bearer = secret
		}
		rec := f.do(t, get(p, bearer))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d %q, want 404", p, rec.Code, rec.Body)
			continue
		}
		if msg := errorBody(t, rec); msg != "not found" {
			t.Errorf("GET %s: %q", p, msg)
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("GET %s leaked: %s", p, rec.Body)
		}
	}
	if rec := f.do(t, get("/leases/out.json", readSecret)); true {
		fmt.Printf("evidence: GET /leases/out.json (a symlink to scripts/run.sh) -> %d %s", rec.Code, rec.Body)
	}

	for p, bearer := range map[string]string{"/leases/in.json": readSecret, "/leases/olddir/b.json": readSecret, "/health": secret} {
		if rec := f.do(t, get(p, bearer)); rec.Code != http.StatusOK {
			t.Errorf("GET %s: %d, want 200", p, rec.Code)
		}
	}
	// The listing holds only what would be served.
	rec := f.do(t, get("/leases", readSecret))
	var names []string
	for _, it := range decodeListing(t, rec, rec.Body.Bytes()).Items {
		names = append(names, it.Name+":"+it.Type)
	}
	if got := strings.Join(names, " "); got != "a.json:file in.json:file old:directory olddir:directory" {
		t.Errorf("listing with links: %s", got)
	}
}

// TestReadNoStateFolder: a machine that has written no state yet is a 404,
// not an error.
func TestReadNoStateFolder(t *testing.T) {
	f := newFixture(t, readConfig)
	for p, bearer := range map[string]string{"/leases": readSecret, "/leases/a.json": readSecret, "/registry": secret, "/health": secret} {
		if rec := f.do(t, get(p, bearer)); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", p, rec.Code)
		}
	}
}

// TestAcceptanceReadTraversal is the third acceptance criterion of 10,
// with the 400s of a bad segment.
func TestAcceptanceReadTraversal(t *testing.T) {
	f, _ := readFixture(t, readConfig)
	write(t, filepath.Join(f.proj, ".decree", "machines", "gpu_lease", "scripts", "run.sh"), "secret")
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/leases/..%2f..%2fscripts", http.StatusBadRequest},
		{"/leases/..%2F..%2F..%2Fscripts/run.sh", http.StatusBadRequest},
		{"/leases/%2e%2e", http.StatusBadRequest},
		{"/leases/%2e%2e/%2e%2e/%2e%2e/scripts/run.sh", http.StatusBadRequest},
		{"/leases/old/%2e%2e%2fa.json", http.StatusBadRequest},
		{"/leases/.hidden", http.StatusBadRequest},
		{"/leases/%2ehidden", http.StatusBadRequest},
		{"/leases/a%20b.json", http.StatusBadRequest},
		{"/leases/a%00.json", http.StatusBadRequest},
		{"/leases/a%5cb", http.StatusBadRequest},
		// An unencoded dot segment fails routing (SPEC.md §4 step 1).
		{"/leases/../scripts", http.StatusNotFound},
		{"/leases/old/../a.json", http.StatusNotFound},
	} {
		rec := f.do(t, get(tc.path, readSecret))
		if rec.Code != tc.want {
			t.Errorf("GET %s: %d %q, want %d", tc.path, rec.Code, rec.Body, tc.want)
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("GET %s leaked: %s", tc.path, rec.Body)
		}
		if tc.path == "/leases/..%2f..%2fscripts" {
			fmt.Printf("evidence: GET %s -> %d %s", tc.path, rec.Code, rec.Body)
		}
	}
}

// TestAcceptanceReadPlaceholders is the fourth acceptance criterion of 10.
func TestAcceptanceReadPlaceholders(t *testing.T) {
	f, state := readFixture(t, readConfig)
	id := "20261124T170000.000000Z-abc123"
	writeState(t, state, "status/leases/"+id+".json", `{"id":"`+id+`"}`)
	for _, tc := range []struct {
		id   string
		want int
	}{
		{id, http.StatusOK},
		{"not-an-id", http.StatusBadRequest},
		{"20261124T170000.000000Z-ffffff", http.StatusNotFound},
		// The pattern allows dots: a value starting with one is still refused.
		{"..-abcdef", http.StatusBadRequest},
		{"%2e%2e-abcdef", http.StatusBadRequest},
	} {
		rec := f.do(t, get("/leases/by-id/"+tc.id, readSecret))
		if rec.Code != tc.want {
			t.Errorf("GET /leases/by-id/%s: %d %q, want %d", tc.id, rec.Code, rec.Body, tc.want)
		}
		fmt.Printf("evidence: GET /leases/by-id/%s -> %d\n", tc.id, rec.Code)
	}
	rec := f.do(t, get("/leases/by-id/"+id, readSecret))
	if rec.Body.String() != `{"id":"`+id+`"}` || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("by id: %q %q", rec.Body, rec.Header().Get("Content-Type"))
	}
	// Nothing below a placeholder endpoint is served.
	if rec := f.do(t, get("/leases/by-id/"+id+"/x", readSecret)); rec.Code == http.StatusOK {
		t.Errorf("below by-id: %d", rec.Code)
	}
}

// TestReadPlaceholderDirectory: a substituted path that is a directory is
// listed, without hrefs, since nothing below the endpoint is served.
func TestReadPlaceholderDirectory(t *testing.T) {
	cfg := `endpoints:
  - path: /dirs/{name}
    action: read
    read: { machine: gpu_lease, path: "status/{{name}}" }
`
	f, _ := readFixture(t, cfg)
	rec := f.do(t, get("/dirs/leases", secret))
	l := decodeListing(t, rec, rec.Body.Bytes())
	if l.Path != "/dirs/leases" || len(l.Items) != 2 {
		t.Fatalf("listing %s", rec.Body)
	}
	for _, it := range l.Items {
		if it.Href != "" {
			t.Errorf("%s has href %q", it.Name, it.Href)
		}
	}
	if strings.Contains(rec.Body.String(), "href") {
		t.Errorf("listing has hrefs: %s", rec.Body)
	}
}

// TestReadWholeStateFolder: without read.path the state folder is served.
func TestReadWholeStateFolder(t *testing.T) {
	f, state := readFixture(t, readConfig)
	writeState(t, state, "health.json", `{"ok":true}`)
	rec := f.do(t, get("/registry", secret))
	l := decodeListing(t, rec, rec.Body.Bytes())
	if got := mustJSON(t, l.Items); !strings.Contains(got, `"href":"/registry/health.json"`) || !strings.Contains(got, `"href":"/registry/status/"`) {
		t.Errorf("listing %s", got)
	}
	if rec := f.do(t, get("/registry/status/leases/old/b.json", secret)); rec.Code != http.StatusOK || rec.Body.String() != `{"holder":"b"}` {
		t.Errorf("deep file: %d %q", rec.Code, rec.Body)
	}
	if rec := f.do(t, get("/health", secret)); rec.Code != http.StatusOK || rec.Body.String() != `{"ok":true}` {
		t.Errorf("file endpoint: %d %q", rec.Code, rec.Body)
	}
}

// TestReadTruncated: a listing holds at most MaxListing entries.
func TestReadTruncated(t *testing.T) {
	f, state := readFixture(t, readConfig)
	big := filepath.Join(state, "status", "leases", "old")
	// old holds b.json already; fill it to exactly MaxListing.
	for i := 0; i < MaxListing-1; i++ {
		if err := os.WriteFile(filepath.Join(big, fmt.Sprintf("f%04d.json", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rec := f.do(t, get("/leases/old", readSecret))
	l := decodeListing(t, rec, rec.Body.Bytes())
	if len(l.Items) != MaxListing || l.Truncated {
		t.Errorf("%d entries: %d items, truncated %v", MaxListing, len(l.Items), l.Truncated)
	}
	if err := os.WriteFile(filepath.Join(big, "zz.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Dotfiles do not count.
	if err := os.WriteFile(filepath.Join(big, ".a"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rec = f.do(t, get("/leases/old", readSecret))
	l = decodeListing(t, rec, rec.Body.Bytes())
	if len(l.Items) != MaxListing || !l.Truncated {
		t.Errorf("%d entries: %d items, truncated %v", MaxListing+1, len(l.Items), l.Truncated)
	}
	if !strings.Contains(rec.Body.String(), `"truncated":true`) {
		t.Errorf("no truncated key")
	}
	if last := l.Items[len(l.Items)-1].Name; last != "f0998.json" {
		t.Errorf("last item %s, want f0998.json: sorted by name", last)
	}
}

// TestReadLogsRoute: the request record holds the route, never the path
// asked for or a file name.
func TestReadLogsRoute(t *testing.T) {
	f, _ := readFixture(t, readConfig)
	logs := captureLogs(f.srv)
	f.do(t, get("/leases/old/b.json", readSecret))
	f.do(t, get("/leases/by-id/20261124T170000.000000Z-abc123", readSecret))
	recs := requestRecords(t, logs)
	if len(recs) != 2 || recs[0]["route"] != "/leases" || recs[1]["route"] != "/leases/by-id/{id}" {
		t.Errorf("records %v", recs)
	}
	if s := logs.String(); strings.Contains(s, "b.json") || strings.Contains(s, "abc123") {
		t.Errorf("the log names the file: %s", s)
	}
}

// TestReadEmitUnchanged: an emit endpoint beside read endpoints still
// takes POST, and GET on it is a 405.
func TestReadEmitUnchanged(t *testing.T) {
	f, _ := readFixture(t, readConfig)
	if rec := f.do(t, req{path: "/notify", bearer: secret, body: "x"}); rec.Code != http.StatusCreated {
		t.Errorf("POST /notify: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(t, get("/notify", secret)); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Errorf("GET /notify: %d Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestContentType(t *testing.T) {
	if got := ContentType("a.YAML"); got != "text/plain; charset=utf-8" {
		t.Errorf("a.YAML: %q", got)
	}
}

// TestOpenAPIReadGolden: the OpenAPI document of every form of read
// endpoint equals testdata/openapi_read.golden.json: GET and HEAD, the
// {rest} form of a directory, the listing schema and the file types.
func TestOpenAPIReadGolden(t *testing.T) {
	f := newFixture(t, readConfig)
	raw, doc := getOpenAPI(t, f.srv)
	golden(t, "openapi_read.golden.json", raw)

	paths := doc["paths"].(map[string]any)
	for _, p := range []string{"/leases", "/leases/{rest}", "/leases/by-id/{id}", "/registry", "/registry/{rest}", "/health", "/health/{rest}"} {
		item, ok := paths[p].(map[string]any)
		if !ok || item["get"] == nil || item["head"] == nil || len(item) != 2 {
			t.Errorf("%s: %v, want get and head", p, item)
			continue
		}
		responses := field(t, item, "get.responses").(map[string]any)
		for _, code := range []string{"200", "304", "400", "401", "404", "429"} {
			if responses[code] == nil {
				t.Errorf("%s: no %s response", p, code)
			}
		}
		if field(t, item, "get.responses.200.content.application/octet-stream") == nil {
			t.Errorf("%s: no application/octet-stream", p)
		}
	}
	if paths["/leases/by-id/{id}/{rest}"] != nil {
		t.Error("an endpoint with path parameters has a {rest} form")
	}
	if field(t, doc, "components.schemas.Listing") == nil {
		t.Error("no Listing schema")
	}
}

// TestReadRangeNotSatisfiable: a Range outside the file is a 416, as JSON
// like every error.
func TestReadRangeNotSatisfiable(t *testing.T) {
	f, _ := readFixture(t, readConfig)
	r := get("/leases/a.json", readSecret)
	r.header = http.Header{"Range": {"bytes=100-200"}}
	rec := f.do(t, r)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status %d, want 416", rec.Code)
	}
	if msg := errorBody(t, rec); msg != "requested range not satisfiable" {
		t.Errorf("message %q", msg)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes */14" {
		t.Errorf("Content-Range %q", got)
	}
}
