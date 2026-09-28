package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The classification dictionary is server-only by design (see the isolation
// note on store.TaskClassifier). This pins that boundary structurally: if any
// future edit reintroduces a direct cmd/cctrace -> internal/store call path
// that imports internal/insights again (or internal/store starts importing it
// directly, bypassing the TaskClassifier interface), the client binary's
// dependency graph grows a package it has no business shipping -- today 76
// lines of keywords, tomorrow potentially a tokenizer dictionary or model.
func TestClientBinaryExcludesInsightsPackage(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./...").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "cctrace/internal/insights" {
			t.Fatalf("cmd/cctrace depends on cctrace/internal/insights -- classification must stay server-only (cmd/cctraced), see store.TaskClassifier")
		}
	}
}
