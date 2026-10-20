//go:build windows

package processio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

func TestWindowsConPTYAgentFixtureProcess(t *testing.T) {
	if os.Getenv("CCTRACE_WINDOWS_CONPTY_AGENT") != "1" {
		return
	}
	home := os.Getenv("CODEX_HOME")
	sessionID := os.Getenv("CCTRACE_WINDOWS_CONPTY_SESSION_ID")
	rollout := filepath.Join(home, "sessions", "2026", "10", "09", "rollout-2026-10-09T12-00-00-"+sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0700); err != nil {
		os.Exit(80)
	}
	file, err := os.OpenFile(rollout, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0600)
	if err != nil {
		os.Exit(81)
	}
	meta := map[string]interface{}{"type": "session_meta", "payload": map[string]string{"id": sessionID, "session_id": sessionID, "cwd": `C:\synthetic\project`}}
	if err := json.NewEncoder(file).Encode(meta); err != nil {
		os.Exit(82)
	}
	if err := file.Sync(); err != nil {
		os.Exit(83)
	}
	defer file.Close()
	if _, err := fmt.Fprintln(os.Stdout, "READY"); err != nil {
		os.Exit(84)
	}
	if os.Getenv("CCTRACE_WINDOWS_CONPTY_MODE") == "interrupt" {
		interrupt := make(chan os.Signal, 1)
		// The ConPTY relay forwards Ctrl+C as a console control event/ETX.
		signal.Notify(interrupt, os.Interrupt)
		defer signal.Stop(interrupt)
		<-interrupt
		_, _ = fmt.Fprintln(os.Stdout, "INTERRUPTED")
		os.Exit(130)
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := readWindowsConPTYLine(reader)
		if err != nil {
			os.Exit(91)
		}
		switch strings.TrimSpace(line) {
		case "":
			continue
		case "size":
			width, height, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil {
				os.Exit(92)
			}
			_, _ = fmt.Fprintf(os.Stdout, "SIZE %d %d\n", width, height)
		case "exit":
			_, _ = fmt.Fprintln(os.Stdout, "EXITING")
			os.Exit(23)
		}
	}
}

func TestWindowsConPTYRelayHelperProcess(t *testing.T) {
	if os.Getenv("CCTRACE_WINDOWS_CONPTY_HELPER") != "1" {
		return
	}
	home := os.Getenv("CODEX_HOME")
	sessionID := os.Getenv("CCTRACE_WINDOWS_CONPTY_SESSION_ID")
	marker := os.Getenv("CCTRACE_WINDOWS_CONPTY_PID_MARKER")
	mode := os.Getenv("CCTRACE_WINDOWS_CONPTY_MODE")
	child := exec.Command(os.Args[0], "-test.run=^TestWindowsConPTYAgentFixtureProcess$")
	child.Env = replaceWindowsTestEnv(os.Environ(), map[string]string{
		"CCTRACE_WINDOWS_CONPTY_AGENT":      "1",
		"CCTRACE_WINDOWS_CONPTY_SESSION_ID": sessionID,
		"CCTRACE_WINDOWS_CONPTY_MODE":       mode,
		"CODEX_HOME":                        home,
	})
	result, runErr := Run(context.Background(), RelayOptions{
		Mode: ModePTY, Command: child, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		ReportInterval: time.Hour,
		OnStarted: func(pid int) {
			_ = os.WriteFile(marker, []byte(strconv.Itoa(pid)), 0600)
		},
	})
	failure := result.FailureCode
	if failure == "" {
		failure = "none"
	}
	_, _ = fmt.Fprintf(os.Stdout, "SUMMARY pid=%d input_bytes=%d input_lines=%d output_bytes=%d output_lines=%d exit=%d partial=%t failure=%s\n",
		result.PID, result.Counts.InputBytes, result.Counts.InputLines, result.Counts.OutputBytes,
		result.Counts.OutputLines, result.ExitCode, result.Partial, failure)
	if result.ExitCode != 0 {
		os.Exit(result.ExitCode)
	}
	if runErr != nil {
		os.Exit(93)
	}
}

func TestWindowsConPTYRelayInteractiveResizeExitAndRestart(t *testing.T) {
	if !conpty.IsConPtyAvailable() {
		t.Skip("Windows ConPTY is unavailable on this runner")
	}
	firstID := "019db82c-66c5-7160-a6a7-b76dc2dd72d1"
	first := startWindowsConPTYHelper(t, firstID, "interactive")
	first.reader.waitFor(t, "READY")
	firstMapping := first.resolveMapping(t)
	if firstMapping.PID != first.pid || firstMapping.SessionID != firstID || firstMapping.StatusSource != "process_io_unknown" {
		t.Fatalf("first exact PID/session mapping = %+v", firstMapping)
	}
	if err := first.console.Resize(113, 37); err != nil {
		t.Fatalf("resize outer ConPTY: %v", err)
	}
	if _, err := first.console.Write([]byte("size\r\n")); err != nil {
		t.Fatalf("send resize query: %v", err)
	}
	first.reader.waitFor(t, "SIZE 113 37")
	if _, err := first.console.Write([]byte("exit\r\n")); err != nil {
		t.Fatalf("send exit: %v", err)
	}
	firstOutput := first.reader.waitFor(t, "SUMMARY")
	firstSummary := parseWindowsConPTYSummary(t, firstOutput)
	if firstSummary.exit != 23 || firstSummary.inputBytes < int64(len("sizeexit")) || firstSummary.inputLines < 2 ||
		firstSummary.outputBytes == 0 || firstSummary.outputLines == 0 || firstSummary.partial {
		t.Fatalf("first PTY relay summary = %+v", firstSummary)
	}
	if code, err := first.console.Wait(context.Background()); err != nil || int(code) != 23 {
		t.Fatalf("first ConPTY exit = %d, %v; want 23", code, err)
	}

	secondID := "019db82c-66c5-7160-a6a7-b76dc2dd72d2"
	second := startWindowsConPTYHelper(t, secondID, "interactive")
	second.reader.waitFor(t, "READY")
	secondMapping := second.resolveMapping(t)
	if secondMapping.PID == firstMapping.PID || secondMapping.SessionID != secondID || secondMapping.ProcStart == firstMapping.ProcStart {
		t.Fatalf("restart mapping reused prior process identity: first=%+v second=%+v", firstMapping, secondMapping)
	}
	if _, err := second.console.Write([]byte("exit\r\n")); err != nil {
		t.Fatalf("send restart exit: %v", err)
	}
	secondOutput := second.reader.waitFor(t, "SUMMARY")
	secondSummary := parseWindowsConPTYSummary(t, secondOutput)
	if secondSummary.exit != 23 || secondSummary.inputBytes >= firstSummary.inputBytes {
		t.Fatalf("restart counters were not fresh: first=%+v second=%+v", firstSummary, secondSummary)
	}
	if code, err := second.console.Wait(context.Background()); err != nil || int(code) != 23 {
		t.Fatalf("restart ConPTY exit = %d, %v; want 23", code, err)
	}
}

func TestWindowsConPTYRelayForwardsCtrlCAndExitCode(t *testing.T) {
	if !conpty.IsConPtyAvailable() {
		t.Skip("Windows ConPTY is unavailable on this runner")
	}
	fixture := startWindowsConPTYHelper(t, "019db82c-66c5-7160-a6a7-b76dc2dd72d3", "interrupt")
	fixture.reader.waitFor(t, "READY")
	if mapping := fixture.resolveMapping(t); mapping.PID != fixture.pid || mapping.SessionID == "" {
		t.Fatalf("interrupt fixture safe mapping = %+v", mapping)
	}
	if _, err := fixture.console.Write([]byte{3}); err != nil {
		t.Fatalf("forward Ctrl+C to outer ConPTY: %v", err)
	}
	output := fixture.reader.waitFor(t, "SUMMARY")
	summary := parseWindowsConPTYSummary(t, output)
	if !strings.Contains(output, "INTERRUPTED") || summary.exit != 130 || summary.inputBytes < 1 {
		t.Fatalf("Ctrl+C relay output=%q summary=%+v", output, summary)
	}
	if code, err := fixture.console.Wait(context.Background()); err != nil || int(code) != 130 {
		t.Fatalf("interrupt ConPTY exit = %d, %v; want 130", code, err)
	}
}

type windowsConPTYFixture struct {
	console   *conpty.ConPty
	reader    *windowsConPTYReader
	home      string
	marker    string
	sessionID string
	pid       int
}

func startWindowsConPTYHelper(t *testing.T, sessionID, mode string) *windowsConPTYFixture {
	t.Helper()
	home := filepath.Join(t.TempDir(), "codex-home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "child-pid")
	workDir := t.TempDir()
	args := []string{os.Args[0], "-test.run=^TestWindowsConPTYRelayHelperProcess$"}
	env := replaceWindowsTestEnv(os.Environ(), map[string]string{
		"CCTRACE_WINDOWS_CONPTY_HELPER":     "1",
		"CCTRACE_WINDOWS_CONPTY_SESSION_ID": sessionID,
		"CCTRACE_WINDOWS_CONPTY_PID_MARKER": marker,
		"CCTRACE_WINDOWS_CONPTY_MODE":       mode,
		"CODEX_HOME":                        home,
	})
	console, err := conpty.Start(windows.ComposeCommandLine(args),
		conpty.ConPtyDimensions(80, 24), conpty.ConPtyWorkDir(workDir), conpty.ConPtyEnv(env))
	if err != nil {
		t.Fatalf("start outer ConPTY helper: %v", err)
	}
	t.Cleanup(func() { _ = console.Close() })
	reader := startWindowsConPTYReader(console)
	fixture := &windowsConPTYFixture{console: console, reader: reader, home: home, marker: marker, sessionID: sessionID}
	fixture.pid = waitWindowsConPTYPID(t, marker)
	return fixture
}

func (f *windowsConPTYFixture) resolveMapping(t *testing.T) SessionMapping {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mapping, err := WaitForSessionMapping(ctx, "codex", f.home, f.pid)
	if err != nil {
		t.Fatalf("map exact Windows child PID %d: %v", f.pid, err)
	}
	if mapping.SessionID != f.sessionID {
		t.Fatalf("mapped session %s, want %s", mapping.SessionID, f.sessionID)
	}
	return mapping
}

func waitWindowsConPTYPID(t *testing.T, marker string) int {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(marker)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("helper did not write a valid child PID to %s", marker)
		}
	}
}

type windowsConPTYSummary struct {
	pid                                              int
	inputBytes, inputLines, outputBytes, outputLines int64
	exit                                             int
	partial                                          bool
	failure                                          string
}

func parseWindowsConPTYSummary(t *testing.T, output string) windowsConPTYSummary {
	t.Helper()
	var summary windowsConPTYSummary
	lineStart := strings.LastIndex(output, "SUMMARY ")
	if lineStart < 0 {
		t.Fatalf("ConPTY output has no summary: %q", output)
	}
	line := strings.TrimSpace(output[lineStart:])
	n, err := fmt.Sscanf(line, "SUMMARY pid=%d input_bytes=%d input_lines=%d output_bytes=%d output_lines=%d exit=%d partial=%t failure=%s",
		&summary.pid, &summary.inputBytes, &summary.inputLines, &summary.outputBytes, &summary.outputLines,
		&summary.exit, &summary.partial, &summary.failure)
	if err != nil || n != 8 {
		t.Fatalf("parse ConPTY summary %q: fields=%d err=%v", line, n, err)
	}
	return summary
}

type windowsConPTYReader struct {
	chunks chan []byte
	seen   bytes.Buffer
}

func startWindowsConPTYReader(console *conpty.ConPty) *windowsConPTYReader {
	reader := &windowsConPTYReader{chunks: make(chan []byte, 32)}
	go func() {
		defer close(reader.chunks)
		buffer := make([]byte, 4096)
		for {
			n, err := console.Read(buffer)
			if n > 0 {
				reader.chunks <- append([]byte(nil), buffer[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	return reader
}

func (r *windowsConPTYReader) waitFor(t *testing.T, text string) string {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for !bytes.Contains(r.seen.Bytes(), []byte(text)) {
		select {
		case chunk, ok := <-r.chunks:
			if !ok {
				t.Fatalf("ConPTY closed before %q; output=%q", text, r.seen.String())
			}
			r.seen.Write(chunk)
		case <-timer.C:
			t.Fatalf("ConPTY did not produce %q; output=%q", text, r.seen.String())
		}
	}
	return r.seen.String()
}

func replaceWindowsTestEnv(env []string, values map[string]string) []string {
	result := make([]string, 0, len(env)+len(values))
	for _, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		if ok {
			replace := false
			for key := range values {
				if strings.EqualFold(name, key) {
					replace = true
					break
				}
			}
			if replace {
				continue
			}
		}
		result = append(result, entry)
	}
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	return result
}

func readWindowsConPTYLine(reader *bufio.Reader) (string, error) {
	var line strings.Builder
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		if b == '\r' || b == '\n' {
			return line.String(), nil
		}
		line.WriteByte(b)
	}
}
