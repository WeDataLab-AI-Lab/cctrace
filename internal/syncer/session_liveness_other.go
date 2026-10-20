//go:build !darwin && !linux

package syncer

import "errors"

// processStart cannot tell here whether a process runs, so no live state is ever
// reported gone from its process file on this platform.
func processStart(int) (processStartInfo, error) {
	return processStartInfo{}, errors.ErrUnsupported
}

// localPIDDomain is empty: with no process to check here, no process file is in
// this client's domain. (Claude Code writes "win32:<hostname>" on Windows.)
func localPIDDomain() string { return "" }

// runsAnotherProgram cannot tell here.
func runsAnotherProgram(int) bool { return false }
