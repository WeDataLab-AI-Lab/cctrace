package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/profile"

	"github.com/spf13/cobra"
)

// #705: `cctrace auth read --profile <name>` stores a read token on a named
// profile, but ls, usage, events, projects, tools, plugins, skills, rules, and
// organization-insights loaded only the default profile and ignored
// --profile / CCTRACE_PROFILE, so that token could never be used. These
// commands now go through loadProfile, the same helper insights and auth read
// already used.
func TestReadCommandsAcceptNamedProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var defaultAuth, namedAuth string
	defaultSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer defaultSrv.Close()
	namedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		namedAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer namedSrv.Close()

	mk := func(endpoint, token string) *profile.Profile {
		p := profile.NewDefault()
		p.Server.SyncEndpoint = endpoint
		p.Server.ReadToken = token
		return p
	}
	if err := profile.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save(mk(defaultSrv.URL, "default-token")); err != nil {
		t.Fatal(err)
	}
	if err := profile.EnsureNamedDir("work"); err != nil {
		t.Fatal(err)
	}
	if err := profile.SaveNamed(mk(namedSrv.URL, "work-token"), "work"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		make func() *cobra.Command
		args []string
	}{
		{"ls", lsCmd, []string{"--json"}},
		{"usage", usageCmd, []string{"--json"}},
		{"events", eventsCmd, []string{"--session", "s1", "--json"}},
		{"projects", projectsCmd, []string{"--json"}},
		{"tools", func() *cobra.Command { return openAPIResourceCmd("tools", true) }, []string{"--json"}},
		{"plugins", func() *cobra.Command { return openAPIResourceCmd("plugins", true) }, []string{"--json"}},
		{"skills", func() *cobra.Command { return openAPIResourceCmd("skills", true) }, []string{"--json"}},
		{"rules", func() *cobra.Command { return openAPIResourceCmd("rules", false) }, []string{"--json"}},
		{"organization-insights", func() *cobra.Command { return openAPIResourceCmd("organization-insights", true) }, []string{"--json"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// --profile work must reach the named profile's endpoint and
			// token, and must never touch the default profile.
			defaultAuth, namedAuth = "", ""
			captureOutput(t, func() {
				cmd := tc.make()
				cmd.SetArgs(append(append([]string{}, tc.args...), "--profile", "work"))
				if err := cmd.Execute(); err != nil {
					t.Errorf("--profile work: %v", err)
				}
			})
			if namedAuth != "Bearer work-token" {
				t.Errorf("--profile work: named server saw Authorization %q, want Bearer work-token", namedAuth)
			}
			if defaultAuth != "" {
				t.Errorf("--profile work: default server was also hit (Authorization %q)", defaultAuth)
			}

			// CCTRACE_PROFILE=work must have the same effect as --profile
			// work when the flag itself is left empty.
			defaultAuth, namedAuth = "", ""
			t.Setenv("CCTRACE_PROFILE", "work")
			captureOutput(t, func() {
				cmd := tc.make()
				cmd.SetArgs(tc.args)
				if err := cmd.Execute(); err != nil {
					t.Errorf("CCTRACE_PROFILE=work: %v", err)
				}
			})
			if namedAuth != "Bearer work-token" {
				t.Errorf("CCTRACE_PROFILE=work: named server saw Authorization %q, want Bearer work-token", namedAuth)
			}
			t.Setenv("CCTRACE_PROFILE", "")

			// With neither set, behavior must stay exactly what it was
			// before #705: the default profile's token and endpoint.
			defaultAuth, namedAuth = "", ""
			captureOutput(t, func() {
				cmd := tc.make()
				cmd.SetArgs(tc.args)
				if err := cmd.Execute(); err != nil {
					t.Errorf("default profile: %v", err)
				}
			})
			if defaultAuth != "Bearer default-token" {
				t.Errorf("default profile: default server saw Authorization %q, want Bearer default-token", defaultAuth)
			}
			if namedAuth != "" {
				t.Errorf("default profile: named server was also hit (Authorization %q)", namedAuth)
			}
		})
	}
}

// An unknown --profile must fail with a clear, specific error instead of
// silently falling back to the default profile.
func TestReadCommandsRejectUnknownProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cases := []struct {
		name string
		make func() *cobra.Command
		args []string
	}{
		{"ls", lsCmd, []string{"--json"}},
		{"usage", usageCmd, []string{"--json"}},
		{"events", eventsCmd, []string{"--session", "s1", "--json"}},
		{"projects", projectsCmd, []string{"--json"}},
		{"tools", func() *cobra.Command { return openAPIResourceCmd("tools", true) }, []string{"--json"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			captureOutput(t, func() {
				cmd := tc.make()
				cmd.SetArgs(append(append([]string{}, tc.args...), "--profile", "nope"))
				err = cmd.Execute()
			})
			if err == nil || !strings.Contains(err.Error(), "not found") {
				t.Fatalf("err = %v, want a profile-not-found error", err)
			}
		})
	}
}
