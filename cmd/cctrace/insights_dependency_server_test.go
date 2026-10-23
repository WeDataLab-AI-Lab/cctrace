package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cctrace/internal/dependencyaudit"
	"cctrace/internal/profile"
)

const (
	fakeUploadToken = "upload-fixture"
	fakeReadToken   = "read-fixture"
	newServerCaps   = `{"reenrich":true,"session_observations":true,"open_api_decision_source":true}`
	oldServerCaps   = `{"reenrich":true,"session_observations":true}`
	emptyPractices  = `{"window_start":"2026-01-01T00:00:00Z","window_end":"2026-01-31T00:00:00Z","rows":[],"self":{"manual_approvals_per_1000":null,"human_rejections_per_1000":null},"caveats":["correlation only"]}`
)

// fakeServer answers the capability route only to the upload token and the
// Open API only to the read token, as the server does. An empty body is a
// route the server does not have.
type fakeServer struct {
	caps, practices, events string
	admin                   bool

	mu    sync.Mutex
	paths []string
	query string
}

func (f *fakeServer) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		deny := func(status int, body string) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
		if r.URL.Path == "/api/sync/capabilities" {
			switch {
			case auth == fakeReadToken:
				deny(http.StatusForbidden, `{"code":"read_only_token","error":"read-only token"}`)
			case auth != fakeUploadToken:
				deny(http.StatusUnauthorized, `{"error":"unauthorized"}`)
			case f.caps == "":
				deny(http.StatusNotFound, "404 page not found")
			default:
				_, _ = w.Write([]byte(f.caps))
			}
			return
		}
		if auth == fakeUploadToken {
			deny(http.StatusForbidden, `{"code":"ingestion_token","error":"upload token cannot read"}`)
			return
		}
		if auth != fakeReadToken {
			deny(http.StatusUnauthorized, `{"error":"unauthorized"}`)
			return
		}
		switch r.URL.Path {
		case "/api/open/v1/organization-insights":
			if f.admin {
				_, _ = w.Write([]byte(`{"available":false}`))
				return
			}
			deny(http.StatusForbidden, `{"error":"forbidden"}`)
		case "/api/open/v1/practices":
			if f.practices == "" {
				deny(http.StatusNotFound, "404 page not found")
				return
			}
			_, _ = w.Write([]byte(f.practices))
		case "/api/open/v1/events":
			f.mu.Lock()
			f.query = r.URL.RawQuery
			f.mu.Unlock()
			_, _ = w.Write([]byte(f.events))
		default:
			deny(http.StatusNotFound, "404 page not found")
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeServer) requested(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.paths, path)
}

// localSession writes one synthetic Claude session holding a manual approval
// of toolu_local, an hour ago, and returns its home.
func localSession(t *testing.T) string {
	t.Helper()
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	home := filepath.Join(t.TempDir(), "claude")
	dir := filepath.Join(home, "projects", "-work-alpha")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		fmt.Sprintf(`{"type":"user","uuid":"u1","timestamp":%q,"sessionId":"sess-local","promptSource":"typed","message":{"role":"user","content":"run the tests"}}`, at),
		fmt.Sprintf(`{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":%q,"sessionId":"sess-local","message":{"id":"m1","role":"assistant","model":"test-model","content":[{"type":"tool_use","id":"toolu_local","name":"Bash","input":{"command":"go test ./..."}}],"usage":{"input_tokens":1,"output_tokens":1}}}`, at),
		fmt.Sprintf(`{"type":"user","uuid":"r1","parentUuid":"a1","timestamp":%q,"sessionId":"sess-local","permissionDecision":{"decision":"accept","source":"user_temporary"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_local","is_error":false,"content":"ok"}]}}`, at),
	}
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

func localDependency(t *testing.T) *dependencyaudit.Result {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	home := localSession(t)
	res, err := dependencyaudit.Run(t.Context(), dependencyaudit.Options{Agent: "claude", Since: now.Add(-30 * 24 * time.Hour), Now: now, ClaudeDirs: []string{home}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Signals.Permission.BySource["user_temporary"] != 1 {
		t.Fatalf("local fixture holds no manual approval: %+v", res.Signals.Permission)
	}
	return res
}

func serverProfile(endpoint, upload, read string) *profile.Profile {
	p := profile.NewDefault()
	p.Server.SyncEndpoint, p.Server.AuthToken, p.Server.ReadToken = endpoint, upload, read
	return p
}

func caveatCodes(res *dependencyaudit.Result) []string {
	var codes []string
	for _, c := range res.Caveats {
		codes = append(codes, c.Code)
	}
	return codes
}

func TestDependencyServerOldServerIsUnsupported(t *testing.T) {
	t.Parallel()
	f := &fakeServer{caps: oldServerCaps}
	res := localDependency(t)
	readDependencyServer(t.Context(), serverProfile(f.start(t), fakeUploadToken, fakeReadToken), res)
	codes := caveatCodes(res)
	if res.Server == nil || res.Server.DecisionSource != "unsupported" || res.Server.Decisions != nil || res.Server.Practices != nil ||
		!slices.Contains(codes, "decision_source_unsupported") || !slices.Contains(codes, "practices_unsupported") {
		t.Fatalf("server = %+v caveats %v", res.Server, codes)
	}
	if f.requested("/api/open/v1/events") || f.requested("/api/open/v1/organization-insights") {
		t.Fatal("an unsupported server was asked for decisions")
	}
}

func TestDependencyServerNewServerWithoutDecisions(t *testing.T) {
	t.Parallel()
	f := &fakeServer{caps: newServerCaps, practices: emptyPractices, events: `[]`}
	res := localDependency(t)
	readDependencyServer(t.Context(), serverProfile(f.start(t), fakeUploadToken, fakeReadToken), res)
	s := res.Server
	if s == nil || s.DecisionSource != "supported" || s.Decisions == nil || s.Decisions.Read != 0 || s.Practices == nil || len(s.Practices.Rows) != 0 ||
		!slices.Equal(s.Agents, []string{"claude"}) || s.Decisions.MergedBySource["user_temporary"] != 1 {
		raw, _ := json.Marshal(s)
		t.Fatalf("server = %s caveats %v", raw, caveatCodes(res))
	}
	for _, code := range caveatCodes(res) {
		if strings.HasPrefix(code, "server_") || code == "decision_source_unsupported" {
			t.Errorf("caveat %s on a supported server", code)
		}
	}
}

// Through the command: the profile's tokens, the decision query, and a merge
// where the local approval wins and another device's is kept.
func TestDependencyServerNewServerWithDecisions(t *testing.T) {
	// Not parallel: it runs the command, which reads the profile and homes
	// from HOME and CLAUDE_CONFIG_DIR, set here with t.Setenv.
	f := &fakeServer{caps: newServerCaps, practices: strings.Replace(emptyPractices, `"rows":[]`, `"rows":[{"kind":"skill","name":"review","users_with":4,"users_without":5,"metrics":{"manual_approvals_per_1000":{"with":10,"without":40},"human_rejections_per_1000":{"with":0,"without":3}}}]`, 1),
		events: `[{"event_name":"tool_decision","session_id":"sess-local","tool_use_id":"toolu_local","decision_source":"user_temporary","tool_name":"Bash","ts":"2026-01-02T00:00:00Z"},
			{"event_name":"tool_decision","session_id":"sess-laptop","tool_use_id":"toolu_remote","decision_source":"user_permanent","tool_name":"Edit","ts":"2026-01-02T00:00:00Z"},
			{"event_name":"tool_decision","session_id":"sess-laptop","decision_source":"hook","tool_name":"Read","ts":"2026-01-02T00:00:00Z"}]`}
	endpoint := f.start(t)
	setupTestHome(t)
	p, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.Server = serverProfile(endpoint, fakeUploadToken, fakeReadToken).Server
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", localSession(t))
	t.Setenv("CODEX_CONFIG_DIR", filepath.Join(t.TempDir(), "no-codex"))
	out, err := runInsights(t, "dependency", "--agent", "claude", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res dependencyaudit.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	d := res.Server.Decisions
	if d == nil || d.Read != 3 || d.LocalMatched != 1 || d.Kept != 1 || d.KeptBySource["user_permanent"] != 1 || d.OverlapUncertain["hook"] != 1 ||
		d.MergedBySource["user_temporary"] != 1 || d.MergedBySource["user_permanent"] != 1 {
		t.Fatalf("decisions = %+v\n%s", d, out)
	}
	if len(res.Server.Practices.Rows) != 1 || res.Server.Practices.Rows[0].Metrics.ManualApprovalsPer1000.Without != 40 {
		t.Fatalf("practices = %+v", res.Server.Practices)
	}
	if !strings.Contains(f.query, "event_name=tool_decision") || !strings.Contains(f.query, "decision_source=user_temporary%2Cuser_permanent%2Cuser_reject%2Cuser_abort%2Chook") {
		t.Errorf("decision query = %s", f.query)
	}
}

func TestDependencyServerPartialUpgradeKeepsDecisions(t *testing.T) {
	t.Parallel()
	f := &fakeServer{caps: newServerCaps, events: `[{"event_name":"tool_decision","session_id":"sess-laptop","tool_use_id":"toolu_remote","decision_source":"user_reject","tool_name":"Bash","ts":"2026-01-02T00:00:00Z"}]`}
	res := localDependency(t)
	readDependencyServer(t.Context(), serverProfile(f.start(t), fakeUploadToken, fakeReadToken), res)
	if res.Server == nil || res.Server.Practices != nil || res.Server.Decisions == nil || res.Server.Decisions.Kept != 1 ||
		!slices.Contains(caveatCodes(res), "practices_unsupported") {
		t.Fatalf("server = %+v caveats %v", res.Server, caveatCodes(res))
	}
}

func TestDependencyServerAdminTokenSkipsDecisions(t *testing.T) {
	t.Parallel()
	f := &fakeServer{caps: newServerCaps, practices: emptyPractices, events: `[]`, admin: true}
	res := localDependency(t)
	readDependencyServer(t.Context(), serverProfile(f.start(t), fakeUploadToken, fakeReadToken), res)
	if res.Server == nil || res.Server.Decisions != nil || res.Server.Practices == nil || !slices.Contains(caveatCodes(res), "admin_scope") {
		t.Fatalf("server = %+v caveats %v", res.Server, caveatCodes(res))
	}
	if f.requested("/api/open/v1/events") {
		t.Fatal("an administrator token read every user's decisions")
	}
}

// Without a read token nothing from the server is shown, whether the profile
// holds only the upload token (which the Open API refuses) or no token.
func TestDependencyServerMissingReadToken(t *testing.T) {
	t.Parallel()
	for name, upload := range map[string]string{"upload token only": fakeUploadToken, "no token": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := &fakeServer{caps: newServerCaps, practices: emptyPractices, events: `[]`}
			res := localDependency(t)
			readDependencyServer(t.Context(), serverProfile(f.start(t), upload, ""), res)
			if res.Server != nil || !slices.Contains(caveatCodes(res), "no_read_token") || f.requested("/api/open/v1/events") {
				t.Fatalf("server = %+v caveats %v", res.Server, caveatCodes(res))
			}
		})
	}
}

func TestDependencyServerUnreachableOrUnconfigured(t *testing.T) {
	t.Parallel()
	res := localDependency(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	readDependencyServer(t.Context(), serverProfile(closed.URL, fakeUploadToken, fakeReadToken), res)
	if res.Server != nil || !slices.Contains(caveatCodes(res), "server_read_failed") {
		t.Fatalf("server = %+v caveats %v", res.Server, caveatCodes(res))
	}
	res = localDependency(t)
	readDependencyServer(t.Context(), nil, res)
	if res.Server != nil || !slices.Contains(caveatCodes(res), "server_not_configured") {
		t.Fatalf("server = %+v caveats %v", res.Server, caveatCodes(res))
	}
}

// Without an upload token, or with one the server refuses, whether the server
// supports decision sources cannot be asked: decisions are left out with their
// own caveat, and the read token still reads practices.
func TestDependencyServerMissingUploadToken(t *testing.T) {
	t.Parallel()
	for name, upload := range map[string]string{"no upload token": "", "revoked": "revoked-fixture"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := &fakeServer{caps: newServerCaps, practices: emptyPractices, events: `[]`}
			res := localDependency(t)
			readDependencyServer(t.Context(), serverProfile(f.start(t), upload, fakeReadToken), res)
			codes := caveatCodes(res)
			if res.Server == nil || res.Server.DecisionSource != "unknown" || res.Server.Decisions != nil || res.Server.Practices == nil ||
				!slices.Contains(codes, "no_upload_token") || slices.Contains(codes, "decision_source_unsupported") {
				t.Fatalf("server = %+v caveats %v", res.Server, codes)
			}
		})
	}
}
