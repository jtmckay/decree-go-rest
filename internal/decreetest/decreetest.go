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
emit)
	n=$$
	printf '%s\n' "$PWD" > "$d/emit.$n.cwd"
	for a in "$@"; do printf '%s\n' "$a"; done > "$d/emit.$n.argv"
	env > "$d/emit.$n.env"
	umask > "$d/emit.$n.umask"
	cat > "$d/emit.$n.stdin"
	echo "$n" >> "$d/emits.log"
	if [ -s "$d/emit.sleep" ]; then sleep "$(cat "$d/emit.sleep")"; fi
	code=$(cat "$d/emit.exit")
	if [ "$code" != 0 ]; then
		cat "$d/emit.stderr" >&2
		exit "$code"
	fi
	if [ -s "$d/emit.json" ]; then cat "$d/emit.json"; exit 0; fi
	id=$(printf '20261005T043125Z-%06x' "$n")
	printf '{\n  "id": "%s",\n  "path": ".decree/inbox/%s.md"\n}\n' "$id" "$id"
	exit 0
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
	s.SetEmit(t, 0, "")
	s.write(t, "emit.json", "")
	s.write(t, "emit.sleep", "")
	return s
}

// SetEmit sets the exit code of `decree emit` and, when it is not 0, what it
// prints on stderr. With exit 0 the stub prints a new id and path, as
// decree 0.5 does, unless SetEmitOutput replaced them.
func (s *Stub) SetEmit(t testing.TB, exit int, stderr string) {
	t.Helper()
	s.write(t, "emit.exit", strconv.Itoa(exit)+"\n")
	s.write(t, "emit.stderr", stderr)
}

// SetEmitOutput replaces what a successful `decree emit` prints on stdout.
func (s *Stub) SetEmitOutput(t testing.TB, out string) {
	t.Helper()
	s.write(t, "emit.json", out)
}

// SetEmitSleep makes `decree emit` sleep before it answers; d is an
// argument of sleep(1), such as "0.5" or "" for none.
func (s *Stub) SetEmitSleep(t testing.TB, d string) {
	t.Helper()
	s.write(t, "emit.sleep", d)
}

// Emit is one recorded `decree emit`.
type Emit struct {
	// Args are the arguments after the program name.
	Args []string
	// Dir is the working directory.
	Dir string
	// Env is the environment, one NAME=value per entry.
	Env []string
	// Stdin is everything read from stdin.
	Stdin []byte
	// StdinFile is the file the stub wrote stdin to, created under the
	// umask decree saw.
	StdinFile string
	// Umask is the output of umask(1), such as "0027".
	Umask string
}

// Getenv returns the value of name in the recorded environment.
func (e Emit) Getenv(name string) (string, bool) {
	for _, kv := range e.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

// Emits returns every `decree emit` the stub completed, in order.
func (s *Stub) Emits(t testing.TB) []Emit {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.Dir, "emits.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []Emit
	for _, n := range strings.Fields(string(raw)) {
		base := filepath.Join(s.Dir, "emit."+n+".")
		e := Emit{
			Args:      lines(s.read(t, base+"argv")),
			Dir:       strings.TrimSuffix(s.read(t, base+"cwd"), "\n"),
			Env:       lines(s.read(t, base+"env")),
			Stdin:     []byte(s.read(t, base+"stdin")),
			StdinFile: base + "stdin",
			Umask:     strings.TrimSpace(s.read(t, base+"umask")),
		}
		out = append(out, e)
	}
	return out
}

func (s *Stub) read(t testing.TB, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
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
