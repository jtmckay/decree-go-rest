// Package decreetest provides a stub `decree` executable for tests
// (SPEC.md §12). The stub is a shell script in a temp directory. It records
// every call and answers from files that a test can change at any time.
package decreetest

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ValidCheck is `decree check --format json` for a valid project.
const ValidCheck = `{"errors":[],"valid":true,"warnings":[]}`

// InvalidCheck is `decree check --format json` for a project with one
// error, as decree 0.5 prints it.
const InvalidCheck = `{"errors":[{"file":"machines/bad.yml","line":1,"message":"missing field ` + "`description`" + `","rule":null}],"valid":false,"warnings":[]}`

// Stub is a stub decree in Dir.
type Stub struct {
	// Dir holds the script, its answers and its records.
	Dir string
	// Path is the stub executable, named decree.
	Path string
}

const script = `#!/bin/sh
d=$(dirname "$0")
{ printf 'cwd=%s' "$PWD"; for a in "$@"; do printf ' %s' "$a"; done; printf '\n'; } >> "$d/calls.log"
case "$1" in
--version|-v)
	cat "$d/version"
	exit 0
	;;
check)
	cat "$d/check.json"
	code=$(cat "$d/check.exit")
	if [ "$code" != 0 ]; then echo "error: decree check found errors" >&2; fi
	exit "$code"
	;;
esac
echo "error: the stub does not implement $1" >&2
exit 2
`

// New writes a stub decree that reports version 0.5.0 and a valid project.
func New(t testing.TB) *Stub {
	t.Helper()
	dir := t.TempDir()
	s := &Stub{Dir: dir, Path: filepath.Join(dir, "decree")}
	if err := os.WriteFile(s.Path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s.SetVersion(t, "decree 0.5.0")
	s.SetCheck(t, ValidCheck, 0)
	return s
}

// SetVersion sets what `decree --version` prints.
func (s *Stub) SetVersion(t testing.TB, out string) {
	t.Helper()
	s.write(t, "version", out+"\n")
}

// SetCheck sets what `decree check` prints on stdout and its exit code.
func (s *Stub) SetCheck(t testing.TB, out string, exit int) {
	t.Helper()
	s.write(t, "check.json", out+"\n")
	s.write(t, "check.exit", strconv.Itoa(exit)+"\n")
}

// Calls returns one line per call: "cwd=<dir> <args...>".
func (s *Stub) Calls(t testing.TB) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.Dir, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// OnPath puts the stub first on PATH for the rest of the test.
func (s *Stub) OnPath(t testing.TB) {
	t.Helper()
	t.Setenv("PATH", s.Dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (s *Stub) write(t testing.TB, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.Dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
