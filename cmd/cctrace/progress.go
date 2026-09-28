package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"cctrace/internal/synclog"

	"golang.org/x/term"
)

// progressIsTTY decides whether the progress line may repaint itself in place.
//
// The repaint uses a carriage return, which is right in a terminal and wrong
// everywhere else: redirected to a file, every update appended another fragment
// to the same physical line. One reported log was 305MB spread over 237 lines,
// the longest single line 32MB — which defeats the `tail -n 100` the
// troubleshooting docs tell users to run.
//
// A var, not a const, so tests can exercise both renderings.
var progressIsTTY = term.IsTerminal(int(os.Stdout.Fd()))

// daemonLogInstalled reports whether output is being routed at the profile's log
// file rather than at this process's own streams.
//
// This is deliberately not "is stdout a terminal". Only a spawned child has its
// stdout and stderr pointed at the crash file, and only that child must divert
// its output; a foreground run with redirected or piped stdout is still a
// foreground run, and moving its output to another file — or another stream —
// would break callers that read it.
var daemonLogInstalled bool

// progressf reports transient status that supersedes the previous line.
func progressf(format string, args ...any) {
	switch {
	case daemonLogInstalled:
		log.Printf(format, args...)
	case progressIsTTY:
		fmt.Printf("\r\033[K  "+format, args...)
	default:
		fmt.Print(plainLine(format, args...))
	}
}

// infof reports a normal result on stdout, the channel scripts read.
func infof(format string, args ...any) {
	if daemonLogInstalled {
		log.Printf(format, args...)
		return
	}
	fmt.Print(plainLine(format, args...))
}

// diagf reports a diagnostic on stderr, keeping it out of the data channel.
func diagf(format string, args ...any) {
	if daemonLogInstalled {
		log.Printf(format, args...)
		return
	}
	fmt.Fprint(os.Stderr, plainLine(format, args...))
}

// diagWriter is diagf for callees that take an io.Writer.
func diagWriter() io.Writer {
	if daemonLogInstalled {
		return log.Writer()
	}
	return os.Stderr
}

// plainLine renders one complete line. Some callers carry a trailing newline in
// their format because the terminal path needs it; normalise so the file and
// non-terminal paths never emit a blank line.
func plainLine(format string, args ...any) string {
	return "  " + strings.TrimRight(fmt.Sprintf(format, args...), "\n") + "\n"
}

// installDaemonLog routes the log package at a size-bounded, repeat-folding
// writer over the profile's log file, and returns a function that flushes it.
//
// Callers gate this on --log-to-file, which spawnSyncProcess sets on every child
// it starts — the same place that points the child's stdout and stderr at the
// crash file. Tying both to one decision is what keeps ordinary diagnostics out
// of the crash log: contaminating it would also mean routine output triggers its
// spawn-time truncation, destroying the evidence it exists to hold.
//
// Everything the syncers emit goes through log.Printf, so redirecting the log
// package covers the great majority of what the file receives — measured at 97%
// of bytes in a 54MB sample.
func installDaemonLog(profileName string) func() {
	w := synclog.NewDeduper(synclog.NewDefault(syncLogPath(profileName)))
	log.SetOutput(w)
	daemonLogInstalled = true
	return func() {
		// Stop routing before closing: a log call racing the close would
		// otherwise write to a closed file.
		daemonLogInstalled = false
		log.SetOutput(os.Stderr)
		_ = w.Close()
	}
}
