//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func reexecDaemonParent(executable string) error {
	return syscall.Exec(executable, daemonParentReexecArgs(executable), daemonParentReexecEnvironment())
}

func daemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func daemonNotifySignals(sigs chan os.Signal) {
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
}
