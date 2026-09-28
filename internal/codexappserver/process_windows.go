//go:build windows

package codexappserver

import (
	"os"
	"os/exec"
)

// setProcessGroup is a no-op on Windows, which has no process groups in the
// POSIX sense to put the child into.
func setProcessGroup(cmd *exec.Cmd) {}

// killGroup ends the child itself. Windows offers no portable way from here to
// reach whatever the child may have started, so the guarantee is narrower than
// on unix: this process leaves no child behind, but a grandchild that detached
// is beyond its reach.
func killGroup(p *os.Process) {
	if p == nil {
		return
	}
	_ = p.Kill()
}
