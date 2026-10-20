//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd && !dragonfly && !solaris && !windows

package processio

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
)

func CheckPTYSupport(stdin, stdout, stderr *os.File) error {
	return errors.New("PTY process I/O is unsupported on this operating system")
}

func configurePipesCommand(command *exec.Cmd) error { return nil }

func forwardInterrupt(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Signal(os.Interrupt)
}

func processExitCode(command *exec.Cmd, waitErr error) int {
	if command == nil || command.ProcessState == nil {
		if waitErr != nil {
			return 1
		}
		return 0
	}
	return command.ProcessState.ExitCode()
}

func runPTY(context.Context, RelayOptions) (RelayResult, error) {
	return RelayResult{}, errors.New("PTY process I/O is unsupported on this operating system")
}

func checkOpenFileMappingSupport() error {
	return errors.New("safe Codex session mapping is unsupported on this operating system")
}

func processStartToken(int) (string, error) {
	return "", errors.New("process start inspection is unsupported")
}

func matchesClaudeProcStart(int, string, string) bool { return false }

func claudeProcStartForPID(int) (string, error) {
	return "", errors.New("process start inspection is unsupported")
}

func processOpenFiles(int) ([]string, error) {
	return nil, errors.New("process open-file inspection is unsupported")
}

func relayFileInput(source *os.File, destination io.WriteCloser, counter *Counter, stop <-chan struct{}) error {
	return relayInput(source, destination, counter)
}

func relayPipesFileInput(source *os.File, destination io.WriteCloser, counter *Counter, stop <-chan struct{}) error {
	return relayInput(source, destination, counter)
}
