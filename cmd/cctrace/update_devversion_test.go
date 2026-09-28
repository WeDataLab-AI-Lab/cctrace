package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The shapes deploy/push.sh can stamp into a build, pinned on the client side.
//
// Both environments derive the version from `git describe --tags --always
// --dirty`, so a release is a plain tag, an untagged build carries the
// commits-ahead form, and a build from a dirty tree gets a -dirty suffix. The
// client has to read all three, because a server reporting a version it cannot
// parse is indistinguishable from a server offering no update at all.
func TestVersionShapesPushShipsAreParseable(t *testing.T) {
	for _, v := range []string{
		"v0.7.10",              // tagged release
		"v0.7.10-152-gbb86a6d", // untagged build past a tag
		"v0.7.9-dirty",         // dirty tree: what prod reports today
		"v0.7.10-152-gbb86a6d-dirty",
	} {
		if parseSemver(v) == nil {
			t.Errorf("parseSemver(%q) = nil; push.sh can stamp this shape", v)
		}
	}
}

// The version comparison that decides whether a client updates, over the same
// shapes. The commits-ahead and -dirty suffixes are dropped, so a build past a
// tag reads as that tag, deliberately conservative: it will not push an update
// on the strength of an untagged build.
func TestUpdateDecisionOverRealVersionShapes(t *testing.T) {
	if !semverGT("v0.7.10", "v0.7.9-dirty") {
		t.Error("a release must be newer than the dirty build of the previous one")
	}
	if semverGT("v0.7.10-152-gbb86a6d", "v0.7.10") {
		t.Error("an untagged build past v0.7.10 must not read as newer than v0.7.10")
	}
	if semverGT("v0.7.9-dirty", "v0.7.10") {
		t.Error("an older dirty build must not read as newer")
	}
}

// The shape dev used to report. Kept as a regression pin: it parses to nothing,
// which is why the dev dashboard judged every client against it as unknown.
func TestLegacyDevVersionIsUnparseable(t *testing.T) {
	if parseSemver("dev-0f95f39") != nil {
		t.Error("dev-<sha> is expected to be unparseable; push.sh no longer stamps it")
	}
	if semverGT("dev-0f95f39", "v0.7.10") {
		t.Error("an unparseable server version must never be treated as newer")
	}
}

func TestApplyDaemonParentUpdateSkipsVersionCheckWhenDevBuild(t *testing.T) {
	// Given
	setupTestHome(t)
	previousVersion := version
	version = "dev"
	t.Cleanup(func() { version = previousVersion })
	var versionCheckRequested atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		versionCheckRequested.Store(true)
	}))
	t.Cleanup(server.Close)

	// When
	result := applyDaemonParentUpdateIfAvailable(context.Background(), "", server.URL, false)

	// Then
	if result != (daemonParentUpdateResult{}) {
		t.Errorf("result = %#v, want no update", result)
	}
	if versionCheckRequested.Load() {
		t.Error("dev build requested an update version check")
	}
}
