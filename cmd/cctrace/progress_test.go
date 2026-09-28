package main

import (
	"io"
	"log"
	"os"
	"strings"
	"testing"
)

// useProgressTTY forces the terminal/non-terminal rendering for one test and
// restores the log package, so a failure cannot leak a redirected logger into
// unrelated tests.
func useProgressTTY(t *testing.T, isTTY bool) {
	t.Helper()
	prevTTY, prevOut, prevInstalled := progressIsTTY, log.Writer(), daemonLogInstalled
	progressIsTTY = isTTY
	t.Cleanup(func() {
		progressIsTTY, daemonLogInstalled = prevTTY, prevInstalled
		log.SetOutput(prevOut)
	})
}

// captureOutput swaps os.Stdout and os.Stderr for pipes and returns what each
// received. The helpers resolve those variables at call time, so substituting
// them observes exactly what a caller's shell would.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prevOut, prevErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = prevOut, prevErr }()

	fn()

	outW.Close()
	errW.Close()
	o, _ := io.ReadAll(outR)
	e, _ := io.ReadAll(errR)
	return string(o), string(e)
}

// A foreground run keeps its channels. Piping or redirecting stdout does not
// make a run a daemon, and moving results off stdout would break every caller
// that reads them — the reason this is keyed on the spawn flag, not on isatty.
func TestForegroundNonTTYKeepsChannels(t *testing.T) {
	useProgressTTY(t, false)
	daemonLogInstalled = false

	stdout, stderr := captureOutput(t, func() {
		infof("Synced %d records", 7)
		diagf("[codex-sync] %v", "load state failed")
	})

	if !strings.Contains(stdout, "Synced 7 records") {
		t.Fatalf("result line left stdout, got stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(stderr, "load state failed") {
		t.Fatalf("diagnostic left stderr, got stderr=%q", stderr)
	}
	if strings.Contains(stdout, "load state failed") {
		t.Fatalf("diagnostic contaminated stdout: %q", stdout)
	}
}

// Off a terminal the progress line must be a plain line: the carriage-return
// repaint is what made redirected logs unreadable.
func TestForegroundNonTTYWritesPlainLines(t *testing.T) {
	useProgressTTY(t, false)
	daemonLogInstalled = false

	stdout, _ := captureOutput(t, func() {
		progressf("[sync] %d records synced", 3)
		progressf("[sync] stopped (%d records total)\n", 3)
	})

	if strings.ContainsAny(stdout, "\r\033") {
		t.Fatalf("terminal control characters reached a non-terminal stream: %q", stdout)
	}
	if got := strings.Count(strings.TrimRight(stdout, "\n"), "\n"); got != 1 {
		t.Fatalf("expected two complete lines, got %q", stdout)
	}
}

// In a terminal the in-place repaint is preserved.
func TestTTYKeepsInPlaceRepaint(t *testing.T) {
	useProgressTTY(t, true)
	daemonLogInstalled = false

	stdout, _ := captureOutput(t, func() {
		progressf("[sync] %d records synced", 3)
	})

	if !strings.HasPrefix(stdout, "\r\033[K") {
		t.Fatalf("terminal repaint lost: %q", stdout)
	}
}

// A spawned child's stdout and stderr are the crash file, so everything it
// reports has to reach sync.log through the log package instead.
func TestInstallDaemonLogWritesToSyncLog(t *testing.T) {
	useTempSyncHome(t)
	useProgressTTY(t, false)

	closeLog := installDaemonLog("")
	progressf("[sync] %d records synced", 7)
	diagf("[codex-sync] %v", "load state failed")
	infof("Synced %d records", 7)
	closeLog()

	data, err := os.ReadFile(syncLogPath(""))
	if err != nil {
		t.Fatalf("read sync log: %v", err)
	}
	for _, want := range []string{"7 records synced", "load state failed", "Synced 7 records"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("sync log missing %q, got:\n%s", want, data)
		}
	}
	if strings.ContainsAny(string(data), "\r\033") {
		t.Fatalf("terminal control characters reached the log file:\n%q", data)
	}
}

// Repeats must be folded on the way to the file, or a retry loop refills the log
// as fast as rotation empties it.
func TestInstallDaemonLogFoldsRepeats(t *testing.T) {
	useTempSyncHome(t)
	useProgressTTY(t, false)

	closeLog := installDaemonLog("")
	for i := 0; i < 20; i++ {
		progressf("[quota] fetch failed: %v", "rate limited")
	}
	closeLog()

	data, err := os.ReadFile(syncLogPath(""))
	if err != nil {
		t.Fatalf("read sync log: %v", err)
	}
	if got := strings.Count(string(data), "rate limited"); got != 2 {
		// One occurrence plus the shutdown summary, which quotes the message.
		t.Fatalf("message appears %d times, want 2 (first write + summary):\n%s", got, data)
	}
	if !strings.Contains(string(data), "repeated 19 times") {
		t.Fatalf("suppressed count not reported:\n%s", data)
	}
}

// Routing survives a terminal being attached: the child is identified by having
// been spawned, not by isatty, and a spawned child never has a terminal anyway.
func TestInstallDaemonLogIgnoresTTY(t *testing.T) {
	useTempSyncHome(t)
	useProgressTTY(t, true)

	closeLog := installDaemonLog("")
	stdout, _ := captureOutput(t, func() { infof("Synced %d records", 1) })
	closeLog()

	if stdout != "" {
		t.Fatalf("output escaped to stdout: %q", stdout)
	}
	data, err := os.ReadFile(syncLogPath(""))
	if err != nil {
		t.Fatalf("read sync log: %v", err)
	}
	if !strings.Contains(string(data), "Synced 1 records") {
		t.Fatalf("sync log missing the message:\n%s", data)
	}
}
