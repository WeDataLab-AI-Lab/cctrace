package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countingUpdateServer answers the version check and counts every artifact
// request. Artifacts are absent, so each attempt fails -- which is the shape
// that matters here. Why an attempt fails is the client's business; that it
// keeps repeating is this file's.
func countingUpdateServer(t *testing.T, serverVersion *atomic.Value, downloads *int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			_ = json.NewEncoder(w).Encode(map[string]string{"version": serverVersion.Load().(string)})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/downloads/") {
			atomic.AddInt32(downloads, 1)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

// stallTestClient stands the update path up with a signing key configured, since
// a client built without one refuses updates before reaching the network and
// would count zero downloads for the wrong reason.
func stallTestClient(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	setupTestHome(t)
	useProgressTTY(t, false)

	previousVersion := version
	version = "v0.0.1"
	t.Cleanup(func() { version = previousVersion })

	_, pub := newSigningKey(t)
	previousKey := updatePublicKeyBase64
	updatePublicKeyBase64 = pub
	t.Cleanup(func() { updatePublicKeyBase64 = previousKey })

	now := at
	previousClock := updateClock
	updateClock = func() time.Time { return now }
	t.Cleanup(func() { updateClock = previousClock })
	return &now
}

// A self-update that fails does not heal by being retried: production's darwin
// case was an install directory owned by root, where the replacement file cannot
// be created at all. The watch child re-checked every five minutes regardless,
// so that install fetched the same artifact 867 times in a week and stayed on
// the version it started with the whole time (#623).
func TestAFailingUpdateStopsRefetchingTheSameArtifact(t *testing.T) {
	now := stallTestClient(t, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))

	var downloads int32
	var serverVersion atomic.Value
	serverVersion.Store("v9.9.9")
	server := countingUpdateServer(t, &serverVersion, &downloads)

	closeLog := installDaemonLog("")
	for i := 0; i < 288; i++ { // one day of the watch child's five-minute checks
		applyDaemonParentUpdateIfAvailable(context.Background(), "", server.URL, false)
		*now = now.Add(watchUpdateCheckInterval)
	}
	closeLog()

	got := atomic.LoadInt32(&downloads)
	if got > 30 {
		t.Fatalf("%d fetches in a day; the unbacked-off loop is 288 and the point is to leave it", got)
	}
	// A backoff that stops retrying is a different bug: the user fixes their
	// install and the client has to notice within the hour.
	if got < 2 {
		t.Fatalf("%d fetches in a day; a stalled install must still recover on its own", got)
	}
}

// The artifact that failed is not the one now on offer. A release the user is
// waiting for must not sit behind a backoff earned by a different build.
func TestANewReleaseIsFetchedWithoutWaitingOutTheBackoff(t *testing.T) {
	now := stallTestClient(t, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))

	var downloads int32
	var serverVersion atomic.Value
	serverVersion.Store("v9.9.9")
	server := countingUpdateServer(t, &serverVersion, &downloads)

	closeLog := installDaemonLog("")
	defer closeLog()

	for i := 0; i < 4; i++ { // enough failures to push the retry an hour out
		applyDaemonParentUpdateIfAvailable(context.Background(), "", server.URL, false)
		*now = now.Add(watchUpdateCheckInterval)
	}
	settled := atomic.LoadInt32(&downloads)

	applyDaemonParentUpdateIfAvailable(context.Background(), "", server.URL, false)
	if atomic.LoadInt32(&downloads) != settled {
		t.Fatalf("the same version was refetched five minutes into an hour-long backoff")
	}

	serverVersion.Store("v9.9.10")
	*now = now.Add(watchUpdateCheckInterval)
	applyDaemonParentUpdateIfAvailable(context.Background(), "", server.URL, false)
	if atomic.LoadInt32(&downloads) == settled {
		t.Fatal("a new release was held back by a backoff the previous release earned")
	}
}

// Catching up clears the record, so a machine that was stalled last week does
// not carry a warning about a version it now runs.
func TestCatchingUpClearsTheStall(t *testing.T) {
	now := stallTestClient(t, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))

	var downloads int32
	var serverVersion atomic.Value
	serverVersion.Store("v9.9.9")
	server := countingUpdateServer(t, &serverVersion, &downloads)

	closeLog := installDaemonLog("")
	defer closeLog()

	applyDaemonParentUpdateIfAvailable(context.Background(), "", server.URL, false)
	if _, ok := readUpdateStall(""); !ok {
		t.Fatal("a failed update left no record for `cctrace status` to report")
	}

	serverVersion.Store(version) // the server is no longer ahead
	*now = now.Add(watchUpdateCheckInterval)
	applyDaemonParentUpdateIfAvailable(context.Background(), "", server.URL, false)
	if st, ok := readUpdateStall(""); ok {
		t.Fatalf("stall survived catching up: %#v", st)
	}
}

func TestUpdateRetryDelayIsBoundedAtAnHour(t *testing.T) {
	if got := updateRetryDelay(1); got != 15*time.Minute {
		t.Errorf("first retry after %s, want 15m", got)
	}
	if updateRetryDelay(2) <= updateRetryDelay(1) {
		t.Error("the delay has to grow, or the second failure costs as much as the first")
	}
	for _, n := range []int{3, 10, 1000} {
		if got := updateRetryDelay(n); got != time.Hour {
			t.Errorf("%d failures give %s, want the hour ceiling: the fix is on the user's side and an install that heals must not wait until tomorrow", n, got)
		}
	}
}

func TestStallCountRestartsOnADifferentVersion(t *testing.T) {
	setupTestHome(t)
	at := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

	noteUpdateFailure("", "v9.9.9", "apply: permission denied", at)
	noteUpdateFailure("", "v9.9.9", "apply: permission denied", at.Add(time.Hour))
	st, ok := readUpdateStall("")
	if !ok || st.Consecutive != 2 {
		t.Fatalf("consecutive = %#v, want 2 against the same version", st)
	}
	if !st.FirstAt.Equal(at) {
		t.Errorf("FirstAt = %s, want the first failure: status reports how long this has been going on", st.FirstAt)
	}

	noteUpdateFailure("", "v9.9.10", "apply: permission denied", at.Add(2*time.Hour))
	st, ok = readUpdateStall("")
	if !ok || st.Consecutive != 1 || st.Version != "v9.9.10" {
		t.Fatalf("a different version kept the old count: %#v", st)
	}
}

func TestClearUpdateStallForgetsTheRecord(t *testing.T) {
	setupTestHome(t)
	noteUpdateFailure("", "v9.9.9", "apply: permission denied", time.Now())
	clearUpdateStall("")
	if st, ok := readUpdateStall(""); ok {
		t.Fatalf("record survived: %#v", st)
	}
}

// The reason is the whole value of the notice. Production's answer sat in
// sync.log for ten days -- one line per attempt, in a file that rotates and that
// nobody opens unprompted -- while the version stayed put and nothing said why.
func TestUpdateStallNoticeCarriesTheReasonVerbatim(t *testing.T) {
	const reason = "apply: rename staged binary: permission denied"
	st := &updateStall{
		Version:     "v0.7.49",
		Consecutive: 2880,
		FirstAt:     time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC),
		LastAt:      time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC),
		Reason:      reason,
	}
	notice := updateStallNotice(st, "v0.7.32")
	if !strings.Contains(notice, reason) {
		t.Errorf("notice drops the reason, which is the only part that says where to look:\n%s", notice)
	}
	if !strings.Contains(notice, "v0.7.49") {
		t.Errorf("notice does not name the version being missed:\n%s", notice)
	}
	if !strings.Contains(notice, "2026-08-31") {
		t.Errorf("notice does not say how long this has been true:\n%s", notice)
	}
}

func TestUpdateStallNoticeIsSilentWhenNotStalled(t *testing.T) {
	if got := updateStallNotice(nil, "v0.7.32"); got != "" {
		t.Errorf("notice on a healthy install: %q", got)
	}
	if got := updateStallNotice(&updateStall{}, "v0.7.32"); got != "" {
		t.Errorf("notice from an empty record: %q", got)
	}
}

// The one failure this path must never have is a permanent one: a client that
// cannot update itself cannot be sent the fix for whatever is wrong with it.
//
// A clock that jumps forward and then back is enough to leave a record stamped
// in the future, and waiting out an hour measured from there parks the install
// until the calendar catches up. That is the self-reinforcing shape #623
// describes for linux, reintroduced by the code meant to bound it.
func TestAStallStampedInTheFutureDoesNotParkTheInstall(t *testing.T) {
	setupTestHome(t)
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	noteUpdateFailure("", "v9.9.9", "apply: permission denied", now.AddDate(1, 0, 0))

	if !updateStallReady("", "v9.9.9", now) {
		t.Fatal("a record from the future parked the install: self-update must never disable itself permanently")
	}
}
