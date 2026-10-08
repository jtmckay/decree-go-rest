package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoOldName: no file outside .decree/migrations/ and .decree/runs/,
// which are history, names the project by its old name. The names are
// built here, so this file does not match itself.
func TestNoOldName(t *testing.T) {
	old := []string{"decree" + "-api", "DECREE" + "_API_"}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	skip := map[string]bool{
		".git":                                 true,
		filepath.Join(".decree", "migrations"): true,
		filepath.Join(".decree", "runs"):       true,
	}
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[rel] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		// A build of the binary in the checkout is ignored by .gitignore,
		// as rg ignores it.
		if rel == "decree-go-rest" {
			return nil
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // removed since it was listed, as a claimed message is
		}
		if err != nil {
			return err
		}
		checked++
		if strings.Contains(rel, old[0]) {
			t.Errorf("%s: the file name holds %s", rel, old[0])
		}
		for _, name := range old {
			if i := bytes.Index(data, []byte(name)); i >= 0 {
				line := 1 + bytes.Count(data[:i], []byte("\n"))
				t.Errorf("%s:%d: holds %s", rel, line, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 20 {
		t.Fatalf("checked only %d files under %s", checked, root)
	}
}

// TestAcceptanceBuiltBinaryChecksExample is the third acceptance
// criterion of 07: the binary `go build ./cmd/decree-go-rest` makes
// validates example/.decree/decree-go-rest.yml with -check, in a temp project with
// a stub decree.
func TestAcceptanceBuiltBinaryChecksExample(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH, so the binary cannot be built here")
	}
	bin := filepath.Join(t.TempDir(), "decree-go-rest")
	build := exec.Command(gobin, "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/decree-go-rest: %v\n%s", err, out)
	}
	path, stub := exampleProject(t)
	cmd := exec.Command(bin, "-check", "-config", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("decree-go-rest -check: %v\nstdout %q\nstderr %q", err, stdout.String(), stderr.String())
	}
	if got := stdout.String(); got != "config ok: 4 endpoints\n" {
		t.Errorf("stdout %q, want \"config ok: 4 endpoints\\n\"", got)
	}
	if calls := stub.Calls(t); len(calls) != 2 {
		t.Errorf("decree calls = %q, want --version and check", calls)
	}

	// The event endpoint's own secret is part of the check.
	t.Setenv("DECREE_GO_REST_APPROVE_SECRET", "too-short")
	cmd = exec.Command(bin, "-check", "-config", path)
	stderr.Reset()
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("-check passed with a short reply secret")
	}
	if !strings.Contains(stderr.String(), "DECREE_GO_REST_APPROVE_SECRET is 9 characters") {
		t.Errorf("stderr %q, want the short approve secret named", stderr.String())
	}
}

// TestServeRefusesMissingEventSecret: an event endpoint's missing secret
// is a startup error; nothing serves.
func TestServeRefusesMissingEventSecret(t *testing.T) {
	path, _ := exampleProject(t)
	os.Unsetenv("DECREE_GO_REST_APPROVE_SECRET")
	code, _, errOut := runCLI("-config", path)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr %q", code, errOut)
	}
	if !strings.Contains(errOut, "endpoint /approve/{wait_id}: secrets: DECREE_GO_REST_APPROVE_SECRET is not set") {
		t.Errorf("stderr %q, want the missing approve secret named", errOut)
	}
}
