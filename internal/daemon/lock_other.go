//go:build !unix

package daemon

import (
	"errors"
	"os"
)

// flock is unavailable without flock(2): decree-go-rest refuses to run rather
// than risk two daemons for one project.
func flock(*os.File) error {
	return errors.New("the single-instance lock needs flock(2), which this system lacks")
}
