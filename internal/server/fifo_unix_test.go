//go:build unix

package server

import (
	"syscall"
	"testing"
)

// makeFIFO makes a named pipe at path.
func makeFIFO(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
}
