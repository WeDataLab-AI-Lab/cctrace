package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/profile"
)

func TestRequestReadTokenStoresTokenOnProfile(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/cli/read-token" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"api_token":"cct_read","name":"CLI read token (laptop)"}`))
	}))
	defer srv.Close()

	p := profile.NewDefault()
	p.Server.SyncEndpoint = srv.URL + "/"
	p.User.ID = "alice"
	p.Server.AuthToken = "cct_upload"
	p.Server.ReadToken = "cct_previous"

	name, err := issueReadToken(p, "pw", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if p.Server.ReadToken != "cct_read" || p.Server.AuthToken != "cct_upload" || name != "CLI read token (laptop)" {
		t.Fatalf("profile server = %+v, name %q", p.Server, name)
	}
	// The held token is named so the server replaces exactly it, and never the
	// upload token.
	if got["user_id"] != "alice" || got["password"] != "pw" || got["device"] != "laptop" || got["replace"] != "cct_previous" {
		t.Fatalf("request body = %v", got)
	}
}

func TestRequestReadTokenErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
		admin  bool
	}{
		"administrator": {http.StatusForbidden, `{"code":"admin_read_token_forbidden","error":"x"}`, "Settings > API Access Tokens", true},
		"credentials":   {http.StatusUnauthorized, `{"error":"invalid credentials"}`, "invalid credentials", false},
		"password":      {http.StatusForbidden, `{"error":"password_change_required"}`, "change your password", false},
		"old server":    {http.StatusNotFound, `404 page not found`, "does not support", false},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			p := profile.NewDefault()
			p.Server.SyncEndpoint = srv.URL
			p.User.ID = "alice"

			_, err := issueReadToken(p, "pw", "laptop")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
			if errors.Is(err, errAdminReadToken) != tc.admin {
				t.Fatalf("admin sentinel = %v", errors.Is(err, errAdminReadToken))
			}
			if p.Server.ReadToken != "" {
				t.Fatal("stored a token on failure")
			}
		})
	}
}

func TestOfferReadTokenDuringInit(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"api_token":"cct_read","name":"CLI read token (laptop)"}`))
	}))
	defer srv.Close()

	for answer, want := range map[string]string{"": "cct_read", "y": "cct_read", "n": ""} {
		calls = 0
		p := profile.NewDefault()
		p.Server.SyncEndpoint = srv.URL
		p.User.ID = "alice"
		var out strings.Builder

		offerReadToken(p, "pw", "laptop", func(string, string) string { return answer }, &out)

		if p.Server.ReadToken != want || (want == "") != (calls == 0) {
			t.Errorf("answer %q: read token %q after %d calls\n%s", answer, p.Server.ReadToken, calls, out.String())
		}
	}
}

func TestOfferReadTokenFailureDoesNotAbortInit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"admin_read_token_forbidden"}`))
	}))
	defer srv.Close()
	p := profile.NewDefault()
	p.Server.SyncEndpoint = srv.URL
	p.User.ID = "root"
	var out strings.Builder

	offerReadToken(p, "pw", "laptop", func(string, string) string { return "y" }, &out)

	if p.Server.ReadToken != "" || !strings.Contains(out.String(), "Settings > API Access Tokens") {
		t.Fatalf("token %q, output:\n%s", p.Server.ReadToken, out.String())
	}
}

// Profiles init creates for additional Claude homes copy the default profile's
// server block, read token included. Replacing that token from one profile
// revokes it on the server, so every local copy of it has to move along.
func TestPropagateReadTokenUpdatesLocalCopiesOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	mk := func(user, endpoint, token string) *profile.Profile {
		p := profile.NewDefault()
		p.User.ID, p.User.Name, p.User.Email, p.User.Team = user, "N", "n@example.test", "t"
		p.Server.SyncEndpoint, p.Server.ReadToken, p.Server.AuthToken = endpoint, token, "cct_upload"
		return p
	}
	if err := profile.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save(mk("alice", "https://cctrace.internal", "cct_old")); err != nil {
		t.Fatal(err)
	}
	named := map[string]*profile.Profile{
		"claude-work":  mk("alice", "https://cctrace.internal/", "cct_old"), // same token, trailing slash
		"other-user":   mk("bob", "https://cctrace.internal", "cct_old"),
		"other-server": mk("alice", "https://cctrace.local", "cct_old"),
		"own-token":    mk("alice", "https://cctrace.internal", "cct_different"),
	}
	for name, p := range named {
		if err := profile.EnsureNamedDir(name); err != nil {
			t.Fatal(err)
		}
		if err := profile.SaveNamed(p, name); err != nil {
			t.Fatal(err)
		}
	}
	// The command ran on claude-work, which already holds the new token.
	source := mk("alice", "https://cctrace.internal/", "cct_new")

	updated, failed := propagateReadToken(source, "claude-work", "cct_old")

	if len(failed) != 0 || len(updated) != 1 || updated[0] != "default" {
		t.Fatalf("updated %v failed %v", updated, failed)
	}
	if p, _ := profile.Load(); p.Server.ReadToken != "cct_new" || p.Server.AuthToken != "cct_upload" {
		t.Fatalf("default profile server = %+v", p.Server)
	}
	for name, want := range map[string]string{"other-user": "cct_old", "other-server": "cct_old", "own-token": "cct_different", "claude-work": "cct_old"} {
		// claude-work is the source; saving it is the caller's job.
		if p, _ := profile.LoadNamed(name); p.Server.ReadToken != want {
			t.Errorf("%s read token = %q, want %q", name, p.Server.ReadToken, want)
		}
	}

	if u, f := propagateReadToken(source, "claude-work", ""); len(u)+len(f) != 0 {
		t.Fatalf("no previous token must touch nothing: %v %v", u, f)
	}
}
