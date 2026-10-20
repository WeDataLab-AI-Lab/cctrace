//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"cctrace/internal/processio"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

const processIOLaunchFixtureSessionID = "019db82c-66c5-7160-a6a7-b76dc2dd72ca"

type capturedProcessIORequest struct {
	request syncer.SessionObservationsRequest
	body    []byte
	auth    string
}

func TestLaunchProcessIOFixtureHelper(t *testing.T) {
	if os.Getenv("CCTRACE_PROCESSIO_LAUNCH_FIXTURE") != "1" {
		return
	}
	processIOReportInterval = 250 * time.Millisecond
	mappedPath := os.Getenv("CCTRACE_PROCESSIO_MAPPED_PATH")
	processIOSessionMappedFn = func(mapping processio.SessionMapping) {
		_ = os.WriteFile(mappedPath, []byte(strconv.Itoa(mapping.PID)+"\n"+mapping.SessionID), 0600)
	}
	cmd := newRootCmd()
	cmd.SetArgs([]string{"launch", "--agent", "claude", "--observe-process-io", "--io-mode", "pipes"})
	if err := cmd.Execute(); err != nil {
		t.Errorf("launch: %v", err)
	}
}

func TestLaunchProcessIOMapsAndUploadsOnlyCounters(t *testing.T) {
	home := setupTestHome(t)
	claudeHome := filepath.Join(home, "agent-home-fixture")
	if err := os.MkdirAll(claudeHome, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeHome, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"019db82c-66c5-7160-a6a7-b76dc2dd72cb"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	capturedRequests := make(chan capturedProcessIORequest, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/session-observations" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		var req syncer.SessionObservationsRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "decode failed", http.StatusBadRequest)
			return
		}
		capturedRequests <- capturedProcessIORequest{request: req, body: body, auth: r.Header.Get("Authorization")}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(syncer.SessionObservationsResponse{Accepted: len(req.LiveStates), Stored: len(req.LiveStates)})
	}))
	defer server.Close()
	p, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.ClaudeConfigDir = claudeHome
	p.Server.SyncEndpoint = server.URL
	p.Server.Endpoint = server.URL
	p.Server.AuthToken = "synthetic-upload-token"
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	fakeClaude := filepath.Join(binDir, "claude")
	script := "#!/bin/sh\n" +
		"set -eu\n" +
		"mkdir -p \"$CLAUDE_CONFIG_DIR/sessions\"\n" +
		"pid=$$\n" +
		"proc_start=$(LC_ALL=C TZ=UTC ps -o lstart= -p \"$pid\")\n" +
		"printf '{\"pid\":%s,\"sessionId\":\"" + processIOLaunchFixtureSessionID + "\",\"procStart\":\"%s\",\"cwd\":\"/synthetic/project\",\"status\":\"idle\",\"statusUpdatedAt\":1791208506977}\\n' \"$pid\" \"$proc_start\" > \"$CLAUDE_CONFIG_DIR/sessions/$pid.json\"\n" +
		"printf 'READY\\n'\n" +
		"cat\n" +
		"printf 'SYNTHETIC-SECRET-ERROR\\n' >&2\n"
	if err := os.WriteFile(fakeClaude, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	launcher := exec.Command(os.Args[0], "-test.run=^TestLaunchProcessIOFixtureHelper$")
	mappedPath := filepath.Join(home, "process-io-mapped.json")
	launcher.Env = append(os.Environ(), "CCTRACE_PROCESSIO_LAUNCH_FIXTURE=1", "CCTRACE_PROCESSIO_MAPPED_PATH="+mappedPath)
	var launcherStdout bytes.Buffer
	launcher.Stdout = &launcherStdout
	var launcherStderr bytes.Buffer
	launcher.Stderr = &launcherStderr
	stdin, err := launcher.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := launcher.Start(); err != nil {
		t.Fatal(err)
	}
	launcherDone := make(chan error, 1)
	go func() { launcherDone <- launcher.Wait() }()
	parentMapping, err := waitForClaudeSessionMapping(claudeHome)
	if err != nil {
		stopLaunchFixtureProcess(launcher, claudeHome, launcherDone)
		t.Fatalf("synthetic Claude PID/session mapping: %v; stderr: %s", err, launcherStderr.String())
	}
	mappedPID, mappedSession, err := waitForMappedMarker(mappedPath)
	if err != nil {
		stopLaunchFixtureProcess(launcher, claudeHome, launcherDone)
		t.Fatalf("launcher did not accept the verified PID/session mapping: %v; stderr: %s", err, launcherStderr.String())
	}
	if mappedPID != parentMapping.PID || mappedSession != parentMapping.SessionID {
		stopLaunchFixtureProcess(launcher, claudeHome, launcherDone)
		t.Fatalf("launcher mapped %d/%s, fixture mapped %d/%s", mappedPID, mappedSession, parentMapping.PID, parentMapping.SessionID)
	}
	first := awaitProcessIORequest(t, capturedRequests, launcherDone, launcher, claudeHome, &launcherStderr, &launcherStdout)
	if first.auth != "Bearer synthetic-upload-token" || len(first.request.LiveStates) != 1 {
		t.Fatalf("upload identity=%q states=%d", first.auth, len(first.request.LiveStates))
	}
	initial := first.request.LiveStates[0]
	if initial.SessionID != processIOLaunchFixtureSessionID || initial.Status != "idle" || initial.StatusSource != "claude_process_file" || initial.IOSource != "process_stream" || initial.IOMode != "pipes" {
		t.Fatalf("initial live state = %+v", initial)
	}
	if strings.Contains(string(first.body), "SYNTHETIC-SECRET") {
		t.Fatal("process stream content appeared in the upload body")
	}
	input := []byte("SYNTHETIC-SECRET-INPUT\n")
	if _, err := stdin.Write(input); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	var final capturedProcessIORequest
	for {
		final = awaitProcessIORequest(t, capturedRequests, launcherDone, launcher, claudeHome, &launcherStderr, &launcherStdout)
		if len(final.request.LiveStates) > 0 && final.request.LiveStates[0].Status == "gone" {
			break
		}
	}
	if strings.Contains(string(final.body), "SYNTHETIC-SECRET") {
		t.Fatal("process stream content appeared in the final upload body")
	}
	if len(final.request.LiveStates) != 1 {
		t.Fatalf("final live states = %d, want one", len(final.request.LiveStates))
	}
	got := final.request.LiveStates[0]
	wantStdout := int64(len("READY\n") + len(input))
	wantStderr := int64(len("SYNTHETIC-SECRET-ERROR\n"))
	if got.Status != "gone" || got.InputBytes == nil || *got.InputBytes != int64(len(input)) || got.InputLines == nil || *got.InputLines != 1 ||
		got.StdoutBytes == nil || *got.StdoutBytes != wantStdout || got.StderrBytes == nil || *got.StderrBytes != wantStderr ||
		got.OutputBytes == nil || *got.OutputBytes != wantStdout+wantStderr || got.OutputLines == nil || *got.OutputLines != 3 ||
		got.IOEpoch == "" || got.IOAsOf == nil || got.IOPartial {
		t.Fatalf("final counters = %+v, want stdin/stdout/stderr totals only", got)
	}
	select {
	case err := <-launcherDone:
		if err != nil {
			t.Fatalf("launch helper: %v, stderr: %s", err, launcherStderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = launcher.Process.Kill()
		<-launcherDone
		t.Fatal("launch helper did not exit after process I/O upload")
	}
}

func awaitProcessIORequest(t *testing.T, requests <-chan capturedProcessIORequest, done <-chan error, command *exec.Cmd, claudeHome string, stderr, stdout *bytes.Buffer) capturedProcessIORequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	default:
	}
	select {
	case request := <-requests:
		return request
	case err := <-done:
		select {
		case request := <-requests:
			return request
		default:
			t.Fatalf("launch helper exited before process I/O upload: %v, stdout: %s stderr: %s", err, stdout.String(), stderr.String())
		}
	case <-time.After(5 * time.Second):
		select {
		case request := <-requests:
			return request
		case err := <-done:
			t.Fatalf("launch helper exited before process I/O upload: %v, stdout: %s stderr: %s", err, stdout.String(), stderr.String())
		default:
			stopLaunchFixtureProcess(command, claudeHome, done)
			t.Fatalf("process I/O upload was not received; stdout: %s stderr: %s", stdout.String(), stderr.String())
		}
	}
	return capturedProcessIORequest{}
}

func stopLaunchFixtureProcess(command *exec.Cmd, claudeHome string, done <-chan error) {
	if matches, _ := filepath.Glob(filepath.Join(claudeHome, "sessions", "*.json")); len(matches) > 0 {
		data, err := os.ReadFile(matches[0])
		if err == nil {
			var state struct {
				PID int `json:"pid"`
			}
			if json.Unmarshal(data, &state) == nil && state.PID > 0 {
				if _, err := processio.ResolveSessionMapping("claude", claudeHome, state.PID); err == nil {
					_ = syscall.Kill(-state.PID, syscall.SIGKILL)
				}
			}
		}
	}
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func waitForClaudeSessionMapping(home string) (processio.SessionMapping, error) {
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		matches, _ := filepath.Glob(filepath.Join(home, "sessions", "*.json"))
		for _, path := range matches {
			pid, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(path), ".json"))
			if err != nil {
				continue
			}
			mapping, err := processio.ResolveSessionMapping("claude", home, pid)
			if err == nil {
				return mapping, nil
			}
			if !errors.Is(err, processio.ErrSessionMappingNotReady) {
				return processio.SessionMapping{}, err
			}
			lastErr = err
		}
		select {
		case <-deadline.C:
			if lastErr != nil {
				return processio.SessionMapping{}, lastErr
			}
			return processio.SessionMapping{}, processio.ErrSessionMappingNotReady
		case <-ticker.C:
		}
	}
}

func waitForMappedMarker(path string) (int, string, error) {
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			parts := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
			if len(parts) == 2 {
				pid, parseErr := strconv.Atoi(parts[0])
				if parseErr == nil {
					return pid, parts[1], nil
				}
			}
		}
		select {
		case <-deadline.C:
			return 0, "", processio.ErrSessionMappingNotReady
		case <-ticker.C:
		}
	}
}
