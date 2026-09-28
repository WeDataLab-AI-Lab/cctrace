//go:build codexlive

package codexappserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// liveHome prepares the dedicated home named by CCTRACE_CODEX_LIVE_HOME, or
// skips. Never point it at ~/.codex: PrepareHome replaces config.toml.
func liveHome(t *testing.T) RuntimeConfig {
	t.Helper()
	home := os.Getenv("CCTRACE_CODEX_LIVE_HOME")
	if home == "" {
		t.Skip("CCTRACE_CODEX_LIVE_HOME not set")
	}
	cfg := RuntimeConfig{
		Home: home, Model: os.Getenv("CCTRACE_CODEX_LIVE_MODEL"), ReasoningEffort: "low",
		APIKey: os.Getenv("CODEX_API_KEY"), Disable: []string{"apps"},
	}
	if err := PrepareHome(cfg); err != nil {
		t.Fatalf("PrepareHome: %v", err)
	}
	return cfg
}

// liveServer starts `codex app-server args...` the way Run does and returns a
// connection with initialize done.
func liveServer(t *testing.T, cfg RuntimeConfig, workDir string, args []string) *conn {
	t.Helper()
	cmd := exec.Command("codex", append([]string{"app-server"}, args...)...)
	cmd.Env = childEnv(cfg, workDir)
	cmd.Dir = workDir
	setProcessGroup(cmd)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		killGroup(cmd.Process)
		_ = cmd.Wait()
	})
	c := newConn(stdin, stdout, defaultMaxLineBytes, defaultMaxStreamBytes)
	t.Cleanup(c.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.Call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": clientName, "version": clientVersion},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := c.Notify("initialized", nil); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestLiveBuiltinToolArgsAccepted checks, without a model turn, that codex
// accepts the production flags and an empty environments list.
func TestLiveBuiltinToolArgsAccepted(t *testing.T) {
	cfg := liveHome(t)
	workDir := t.TempDir()
	args := append(append([]string{}, builtinToolArgs...), "--disable", "apps")
	c := liveServer(t, cfg, workDir, args)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := c.Call(ctx, "thread/start", map[string]any{
		"ephemeral": true, "cwd": workDir, "approvalPolicy": "untrusted", "sandbox": "read-only",
		"environments": []any{},
		"dynamicTools": []any{map[string]any{"type": "function", "name": "noop", "description": "noop", "inputSchema": map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	t.Logf("thread/start: %s", raw)
}

// TestLiveApprovalRefused spends one model turn: the model is asked to run a
// command that needs approval, and every server request is refused the way
// turnLoop refuses it. It records what codex does with the refusal. The shell
// is left on here on purpose -- production turns it off.
//
//	CCTRACE_CODEX_LIVE_HOME=/tmp/cctrace-live-home go test -tags codexlive ./internal/codexappserver -run LiveApprovalRefused -v
func TestLiveApprovalRefused(t *testing.T) {
	cfg := liveHome(t)
	workDir := t.TempDir()
	c := liveServer(t, cfg, workDir, []string{"--disable", "apps"})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	raw, err := c.Call(ctx, "thread/start", map[string]any{
		"ephemeral": true, "cwd": workDir, "approvalPolicy": "untrusted", "sandbox": "read-only",
	})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	var ts struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &ts)
	t.Logf("thread %s model %q", ts.Thread.ID, ts.Model)
	if _, err := c.Call(ctx, "turn/start", map[string]any{
		"threadId": ts.Thread.ID,
		"input": []any{map[string]any{"type": "text", "text": "셸에서 정확히 `touch approval-probe.txt` 명령 하나만 실행하고, 실행됐는지 한 줄로 답해. " +
			"거절되면 다시 시도하지 말고 거절됐다고만 답해."}},
	}); err != nil {
		t.Fatalf("turn/start: %v", err)
	}

	var refused []string
	status := ""
	for status == "" {
		select {
		case <-ctx.Done():
			t.Fatalf("turn did not end within the deadline after refusing %v", refused)
		case m, ok := <-c.Incoming():
			if !ok {
				t.Fatalf("stream ended: %v", c.Err())
			}
			switch {
			case len(m.ID) > 0:
				refused = append(refused, m.Method)
				t.Logf("server request %s: %s", m.Method, m.Params)
				_ = c.RespondError(m.ID, -32601, "cctrace does not handle "+m.Method)
			case m.Method == "item/started" || m.Method == "item/completed":
				t.Logf("%s: %s", m.Method, m.Params)
			case m.Method == "turn/completed":
				var p struct {
					Turn struct {
						Status string `json:"status"`
					} `json:"turn"`
				}
				_ = json.Unmarshal(m.Params, &p)
				status = p.Turn.Status
				t.Logf("turn/completed: %s", m.Params)
			}
		}
	}
	_, statErr := os.Stat(filepath.Join(workDir, "approval-probe.txt"))
	t.Logf("RESULT refused=%v turn=%s probe_created=%v", refused, status, statErr == nil)
}
