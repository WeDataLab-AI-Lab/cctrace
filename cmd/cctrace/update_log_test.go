package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"cctrace/internal/syncer"
)

// The spawned child's stderr is sync-crash.log, not sync.log, and that file is
// reset when it outgrows its cap. An update that fails inside the child --
// exactly the case #458 could not tell apart from a successful one -- therefore
// left no trace anywhere a person looks. Both self-update entry points run in a
// child (`sync --once` and the watch child's own pre-collection update), so both
// have to report through the log package.
func newerVersionServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			_ = json.NewEncoder(w).Encode(map[string]string{"version": "v9.9.9"})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func readSyncLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(syncLogPath(""))
	if err != nil {
		t.Fatalf("read sync log: %v", err)
	}
	return string(data)
}

func TestDaemonParentUpdateFailureReachesSyncLog(t *testing.T) {
	setupTestHome(t)
	useProgressTTY(t, false)
	previous := version
	version = "v0.0.1"
	t.Cleanup(func() { version = previous })
	server := newerVersionServer(t)

	closeLog := installDaemonLog("")
	result := applyDaemonParentUpdateIfAvailable(context.Background(), "", server.URL, false)
	closeLog()

	if result.Applied {
		t.Fatalf("update unexpectedly applied: %#v", result)
	}
	if got := readSyncLog(t); !strings.Contains(got, "update:") {
		t.Fatalf("apply failure missing from sync.log:\n%s", got)
	}
}

func TestOneShotUpdateFailureReachesSyncLog(t *testing.T) {
	setupTestHome(t)
	useProgressTTY(t, false)
	previous := version
	version = "v0.0.1"
	t.Cleanup(func() { version = previous })
	server := newerVersionServer(t)
	client := syncer.NewClient(server.URL, "", version)

	closeLog := installDaemonLog("")
	applyUpdateIfAvailable(context.Background(), client, server.URL, "")
	closeLog()

	if got := readSyncLog(t); !strings.Contains(got, "update:") {
		t.Fatalf("apply failure missing from sync.log:\n%s", got)
	}
}

// The insecure-transport override warns "every time" so that a machine running
// with it says so in the log pasted into a bug report. Both callers of
// validateUpdateEndpoint sit inside the update path, which the hook-spawned
// daemon parent and its --log-to-file child both run with the log installed --
// so writing to raw stderr sends the warning to a discarded stream and the
// comment's promise does not hold.
func TestInsecureEndpointOverrideWarningReachesSyncLog(t *testing.T) {
	setupTestHome(t)
	useProgressTTY(t, false)
	t.Setenv(allowInsecureEndpointEnv, "1")

	closeLog := installDaemonLog("")
	err := validateUpdateEndpoint("http://trace.example.com")
	closeLog()

	if err != nil {
		t.Fatalf("validateUpdateEndpoint with override = %v, want nil", err)
	}
	if got := readSyncLog(t); !strings.Contains(got, allowInsecureEndpointEnv) {
		t.Fatalf("override warning missing from sync.log:\n%s", got)
	}
}
