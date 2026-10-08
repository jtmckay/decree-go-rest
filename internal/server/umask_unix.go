//go:build unix

package server

import "syscall"

// Umask is the service's file mode mask (SPEC.md §4 step 6): messages may
// carry secrets, so they are not world-readable.
const Umask = 0o027

// SetUmask sets the process umask to Umask, so every decree it runs
// inherits it. It returns the previous mask.
func SetUmask() int {
	return syscall.Umask(Umask)
}
