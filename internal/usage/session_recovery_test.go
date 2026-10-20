//go:build darwin || linux

package usage

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Per-instance dependencies never use real credentials or the installed CLI.
func TestOwnerStartupRecoveryIsolation(t *testing.T) {
	t.Parallel()
	root, home := recoveryTestRoot(t), recoveryTestRoot(t)
	ready := filepath.Join(root, "ready")
	inputMarker := filepath.Join(root, "input")
	d := recoveryDependencies{
		environment: []string{"HOME=" + root, "PATH=/usr/bin:/bin", "ANTHROPIC_API_KEY=secret", "CLAUDE_CODE_OAUTH_TOKEN=secret", "CLAUDE_CONFIG_DIR=/wrong"},
		now:         time.Now, interval: time.Millisecond, timeout: time.Second, grace: time.Millisecond,
		command: func(args []string) *exec.Cmd {
			joined := strings.Join(args, " ")
			for _, want := range []string{"--safe-mode", "--no-chrome", "--tools  --strict-mcp-config", "disableAllHooks", "remoteControlAtStartup", "--session-id"} {
				if !strings.Contains(joined, want) {
					t.Errorf("missing %s", want)
				}
			}
			return exec.Command("/bin/sh", "-c", `test -t 0 && test "$HOME" = "$USER_HOME" && test "$CLAUDE_CONFIG_DIR" = "$SELECTED" && test -z "$ANTHROPIC_API_KEY$CLAUDE_CODE_OAUTH_TOKEN" || exit 1
test "$DISABLE_AUTOUPDATER" = 1 || exit 1
printf ready > "$READY"
IFS= read -r input
printf unexpected-input > "$INPUT_MARKER"
`, "owner")
		},
	}
	// These extra keys are passed by the fake command seam, not the production allowlist.
	original := d.command
	var owned *exec.Cmd
	d.read = func(_ context.Context, h string) (Credentials, error) {
		if h != home {
			t.Error("wrong home read")
		}
		if _, e := os.Stat(ready); e == nil {
			return Credentials{AccessToken: "new", ExpiresAt: time.Now().Add(time.Hour), RefreshAvailable: true}, nil
		}
		return Credentials{AccessToken: "old", ExpiresAt: time.Unix(1, 0), RefreshAvailable: true}, nil
	}
	// Script values are literals generated from synthetic roots, never user input.
	d.command = func(args []string) *exec.Cmd {
		c := original(args)
		owned = c
		c.Args[2] = "USER_HOME='" + root + "'; SELECTED='" + home + "'; READY='" + ready + "'; INPUT_MARKER='" + inputMarker + "'; " + c.Args[2]
		return c
	}
	if err := recoverClaudeSession(context.Background(), home, root, d); err != nil {
		t.Fatal(err)
	}
	if _, e := os.Stat(inputMarker); !os.IsNotExist(e) {
		t.Fatal("owner received stdin")
	}
	if owned == nil || owned.ProcessState == nil || !errors.Is(owned.Process.Signal(syscall.Signal(0)), os.ErrProcessDone) {
		t.Fatal("owner not reaped")
	}
	entries, e := os.ReadDir(filepath.Join(root, "claude-session-recovery"))
	if e != nil || len(entries) != 1 {
		t.Fatalf("private directory: %v", e)
	}
	info, e := os.Lstat(filepath.Join(root, "claude-session-recovery", entries[0].Name()))
	if e != nil || info.Mode().Perm() != 0700 {
		t.Fatal("unsafe cwd")
	}
}

func TestOwnerRecoveryFailsClosed(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"valid", "unchanged", "prompt", "overflow", "cancel", "symlink", "unsupported", "cancel-started"} {
		t.Run(kind, func(t *testing.T) {
			root, home := recoveryTestRoot(t), recoveryTestRoot(t)
			calls := 0
			var owned *exec.Cmd
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := recoveryDependencies{environment: []string{"HOME=" + root, "PATH=/usr/bin:/bin"}, now: time.Now, interval: time.Millisecond, timeout: 30 * time.Millisecond, grace: time.Millisecond}
			if kind == "cancel-started" {
				// Cancel at successful PTY startup. Waiting for the first credential
				// tick races this test's unchanged 30ms timeout under suite load.
				d.ownerStarted = cancel
			}
			d.read = func(context.Context, string) (Credentials, error) {
				c := Credentials{AccessToken: "old", RefreshAvailable: true, ExpiresAt: time.Unix(1, 0)}
				if kind == "valid" {
					c.ExpiresAt = time.Now().Add(time.Hour)
				}
				return c, nil
			}
			d.command = func([]string) *exec.Cmd {
				calls++
				script := "IFS= read -r x"
				if kind == "prompt" {
					script = "printf 'Please log in'; " + script
				}
				if kind == "overflow" {
					script = "/usr/bin/head -c 65537 /dev/zero; " + script
				}
				if kind == "unsupported" {
					script = "printf 'unknown option --safe-mode'; " + script
				}
				owned = exec.Command("/bin/sh", "-c", script)
				return owned
			}
			if kind == "cancel" {
				cancel()
			}
			if kind == "symlink" {
				if e := os.Symlink(t.TempDir(), filepath.Join(root, "claude-session-recovery")); e != nil {
					t.Fatal(e)
				}
			}
			err := recoverClaudeSession(ctx, home, root, d)
			if kind == "valid" {
				if err != nil || calls != 0 {
					t.Fatalf("valid token launched owner: %v", err)
				}
				return
			}
			if owned != nil && (owned.ProcessState == nil || !errors.Is(owned.Process.Signal(syscall.Signal(0)), os.ErrProcessDone)) {
				t.Fatal("failed owner not reaped")
			}
			if kind == "cancel-started" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				return
			}
			if kind == "cancel" {
				if !errors.Is(err, context.Canceled) || calls != 0 {
					t.Fatalf("cancel: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrRecoveryBlocked) {
				t.Fatalf("%s: %v", kind, err)
			}
		})
	}
}

func recoveryTestRoot(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}
