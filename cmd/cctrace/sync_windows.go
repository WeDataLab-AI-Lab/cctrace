//go:build windows

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func reexecDaemonParent(executable string) error {
	proc, err := startProcessFn(executable, daemonParentReexecArgs(executable), &os.ProcAttr{
		Env:   daemonParentReexecEnvironment(),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Sys:   daemonSysProcAttr(),
	})
	if err != nil {
		return fmt.Errorf("start replacement parent: %w", err)
	}
	// StartProcess succeeded, so the replacement now owns the handoff. Falling
	// back to an old-code spawn because Release failed would start both paths.
	_ = releaseProcessFn(proc)
	return nil
}

func daemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

func daemonNotifySignals(sigs chan os.Signal) {
	signal.Notify(sigs, os.Interrupt)
}
