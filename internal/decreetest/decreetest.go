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
	"time"
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
# record saves this call of $cmd in $cmd.<n>.*, NUL-separating argv so that an
# argument holding a newline survives, and lists <n> in $cmd.calls.
record() {
	n=$$
	printf '%s\n' "$PWD" > "$d/$cmd.$n.cwd"
	for a in "$@"; do printf '%s\0' "$a"; done > "$d/$cmd.$n.argv"
	env > "$d/$cmd.$n.env"
	umask > "$d/$cmd.$n.umask"
	cat > "$d/$cmd.$n.stdin"
	echo "$n" >> "$d/$cmd.calls"
}
# answer prints $1's answer, after its sleep if one is set: its stderr and
# exit code when that is not 0, else its stdout.
answer() {
	if [ -s "$d/$1.sleep" ]; then sleep "$(cat "$d/$1.sleep")"; fi
	code=$(cat "$d/$1.exit")
	if [ "$code" != 0 ]; then
		cat "$d/$1.stderr" >&2
		exit "$code"
	fi
	cat "$d/$1.json"
	exit 0
}
cmd=$1
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
status|event)
	record "$@"
	answer "$cmd"
	;;
emit)
	record "$@"
	if [ -s "$d/emit.sleep" ]; then sleep "$(cat "$d/emit.sleep")"; fi
	code=$(cat "$d/emit.exit")
	echo "emit-done $n" >> "$d/events.log"
	if [ "$code" != 0 ]; then
		cat "$d/emit.stderr" >&2
		exit "$code"
	fi
	if [ -s "$d/emit.json" ]; then cat "$d/emit.json"; exit 0; fi
	id=$(printf '20261005T043125Z-%06x' "$n")
	printf '{\n  "id": "%s",\n  "path": ".decree/inbox/%s.md"\n}\n' "$id" "$id"
	exit 0
	;;
daemon)
	if [ "$(cat "$d/daemon.term")" = ignore ]; then
		trap '' TERM
	else
		trap 'echo "term $$ $(date +%s.%N)" >> "$d/events.log"; exit 0' TERM
	fi
	echo "start $$ $(date +%s.%N)" >> "$d/events.log"
	echo "daemon $$ polling"
	echo "daemon $$ warning" >&2
	ticks=$(cat "$d/daemon.ticks")
	n=0
	while :; do
		if [ -e "$d/daemon.exit" ]; then
			code=$(cat "$d/daemon.exit")
			rm -f "$d/daemon.exit"
			echo "exit $$ $(date +%s.%N)" >> "$d/events.log"
			exit "$code"
		fi
		if [ -n "$ticks" ] && [ "$n" -ge "$ticks" ]; then
			echo "exit $$ $(date +%s.%N)" >> "$d/events.log"
			exit 3
		fi
		sleep 0.05
		n=$((n + 1))
	done
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
	s.SetStatus(t, 0, RunStatus, "")
	s.SetEvent(t, 0, EventReply, "")
	s.write(t, "daemon.term", "")
	s.write(t, "daemon.ticks", "")
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

// RunStatus is `decree status <id> --format json` for a run waiting in a
// person state, as decree 0.5 prints it; the stub prints it by default.
const RunStatus = `{
  "id": "20261005T043125Z-0a1b2c",
  "machine": "review",
  "status": "waiting",
  "state": "approval",
  "events": [
    {"event":"claimed","exit_code":null,"from":null,"machine":"review","run_id":"20261005T043125Z-0a1b2c","seq":1,"source":"claim","to":"approval","trigger":"inbox","ts":"2026-10-05T04:31:26.000Z","type":"transition","v":1}
  ]
}
`

// EventReply is `decree event --format json` for a queued reply, as decree
// 0.5 prints it; the stub prints it by default.
const EventReply = `{
  "id": "20261005T043200Z-3d4e5f",
  "path": ".decree/inbox/20261005T043200Z-3d4e5f.md"
}
`

// SetStatus sets what `decree status` prints on stdout and stderr, and its
// exit code. stdout is printed only on exit 0, stderr only otherwise.
func (s *Stub) SetStatus(t testing.TB, exit int, stdout, stderr string) {
	t.Helper()
	s.setAnswer(t, "status", exit, stdout, stderr)
}

// SetEvent sets what `decree event` prints on stdout and stderr, and its
// exit code. stdout is printed only on exit 0, stderr only otherwise.
func (s *Stub) SetEvent(t testing.TB, exit int, stdout, stderr string) {
	t.Helper()
	s.setAnswer(t, "event", exit, stdout, stderr)
}

func (s *Stub) setAnswer(t testing.TB, cmd string, exit int, stdout, stderr string) {
	t.Helper()
	s.write(t, cmd+".exit", strconv.Itoa(exit)+"\n")
	s.write(t, cmd+".json", stdout)
	s.write(t, cmd+".stderr", stderr)
	s.SetSleep(t, cmd, "")
}

// SetSleep makes `decree status` or `decree event` (cmd) sleep before it
// answers; d is an argument of sleep(1), such as "0.5" or "" for none.
func (s *Stub) SetSleep(t testing.TB, cmd, d string) {
	t.Helper()
	s.write(t, cmd+".sleep", d)
}

// Emit is one recorded `decree emit`, `decree status` or `decree event`.
type Emit = Invocation

// Invocation is one recorded call of a decree command.
type Invocation struct {
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
func (e Invocation) Getenv(name string) (string, bool) {
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
	return s.Invocations(t, "emit")
}

// Invocations returns every recorded call of cmd ("emit", "status" or
// "event"), in order.
func (s *Stub) Invocations(t testing.TB, cmd string) []Invocation {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.Dir, cmd+".calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []Invocation
	for _, n := range strings.Fields(string(raw)) {
		base := filepath.Join(s.Dir, cmd+"."+n+".")
		var args []string
		if a := s.read(t, base+"argv"); a != "" {
			args = strings.Split(strings.TrimSuffix(a, "\x00"), "\x00")
		}
		out = append(out, Invocation{
			Args:      args,
			Dir:       strings.TrimSuffix(s.read(t, base+"cwd"), "\n"),
			Env:       lines(s.read(t, base+"env")),
			Stdin:     []byte(s.read(t, base+"stdin")),
			StdinFile: base + "stdin",
			Umask:     strings.TrimSpace(s.read(t, base+"umask")),
		})
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

// SetDaemonLife makes each `decree daemon` exit with status 3 after
// about d; 0 means it runs until stopped.
func (s *Stub) SetDaemonLife(t testing.TB, d time.Duration) {
	t.Helper()
	ticks := ""
	if d > 0 {
		ticks = strconv.FormatInt(int64(d/(50*time.Millisecond)), 10)
	}
	s.write(t, "daemon.ticks", ticks)
}

// SetDaemonIgnoreTerm makes `decree daemon` ignore SIGTERM, so only
// SIGKILL stops it. Otherwise it records the SIGTERM and exits 0.
func (s *Stub) SetDaemonIgnoreTerm(t testing.TB, ignore bool) {
	t.Helper()
	v := ""
	if ignore {
		v = "ignore"
	}
	s.write(t, "daemon.term", v)
}

// ExitDaemon makes the running `decree daemon` exit with code, within
// about 50 ms.
func (s *Stub) ExitDaemon(t testing.TB, code int) {
	t.Helper()
	tmp := filepath.Join(s.Dir, "daemon.exit.tmp")
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(code)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(s.Dir, "daemon.exit")); err != nil {
		t.Fatal(err)
	}
}

// Event is one line of the stub's event log, which orders what the
// daemons and emits did: "start <pid> <time>" (a daemon is ready, its
// SIGTERM handling in place), "exit <pid> <time>",
// "term <pid> <time>" (a daemon got SIGTERM) and "emit-done <n>" (an emit
// is about to exit).
type Event struct {
	Kind string
	PID  int
	// Time is when it happened, for the daemon's events.
	Time time.Time
}

// Events returns the event log, in order.
func (s *Stub) Events(t testing.TB) []Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.Dir, "events.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, l := range lines(string(raw)) {
		f := strings.Fields(l)
		if len(f) < 2 {
			t.Fatalf("stub event %q", l)
		}
		e := Event{Kind: f[0]}
		e.PID, _ = strconv.Atoi(f[1])
		if len(f) > 2 {
			sec, err := strconv.ParseFloat(f[2], 64)
			if err != nil {
				t.Fatalf("stub event %q: %v", l, err)
			}
			e.Time = time.Unix(0, int64(sec*1e9))
		}
		out = append(out, e)
	}
	return out
}

// DaemonEvents returns the events of kind among Events.
func (s *Stub) DaemonEvents(t testing.TB, kind string) []Event {
	t.Helper()
	var out []Event
	for _, e := range s.Events(t) {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
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
