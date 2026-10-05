package main

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// TestSystemdUnit keeps deploy/decree-go-rest.service the unit of SPEC.md §11,
// with KillMode=mixed so that systemd does not signal the daemon before
// decree-go-rest's own shutdown order runs (SPEC.md §5).
func TestSystemdUnit(t *testing.T) {
	f, err := os.Open("../../deploy/decree-go-rest.service")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := map[string][]string{}
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "["):
			section = line
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				t.Errorf("%s: not key=value: %q", section, line)
				continue
			}
			got[section+k] = append(got[section+k], v)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"[Service]Restart":        "on-failure",
		"[Service]KillSignal":     "SIGTERM",
		"[Service]KillMode":       "mixed",
		"[Service]TimeoutStopSec": "40",
		"[Install]WantedBy":       "default.target",
	} {
		if v := got[key]; len(v) != 1 || v[0] != want {
			t.Errorf("%s = %q, want %q", key, v, want)
		}
	}
	for _, key := range []string{"[Service]EnvironmentFile", "[Service]ExecStart"} {
		if len(got[key]) != 1 {
			t.Errorf("%s: %q, want one", key, got[key])
		}
	}
	if exec := strings.Join(got["[Service]ExecStart"], ""); !strings.Contains(exec, "decree-go-rest -config ") {
		t.Errorf("ExecStart %q does not run decree-go-rest -config", exec)
	}
}
