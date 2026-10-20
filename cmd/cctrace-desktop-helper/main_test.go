package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type subprocessResponse struct {
	Version int             `json:"version"`
	Kind    string          `json:"kind"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func buildHelper(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cctrace-desktop-helper")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", path, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}
	return path
}

func TestSubprocessMultipleJSONLRequestsEOFAndSentinelHashes(t *testing.T) {
	helper := buildHelper(t)
	base := t.TempDir()
	appSupport := filepath.Join(base, "app support")
	claudeHome := filepath.Join(base, "Claude Home 日本語")
	codexHome := filepath.Join(base, "Codex Home Space")
	if err := os.MkdirAll(claudeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	claudeSentinel := filepath.Join(claudeHome, "sentinel")
	codexSentinel := filepath.Join(codexHome, "sentinel")
	if err := os.WriteFile(claudeSentinel, []byte("claude fixture bytes"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexSentinel, []byte("codex fixture bytes"), 0o400); err != nil {
		t.Fatal(err)
	}
	claudeBefore := digest(t, claudeSentinel)
	codexBefore := digest(t, codexSentinel)

	requests := []string{
		`{"version":1,"kind":"request","id":"hello-1","op":"hello","payload":{}}`,
		`{"version":1,"kind":"request","id":"status-1","op":"status","payload":{}}`,
		`{"version":1,"kind":"request","id":"profile-add","op":"profiles.add","payload":{"name":"work","claude_home":"` + claudeHome + `","codex_home":"` + codexHome + `"}}`,
		`{"version":1,"kind":"request","id":"profile-list","op":"profiles.list","payload":{}}`,
		`{"version":1,"kind":"request","id":"profile-remove","op":"profiles.remove","payload":{"name":"work"}}`,
		`{"version":1,"kind":"request","id":"quota-read","op":"quota.read","payload":{"provider":"claude"}}`,
		`{"version":1,"kind":"request","id":"update-check","op":"update.check","payload":{}}`,
	}
	cmd := exec.Command(helper, "--app-support", appSupport)
	cmd.Stdin = strings.NewReader(strings.Join(requests, "\n") + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper exit: %v\nstderr=%s", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
	responses := parseResponses(t, stdout.Bytes())
	wantIDs := []string{"hello-1", "status-1", "profile-add", "profile-list", "profile-remove", "quota-read", "update-check"}
	if len(responses) != len(wantIDs) {
		t.Fatalf("response count=%d, want %d: %s", len(responses), len(wantIDs), stdout.String())
	}
	for i, response := range responses {
		if response.Version != 1 || response.Kind != "response" || response.ID != wantIDs[i] || response.Error != nil {
			t.Fatalf("response %d = %+v", i, response)
		}
	}
	fixtureRoot, err := filepath.Abs(filepath.Join("..", "..", "tests", "desktop", "fixtures", "v1", "ipc", "quota", "fresh"))
	if err != nil {
		t.Fatal(err)
	}
	fixtureSupport := filepath.Join(base, "fixture app support")
	fixtureCmd := exec.Command(helper, "--app-support", fixtureSupport, "--fixture-root", fixtureRoot, "--clock", "2026-10-04T12:30:00Z")
	fixtureCmd.Stdin = strings.NewReader(`{"version":1,"kind":"request","id":"fixture-quota","op":"quota.read","payload":{"provider":"claude"}}` + "\n")
	var fixtureStdout, fixtureStderr bytes.Buffer
	fixtureCmd.Stdout = &fixtureStdout
	fixtureCmd.Stderr = &fixtureStderr
	if err := fixtureCmd.Run(); err != nil {
		t.Fatalf("fixture helper exit: %v\nstderr=%s", err, fixtureStderr.String())
	}
	fixtureResponses := parseResponses(t, fixtureStdout.Bytes())
	if fixtureStderr.Len() != 0 || len(fixtureResponses) != 1 || fixtureResponses[0].Error != nil {
		t.Fatalf("fixture response=%+v stderr=%q", fixtureResponses, fixtureStderr.String())
	}
	var fixtureQuota struct {
		State   string `json:"state"`
		Windows []struct {
			Utilization float64 `json:"utilization"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(fixtureResponses[0].Result, &fixtureQuota); err != nil || fixtureQuota.State != "fresh" || len(fixtureQuota.Windows) != 1 || fixtureQuota.Windows[0].Utilization != 0 {
		t.Fatalf("fixture quota result=%+v err=%v", fixtureQuota, err)
	}
	var hello struct {
		ProtocolVersion int  `json:"protocol_version"`
		SelfUpdate      bool `json:"self_update"`
	}
	if err := json.Unmarshal(responses[0].Result, &hello); err != nil || hello.ProtocolVersion != 1 || hello.SelfUpdate {
		t.Fatalf("hello result=%+v err=%v", hello, err)
	}
	var status struct {
		Mode string `json:"mode"`
		Sync struct {
			State string `json:"state"`
		} `json:"sync"`
	}
	if err := json.Unmarshal(responses[1].Result, &status); err != nil || status.Mode != "fixture" || status.Sync.State != "disabled" {
		t.Fatalf("status result=%+v err=%v", status, err)
	}
	var quota struct {
		State   string `json:"state"`
		Windows []any  `json:"windows"`
	}
	if err := json.Unmarshal(responses[5].Result, &quota); err != nil || quota.State != "unavailable" || len(quota.Windows) != 0 {
		t.Fatalf("quota result=%+v err=%v", quota, err)
	}
	var update struct {
		Owner      string `json:"owner"`
		SelfUpdate bool   `json:"self_update"`
		State      string `json:"state"`
	}
	if err := json.Unmarshal(responses[6].Result, &update); err != nil || update.Owner != "app" || update.SelfUpdate || update.State != "delegated" {
		t.Fatalf("update result=%+v err=%v", update, err)
	}
	claudeAfter := digest(t, claudeSentinel)
	codexAfter := digest(t, codexSentinel)
	if claudeAfter != claudeBefore || codexAfter != codexBefore {
		t.Fatal("helper profile removal changed a mapped-home sentinel")
	}
	t.Logf("sentinel sha256 claude before=%s after=%s codex before=%s after=%s", claudeBefore, claudeAfter, codexBefore, codexAfter)
}

func TestSubprocessDaemonStatusUsesExplicitDaemonRoot(t *testing.T) {
	helper := buildHelper(t)
	base := t.TempDir()
	appSupport := filepath.Join(base, "app support")
	daemonRoot := filepath.Join(base, "isolated cli root")
	if err := os.MkdirAll(daemonRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(daemonRoot, "profile.json"), []byte(`{"options":{"sync_enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := strings.Join([]string{
		`{"version":1,"kind":"request","id":"hello","op":"hello","payload":{"daemon_status":true}}`,
		`{"version":1,"kind":"request","id":"daemon","op":"daemon.status.read","payload":{}}`,
	}, "\n") + "\n"
	command := exec.Command(helper, "--app-support", appSupport, "--provider-mode", "--daemon-root", daemonRoot)
	command.Stdin = strings.NewReader(requests)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("helper exit: %v stderr=%s", err, stderr.String())
	}
	responses := parseResponses(t, stdout.Bytes())
	if stderr.Len() != 0 || len(responses) != 2 || responses[0].Error != nil || responses[1].Error != nil {
		t.Fatalf("responses=%+v stderr=%q", responses, stderr.String())
	}
	var hello struct {
		Capabilities struct {
			DaemonStatus bool `json:"daemon_status"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(responses[0].Result, &hello); err != nil || !hello.Capabilities.DaemonStatus {
		t.Fatalf("daemon capability=%+v err=%v", hello, err)
	}
	var status struct {
		Profiles []struct {
			ID     string `json:"id"`
			Daemon struct {
				State    string `json:"state"`
				Evidence string `json:"evidence"`
			} `json:"daemon"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(responses[1].Result, &status); err != nil || len(status.Profiles) != 1 ||
		status.Profiles[0].ID != "default" || status.Profiles[0].Daemon.State != "stopped" || status.Profiles[0].Daemon.Evidence != "lock_free" {
		t.Fatalf("daemon status=%+v err=%v", status, err)
	}
}

func TestSubprocessRequiresExplicitAbsoluteAppSupportAndKeepsErrorsPrivate(t *testing.T) {
	helper := buildHelper(t)
	cmd := exec.Command(helper, "--app-support", "relative/private-sentinel-path")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatal("helper accepted a relative app-support root")
	}
	if stdout.Len() != 0 {
		t.Fatalf("configuration failure wrote stdout: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "private-sentinel-path") {
		t.Fatalf("configuration error leaked path: %q", stderr.String())
	}
}

func parseResponses(t *testing.T, data []byte) []subprocessResponse {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var responses []subprocessResponse
	for scanner.Scan() {
		var response subprocessResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatalf("stdout line is not protocol JSON: %q: %v", scanner.Text(), err)
		}
		responses = append(responses, response)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return responses
}

func digest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
