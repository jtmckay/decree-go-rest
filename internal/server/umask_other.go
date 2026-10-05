//go:build !unix

package server

// Umask is the service's file mode mask (SPEC.md §4 step 6).
const Umask = 0o027

// SetUmask does nothing where there is no umask. It returns Umask.
func SetUmask() int {
	return Umask
}
