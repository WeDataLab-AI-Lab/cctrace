//go:build darwin || linux

package usage

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
)

func runRecoveryOwner(parent context.Context, cmd *exec.Cmd, home string, before Credentials, d recoveryDependencies) error {
	ctx, cancel := context.WithTimeout(parent, d.timeout)
	defer cancel()
	terminal, err := pty.Start(cmd)
	if err != nil {
		return ErrRecoveryBlocked
	}
	// Start creates a new session/process group. Never write to the terminal.
	output := make(chan []byte, 8)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		defer close(output)
		for {
			b := make([]byte, 4096)
			n, e := terminal.Read(b)
			if n > 0 {
				select {
				case output <- b[:n]:
				case <-ctx.Done():
					return
				}
			}
			if e != nil {
				return
			}
		}
	}()
	defer func() {
		// Keep the leader unreaped until all group signals have been sent, preventing
		// a recycled PID from identifying an unrelated process group.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(d.grace)
		<-timer.C
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cancel()
		_ = terminal.Close()
		_ = cmd.Wait()
		<-stopped
	}()
	if d.ownerStarted != nil {
		d.ownerStarted()
	}
	tick := time.NewTicker(d.interval)
	defer tick.Stop()
	total := 0
	scan := ""
	for {
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return parent.Err()
			}
			return ErrRecoveryBlocked
		case b, ok := <-output:
			if !ok {
				return ErrRecoveryBlocked
			}
			total += len(b)
			if total > 64<<10 {
				return ErrRecoveryBlocked
			}
			scan += string(b)
			if len(scan) > 8192 {
				scan = scan[len(scan)-8192:]
			}
			if recoveryPrompt(scan) {
				return ErrRecoveryBlocked
			}
		case <-tick.C:
			if !recoveryHomeCurrent(home) {
				return ErrRecoveryBlocked
			}
			after, e := d.read(ctx, home)
			if e != nil {
				if ctx.Err() != nil {
					continue
				}
				return ErrCredentialAccessRequired
			}
			if after.AccessToken != "" && after.AccessToken != before.AccessToken && !after.ExpiresAt.IsZero() && after.ExpiresAt.After(d.now()) {
				return nil
			}
		}
	}
}

func recoveryDirectoryOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
