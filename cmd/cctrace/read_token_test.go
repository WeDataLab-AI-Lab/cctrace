package main

import (
	"testing"

	"cctrace/internal/profile"
)

// The read API refuses the upload token init stores. A separate read_token lets
// one profile both sync and read, so read commands no longer need --profile.
func TestOpenAPITokenPrefersReadToken(t *testing.T) {
	p := profile.NewDefault()
	p.Server.AuthToken = "cct_upload"
	if got := openAPIToken(p); got != "cct_upload" {
		t.Fatalf("without read_token = %q, want the auth token (a web token stored there before read_token existed)", got)
	}
	p.Server.ReadToken = "cct_read"
	if got := openAPIToken(p); got != "cct_read" {
		t.Fatalf("with read_token = %q", got)
	}
}

func TestConfigReadTokenIsSettableAndMasked(t *testing.T) {
	p := profile.NewDefault()
	if err := setProfileField(p, "server.read_token", "cct_read-fixture"); err != nil {
		t.Fatalf("setProfileField: %v", err)
	}
	if p.Server.ReadToken != "cct_read-fixture" {
		t.Fatalf("ReadToken = %q", p.Server.ReadToken)
	}
	for _, s := range flattenProfile(p) {
		if s.key == "server.read_token" {
			if s.value == p.Server.ReadToken {
				t.Fatal("config list prints the read token unmasked")
			}
			return
		}
	}
	t.Fatal("server.read_token missing from config settings")
}

// Re-running init for another user or server must not keep reading with the old
// read token: openAPIToken prefers it, so commands would show the previous
// user's data or send that token to the new host.
func TestDropStaleReadToken(t *testing.T) {
	base := func() *profile.Profile {
		p := profile.NewDefault()
		p.User.ID = "alice"
		p.Server.SyncEndpoint = "https://cctrace.internal"
		p.Server.ReadToken = "cct_old"
		return p
	}
	for name, tc := range map[string]struct {
		change func(*profile.Profile)
		keep   bool
	}{
		"same user and server": {func(*profile.Profile) {}, true},
		"other user":           {func(p *profile.Profile) { p.User.ID = "bob" }, false},
		"other server":         {func(p *profile.Profile) { p.Server.SyncEndpoint = "https://cctrace.local" }, false},
		"trailing slash only":  {func(p *profile.Profile) { p.Server.SyncEndpoint = "https://cctrace.internal/" }, true},
	} {
		t.Run(name, func(t *testing.T) {
			existing, p := base(), base()
			tc.change(p)
			dropStaleReadToken(existing, p)
			if (p.Server.ReadToken != "") != tc.keep {
				t.Fatalf("read token = %q, keep = %v", p.Server.ReadToken, tc.keep)
			}
		})
	}
	p := base()
	dropStaleReadToken(nil, p)
	if p.Server.ReadToken != "cct_old" {
		t.Fatal("a fresh profile has no previous target to compare against")
	}
}
