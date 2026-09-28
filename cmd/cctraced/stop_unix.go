//go:build !windows

package main

import (
	"os"
	"syscall"
)

func stopProcess(proc *os.Process) error {
	return proc.Signal(syscall.SIGTERM)
}
