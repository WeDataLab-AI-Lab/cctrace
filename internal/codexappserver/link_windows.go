//go:build windows

package codexappserver

import "os"

// hardLinked is not detected on Windows: FileInfo carries no link count.
func hardLinked(os.FileInfo) bool { return false }
