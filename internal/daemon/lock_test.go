//go:build unix

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".decree"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestLockRefusesSecondInstance: a second lock of the same project fails
// with an error naming the lock file, and succeeds once the first is
// released. flock locks belong to the open file, so two opens in one
// process conflict as two processes do.
func TestLockRefusesSecondInstance(t *testing.T) {
	dir := project(t)
	first, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".decree", "decree-go-rest.lock")
	if first.Path() != want {
		t.Errorf("lock file %s, want %s", first.Path(), want)
	}
	_, err = Acquire(dir)
	if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), want) {
		t.Fatalf("second lock: %v, want ErrLocked naming %s", err, want)
	}
	if err := first.Unlock(); err != nil {
		t.Fatal(err)
	}
	again, err := Acquire(dir)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	again.Unlock()
}

func TestLockOtherProjects(t *testing.T) {
	a, err := Acquire(project(t))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Unlock()
	b, err := Acquire(project(t))
	if err != nil {
		t.Fatalf("a second project: %v", err)
	}
	b.Unlock()
}

func TestLockNoDecreeDir(t *testing.T) {
	dir := t.TempDir()
	_, err := Acquire(dir)
	if err == nil || errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), LockFile(dir)) {
		t.Errorf("no .decree: %v, want an error naming the lock", err)
	}
}
