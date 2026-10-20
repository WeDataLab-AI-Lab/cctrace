package main

import (
	"io"
	"path/filepath"
	"testing"

	"cctrace/internal/profile"
)

func TestExplicitMembershipRestrictsRuntimeRoots(t *testing.T) {
	t.Parallel()
	allow, _ := filepath.EvalSymlinks(t.TempDir())
	deny, _ := filepath.EvalSymlinks(t.TempDir())
	p := profile.NewDefault()
	p.HomesVersion = 1
	p.Homes = []profile.HomeMembership{{Provider: "codex", Home: allow, Transmission: "allow"}, {Provider: "codex", Home: deny, Transmission: "exclude"}}
	p.Options.CodexDirs = []string{deny}
	p.CodexHome = deny
	p.Options.CodexSyncEnabled = true
	dirs := resolveCodexScanDirs(io.Discard, p)
	if len(dirs) != 1 || dirs[0] != allow {
		t.Fatalf("runtime used unapproved roots: %v", dirs)
	}
	p.Homes = []profile.HomeMembership{{Provider: "claude", Home: deny, Transmission: "exclude"}}
	spec := profileJobSpec(profile.Entry{ID: "default", Name: "default", CollectionState: "enabled", Profile: p}, daemonOverrides{})
	if len(spec.Sources) != 0 {
		t.Fatalf("excluded Claude home allocated: %v", spec.Sources)
	}
}
