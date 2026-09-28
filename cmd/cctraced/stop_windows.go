//go:build windows

package main

import "os"

func stopProcess(proc *os.Process) error {
	return proc.Kill()
}
