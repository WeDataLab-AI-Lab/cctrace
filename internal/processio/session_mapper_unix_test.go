//go:build darwin || linux

package processio

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/codexlog"
)

func TestCodexMapperFixtureProcess(t *testing.T) {
	if os.Getenv("CCTRACE_SESSION_MAP_FIXTURE") != "1" {
		return
	}
	files := strings.Split(os.Getenv("CCTRACE_SESSION_MAP_FILES"), string(os.PathListSeparator))
	var opened []*os.File
	for _, path := range files {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0600)
		if err != nil {
			os.Exit(80)
		}
		id := codexlog.SessionIDFromPath(path)
		if os.Getenv("CCTRACE_SESSION_MAP_MISMATCH") == "1" {
			id = "different-session-id"
		}
		_, _ = fmt.Fprintf(f, `{"type":"session_meta","payload":{"id":%q,"session_id":%q,"cwd":"/synthetic/project"}}`+"\n", id, id)
		_ = f.Sync()
		opened = append(opened, f)
	}
	fmt.Fprintln(os.Stdout, "READY")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	for _, f := range opened {
		_ = f.Close()
	}
	os.Exit(0)
}

func TestClaudeMapperFixtureProcess(t *testing.T) {
	if os.Getenv("CCTRACE_CLAUDE_MAP_FIXTURE") != "1" {
		return
	}
	home := os.Getenv("CCTRACE_CLAUDE_MAP_HOME")
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0700); err != nil {
		os.Exit(81)
	}
	started, err := claudeProcStartForPID(os.Getpid())
	if err != nil {
		os.Exit(82)
	}
	if os.Getenv("CCTRACE_CLAUDE_MAP_STALE") == "1" {
		started = "Sat Jan  1 00:00:00 2000"
	}
	state := claudeProcessFile{
		PID: os.Getpid(), SessionID: "019db82c-66c5-7160-a6a7-b76dc2dd72c9", ProcStart: started,
		Status: "idle", StatusUpdatedAt: 1791208506977,
	}
	file, err := os.Create(filepath.Join(home, "sessions", fmt.Sprintf("%d.json", os.Getpid())))
	if err != nil {
		os.Exit(83)
	}
	if err := json.NewEncoder(file).Encode(state); err != nil {
		os.Exit(84)
	}
	_ = file.Close()
	fmt.Fprintln(os.Stdout, "READY")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(0)
}

func TestResolveClaudeSessionRequiresMatchingLivePIDAndProcStart(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestClaudeMapperFixtureProcess$")
	cmd.Env = append(os.Environ(), "CCTRACE_CLAUDE_MAP_FIXTURE=1", "CCTRACE_CLAUDE_MAP_HOME="+home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "READY\n" {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("Claude fixture ready line = %q, err %v", line, err)
	}
	mapping, err := ResolveSessionMapping("claude", home, cmd.Process.Pid)
	if err != nil {
		t.Fatalf("ResolveSessionMapping: %v", err)
	}
	if mapping.SessionID != "019db82c-66c5-7160-a6a7-b76dc2dd72c9" || mapping.ProcStart == "" || mapping.PID != cmd.Process.Pid ||
		mapping.Status != "idle" || mapping.StatusSource != "claude_process_file" || mapping.StatusChangedAt == nil ||
		!mapping.StatusChangedAt.Equal(time.UnixMilli(1791208506977).UTC()) {
		t.Fatalf("mapping = %+v", mapping)
	}
	_, _ = stdin.Write([]byte("exit\n"))
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveClaudeSessionRejectsStaleProcStart(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestClaudeMapperFixtureProcess$")
	cmd.Env = append(os.Environ(), "CCTRACE_CLAUDE_MAP_FIXTURE=1", "CCTRACE_CLAUDE_MAP_HOME="+home, "CCTRACE_CLAUDE_MAP_STALE=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "READY\n" {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("Claude fixture ready line = %q, err %v", line, err)
	}
	if _, err := ResolveSessionMapping("claude", home, cmd.Process.Pid); !errors.Is(err, ErrSessionMappingNotReady) {
		t.Fatalf("stale Claude process state error = %v; want mapping unavailable", err)
	}
	_, _ = stdin.Write([]byte("exit\n"))
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessWaitingKindUsesClaudeProcessFileVocabulary(t *testing.T) {
	for waitingFor, want := range map[string]string{
		"": "", "input needed": "input", "permission prompt": "permission", "dialog open": "dialog",
		"sandbox request": "sandbox", "worker request": "worker", "goal proposal": "goal", "future state": "other",
	} {
		if got := processWaitingKind(waitingFor); got != want {
			t.Errorf("processWaitingKind(%q) = %q, want %q", waitingFor, got, want)
		}
	}
}

func TestVerifyClaudeSessionMappingRejectsAChangedSessionOnSamePID(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestClaudeMapperFixtureProcess$")
	cmd.Env = append(os.Environ(), "CCTRACE_CLAUDE_MAP_FIXTURE=1", "CCTRACE_CLAUDE_MAP_HOME="+home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "READY\n" {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("Claude fixture ready line = %q, err %v", line, err)
	}
	expected, err := ResolveSessionMapping("claude", home, cmd.Process.Pid)
	if err != nil {
		t.Fatalf("initial mapping: %v", err)
	}
	statePath := filepath.Join(home, "sessions", fmt.Sprintf("%d.json", cmd.Process.Pid))
	changed := claudeProcessFile{PID: cmd.Process.Pid, SessionID: "019db82c-66c5-7160-a6a7-b76dc2dd72c4", ProcStart: expected.ProcStart, Status: "idle"}
	data, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySessionMapping("claude", home, cmd.Process.Pid, expected); !errors.Is(err, ErrSessionMappingUnavailable) {
		t.Fatalf("changed session verification error = %v; want safe mapping rejection", err)
	}
	_, _ = stdin.Write([]byte("exit\n"))
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveCodexSessionRequiresUniqueOpenRolloutAndRechecksProcess(t *testing.T) {
	home := t.TempDir()
	id := "019db82c-66c5-7160-a6a7-b76dc2dd72c5"
	path := filepath.Join(home, "sessions", "2026", "10", "09", "rollout-2026-10-09T12-00-00-"+id+".jsonl")
	fixture := startCodexMapFixture(t, []string{path})
	mapping, err := ResolveSessionMapping("codex", home, fixture.pid)
	if err != nil {
		t.Fatalf("ResolveSessionMapping: %v", err)
	}
	if mapping.SessionID != id || mapping.Agent != "codex" || mapping.PID != fixture.pid || mapping.ProcStart == "" ||
		mapping.Status != "unknown" || mapping.StatusSource != "process_io_unknown" {
		t.Fatalf("mapping = %+v", mapping)
	}
	if err := fixture.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveCodexSessionDoesNotUseAFileThatTheProcessDidNotOpen(t *testing.T) {
	home := t.TempDir()
	id := "019db82c-66c5-7160-a6a7-b76dc2dd72c6"
	path := filepath.Join(home, "sessions", "2026", "10", "09", "rollout-2026-10-09T12-00-00-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data := `{"type":"session_meta","payload":{"id":"` + id + `","session_id":"` + id + `"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveSessionMapping("codex", home, os.Getpid()); err == nil {
		t.Fatal("a rollout file not open by the process was mapped")
	}
}

func TestResolveCodexSessionRejectsMultipleOpenRollouts(t *testing.T) {
	home := t.TempDir()
	paths := []string{
		filepath.Join(home, "sessions", "2026", "10", "09", "rollout-2026-10-09T12-00-00-019db82c-66c5-7160-a6a7-b76dc2dd72c7.jsonl"),
		filepath.Join(home, "sessions", "2026", "10", "09", "rollout-2026-10-09T12-01-00-019db82c-66c5-7160-a6a7-b76dc2dd72c8.jsonl"),
	}
	fixture := startCodexMapFixture(t, paths)
	if _, err := ResolveSessionMapping("codex", home, fixture.pid); err == nil {
		t.Fatal("multiple open rollout files were assigned an arbitrary session")
	}
	if err := fixture.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveCodexSessionRejectsSessionMetaIDMismatch(t *testing.T) {
	home := t.TempDir()
	id := "019db82c-66c5-7160-a6a7-b76dc2dd72c9"
	path := filepath.Join(home, "sessions", "2026", "10", "09", "rollout-2026-10-09T12-00-00-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCodexMapperFixtureProcess$")
	cmd.Env = append(os.Environ(), "CCTRACE_SESSION_MAP_FIXTURE=1", "CCTRACE_SESSION_MAP_FILES="+path, "CCTRACE_SESSION_MAP_MISMATCH=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "READY\n" {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("Codex fixture ready line = %q, err %v", line, err)
	}
	if _, err := ResolveSessionMapping("codex", home, cmd.Process.Pid); err == nil {
		t.Fatal("Codex session metadata for a different ID was accepted")
	}
	_, _ = stdin.Write([]byte("exit\n"))
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

type codexMapFixture struct {
	pid int
	cmd *exec.Cmd
	in  io.WriteCloser
}

func startCodexMapFixture(t *testing.T, paths []string) codexMapFixture {
	t.Helper()
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCodexMapperFixtureProcess$")
	cmd.Env = append(os.Environ(), "CCTRACE_SESSION_MAP_FIXTURE=1", "CCTRACE_SESSION_MAP_FILES="+strings.Join(paths, string(os.PathListSeparator)))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = stdout.(*os.File).SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "READY\n" {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("mapper fixture ready line = %q, err %v", line, err)
	}
	return codexMapFixture{pid: cmd.Process.Pid, cmd: cmd, in: stdin}
}

func (f codexMapFixture) finish() error {
	_, _ = f.in.Write([]byte("exit\n"))
	_ = f.in.Close()
	return f.cmd.Wait()
}
