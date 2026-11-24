//go:build !unix

package server

import "testing"

// makeFIFO is a no-op where there are no named pipes.
func makeFIFO(t *testing.T, path string) {
	t.Helper()
	write(t, path+".skip", "")
	t.Log("no named pipes on this platform")
}
