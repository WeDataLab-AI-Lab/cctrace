//go:build !darwin && !linux

package usage

import (
	"context"
	"os"
	"os/exec"
)

func runRecoveryOwner(context.Context, *exec.Cmd, string, Credentials, recoveryDependencies) error {
	return ErrRecoveryBlocked
}

func recoveryDirectoryOwned(os.FileInfo) bool { return false }
