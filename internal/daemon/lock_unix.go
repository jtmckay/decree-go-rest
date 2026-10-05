//go:build unix

package daemon

import (
	"errors"
	"os"
	"syscall"
)

// flock takes an exclusive flock(2) on f without waiting.
func flock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return ErrLocked
		default:
			return err
		}
	}
}
