package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LockName is the lock file in the project's .decree directory, the only
// file decree-go-rest creates there (SPEC.md §2, §5).
const LockName = "decree-go-rest.lock"

// ErrLocked is returned by Lock when another decree-go-rest holds the lock.
var ErrLocked = errors.New("another decree-go-rest is running for this project")

// LockFile is the lock file of a project.
func LockFile(projectDir string) string {
	return filepath.Join(projectDir, ".decree", LockName)
}

// Lock is a held single-instance lock.
type Lock struct {
	f *os.File
}

// Path is the lock file.
func (l *Lock) Path() string { return l.f.Name() }

// Unlock releases the lock. The file stays: removing it would let a third
// instance lock a new file while a second one holds the old.
func (l *Lock) Unlock() error {
	return l.f.Close()
}

// Acquire takes the exclusive, non-blocking lock of a project (SPEC.md §5,
// One daemon only). The error names the lock file; it wraps ErrLocked when
// another process holds it.
func Acquire(projectDir string) (*Lock, error) {
	path := LockFile(projectDir)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if err := flock(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}
