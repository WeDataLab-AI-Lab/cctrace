//go:build !windows

package codexappserver

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in its own process group.
//
// Without it a signal aimed at the child would reach only the child, and any
// process it started would be left running with no parent that knows about it.
// With it the group is a handle on the whole subtree, so one kill ends all of
// it. It also detaches the child from this process's group, so a terminal
// signal to the daemon does not race the teardown below for the same child.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup sends SIGKILL to the child's whole process group.
//
// Setpgid makes the child its own group leader, so the group id is its pid and
// -pid addresses every member. Errors are ignored: the only ones reachable here
// say the group is already gone, which is the outcome being asked for.
//
// This must be called before the child is reaped. A process group is named
// after its leader, and once the leader has been waited for, that number is
// free for the kernel to hand to an unrelated process.
func killGroup(p *os.Process) {
	if p == nil {
		return
	}
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
}
