package usage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

const contextCredentialLimit = 64 << 10

var contextSecurityCommand = func(ctx context.Context, service string) *exec.Cmd {
	return exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", service, "-w")
}

type credentialOutput struct{ bytes.Buffer }

func (b *credentialOutput) Write(p []byte) (int, error) {
	if len(p) > contextCredentialLimit-b.Len() {
		return 0, errors.New("credential output exceeds size limit")
	}
	return b.Buffer.Write(p)
}

func credentialsForContext(ctx context.Context, claudeDir string) (Credentials, error) {
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	if !filepath.IsAbs(claudeDir) {
		return Credentials{}, errors.New("explicit absolute Claude home required")
	}
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir() // Service-name comparison only; never a read fallback.
		if err != nil {
			return Credentials{}, ErrCredentialAccessRequired
		}
		cmd := contextSecurityCommand(ctx, keychainService(claudeDir, home))
		var output credentialOutput
		cmd.Stdout, cmd.Stderr = &output, io.Discard
		cmd.WaitDelay = 100 * time.Millisecond
		err = cmd.Run() // Wait reaps this command on success, failure, or cancellation.
		if ctx.Err() != nil {
			return Credentials{}, ctx.Err()
		}
		if err == nil {
			return parseCredentials(bytes.TrimSpace(output.Bytes()), "selected provider credentials")
		}
		if output.Len() > 0 {
			return Credentials{}, ErrCredentialAccessRequired
		}
	}
	// Only a failed, non-canceled lookup may fall back to the selected home's
	// regular file. No default-home or other-account credential fallback.
	path := filepath.Join(claudeDir, ".credentials.json")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > contextCredentialLimit {
		return Credentials{}, ErrCredentialAccessRequired
	}
	file, err := os.Open(path)
	if err != nil {
		return Credentials{}, ErrCredentialAccessRequired
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return Credentials{}, ErrCredentialAccessRequired
	}
	data, err := io.ReadAll(io.LimitReader(file, contextCredentialLimit+1))
	if ctx.Err() != nil {
		return Credentials{}, ctx.Err()
	}
	if err != nil || len(data) > contextCredentialLimit {
		return Credentials{}, ErrCredentialAccessRequired
	}
	return parseCredentials(data, "selected provider credentials")
}
