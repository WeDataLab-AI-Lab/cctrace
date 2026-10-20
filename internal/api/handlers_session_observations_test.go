package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"cctrace/internal/auth"
	"cctrace/internal/emailalias"
	"cctrace/internal/ingestblock"
	"cctrace/internal/projecthash"
	"cctrace/internal/store"
)

const observedAt = "2026-10-05T09:00:00Z"

// observationRecorder is a mockStore that keeps every batch the handler hands it
// and answers with a fixed stored count.
func observationRecorder(stored int) (*mockStore, *[]store.SessionObservationBatch) {
	var batches []store.SessionObservationBatch
	m := &mockStore{
		ingestSessionObservationsFn: func(ctx context.Context, b store.SessionObservationBatch) (store.SessionObservationResult, error) {
			batches = append(batches, b)
			return store.SessionObservationResult{Stored: stored}, nil
		},
	}
	return m, &batches
}

func postSessionObservations(t *testing.T, srv *Server, body string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/session-observations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cctrace-Version", "v0.7.60")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeObservationsResponse(t *testing.T, rec *httptest.ResponseRecorder) sessionObservationsResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var out sessionObservationsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func snapshotJSON(sessionID string) string {
	return `{"session_id":"` + sessionID + `","agent":"claude","project_hash":"ph","observed_at":"` + observedAt + `","collect_version":1,"snapshot":{}}`
}

func liveStateJSON(sessionID string) string {
	return `{"session_id":"` + sessionID + `","agent":"claude","project_hash":"ph","pid":1,"proc_start":"p","status":"busy","heartbeat_at":"` + observedAt + `"}`
}

func eventJSON(sessionID, eventID string) string {
	return `{"session_id":"` + sessionID + `","agent":"claude","project_hash":"ph","event_id":"` + eventID + `","kind":"session_end","observed_at":"` + observedAt + `","payload":{}}`
}

func repeatJSON(n int, item func(i int) string) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = item(i)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// Identity is the envelope's, after alias resolution, exactly as handleSync
// resolves it: an item names no user or profile of its own. login_email is the
// one identity an item may carry, and one without it takes the envelope's. The
// project hash is repaired on the way in, because the blocklist and every other
// table hold the repaired spelling. Every item keeps the columns an exclusion is
// judged by, the billing provider defaulted from the agent as sync defaults it.
func TestSessionObservations_storesWithEnvelopeIdentity(t *testing.T) {
	m, batches := observationRecorder(2)
	srv := newTestServer(m, nil).WithAliases(emailalias.Aliases{"alias@example.com": "canonical@example.com"})

	before := time.Now()
	rec := postSessionObservations(t, srv, `{
		"profile_email": "Alias@example.com", "user_id": "u-1", "login_email": "envelope@example.com",
		"heartbeat_at": "2030-01-01T00:00:00Z",
		"snapshots": [{"session_id": "s1", "agent": "claude", "project_hash": "Users-me-repo",
			"observed_at": "`+observedAt+`", "collect_version": 3, "snapshot": {"aiTitle": "t"}},
			{"session_id": "s2", "agent": "codex", "project_hash": "ph", "login_email": "own@example.com",
			"account_id": "acct-1", "observed_at": "`+observedAt+`", "snapshot": {}}],
		"live_states": [{"session_id": "s1", "agent": "claude", "project_hash": "ph", "pid": 4242,
			"proc_start": "Mon Oct  5 09:00:00 2026", "status": "waiting", "waiting_kind": "permission",
			"name": "n", "name_source": "auto", "status_changed_at": "`+observedAt+`", "heartbeat_at": "`+observedAt+`"}],
		"events": [{"session_id": "s1", "agent": "claude", "project_hash": "ph", "billing_provider": "amazon-bedrock",
			"event_id": "e1", "kind": "session_end", "observed_at": "`+observedAt+`"}]
	}`)
	after := time.Now()

	got := decodeObservationsResponse(t, rec)
	if got != (sessionObservationsResponse{Accepted: 4, Stored: 2, Skipped: 0}) {
		t.Errorf("response = %+v, want accepted 4, stored 2 (the store's own count), skipped 0", got)
	}
	if len(*batches) != 1 {
		t.Fatalf("store called %d times, want 1", len(*batches))
	}
	b := (*batches)[0]
	if len(b.Snapshots) != 2 || len(b.LiveStates) != 1 || len(b.Events) != 1 {
		t.Fatalf("batch = %d snapshots, %d live states, %d events; want 2, 1, 1",
			len(b.Snapshots), len(b.LiveStates), len(b.Events))
	}
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

	sn := b.Snapshots[0]
	if sn.ProfileEmail != "canonical@example.com" || sn.UserID != "u-1" {
		t.Errorf("snapshot identity = %q/%q, want the alias-resolved envelope canonical@example.com/u-1",
			sn.ProfileEmail, sn.UserID)
	}
	if sn.LoginEmail != "envelope@example.com" {
		t.Errorf("snapshot login_email = %q, want the envelope's for an item that names none", sn.LoginEmail)
	}
	if want := projecthash.Repair("Users-me-repo"); sn.ProjectHash != want {
		t.Errorf("snapshot project_hash = %q, want repaired %q", sn.ProjectHash, want)
	}
	// Re-encoded from its decoded form, so the client's spacing does not survive.
	if sn.SessionID != "s1" || sn.Agent != "claude" || sn.CollectVersion != 3 ||
		string(sn.Snapshot) != `{"aiTitle":"t"}` || !sn.ObservedAt.Equal(at) || sn.BillingProvider != "anthropic" {
		t.Errorf("snapshot = %+v (snapshot %s)", sn, sn.Snapshot)
	}
	if s2 := b.Snapshots[1]; s2.LoginEmail != "own@example.com" || s2.BillingProvider != "openai" || s2.AccountID != "acct-1" {
		t.Errorf("second snapshot = login %q, billing %q/%q; want its own login, codex's default provider and its account",
			s2.LoginEmail, s2.BillingProvider, s2.AccountID)
	}

	ls := b.LiveStates[0]
	if ls.ProfileEmail != "canonical@example.com" || ls.UserID != "u-1" || ls.LoginEmail != "envelope@example.com" {
		t.Errorf("live state identity = %q/%q, login %q", ls.ProfileEmail, ls.UserID, ls.LoginEmail)
	}
	if ls.PID != 4242 || ls.ProcStart != "Mon Oct  5 09:00:00 2026" || ls.Status != "waiting" ||
		ls.WaitingKind != "permission" || ls.Name != "n" || ls.NameSource != "auto" || ls.BillingProvider != "anthropic" ||
		ls.StatusChangedAt == nil || !ls.StatusChangedAt.Equal(at) || !ls.HeartbeatAt.Equal(at) {
		t.Errorf("live state = %+v", ls)
	}

	ev := b.Events[0]
	if ev.ProfileEmail != "canonical@example.com" || ev.UserID != "u-1" || ev.EventID != "e1" ||
		ev.Kind != "session_end" || !ev.ObservedAt.Equal(at) || ev.LoginEmail != "envelope@example.com" ||
		ev.BillingProvider != "amazon-bedrock" {
		t.Errorf("event = %+v", ev)
	}
	if string(ev.Payload) != `{}` {
		t.Errorf("event payload = %q, want {} for an event that sent none", ev.Payload)
	}

	hb := b.Heartbeat
	if hb == nil {
		t.Fatal("heartbeat_at was sent but no heartbeat reached the store")
	}
	if hb.ProfileEmail != "canonical@example.com" || hb.UserID != "u-1" || hb.ClientVersion != "v0.7.60" {
		t.Errorf("heartbeat = %+v, want the envelope identity and the X-Cctrace-Version header", hb)
	}
	// The client said 2030. The row is dated by the server's clock on receipt.
	if hb.HeartbeatAt.Before(before) || hb.HeartbeatAt.After(after) {
		t.Errorf("heartbeat_at = %s, want the receipt time, between %s and %s", hb.HeartbeatAt, before, after)
	}
}

// The collector is alive whether or not a session has anything to report, so a
// heartbeat alone is a whole request.
func TestSessionObservations_heartbeatOnly(t *testing.T) {
	m, batches := observationRecorder(0)
	srv := newTestServer(m, nil)

	got := decodeObservationsResponse(t, postSessionObservations(t, srv,
		`{"profile_email":"p@example.com","user_id":"u-1","heartbeat_at":"`+observedAt+`"}`))

	if got != (sessionObservationsResponse{}) {
		t.Errorf("response = %+v, want all zero: a heartbeat is not an item", got)
	}
	if len(*batches) != 1 {
		t.Fatalf("store called %d times, want 1", len(*batches))
	}
	b := (*batches)[0]
	if len(b.Snapshots)+len(b.LiveStates)+len(b.Events) != 0 {
		t.Errorf("batch carries items: %+v", b)
	}
	if b.Heartbeat == nil || b.Heartbeat.ProfileEmail != "p@example.com" || b.Heartbeat.UserID != "u-1" {
		t.Errorf("heartbeat = %+v", b.Heartbeat)
	}
}

// The heartbeat row is keyed by identity. Without a profile_email it is not
// written -- the guard handleSync keeps on the client version -- while the items
// beside it still are.
func TestSessionObservations_noHeartbeatWithoutProfileEmail(t *testing.T) {
	m, batches := observationRecorder(1)
	srv := newTestServer(m, nil)

	got := decodeObservationsResponse(t, postSessionObservations(t, srv,
		`{"user_id":"u-1","heartbeat_at":"`+observedAt+`","snapshots":[`+snapshotJSON("s1")+`]}`))

	if got.Accepted != 1 || got.Skipped != 0 {
		t.Errorf("response = %+v, want the snapshot accepted", got)
	}
	b := (*batches)[0]
	if b.Heartbeat != nil {
		t.Errorf("heartbeat = %+v, want none for a request without profile_email", b.Heartbeat)
	}
	if len(b.Snapshots) != 1 {
		t.Errorf("snapshots = %d, want the item stored all the same", len(b.Snapshots))
	}
}

// ingestAuthenticator is the ingest middleware as cctraced builds it: the global
// key, then per-user tokens. Its validator accepts every other token, as the real
// one does a token whose account was active when the request arrived. The
// identity a route binds to is the token this middleware left in the context, so
// a test of that binding has to go through it.
func ingestAuthenticator() *auth.Authenticator {
	return auth.New("global-key", auth.WithTokenValidator(func(context.Context, string) (bool, error) {
		return true, nil
	}))
}

// An observation overwrites a row, so a per-user token decides whose rows it
// writes, as it does on the OTLP receivers (#535): profile_email and user_id come
// from the token's account, never from the payload. login_email names the
// Anthropic account, which the token does not prove, so it stays the payload's.
// The token is the one the ingest middleware accepted. The global key names no
// user and leaves none, so a request carrying it keeps the payload's identity, as
// does every request with authentication disabled -- nothing authenticated its
// token, if it has one, so there is nothing to bind to.
func TestSessionObservations_bindsIdentityToPerUserToken(t *testing.T) {
	const body = `{"profile_email":"bob@example.com","user_id":"u-bob","login_email":"bob-login@example.com",
		"heartbeat_at":"` + observedAt + `",
		"snapshots":[{"session_id":"s1","project_hash":"ph","observed_at":"` + observedAt + `","snapshot":{}}],
		"live_states":[{"session_id":"s1","project_hash":"ph","pid":1,"status":"busy","heartbeat_at":"` + observedAt + `"}],
		"events":[{"session_id":"s1","project_hash":"ph","event_id":"e","kind":"session_end","observed_at":"` + observedAt + `"}]}`
	alice := &store.DashboardUser{Email: "alice@example.com", CctraceUserID: "u-alice", IsActive: true}

	for _, tc := range []struct {
		name         string
		authDisabled bool
		header       string
		lookup       func(token string) (*store.DashboardUser, error)
		wantProfile  string
		wantUser     string
		wantLookups  int
	}{
		{
			name:   "per-user token",
			header: "Bearer tok-alice",
			lookup: func(token string) (*store.DashboardUser, error) {
				if token != "tok-alice" {
					return nil, fmt.Errorf("unexpected token %q", token)
				}
				return alice, nil
			},
			wantProfile: "alice@example.com", wantUser: "u-alice", wantLookups: 1,
		},
		{
			// The middleware accepts a token without the scheme too; so does this.
			name:        "per-user token without the Bearer prefix",
			header:      "tok-alice",
			lookup:      func(string) (*store.DashboardUser, error) { return alice, nil },
			wantProfile: "alice@example.com", wantUser: "u-alice", wantLookups: 1,
		},
		{
			// The account's user id even when it has none. Keeping the payload's
			// would let a token whose account lost its cctrace_user_id write under
			// another user's, and observations are joined to the rest on
			// (session_id, user_id).
			name:   "account without a cctrace user id writes none",
			header: "Bearer tok-alice",
			lookup: func(string) (*store.DashboardUser, error) {
				return &store.DashboardUser{Email: "alice@example.com", IsActive: true}, nil
			},
			wantProfile: "alice@example.com", wantUser: "", wantLookups: 1,
		},
		{
			// MergeUsers leaves dashboard_users alone, so a merged-away address can
			// keep a live account and token. Its address is alias-resolved, as the
			// payload's is, or its observations would land under the old address
			// while its sync records land under the canonical one.
			name:   "account under a merged-away address",
			header: "Bearer tok-alice",
			lookup: func(string) (*store.DashboardUser, error) {
				return &store.DashboardUser{Email: "old-alice@example.com", CctraceUserID: "u-alice", IsActive: true}, nil
			},
			wantProfile: "alice@example.com", wantUser: "u-alice", wantLookups: 1,
		},
		{
			name:        "global key",
			header:      "Bearer global-key",
			lookup:      func(string) (*store.DashboardUser, error) { return alice, nil },
			wantProfile: "bob@example.com", wantUser: "u-bob", wantLookups: 0,
		},
		{
			name:         "authentication disabled, no token",
			authDisabled: true,
			lookup:       func(string) (*store.DashboardUser, error) { return alice, nil },
			wantProfile:  "bob@example.com", wantUser: "u-bob", wantLookups: 0,
		},
		{
			name:         "authentication disabled, a bearer token",
			authDisabled: true,
			header:       "Bearer tok-alice",
			lookup:       func(string) (*store.DashboardUser, error) { return alice, nil },
			wantProfile:  "bob@example.com", wantUser: "u-bob", wantLookups: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, batches := observationRecorder(3)
			lookups := 0
			m.getDashboardUserByAPITokenFn = func(_ context.Context, token string) (*store.DashboardUser, error) {
				lookups++
				return tc.lookup(token)
			}
			authn := ingestAuthenticator()
			if tc.authDisabled {
				authn = auth.New("")
			}
			srv := newTestServer(m, nil, authn.HTTPMiddleware).
				WithAliases(emailalias.Aliases{"old-alice@example.com": "alice@example.com"})
			var header []string
			if tc.header != "" {
				header = []string{"Authorization", tc.header}
			}
			decodeObservationsResponse(t, postSessionObservations(t, srv, body, header...))

			if lookups != tc.wantLookups {
				t.Errorf("token lookups = %d, want %d", lookups, tc.wantLookups)
			}
			b := (*batches)[0]
			if len(b.Snapshots) != 1 || len(b.LiveStates) != 1 || len(b.Events) != 1 || b.Heartbeat == nil {
				t.Fatalf("batch = %+v, want one of each and a heartbeat", b)
			}
			for what, id := range map[string][2]string{
				"snapshot":   {b.Snapshots[0].ProfileEmail, b.Snapshots[0].UserID},
				"live state": {b.LiveStates[0].ProfileEmail, b.LiveStates[0].UserID},
				"event":      {b.Events[0].ProfileEmail, b.Events[0].UserID},
				"heartbeat":  {b.Heartbeat.ProfileEmail, b.Heartbeat.UserID},
			} {
				if id != [2]string{tc.wantProfile, tc.wantUser} {
					t.Errorf("%s identity = %s/%s, want %s/%s", what, id[0], id[1], tc.wantProfile, tc.wantUser)
				}
			}
			provenance := "shared_key_claim"
			if tc.wantLookups == 1 {
				provenance = "personal_token"
			}
			if b.Snapshots[0].IdentityProvenance != provenance || b.LiveStates[0].IdentityProvenance != provenance || b.Events[0].IdentityProvenance != provenance {
				t.Errorf("observation provenance = %q/%q/%q, want %q", b.Snapshots[0].IdentityProvenance, b.LiveStates[0].IdentityProvenance, b.Events[0].IdentityProvenance, provenance)
			}
			if b.Snapshots[0].LoginEmail != "bob-login@example.com" || b.LiveStates[0].LoginEmail != "bob-login@example.com" {
				t.Errorf("login_email = %q/%q, want the payload's: a cctrace token proves no Anthropic account",
					b.Snapshots[0].LoginEmail, b.LiveStates[0].LoginEmail)
			}
		})
	}
}

// A per-user token that cannot be resolved is a 503 and nothing is stored, as on
// the OTLP HTTP receiver. Falling back to the payload is what let a token revoked
// mid-upload -- accepted by the middleware, gone by the time of the lookup --
// write under whatever identity the payload named.
func TestSessionObservations_unresolvableTokenIs503(t *testing.T) {
	body := `{"profile_email":"bob@example.com","user_id":"u-bob","heartbeat_at":"` + observedAt + `",
		"snapshots":[` + snapshotJSON("s1") + `]}`
	for _, tc := range []struct {
		name   string
		lookup func() (*store.DashboardUser, error)
	}{
		{"lookup failed", func() (*store.DashboardUser, error) { return nil, errors.New("db down") }},
		// What Postgres answers for a token revoked or rotated after the middleware
		// accepted it: no active token row.
		{"token revoked mid-upload", func() (*store.DashboardUser, error) { return nil, errors.New("no rows in result set") }},
		{"inactive account", func() (*store.DashboardUser, error) {
			return &store.DashboardUser{Email: "alice@example.com", CctraceUserID: "u-alice"}, nil
		}},
		{"no account", func() (*store.DashboardUser, error) { return nil, nil }},
		// Unreachable through the app (an account cannot be created or edited to
		// have no address), but the payload's address must not stand in for it.
		{"account without an address", func() (*store.DashboardUser, error) {
			return &store.DashboardUser{IsActive: true, CctraceUserID: "u-alice"}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, batches := observationRecorder(1)
			m.getDashboardUserByAPITokenFn = func(context.Context, string) (*store.DashboardUser, error) { return tc.lookup() }
			srv := newTestServer(m, nil, ingestAuthenticator().HTTPMiddleware)

			rec := postSessionObservations(t, srv, body, "Authorization", "Bearer tok-alice")
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
			}
			if len(*batches) != 0 {
				t.Errorf("store was called with %+v, want nothing written", *batches)
			}
		})
	}
}

// Each of handleSync's refusals applies to every kind of item, and each is a
// skip, never a 4xx: the client would retry a decision that is not going to
// change. Only the refused item goes; the one beside it is stored.
func TestSessionObservations_skipsRefusedItems(t *testing.T) {
	for _, tc := range []struct {
		name    string
		block   func(bl *ingestblock.Cache)
		refused string // one item, in the request's items JSON form
	}{
		{
			name:    "deleted session",
			block:   func(bl *ingestblock.Cache) { bl.AddSession("gone") },
			refused: `"snapshots":[` + snapshotJSON("gone") + `]`,
		},
		{
			// Added in the repaired form: that is the only form blocked_projects
			// can hold, so the item's raw hash has to be repaired to match it.
			name:    "blocked project",
			block:   func(bl *ingestblock.Cache) { bl.AddProject(projecthash.Repair("ph-blocked")) },
			refused: `"live_states":[{"session_id":"s-blocked","project_hash":"ph-blocked","pid":1,"status":"idle","heartbeat_at":"` + observedAt + `"}]`,
		},
		{
			name:    "excluded billing account",
			block:   func(bl *ingestblock.Cache) { bl.AddExcludedAccount("openai", "acct-personal") },
			refused: `"events":[{"session_id":"s-acct","project_hash":"ph","billing_provider":"openai","account_id":"acct-personal","event_id":"e","kind":"session_end","observed_at":"` + observedAt + `"}]`,
		},
		{
			// No billing_provider: the agent's default stands in, as it does for
			// sync, or an account named by id alone would slip through.
			name:    "excluded billing account, provider from agent",
			block:   func(bl *ingestblock.Cache) { bl.AddExcludedAccount("openai", "acct-personal") },
			refused: `"snapshots":[{"session_id":"s-codex","agent":"codex","project_hash":"ph","account_id":"acct-personal","observed_at":"` + observedAt + `","snapshot":{}}]`,
		},
		{
			name:    "excluded login email on the item",
			block:   func(bl *ingestblock.Cache) { bl.AddExcludedEmail("excluded@example.test") },
			refused: `"live_states":[{"session_id":"s-email","project_hash":"ph","login_email":"Excluded@example.test","pid":1,"status":"shell","heartbeat_at":"` + observedAt + `"}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, batches := observationRecorder(1)
			bl := ingestblock.New()
			tc.block(bl)
			srv := newTestServer(m, nil).WithIngestBlocklist(bl)

			// The kept item travels in a different array from the refused one, so
			// a skip that took the whole array would show.
			kept := `"events":[` + eventJSON("kept", "k1") + `]`
			if strings.HasPrefix(tc.refused, `"events"`) {
				kept = `"snapshots":[` + snapshotJSON("kept") + `]`
			}
			got := decodeObservationsResponse(t, postSessionObservations(t, srv,
				`{"profile_email":"p@example.com","user_id":"u-1",`+tc.refused+`,`+kept+`}`))

			if got.Accepted != 2 || got.Skipped != 1 {
				t.Errorf("response = %+v, want accepted 2, skipped 1", got)
			}
			b := (*batches)[0]
			var ids []string
			for _, sn := range b.Snapshots {
				ids = append(ids, sn.SessionID)
			}
			for _, ls := range b.LiveStates {
				ids = append(ids, ls.SessionID)
			}
			for _, ev := range b.Events {
				ids = append(ids, ev.SessionID)
			}
			if len(ids) != 1 || ids[0] != "kept" {
				t.Errorf("stored sessions %v, want only the one nothing refuses", ids)
			}
		})
	}
}

// An excluded envelope address reaches every item that names no address of its
// own, and only those.
func TestSessionObservations_envelopeLoginEmailIsJudgedPerItem(t *testing.T) {
	m, batches := observationRecorder(1)
	bl := ingestblock.New()
	bl.AddExcludedEmail("excluded@example.test")
	srv := newTestServer(m, nil).WithIngestBlocklist(bl)

	got := decodeObservationsResponse(t, postSessionObservations(t, srv, `{
		"profile_email":"p@example.com","user_id":"u-1","login_email":"excluded@example.test",
		"snapshots":[`+snapshotJSON("inherits")+`,
			{"session_id":"own","project_hash":"ph","login_email":"fine@example.test","observed_at":"`+observedAt+`","snapshot":{}}]}`))

	if got.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", got.Skipped)
	}
	b := (*batches)[0]
	if len(b.Snapshots) != 1 || b.Snapshots[0].SessionID != "own" {
		t.Errorf("stored %+v, want only the snapshot with its own, unexcluded address", b.Snapshots)
	}
}

// Malformed input, over-limit counts and anything Postgres would refuse later are
// the 400s, and they take the whole request: nothing in it reaches the store.
// Every item here carries what a valid one does, so each case fails for the one
// reason it names.
func TestSessionObservations_rejectsMalformedOrOverLimit(t *testing.T) {
	bigSnapshot := `{"t":"` + strings.Repeat("a", maxSessionSnapshotBytes) + `"}`
	bigPayload := `{"t":"` + strings.Repeat("a", maxSessionEventPayloadBytes) + `"}`
	key := strings.Repeat("k", maxSessionObservationKeyBytes+1)
	tag := strings.Repeat("t", maxSessionObservationTagBytes+1)
	snapshotWith := func(fields string) string {
		return `{"snapshots":[{"session_id":"s","project_hash":"ph","observed_at":"` + observedAt + `","snapshot":{}` + fields + `}]}`
	}
	liveStateWith := func(fields string) string {
		return `{"live_states":[{"session_id":"s","project_hash":"ph","pid":1,"status":"busy","heartbeat_at":"` + observedAt + `"` + fields + `}]}`
	}
	eventWith := func(fields string) string {
		return `{"events":[{"session_id":"s","project_hash":"ph","event_id":"e","kind":"session_end","observed_at":"` + observedAt + `"` + fields + `}]}`
	}
	// A named value, not an expression written in place: a 32-character token with a
	// digit in it is what the mirror export reads as a secret.
	liveStatesOver := maxSessionLiveStatesPerRequest + 1
	for _, tc := range []struct{ name, body string }{
		{"not json", `{"snapshots":`},
		{"wrong type", `{"snapshots":{}}`},
		{"too many snapshots", `{"snapshots":` + repeatJSON(maxSessionSnapshotsPerRequest+1, func(i int) string { return snapshotJSON(fmt.Sprint("s", i)) }) + `}`},
		{"too many live states", `{"live_states":` + repeatJSON(liveStatesOver, func(i int) string { return liveStateJSON(fmt.Sprint("s", i)) }) + `}`},
		{"too many events", `{"events":` + repeatJSON(maxSessionEventsPerRequest+1, func(i int) string { return eventJSON("s", fmt.Sprint("e", i)) }) + `}`},
		{"zero heartbeat", `{"heartbeat_at":"0001-01-01T00:00:00Z"}`},
		{"snapshot without session", `{"snapshots":[{"project_hash":"ph","observed_at":"` + observedAt + `","snapshot":{}}]}`},
		{"snapshot without project", `{"snapshots":[{"session_id":"s","observed_at":"` + observedAt + `","snapshot":{}}]}`},
		{"snapshot without time", `{"snapshots":[{"session_id":"s","project_hash":"ph","snapshot":{}}]}`},
		{"snapshot missing", `{"snapshots":[{"session_id":"s","project_hash":"ph","observed_at":"` + observedAt + `"}]}`},
		{"snapshot null", `{"snapshots":[{"session_id":"s","project_hash":"ph","observed_at":"` + observedAt + `","snapshot":null}]}`},
		{"snapshot not an object", `{"snapshots":[{"session_id":"s","project_hash":"ph","observed_at":"` + observedAt + `","snapshot":["x"]}]}`},
		{"snapshot over its cap", `{"snapshots":[{"session_id":"s","project_hash":"ph","observed_at":"` + observedAt + `","snapshot":` + bigSnapshot + `}]}`},
		// Valid JSON syntax with a byte that is not UTF-8: jsonb would refuse it,
		// so the client hears it here instead of retrying a 500.
		{"snapshot not UTF-8", `{"snapshots":[{"session_id":"s","project_hash":"ph","observed_at":"` + observedAt + `","snapshot":{"t":"` + "\xff" + `"}}]}`},
		// The column is INT; a larger number must be the client's 400, not the
		// database's 500.
		{"collect_version beyond INT", snapshotWith(`,"collect_version":2147483648`)},
		{"live state without session", `{"live_states":[{"project_hash":"ph","pid":1,"status":"busy","heartbeat_at":"` + observedAt + `"}]}`},
		{"live state without project", `{"live_states":[{"session_id":"s","pid":1,"status":"busy","heartbeat_at":"` + observedAt + `"}]}`},
		{"live state unknown status", liveStateWith(`,"status":"sleeping"`)},
		{"live state unknown waiting kind", liveStateWith(`,"waiting_kind":"coffee"`)},
		{"live state without pid", `{"live_states":[{"session_id":"s","project_hash":"ph","status":"busy","heartbeat_at":"` + observedAt + `"}]}`},
		{"live state negative pid", liveStateWith(`,"pid":-1`)},
		{"live state without heartbeat", `{"live_states":[{"session_id":"s","project_hash":"ph","pid":1,"status":"busy"}]}`},
		{"live state bad time", liveStateWith(`,"heartbeat_at":"yesterday"`)},
		{"event without session", `{"events":[{"project_hash":"ph","event_id":"e","kind":"session_end","observed_at":"` + observedAt + `"}]}`},
		{"event without project", `{"events":[{"session_id":"s","event_id":"e","kind":"session_end","observed_at":"` + observedAt + `"}]}`},
		{"event without id", `{"events":[{"session_id":"s","project_hash":"ph","kind":"session_end","observed_at":"` + observedAt + `"}]}`},
		{"event unknown kind", eventWith(`,"kind":"resumed"`)},
		// The server derives state history from live states; a client's own would
		// record each change twice.
		{"event kind state_change from a client", eventWith(`,"kind":"state_change"`)},
		{"event kind gone from a client", eventWith(`,"kind":"gone"`)},
		{"event without time", `{"events":[{"session_id":"s","project_hash":"ph","event_id":"e","kind":"session_end"}]}`},
		{"event payload not an object", eventWith(`,"payload":"x"`)},
		{"event payload over its cap", eventWith(`,"payload":` + bigPayload)},
		{"event payload not UTF-8", eventWith(`,"payload":{"t":"` + "\xc3" + `"}`)},
		// Numbers jsonb would refuse -- a 500 the client retries forever -- or
		// store and read back as 131,072 digits. ParseFloat takes 1e-999999 as 0.
		{"snapshot number 1e999999", snapshotWith(`,"snapshot":{"n":1e999999}`)},
		{"snapshot number 1e-999999", snapshotWith(`,"snapshot":{"n":1e-999999}`)},
		{"snapshot number of 20,000 fraction digits", snapshotWith(`,"snapshot":{"n":0.` + strings.Repeat("1", 20000) + `}`)},
		{"snapshot number 1e131071", snapshotWith(`,"snapshot":{"n":1e131071}`)},
		{"snapshot number nested in an array", snapshotWith(`,"snapshot":{"a":[{"n":1E+309}]}`)},
		{"snapshot number literal one byte over its cap", snapshotWith(`,"snapshot":{"n":1.` + strings.Repeat("0", maxSessionJSONNumberBytes-1) + `}`)},
		{"payload number 1e999999", eventWith(`,"payload":{"n":1e999999}`)},
		{"payload number 1e-999999", eventWith(`,"payload":{"n":-1e-999999}`)},
		// Text caps: 256 bytes for keys and identities, 64 for the short labels.
		{"envelope profile_email over its cap", `{"profile_email":"` + key + `"}`},
		{"envelope user_id over its cap", `{"user_id":"` + key + `"}`},
		{"envelope login_email over its cap", `{"login_email":"` + key + `"}`},
		{"session_id over its cap", snapshotWith(`,"session_id":"` + key + `"`)},
		{"project_hash over its cap", snapshotWith(`,"project_hash":"` + key + `"`)},
		{"item login_email over its cap", snapshotWith(`,"login_email":"` + key + `"`)},
		{"billing_provider over its cap", eventWith(`,"billing_provider":"` + key + `"`)},
		{"account_id over its cap", eventWith(`,"account_id":"` + key + `"`)},
		{"agent over its cap", liveStateWith(`,"agent":"` + tag + `"`)},
		{"proc_start over its cap", liveStateWith(`,"proc_start":"` + key + `"`)},
		{"name_source over its cap", liveStateWith(`,"name_source":"` + tag + `"`)},
		{"event_id over its cap", eventWith(`,"event_id":"` + key + `"`)},
		{"event kind over its cap", eventWith(`,"kind":"` + tag + `"`)},
		// The cap is on what is stored: NUL becomes U+FFFD, three bytes for one.
		{"session_id over its cap once NUL is replaced", snapshotWith(`,"session_id":"` + strings.Repeat("k", maxSessionObservationKeyBytes-1) + `\u0000"`)},
		// One bad item among good ones still takes the request.
		{"bad item after good ones", `{"snapshots":[` + snapshotJSON("ok") + `],"events":[` + eventJSON("ok", "e1") + `,{"session_id":"","project_hash":"ph","event_id":"e2","kind":"session_end","observed_at":"` + observedAt + `"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, batches := observationRecorder(0)
			rec := postSessionObservations(t, newTestServer(m, nil), tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if len(*batches) != 0 {
				t.Errorf("store was called for a rejected request: %+v", *batches)
			}
		})
	}
}

// The limits are inclusive: a request at every limit at once is accepted whole.
func TestSessionObservations_acceptsRequestAtTheLimits(t *testing.T) {
	m, batches := observationRecorder(0)
	srv := newTestServer(m, nil)
	snapshotAtCap := `{"t":"` + strings.Repeat("a", maxSessionSnapshotBytes-len(`{"t":""}`)) + `"}`
	payloadAtCap := `{"t":"` + strings.Repeat("a", maxSessionEventPayloadBytes-len(`{"t":""}`)) + `"}`

	body := `{"snapshots":` + repeatJSON(maxSessionSnapshotsPerRequest, func(i int) string {
		if i == 0 {
			return `{"session_id":"s0","project_hash":"ph","observed_at":"` + observedAt + `","snapshot":` + snapshotAtCap + `}`
		}
		return snapshotJSON(fmt.Sprint("s", i))
	}) + `,"live_states":` + repeatJSON(maxSessionLiveStatesPerRequest, func(i int) string {
		return liveStateJSON(fmt.Sprint("s", i))
	}) + `,"events":` + repeatJSON(maxSessionEventsPerRequest, func(i int) string {
		if i == 0 {
			return `{"session_id":"s","project_hash":"ph","event_id":"e0","kind":"session_end","observed_at":"` + observedAt + `","payload":` + payloadAtCap + `}`
		}
		return eventJSON("s", fmt.Sprint("e", i))
	}) + `}`

	got := decodeObservationsResponse(t, postSessionObservations(t, srv, body))
	want := maxSessionSnapshotsPerRequest + maxSessionLiveStatesPerRequest + maxSessionEventsPerRequest
	if got.Accepted != want {
		t.Errorf("accepted = %d, want %d", got.Accepted, want)
	}
	if b := (*batches)[0]; len(b.Snapshots)+len(b.LiveStates)+len(b.Events) != want {
		t.Errorf("store got %d items, want %d", len(b.Snapshots)+len(b.LiveStates)+len(b.Events), want)
	}
}

// Every text field at exactly its cap goes through untouched, and a name past
// its cap is cut, not refused -- at a rune boundary, so it stays valid UTF-8.
func TestSessionObservations_textAtItsCapAndNameCut(t *testing.T) {
	m, batches := observationRecorder(1)
	srv := newTestServer(m, nil)
	key := strings.Repeat("k", maxSessionObservationKeyBytes)
	tag := strings.Repeat("t", maxSessionObservationTagBytes)
	// 171 three-byte runes are 513 bytes: one byte over, inside the last rune.
	longName := strings.Repeat("\uac00", 171)

	decodeObservationsResponse(t, postSessionObservations(t, srv, `{
		"profile_email":"`+key+`","user_id":"`+key+`","login_email":"`+key+`",
		"live_states":[{"session_id":"`+key+`","project_hash":"`+key+`","login_email":"`+key+`",
			"billing_provider":"`+key+`","account_id":"`+key+`","agent":"`+tag+`","pid":1,
			"proc_start":"`+key+`","status":"busy","name":"`+longName+`","name_source":"`+tag+`",
			"heartbeat_at":"`+observedAt+`"}]}`))

	ls := (*batches)[0].LiveStates[0]
	if ls.SessionID != key || ls.ProcStart != key || ls.AccountID != key || ls.Agent != tag || ls.NameSource != tag ||
		ls.UserID != key || ls.ProfileEmail != key {
		t.Errorf("a field at its cap was changed: %+v", ls)
	}
	if want := strings.Repeat("\uac00", 170); ls.Name != want {
		t.Errorf("name = %d bytes (valid UTF-8 %v), want the 510 bytes of whole runes under the cap",
			len(ls.Name), utf8.ValidString(ls.Name))
	}
}

// What would pass every check and still fail in Postgres is put right here: NUL,
// which a TEXT column refuses, becomes U+FFFD in every text field; a snapshot or
// payload is re-encoded, which turns a lone surrogate -- refused by jsonb -- into
// U+FFFD, keeps every digit of a number, and leaves the NUL escape for the store
// to replace, as it does for session_records.
func TestSessionObservations_normalisesTextAndJSON(t *testing.T) {
	m, batches := observationRecorder(1)
	srv := newTestServer(m, nil)

	decodeObservationsResponse(t, postSessionObservations(t, srv, `{
		"profile_email":"p\u0000@example.com","user_id":"u\u0000","login_email":"l\u0000@example.com",
		"snapshots":[{"session_id":"s\u00001","project_hash":"ph","account_id":"a\u0000",
			"observed_at":"`+observedAt+`",
			"snapshot":{ "z": "<&>", "lone": "\ud800", "big": 123456789012345678901234567890.5, "nul": "\u0000" }}],
		"live_states":[{"session_id":"s1","project_hash":"ph","pid":1,"proc_start":"p\u0000",
			"status":"busy","name":"n\u0000","name_source":"auto\u0000","heartbeat_at":"`+observedAt+`"}],
		"events":[{"session_id":"s1","project_hash":"ph","event_id":"e\u0000","kind":"session_end",
			"observed_at":"`+observedAt+`","payload":{"reason":"\udc00x"}}]}`))

	b := (*batches)[0]
	sn, ls, ev := b.Snapshots[0], b.LiveStates[0], b.Events[0]
	for name, got := range map[string][2]string{
		"profile_email": {sn.ProfileEmail, "p\uFFFD@example.com"},
		"user_id":       {sn.UserID, "u\uFFFD"},
		"login_email":   {sn.LoginEmail, "l\uFFFD@example.com"},
		"session_id":    {sn.SessionID, "s\uFFFD1"},
		"account_id":    {sn.AccountID, "a\uFFFD"},
		"proc_start":    {ls.ProcStart, "p\uFFFD"},
		"name":          {ls.Name, "n\uFFFD"},
		"name_source":   {ls.NameSource, "auto\uFFFD"},
		"event_id":      {ev.EventID, "e\uFFFD"},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %q, want %q", name, got[0], got[1])
		}
	}
	if want := `{"big":123456789012345678901234567890.5,"lone":"` + "\uFFFD" + `","nul":"\u0000","z":"<&>"}`; string(sn.Snapshot) != want {
		t.Errorf("snapshot = %s\nwant       %s", sn.Snapshot, want)
	}
	if want := `{"reason":"` + "\uFFFD" + `x"}`; string(ev.Payload) != want {
		t.Errorf("payload = %s, want %s", ev.Payload, want)
	}
}

// The number caps refuse only what jsonb cannot keep as written. Everything up to
// them -- a 64-byte literal, an exponent of 308 either way -- goes through with
// every digit as the client wrote it.
func TestSessionObservations_keepsNumbersWithinTheCaps(t *testing.T) {
	m, batches := observationRecorder(1)
	srv := newTestServer(m, nil)
	atCap := "-9." + strings.Repeat("9", maxSessionJSONNumberBytes-len("-9.e+308")) + "e+308"
	if len(atCap) != maxSessionJSONNumberBytes {
		t.Fatalf("fixture is %d bytes, want %d", len(atCap), maxSessionJSONNumberBytes)
	}
	numbers := `{"a":0,"b":-1,"c":3.14,"d":1e10,"e":1.5E-7,"f":123456789012345678901234567890.5,` +
		`"g":1e308,"h":-1E-308,"i":` + atCap + `,"j":[2e+308,0.000001]}`

	decodeObservationsResponse(t, postSessionObservations(t, srv, `{
		"snapshots":[{"session_id":"s1","project_hash":"ph","observed_at":"`+observedAt+`","snapshot":`+numbers+`}],
		"events":[{"session_id":"s1","project_hash":"ph","event_id":"e1","kind":"session_end",
			"observed_at":"`+observedAt+`","payload":`+numbers+`}]}`))

	b := (*batches)[0]
	if string(b.Snapshots[0].Snapshot) != numbers {
		t.Errorf("snapshot = %s\nwant       %s", b.Snapshots[0].Snapshot, numbers)
	}
	if string(b.Events[0].Payload) != numbers {
		t.Errorf("payload = %s\nwant      %s", b.Events[0].Payload, numbers)
	}
}

// A kind is capped before it can be echoed: an unknown kind is named in the 400,
// and a client could otherwise make the server send back megabytes of its own.
func TestSessionObservations_doesNotEchoAnOverlongKind(t *testing.T) {
	m, _ := observationRecorder(0)
	kind := strings.Repeat("k", 10000)
	rec := postSessionObservations(t, newTestServer(m, nil), `{"events":[{"session_id":"s","project_hash":"ph",
		"event_id":"e","kind":"`+kind+`","observed_at":"`+observedAt+`"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, kind[:maxSessionObservationTagBytes+1]) || !strings.Contains(body, "kind is 10000 bytes") {
		t.Errorf("body = %.200s..., want the kind's size and not the kind", body)
	}
}

// A decode error can quote the input, and a time.ParseError quotes it twice: a
// 100,000-byte heartbeat_at drew a 200,111-byte 400. The error is still echoed,
// cut to its cap.
func TestSessionObservations_doesNotEchoAnOversizedTime(t *testing.T) {
	m, batches := observationRecorder(0)
	rec := postSessionObservations(t, newTestServer(m, nil), `{"heartbeat_at":"`+strings.Repeat("9", 100000)+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %.200s...)", err, rec.Body.String())
	}
	const prefix = "invalid request: "
	if msg := out["error"]; len(msg) != len(prefix)+maxEchoedDecodeErrorBytes || !strings.HasPrefix(msg, prefix+"parsing time") {
		t.Errorf("error = %d bytes, %.120q...; want %q and the parse error cut to %d bytes",
			len(msg), msg, prefix, maxEchoedDecodeErrorBytes)
	}
	if len(*batches) != 0 {
		t.Errorf("store was called for a rejected request: %+v", *batches)
	}
}

// Times are kept as sent, so a client clock running ahead would win every
// newer-wins comparison for as long as it stayed ahead. An item dated more than
// five minutes past receipt, by any of its times, is skipped and counted, and the
// request logs it once; one a second ahead is a working clock and goes through
// with its time untouched.
func TestSessionObservations_skipsItemsDatedTooFarAhead(t *testing.T) {
	m, batches := observationRecorder(0)
	srv := newTestServer(m, nil)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	now := time.Now().UTC()
	year := now.AddDate(1, 0, 0).Format(time.RFC3339Nano)
	second := now.Add(time.Second)
	sec := second.Format(time.RFC3339Nano)
	got := decodeObservationsResponse(t, postSessionObservations(t, srv, `{"profile_email":"p@example.com","user_id":"u-1",
		"snapshots":[
			{"session_id":"year","project_hash":"ph","observed_at":"`+year+`","snapshot":{}},
			{"session_id":"second","project_hash":"ph","observed_at":"`+sec+`","snapshot":{}}],
		"live_states":[
			{"session_id":"year","project_hash":"ph","pid":1,"status":"busy","heartbeat_at":"`+year+`"},
			{"session_id":"year-changed","project_hash":"ph","pid":1,"status":"busy",
			 "status_changed_at":"`+year+`","heartbeat_at":"`+observedAt+`"},
			{"session_id":"second","project_hash":"ph","pid":1,"status":"busy",
			 "status_changed_at":"`+sec+`","heartbeat_at":"`+sec+`"}],
		"events":[
			{"session_id":"year","project_hash":"ph","event_id":"e","kind":"session_end","observed_at":"`+year+`"},
			{"session_id":"second","project_hash":"ph","event_id":"e","kind":"session_end","observed_at":"`+sec+`"}]}`))

	if got.Accepted != 7 || got.Skipped != 4 {
		t.Errorf("response = %+v, want accepted 7, skipped 4", got)
	}
	b := (*batches)[0]
	if len(b.Snapshots) != 1 || len(b.LiveStates) != 1 || len(b.Events) != 1 {
		t.Fatalf("batch = %d snapshots, %d live states, %d events; want the second-ahead one of each",
			len(b.Snapshots), len(b.LiveStates), len(b.Events))
	}
	ls := b.LiveStates[0]
	if !b.Snapshots[0].ObservedAt.Equal(second) || !ls.HeartbeatAt.Equal(second) ||
		ls.StatusChangedAt == nil || !ls.StatusChangedAt.Equal(second) || !b.Events[0].ObservedAt.Equal(second) {
		t.Errorf("times = %s, %s/%v, %s; want each as sent, %s", b.Snapshots[0].ObservedAt, ls.HeartbeatAt,
			ls.StatusChangedAt, b.Events[0].ObservedAt, second)
	}
	if n := strings.Count(logs.String(), "past receipt"); n != 1 {
		t.Errorf("logged %d line(s) about the clock, want 1:\n%s", n, logs.String())
	}

	// The next report from a corrected clock goes through: nothing of the year
	// ahead reached the store to pin a row.
	got = decodeObservationsResponse(t, postSessionObservations(t, srv,
		`{"profile_email":"p@example.com","user_id":"u-1","snapshots":[`+snapshotJSON("year")+`]}`))
	if got.Skipped != 0 || len(*batches) != 2 || len((*batches)[1].Snapshots) != 1 {
		t.Errorf("the corrected report: response %+v, %d batch(es); want it passed to the store", got, len(*batches))
	}
}

// The live-state rules a client can get wrong without the request being
// malformed: a zero status_changed_at is no time at all, and an empty agent is
// claude, with claude's billing provider, as it is for sync.
func TestSessionObservations_liveStateDefaults(t *testing.T) {
	m, batches := observationRecorder(1)
	srv := newTestServer(m, nil)

	decodeObservationsResponse(t, postSessionObservations(t, srv, `{"live_states":[
		{"session_id":"s1","project_hash":"ph","pid":7,"status":"idle",
		 "status_changed_at":"0001-01-01T00:00:00Z","heartbeat_at":"`+observedAt+`"}]}`))

	ls := (*batches)[0].LiveStates[0]
	if ls.StatusChangedAt != nil {
		t.Errorf("status_changed_at = %v, want nil for a zero time", ls.StatusChangedAt)
	}
	if ls.Agent != "claude" || ls.BillingProvider != "anthropic" {
		t.Errorf("agent %q, billing provider %q; want claude and anthropic", ls.Agent, ls.BillingProvider)
	}
}

func TestSessionObservationsAcceptsProcessIOMetricsWithoutStreamContent(t *testing.T) {
	m, batches := observationRecorder(1)
	m.getDashboardUserByAPITokenFn = func(_ context.Context, _ string) (*store.DashboardUser, error) {
		return &store.DashboardUser{Email: "owner@example.com", CctraceUserID: "u-1", IsActive: true}, nil
	}
	srv := newTestServer(m, nil, ingestAuthenticator().HTTPMiddleware)
	body := `{"profile_email":"p@example.com","user_id":"u-1","live_states":[{
		"session_id":"s-io","agent":"codex","project_hash":"ph","billing_provider":"openai","account_id":"acct-1",
		"pid":42,"proc_start":"linux:123","status":"busy","heartbeat_at":"` + observedAt + `",
		"io_epoch":"epoch-1","io_as_of":"` + observedAt + `","io_source":"process_stream","io_mode":"pipes",
		"io_partial":false,"io_failure_code":"","input_bytes":4,"input_lines":1,"output_bytes":13,"output_lines":3,
		"stdout_bytes":9,"stdout_lines":2,"stderr_bytes":4,"stderr_lines":1}]}`
	rec := postSessionObservations(t, srv, body, "Authorization", "Bearer tok-owner")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(*batches) != 1 || len((*batches)[0].LiveStates) != 1 {
		t.Fatalf("stored batches = %+v", *batches)
	}
	got := (*batches)[0].LiveStates[0]
	if got.IOEpoch != "epoch-1" || got.InputBytes == nil || *got.InputBytes != 4 || got.StdoutBytes == nil || *got.StdoutBytes != 9 || got.StderrBytes == nil || *got.StderrBytes != 4 {
		t.Fatalf("stored process I/O = %+v", got)
	}
}

func TestSessionObservationsAcceptsExplicitUnknownProcessStatusWithSource(t *testing.T) {
	m, batches := observationRecorder(1)
	m.getDashboardUserByAPITokenFn = func(_ context.Context, _ string) (*store.DashboardUser, error) {
		return &store.DashboardUser{Email: "owner@example.com", CctraceUserID: "u-1", IsActive: true}, nil
	}
	srv := newTestServer(m, nil, ingestAuthenticator().HTTPMiddleware)
	body := `{"profile_email":"p@example.com","user_id":"u-1","live_states":[{
		"session_id":"s-io-unknown","agent":"codex","project_hash":"ph","billing_provider":"openai","account_id":"acct-1",
		"pid":42,"proc_start":"linux:123","status":"unknown","status_source":"process_io_unknown","heartbeat_at":"` + observedAt + `",
		"io_epoch":"epoch-unknown","io_as_of":"` + observedAt + `","io_source":"process_stream","io_mode":"pipes",
		"io_partial":false,"io_failure_code":"","input_bytes":0,"input_lines":0,"output_bytes":0,"output_lines":0,
		"stdout_bytes":0,"stdout_lines":0,"stderr_bytes":0,"stderr_lines":0}]}`
	rec := postSessionObservations(t, srv, body, "Authorization", "Bearer tok-owner")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(*batches) != 1 || len((*batches)[0].LiveStates) != 1 {
		t.Fatalf("stored batches = %+v", *batches)
	}
	got := (*batches)[0].LiveStates[0]
	if got.Status != "unknown" || got.StatusSource != "process_io_unknown" {
		t.Fatalf("status and source = %q/%q, want explicit unknown process status", got.Status, got.StatusSource)
	}
}

func TestSessionObservationsRejectsUnknownStatusWithoutProcessIOMetrics(t *testing.T) {
	m, batches := observationRecorder(0)
	body := `{"live_states":[{"session_id":"s","agent":"codex","project_hash":"ph","pid":42,
		"proc_start":"linux:123","status":"unknown","status_source":"process_io_unknown","heartbeat_at":"` + observedAt + `"}]}`
	rec := postSessionObservations(t, newTestServer(m, nil), body)
	if rec.Code != http.StatusBadRequest || len(*batches) != 0 {
		t.Fatalf("status = %d, batches = %d, body = %s; want 400 without process I/O evidence", rec.Code, len(*batches), rec.Body.String())
	}
}

func TestSessionObservationsProcessIOMetricsRequirePersonalToken(t *testing.T) {
	m, batches := observationRecorder(1)
	body := `{"live_states":[{"session_id":"s-io","agent":"codex","project_hash":"ph","pid":42,"proc_start":"linux:123",
		"status":"busy","heartbeat_at":"` + observedAt + `","io_epoch":"epoch-1","io_as_of":"` + observedAt + `",
		"io_source":"process_stream","io_mode":"pty","input_bytes":1,"input_lines":1,"output_bytes":1,"output_lines":1}]}`
	rec := postSessionObservations(t, newTestServer(m, nil), body)
	if rec.Code != http.StatusForbidden || len(*batches) != 0 {
		t.Fatalf("status = %d, store calls = %d; want 403 and no store call", rec.Code, len(*batches))
	}
}

func TestSessionObservationsRejectsInvalidProcessIOMetrics(t *testing.T) {
	base := `"io_epoch":"epoch-1","io_as_of":"` + observedAt + `","io_source":"process_stream","io_mode":"pipes",
		"input_bytes":4,"input_lines":1,"output_bytes":13,"output_lines":3,"stdout_bytes":9,"stdout_lines":2,"stderr_bytes":4,"stderr_lines":1`
	for _, test := range []struct {
		name string
		io   string
	}{
		{name: "negative counter", io: strings.Replace(base, `"input_bytes":4`, `"input_bytes":-1`, 1)},
		{name: "pty claims split streams", io: strings.Replace(base, `"io_mode":"pipes"`, `"io_mode":"pty"`, 1)},
		{name: "unknown failure code", io: strings.Replace(base, `"io_mode":"pipes"`, `"io_mode":"pipes","io_partial":true,"io_failure_code":"raw-process-output"`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, batches := observationRecorder(0)
			body := `{"live_states":[{"session_id":"s-io","agent":"codex","project_hash":"ph","pid":42,"proc_start":"p","status":"busy","heartbeat_at":"` + observedAt + `",` + test.io + `}]}`
			rec := postSessionObservations(t, newTestServer(m, nil), body)
			if rec.Code != http.StatusBadRequest || len(*batches) != 0 {
				t.Fatalf("status = %d, batches = %d, body = %s; want 400 with no store write", rec.Code, len(*batches), rec.Body.String())
			}
		})
	}
}

// A store failure is the server's, so it is a 500 and the client retries.
func TestSessionObservations_storeErrorIs500(t *testing.T) {
	m := &mockStore{
		ingestSessionObservationsFn: func(context.Context, store.SessionObservationBatch) (store.SessionObservationResult, error) {
			return store.SessionObservationResult{}, errors.New("db down")
		},
	}
	rec := postSessionObservations(t, newTestServer(m, nil), `{"snapshots":[`+snapshotJSON("s")+`]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestSessionObservations_routeIsAuthenticated(t *testing.T) {
	invoked := false
	authMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			invoked = true
			next.ServeHTTP(w, r)
		})
	}
	m, _ := observationRecorder(0)
	rec := postSessionObservations(t, newTestServer(m, nil, authMW), `{"heartbeat_at":"`+observedAt+`"}`)
	if rec.Code != http.StatusOK || !invoked {
		t.Fatalf("status = %d, auth invoked = %v; want 200 behind the ingest auth", rec.Code, invoked)
	}
}

// A client asks before it starts sending, and an older server -- which has no
// such key -- answers false by omission.
func TestSyncCapabilities_ReportsSessionObservations(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestServer(&mockStore{}, nil).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sync/capabilities", nil))
	var got map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if !got["session_observations"] || !got["reenrich"] {
		t.Errorf("capabilities = %v, want session_observations and reenrich", got)
	}
}

// last_stored_at has to mean "stored": touched after InsertSessionRecords
// returned, with the envelope identity, and not on any path that stored nothing.
func TestHandleSync_touchesCollectorStoredOnlyAfterInsert(t *testing.T) {
	type touch struct{ userID, profileEmail string }
	newServer := func(insertErr error) (*Server, *[]string, *[]touch) {
		var calls []string
		var touches []touch
		m := &mockStore{
			insertSessionRecordsFn: func(context.Context, []*store.SessionRecord) error {
				calls = append(calls, "insert")
				return insertErr
			},
			reenrichSessionRecordsFn: func(context.Context, []*store.SessionRecord) (int, error) {
				calls = append(calls, "reenrich")
				return 1, nil
			},
			touchCollectorStoredFn: func(ctx context.Context, userID, profileEmail string, at time.Time) error {
				calls = append(calls, "touch")
				touches = append(touches, touch{userID, profileEmail})
				return nil
			},
		}
		srv := newTestServer(m, nil).WithAliases(emailalias.Aliases{"p@example.com": "canonical@example.com"})
		return srv, &calls, &touches
	}

	t.Run("insert succeeded", func(t *testing.T) {
		srv, calls, touches := newServer(nil)
		postSync(t, srv, syncBody("ph-ok", "s1"))
		if strings.Join(*calls, ",") != "insert,touch" {
			t.Errorf("calls = %v, want the touch after the insert", *calls)
		}
		if len(*touches) != 1 || (*touches)[0] != (touch{"u-1", "canonical@example.com"}) {
			t.Errorf("touched %v, want the alias-resolved envelope identity", *touches)
		}
	})
	t.Run("insert failed", func(t *testing.T) {
		srv, calls, _ := newServer(errors.New("db down"))
		rec := httptest.NewRecorder()
		srv.handleSync(rec, httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(syncBody("ph-ok", "s1"))))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if strings.Join(*calls, ",") != "insert" {
			t.Errorf("calls = %v, want no touch after a failed insert", *calls)
		}
	})
	t.Run("nothing to store", func(t *testing.T) {
		srv, calls, _ := newServer(nil)
		postSync(t, srv, `{"profile_email":"p@example.com","user_id":"u-1","records":[]}`)
		if len(*calls) != 0 {
			t.Errorf("calls = %v, want none for an empty sync", *calls)
		}
	})
	t.Run("reenrich", func(t *testing.T) {
		srv, calls, _ := newServer(nil)
		body := strings.Replace(syncBody("ph-ok", "s1"), `{`, `{"reenrich":true,`, 1)
		postSync(t, srv, body)
		if strings.Join(*calls, ",") != "reenrich" {
			t.Errorf("calls = %v, want no touch for a reenrich pass, which rewrites old rows", *calls)
		}
	})
	// The row is keyed by identity: an anonymous sync stores its records and
	// writes no row it would share with every other anonymous client.
	t.Run("no profile_email", func(t *testing.T) {
		srv, calls, _ := newServer(nil)
		postSync(t, srv, strings.ReplaceAll(syncBody("ph-ok", "s1"), `"profile_email":"p@example.com"`, `"profile_email":""`))
		if strings.Join(*calls, ",") != "insert" {
			t.Errorf("calls = %v, want the insert and no touch", *calls)
		}
	})
	t.Run("touch failure is not the sync's", func(t *testing.T) {
		m := &mockStore{
			touchCollectorStoredFn: func(context.Context, string, string, time.Time) error { return errors.New("db hiccup") },
		}
		out := postSync(t, newTestServer(m, nil), syncBody("ph-ok", "s1"))
		if out["inserted"] != 1 {
			t.Errorf("response = %v, want the insert reported despite the failed touch", out)
		}
	})
}

// Sync's stored mark and the collector's heartbeat are one row, so both are keyed
// by the same rule (resolveIngestIdentity): the account of the per-user token
// the ingest middleware accepted, else the payload's identity. Sync's records
// keep the payload's; the mark does not, or a token could move another user's
// last_stored_at. A token that cannot be resolved skips the mark rather than
// falling back -- and the sync, whose records are in, still succeeds.
func TestHandleSync_touchesCollectorStoredUnderTheObservationIdentity(t *testing.T) {
	type identity struct{ userID, profileEmail string }
	alice := &store.DashboardUser{Email: "alice@example.com", CctraceUserID: "u-alice", IsActive: true}
	for _, tc := range []struct {
		name   string
		authn  *auth.Authenticator
		header string
		lookup func() (*store.DashboardUser, error)
		want   []identity
	}{
		{"per-user token", ingestAuthenticator(), "Bearer tok-alice", func() (*store.DashboardUser, error) { return alice, nil },
			[]identity{{"u-alice", "alice@example.com"}}},
		{"global key", ingestAuthenticator(), "Bearer global-key", nil, []identity{{"u-1", "p@example.com"}}},
		// Nothing authenticated the token, so it binds nothing and is not looked up.
		{"authentication disabled", auth.New(""), "Bearer tok-alice", nil, []identity{{"u-1", "p@example.com"}}},
		{"lookup failed", ingestAuthenticator(), "Bearer tok-alice", func() (*store.DashboardUser, error) { return nil, errors.New("db down") }, nil},
		{"inactive account", ingestAuthenticator(), "Bearer tok-alice", func() (*store.DashboardUser, error) {
			return &store.DashboardUser{Email: "alice@example.com", CctraceUserID: "u-alice"}, nil
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var touched, heartbeats []identity
			var inserted []*store.SessionRecord
			m := &mockStore{
				insertSessionRecordsFn: func(_ context.Context, records []*store.SessionRecord) error {
					inserted = append(inserted, records...)
					return nil
				},
				touchCollectorStoredFn: func(_ context.Context, userID, profileEmail string, _ time.Time) error {
					touched = append(touched, identity{userID, profileEmail})
					return nil
				},
				ingestSessionObservationsFn: func(_ context.Context, b store.SessionObservationBatch) (store.SessionObservationResult, error) {
					if b.Heartbeat != nil {
						heartbeats = append(heartbeats, identity{b.Heartbeat.UserID, b.Heartbeat.ProfileEmail})
					}
					return store.SessionObservationResult{}, nil
				},
			}
			if tc.lookup != nil {
				m.getDashboardUserByAPITokenFn = func(context.Context, string) (*store.DashboardUser, error) { return tc.lookup() }
			}
			srv := newTestServer(m, nil, tc.authn.HTTPMiddleware)

			req := httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(syncBody("ph-ok", "s1")))
			req.Header.Set("Authorization", tc.header)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("sync status = %d, want 200 whatever the mark does (body %s)", rec.Code, rec.Body.String())
			}
			if len(inserted) != 1 || inserted[0].ProfileEmail != "p@example.com" || inserted[0].UserID != "u-1" {
				t.Errorf("records = %+v, want the one record under the payload's identity", inserted)
			}
			if fmt.Sprint(touched) != fmt.Sprint(tc.want) {
				t.Errorf("stored mark under %v, want %v", touched, tc.want)
			}
			if tc.want == nil {
				return
			}
			postSessionObservations(t, srv, `{"profile_email":"p@example.com","user_id":"u-1","heartbeat_at":"`+observedAt+`"}`,
				"Authorization", tc.header)
			if fmt.Sprint(heartbeats) != fmt.Sprint(touched) {
				t.Errorf("heartbeat under %v, stored mark under %v; want one row", heartbeats, touched)
			}
		})
	}
}
