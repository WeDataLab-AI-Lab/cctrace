package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/profile"
)

// The watch hooks and HOME are process-global.
func TestLocalWatchUpdateHandoffRestoresListener(t *testing.T) {
	p := localTestProfile(t)
	resetSyncControlHooks(t)
	oldVersion, oldInterval := version, watchUpdateCheckInterval
	version, watchUpdateCheckInterval = "v0.7.18", 0
	t.Cleanup(func() { version, watchUpdateCheckInterval = oldVersion, oldInterval })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"inserted":0}`))
	}))
	defer srv.Close()
	p.Options.SyncEnabled = true
	p.Server.Endpoint = srv.URL
	p.Server.SyncEndpoint = srv.URL
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runLocalEnable(&out, "", "http://localhost:3000"); err != nil {
		t.Fatal(err)
	}
	var bootstrap struct{ Endpoint, Token string }
	if err := json.Unmarshal(out.Bytes(), &bootstrap); err != nil {
		t.Fatal(err)
	}
	updates, reexecs := 0, 0
	applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
		updates++
		if updates == 1 {
			return daemonParentUpdateResult{}
		}
		req, _ := http.NewRequest("GET", bootstrap.Endpoint+"/v1/health", nil)
		req.Header.Set("Authorization", "Bearer "+bootstrap.Token)
		resp, err := (&http.Client{Timeout: time.Second}).Do(req)
		if err != nil {
			t.Errorf("listener unavailable before handoff %d: %v", updates, err)
		} else {
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Errorf("health=%d", resp.StatusCode)
			}
		}
		return daemonParentUpdateResult{Applied: true}
	}
	reexecDaemonParentFn = func(string) error {
		reexecs++
		listener, err := net.Listen("tcp", strings.TrimPrefix(bootstrap.Endpoint, "http://"))
		if err != nil {
			t.Errorf("listener still held during handoff: %v", err)
		} else {
			listener.Close()
		}
		if reexecs == 1 {
			return errors.New("test handoff failure")
		}
		return nil
	}
	runWatchUntilStopped(t, "local listener handoff", func() error {
		return runSync(false, p.ClaudeConfigDir, true, false, false, false, time.Millisecond, "", "", "", false, "", false)
	})
	if reexecs != 2 {
		t.Fatalf("reexecs=%d", reexecs)
	}
}
