package main

import (
	"strings"
	"testing"
)

// A dev build refuses every update path, and the refusal is correct -- replacing
// a locally built binary would destroy whatever somebody was reproducing. It was
// silent, so the only way to learn it was to ask why automatic updates were not
// happening. status already answers "why is this binary not updating" for an
// unwritable directory; this is the other answer (#406).
func TestStatusSaysWhenTheBinaryIsADevBuild(t *testing.T) {
	setupTestHome(t)
	reachableOTELEndpoint(t)
	runningVersion(t, "dev")

	out := captureStatus(t, "")
	if !strings.Contains(out, "version=dev") {
		t.Errorf("status never says the binary is a dev build:\n%s", out)
	}
	// Naming the condition without naming the remedy leaves the reader where they
	// started -- asking somebody.
	if !strings.Contains(out, "릴리스 바이너리") {
		t.Errorf("status names the condition but not what to do about it:\n%s", out)
	}
}

// The notice has to be absent on a release build, or it becomes noise that means
// nothing -- which is how a warning stops being read.
func TestStatusIsSilentAboutDevOnAReleaseBuild(t *testing.T) {
	setupTestHome(t)
	reachableOTELEndpoint(t)
	runningVersion(t, "v0.7.49")

	out := captureStatus(t, "")
	if strings.Contains(out, "version=dev") {
		t.Errorf("a release build was reported as a dev build:\n%s", out)
	}
}
