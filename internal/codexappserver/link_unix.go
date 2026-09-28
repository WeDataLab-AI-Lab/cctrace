//go:build !windows

package codexappserver

import (
	"os"
	"syscall"
)

// hardLinked reports a file that has another name somewhere else.
func hardLinked(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
