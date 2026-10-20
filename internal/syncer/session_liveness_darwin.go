package syncer

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// processStart reports on the process with pid: whether there is one, and when
// it started. The kernel records the start time when the process forks, so a
// later step of the wall clock does not move it: a start time other than the one
// a file records is another process.
func processStart(pid int) (processStartInfo, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err == nil {
		at := time.Unix(k.Proc.P_starttime.Unix())
		return processStartInfo{exists: true, at: at, id: at.UnixMicro(), fixed: true}, nil
	}
	// For a pid with no process the kernel answers with nothing, which
	// SysctlKinfoProc reports as EIO. kill(pid, 0) tells that apart from failing
	// to read a process that exists, which must not read as gone.
	if errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
		return processStartInfo{}, nil
	}
	return processStartInfo{}, err
}

// localPIDDomain is the pidDomain Claude Code writes here: on darwin the whole
// machine is one process space.
func localPIDDomain() string { return "darwin" }

// runsAnotherProgram is never asked here: a start time other than a file's
// procStart is another process already.
func runsAnotherProgram(int) bool { return false }
