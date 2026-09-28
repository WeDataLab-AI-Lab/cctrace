package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// clearSyncEndpoint leaves the profile with only Server.Endpoint, which is the
// shape self-update still works in: profileHTTPAPIEndpoint falls back to it.
func clearSyncEndpoint(t *testing.T) {
	t.Helper()
	p, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.Server.SyncEndpoint = ""
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
}

func newTestSyncClient(t *testing.T, endpoint string) *syncer.Client {
	t.Helper()
	return syncer.NewClient(endpoint, "", version)
}

// reachableOTELEndpoint points the profile's OTEL endpoint at a live listener.
//
// runStatus ends in os.Exit(2) when that endpoint does not answer, which takes
// the test binary with it before any assertion runs -- the failure looks like a
// silent exit status 2 with no verdict. A real listener is also the honest
// setup: the profile shape under test is a normal one, not a broken one.
func reachableOTELEndpoint(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)
	p, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.Server.Endpoint = srv.URL
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
}

// captureStatus runs the real status command and returns what a user would see.
//
// The notice tests that shipped with the backoff called the formatter directly,
// which proves a string can be built and nothing about whether anybody is shown
// it. The feature this file guards is "cctrace status says so", and that
// sentence has a second half.
func captureStatus(t *testing.T, profileName string) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	runErr := runStatus(profileName)
	os.Stdout = saved
	_ = w.Close()
	out, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if runErr != nil {
		t.Fatalf("status failed: %v\n%s", runErr, out)
	}
	return string(out)
}

// runningVersion stamps the binary the way a release build is stamped. A dev
// build never self-updates, so it never records a stall and never has one to
// report; leaving version as "dev" would suppress the notice for a reason that
// has nothing to do with what these tests are about.
func runningVersion(t *testing.T, v string) {
	t.Helper()
	previous := version
	version = v
	t.Cleanup(func() { version = previous })
}

func TestStatusPrintsAStalledUpdate(t *testing.T) {
	setupTestHome(t)
	reachableOTELEndpoint(t)
	runningVersion(t, "v0.7.32")
	noteUpdateFailure("", "v9.9.9", "apply: rename staged binary: permission denied", time.Now().Add(-time.Hour))

	out := captureStatus(t, "")
	if !strings.Contains(out, "permission denied") {
		t.Fatalf("the stall never reached the user's screen:\n%s", out)
	}
}

// The notice has to survive the shape of the profile it is read from. It was
// nested under the sync-endpoint block, but self-update falls back to
// Server.Endpoint when that is empty, so an install could stall and record it
// and still show nothing.
func TestStatusPrintsAStalledUpdateWithoutASyncEndpoint(t *testing.T) {
	setupTestHome(t)
	reachableOTELEndpoint(t)
	runningVersion(t, "v0.7.32")
	clearSyncEndpoint(t)
	noteUpdateFailure("", "v9.9.9", "apply: rename staged binary: permission denied", time.Now().Add(-time.Hour))

	out := captureStatus(t, "")
	if !strings.Contains(out, "permission denied") {
		t.Fatalf("a profile with no sync endpoint hid the stall:\n%s", out)
	}
}

// A notice that outlives the problem is worse than no notice: it is the record
// the next investigation starts from. The record survives a manual binary
// replacement, and the running version is what says it is over.
func TestStatusSaysNothingOnceTheVersionIsReached(t *testing.T) {
	setupTestHome(t)
	reachableOTELEndpoint(t)
	runningVersion(t, "v9.9.9") // already there

	noteUpdateFailure("", "v9.9.9", "apply: rename staged binary: permission denied", time.Now().Add(-time.Hour))

	out := captureStatus(t, "")
	if strings.Contains(out, "cannot update itself") {
		t.Fatalf("status reports a failure to reach the version it is running:\n%s", out)
	}
}

// The one-shot leg is not the manual escape hatch its comment claimed. The
// SessionEnd hook is `cctrace sync --daemon --once`, whose child runs
// `sync --once` -- so this leg fires on every session end, and left ungated it
// refetched the artifact while the daemon leg was backing off.
func TestTheOneShotLegBacksOffWhenItIsAutomated(t *testing.T) {
	now := stallTestClient(t, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))

	var downloads int32
	var serverVersion atomic.Value
	serverVersion.Store("v9.9.9")
	server := countingUpdateServer(t, &serverVersion, &downloads)

	previousLogToFile := syncLogToFile
	syncLogToFile = true // what spawnSyncProcess and the hooks set
	t.Cleanup(func() { syncLogToFile = previousLogToFile })

	closeLog := installDaemonLog("")
	defer closeLog()

	// Put a settled backoff in place through the daemon leg.
	for i := 0; i < 4; i++ {
		applyDaemonParentUpdateIfAvailable(context.Background(), "", server.URL, false)
		*now = now.Add(watchUpdateCheckInterval)
	}
	settled := atomic.LoadInt32(&downloads)

	client := newTestSyncClient(t, server.URL)
	for i := 0; i < 10; i++ {
		applyUpdateIfAvailable(context.Background(), client, server.URL, "")
	}
	if got := atomic.LoadInt32(&downloads); got != settled {
		t.Fatalf("the one-shot leg refetched %d times inside the backoff", got-settled)
	}
}

// Typing `cctrace sync` is a deliberate act and stays the way to force a retry
// after fixing an install. Only the automated legs wait.
func TestTypingSyncStillRetriesImmediately(t *testing.T) {
	now := stallTestClient(t, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))

	var downloads int32
	var serverVersion atomic.Value
	serverVersion.Store("v9.9.9")
	server := countingUpdateServer(t, &serverVersion, &downloads)

	previousLogToFile := syncLogToFile
	syncLogToFile = false // a person at a terminal
	t.Cleanup(func() { syncLogToFile = previousLogToFile })

	closeLog := installDaemonLog("")
	defer closeLog()

	for i := 0; i < 4; i++ {
		applyDaemonParentUpdateIfAvailable(context.Background(), "", server.URL, false)
		*now = now.Add(watchUpdateCheckInterval)
	}
	settled := atomic.LoadInt32(&downloads)

	client := newTestSyncClient(t, server.URL)
	applyUpdateIfAvailable(context.Background(), client, server.URL, "")
	if atomic.LoadInt32(&downloads) == settled {
		t.Fatal("a person who just fixed their install was made to wait out the backoff")
	}
}
