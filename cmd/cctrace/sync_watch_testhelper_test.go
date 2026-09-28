package main

import (
	"testing"
	"time"
)

// A watch test stops runSync by writing a stop request addressed to the running
// watcher's instance id (sync.go matches req.InstanceID == instanceID). Nothing
// else terminates the loop, so a test that fails to learn that id blocks forever.
//
// These bounds are deliberately generous. What they guard against is a hang, and a
// loaded CI runner must not be mistaken for one — the previous 2s ack wait gave up
// silently on slow Windows runners, leaving runSync watching until the package-level
// timeout failed every test in cmd/cctrace.
const (
	watchInstanceTimeout = 60 * time.Second
	watchStopTimeout     = 90 * time.Second
	watchPollInterval    = 10 * time.Millisecond
)

// watchInstanceID waits for the watcher to publish its instance id. The start-ack
// file is checked first, then the runtime file the watcher also writes — two
// independent sources, so one lagging does not strand the test.
func watchInstanceID(t *testing.T, ackPath string) (string, bool) {
	t.Helper()
	for deadline := time.Now().Add(watchInstanceTimeout); time.Now().Before(deadline); time.Sleep(watchPollInterval) {
		if ack, ok, err := readSyncStartAck(ackPath); err == nil && ok && ack.InstanceID != "" {
			return ack.InstanceID, true
		}
		if rt, ok, err := readSyncRuntime(""); err == nil && ok && rt.InstanceID != "" {
			return rt.InstanceID, true
		}
	}
	return "", false
}

// stopWatchWhenReady asks the watcher to stop as soon as it announces itself. The
// returned channel closes once the request has been written (or the id never
// arrived), so a test can assert on post-exit state without racing the writer.
func stopWatchWhenReady(t *testing.T, ackPath string) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		id, ok := watchInstanceID(t, ackPath)
		if !ok {
			// Not t.Fatalf: this runs off the test goroutine. runWatchUntilStopped
			// reports the resulting timeout, which is the actionable symptom.
			t.Errorf("watcher never published an instance id within %s", watchInstanceTimeout)
			return
		}
		if err := writeSyncStopRequest("", syncStopRequest{InstanceID: id, RequestedAt: time.Now().UTC()}); err != nil {
			t.Errorf("writeSyncStopRequest: %v", err)
		}
	}()
	return done
}

// stopWatchWhen is stopWatchWhenReady for tests that assert on what a pass did.
// Stopping as soon as the watcher announces itself races the first pass: under
// load (-race, the whole package in parallel) the stop request can land before
// any file is attempted, and the test then reads a log and a state file that no
// pass ever wrote. This waits until cond reports the pass has happened.
func stopWatchWhen(t *testing.T, ackPath string, cond func() bool) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		id, ok := watchInstanceID(t, ackPath)
		if !ok {
			t.Errorf("watcher never published an instance id within %s", watchInstanceTimeout)
			return
		}
		for deadline := time.Now().Add(watchInstanceTimeout); !cond(); time.Sleep(watchPollInterval) {
			if time.Now().After(deadline) {
				t.Errorf("the watch pass the test waits for did not happen within %s", watchInstanceTimeout)
				break
			}
		}
		if err := writeSyncStopRequest("", syncStopRequest{InstanceID: id, RequestedAt: time.Now().UTC()}); err != nil {
			t.Errorf("writeSyncStopRequest: %v", err)
		}
	}()
	return done
}

// runWatchUntilStopped runs a blocking watch call in the background so a watcher
// that never stops fails this one test with a clear message, instead of running out
// the package timeout and taking every other test in cmd/cctrace down with it.
func runWatchUntilStopped(t *testing.T, label string, run func() error) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- run() }()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	case <-time.After(watchStopTimeout):
		t.Fatalf("%s: watcher still running %s after the stop request; "+
			"it did not observe the request or never published its instance id",
			label, watchStopTimeout)
	}
}
