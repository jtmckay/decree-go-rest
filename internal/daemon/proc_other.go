//go:build !unix

package daemon

import "os/exec"

func setProcessGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
