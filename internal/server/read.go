package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jtmckay/decree-go-rest/internal/config"
)

// MaxListing is the most entries a directory listing holds; a listing cut
// there says "truncated": true.
const MaxListing = 1000

// readFailed is the body of every 500 from a read endpoint; the reason is
// logged, without the file's name.
const readFailed = "could not read the state"

// listing is the JSON a read endpoint answers for a directory.
type listing struct {
	Path      string         `json:"path"`
	Items     []listingEntry `json:"items"`
	Truncated bool           `json:"truncated,omitempty"`
}

type listingEntry struct {
	Name string `json:"name"`
	// Type is "file" or "directory".
	Type string `json:"type"`
	// Size is a file's size in bytes; a directory has none.
	Size     *int64 `json:"size,omitempty"`
	Modified string `json:"modified"`
	// Href is the URL path that serves the entry, ending in / for a
	// directory. An endpoint with path parameters serves nothing below
	// its path, so its entries have none.
	Href string `json:"href,omitempty"`
}

// serveRead is SPEC.md §4 steps 6–7 for a read endpoint: it serves the
// file or directory the request names under the machine's state/ folder.
// Authentication, the budget and the parameters were checked already.
func (s *Server) serveRead(w http.ResponseWriter, r *http.Request, e *endpoint, values map[string]string) {
	rel := e.readPath
	if len(e.params) > 0 {
		rel = config.Substitute(rel, values)
	}
	// A parameter's pattern may allow what a segment must not hold, such
	// as a dot first or an encoded slash.
	if msg := checkSegments(rel, "a parameter"); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	var rest string
	if len(e.params) == 0 {
		// Set only by the <path>/{rest...} route: ServeHTTP strips a
		// trailing slash, so it is never empty there.
		rest = r.PathValue(config.RestParam)
	}
	if msg := checkSegments(rest, "the path below the endpoint"); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	root, err := filepath.EvalSymlinks(e.stateDir)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	target := filepath.Join(root, filepath.FromSlash(rel), filepath.FromSlash(rest))
	resolved, fi, ok := contained(root, target)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	switch {
	case fi.Mode().IsRegular():
		s.serveFile(w, r, e, resolved, fi)
	case fi.IsDir():
		u := r.URL.Path
		if len(e.params) == 0 {
			u = e.route
			if rest != "" {
				u += "/" + rest
			}
		}
		s.serveListing(w, r, e, resolved, root, u, len(e.params) == 0)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// checkSegments returns why a slash-separated path is not one a read
// endpoint serves, or "" when every segment is valid. "" itself is valid.
func checkSegments(p, what string) string {
	if p == "" {
		return ""
	}
	for _, seg := range strings.Split(p, "/") {
		if !config.ValidSegment(seg) {
			return what + " must be made of segments that match [A-Za-z0-9._-]+ and do not start with a dot"
		}
	}
	return ""
}

// contained resolves target's symlinks and reports whether the result is
// a regular file or a directory inside root, none of whose names below
// root starts with a dot. Anything else is answered as missing, so that
// a symlink reveals nothing outside root.
func contained(root, target string) (string, os.FileInfo, bool) {
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", nil, false
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", nil, false
	}
	if rel != "." {
		for _, seg := range strings.Split(rel, string(filepath.Separator)) {
			if strings.HasPrefix(seg, ".") {
				return "", nil, false
			}
		}
	}
	fi, err := os.Stat(resolved)
	if err != nil || !(fi.Mode().IsRegular() || fi.IsDir()) {
		return "", nil, false
	}
	return resolved, fi, true
}

// contentTypes are the Content-Types of files by extension; any other is
// application/octet-stream.
var contentTypes = map[string]string{
	".json": "application/json",
	".md":   "text/markdown; charset=utf-8",
	".txt":  "text/plain; charset=utf-8",
	".log":  "text/plain; charset=utf-8",
	".yml":  "text/plain; charset=utf-8",
	".yaml": "text/plain; charset=utf-8",
}

// ContentType is the Content-Type a read endpoint serves a file name with.
func ContentType(name string) string {
	if ct, ok := contentTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return ct
	}
	return "application/octet-stream"
}

// serveFile serves one regular file with http.ServeContent, which handles
// HEAD, Range and the conditional headers, under a strong ETag of its
// content's SHA-256.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, e *endpoint, path string, fi os.FileInfo) {
	f, err := os.Open(path)
	if err != nil {
		s.readError(w, e, err)
		return
	}
	defer f.Close()
	// What was opened must be what was checked: a regular file, not a
	// FIFO or device swapped in since.
	ofi, err := f.Stat()
	if err != nil {
		s.readError(w, e, err)
		return
	}
	if !ofi.Mode().IsRegular() || !os.SameFile(fi, ofi) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		s.readError(w, e, err)
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		s.readError(w, e, err)
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Type", ContentType(fi.Name()))
	hdr.Set("ETag", `"`+hex.EncodeToString(h.Sum(nil))+`"`)
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(&jsonErrorWriter{ResponseWriter: w}, r, "", ofi.ModTime(), f)
}

// jsonErrorWriter turns the plain-text errors http.ServeContent writes,
// such as a 416, into the JSON every error response is.
type jsonErrorWriter struct {
	http.ResponseWriter
	failed bool
}

func (w *jsonErrorWriter) WriteHeader(code int) {
	if code < http.StatusBadRequest {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.failed = true
	w.Header().Del("Content-Length")
	writeError(w.ResponseWriter, code, strings.ToLower(http.StatusText(code)))
}

func (w *jsonErrorWriter) Write(b []byte) (int, error) {
	if w.failed {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

// serveListing answers a directory with its listing: the entries a read
// endpoint would serve, sorted by name, at most MaxListing. href says
// whether entries get an href, below dirURL.
func (s *Server) serveListing(w http.ResponseWriter, r *http.Request, e *endpoint, dir, root, dirURL string, href bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.readError(w, e, err)
		return
	}
	out := listing{Path: dirURL, Items: []listingEntry{}}
	for _, de := range entries {
		name := de.Name()
		if !config.ValidSegment(name) {
			// A dotfile is never listed, and a name no request can
			// spell is not served.
			continue
		}
		fi, ok := listable(root, filepath.Join(dir, name), de)
		if !ok {
			continue
		}
		if len(out.Items) == MaxListing {
			out.Truncated = true
			break
		}
		item := listingEntry{Name: name, Type: "file", Modified: fi.ModTime().UTC().Format(time.RFC3339)}
		if fi.IsDir() {
			item.Type = "directory"
		} else {
			size := fi.Size()
			item.Size = &size
		}
		if href {
			item.Href = dirURL + "/" + name
			if fi.IsDir() {
				item.Href += "/"
			}
		}
		out.Items = append(out.Items, item)
	}
	body, err := json.Marshal(out)
	if err != nil {
		s.readError(w, e, err)
		return
	}
	body = append(body, '\n')
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	hdr := w.Header()
	hdr.Set("ETag", etag)
	hdr.Set("Cache-Control", "no-cache")
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	hdr.Set("Content-Type", "application/json")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

// listable returns the info of a directory entry a read endpoint would
// serve: a regular file or directory, or a symlink to one inside root.
func listable(root, path string, de fs.DirEntry) (os.FileInfo, bool) {
	switch t := de.Type(); {
	case t.IsRegular(), t.IsDir():
		fi, err := de.Info()
		return fi, err == nil
	case t&fs.ModeSymlink != 0:
		_, fi, ok := contained(root, path)
		return fi, ok
	}
	return nil, false
}

// etagMatch is the weak comparison of If-None-Match (RFC 9110 §13.1.2):
// whether the header lists etag, or is *.
func etagMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || strings.TrimPrefix(t, "W/") == etag {
			return true
		}
	}
	return false
}

// readError answers an I/O failure inside the state folder with a 500. It
// logs the route and the failure, never the file's name.
func (s *Server) readError(w http.ResponseWriter, e *endpoint, err error) {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	s.Logger.Error("read failed", "route", e.route, "reason", err.Error())
	writeError(w, http.StatusInternalServerError, readFailed)
}
