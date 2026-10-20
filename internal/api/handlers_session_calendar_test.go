package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	tc "github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"cctrace/internal/auth"
	"cctrace/internal/containertest"
	"cctrace/internal/sessionfacts"
	"cctrace/internal/store"
)

func (m *mockStore) ReadSessionCalendar(_ context.Context, f store.SessionCalendarFilter) (store.SessionCalendar, error) {
	if m.sessionCalendarReadFn != nil {
		return m.sessionCalendarReadFn(f)
	}
	return store.SessionCalendar{}, nil
}

func TestSessionCalendarPostgresHTTP(t *testing.T) {
	if home, err := os.UserHomeDir(); err == nil {
		sock := filepath.Join(home, ".colima", "default", "docker.sock")
		if _, err := os.Stat(sock); err == nil {
			host := "unix://" + sock
			if os.Getenv("DOCKER_HOST") == "" {
				t.Setenv("DOCKER_HOST", host)
			}
			if os.Getenv("DOCKER_HOST") == host && os.Getenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE") == "" {
				t.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", "/var/run/docker.sock")
			}
		}
	}
	ctx := context.Background()
	ctr, err := pgmod.Run(ctx, "timescale/timescaledb:latest-pg16", pgmod.WithDatabase("calendar"), pgmod.WithUsername("test"), pgmod.WithPassword("test"),
		tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)))
	if err != nil {
		containertest.SkipOrFail(t, "postgres container", err)
	}
	t.Cleanup(func() {
		if err := ctr.Terminate(context.Background()); err != nil {
			t.Errorf("terminate fixture: %v", err)
		}
	})
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.NewPgStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	jwt, err := auth.NewJWTManager(strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, role := range []string{"user", "admin"} {
		user, err := db.CreateDashboardUser(ctx, &store.DashboardUser{Email: role + "@example.com", Role: role, PasswordHash: "fixture", CctraceUserID: role})
		if err != nil {
			t.Fatal(err)
		}
		tokens[role], err = jwt.GenerateAccessToken(user.ID, user.Email, role, "", role, false)
		if err != nil {
			t.Fatal(err)
		}
	}
	from := time.Now().UTC().Truncate(time.Second)
	for _, uid := range []string{"user", "admin"} {
		if _, err := conn.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
			VALUES($1,$1,$1||'@example.com','claude',$2,'{}')`, uid, from); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent,model,input_tokens)
			VALUES('user',$1,$1,$2,$2::timestamptz+interval '1 hour',true,'claude','http-real',123)`, uid, from); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO session_analysis_facts(session_id,agent,coverage,user_ids,profile_emails,owner_user_id,owner_profile_email,
			transcript,title,title_source,title_state,turn_boundaries,cost_source,output_hash,source_generation,rules_version)
			VALUES($1,'claude','full',ARRAY[$1],ARRAY[$1||'@example.com'],$1,$1||'@example.com',true,'private title','first_request','ok','observed','none',decode('01','hex'),1,$2)`, uid, sessionfacts.RulesVersion); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO session_activity_segments(session_id,seg_start,seg_end,owner_user_id,owner_profile_email,agent,records,human_inputs,rules_version)
			VALUES($1,$2,$2::timestamptz+interval '1 hour',$1,$1||'@example.com','claude',1,2001,$3)`, uid, from, sessionfacts.RulesVersion); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO session_turn_facts(session_id,turn_key,agent,ordinal,branch,started_at,ended_at,input_kind,is_request,status,preview_state,cost_source,context_source,rules_version,detail)
			SELECT $1,'turn-'||n,'claude',n,'active',$2::timestamptz-interval '1 hour',$2,'text',true,'ended','none','none','unknown',$3,
			jsonb_build_object('inputs',jsonb_build_array(jsonb_build_object('ts',to_char($2::timestamptz+make_interval(secs=>n),'YYYY-MM-DD"T"HH24:MI:SS"Z"')))) FROM generate_series(1,2001) n`, uid, from, sessionfacts.RulesVersion); err != nil {
			t.Fatal(err)
		}
	}
	srv := NewServer(db, jwt)
	srv.useUserIDAccessControl = true
	srv.WithSessionAnalysisEnabled(true)
	server := httptest.NewServer(srv.Handler())
	defer server.Close()
	query := "?from=" + url.QueryEscape(calendarHTTPTime(from)) + "&to=" + url.QueryEscape(calendarHTTPTime(from.Add(2*time.Hour)))
	read := func(role, suffix string, status int) store.SessionCalendar {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/session-calendar"+query+suffix, nil)
		if err != nil {
			t.Fatal(err)
		}
		if role != "" {
			req.AddCookie(&http.Cookie{Name: "cctrace_token", Value: tokens[role]})
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != status {
			t.Fatalf("%s %s status=%d want=%d", role, suffix, resp.StatusCode, status)
		}
		var result store.SessionCalendar
		if status == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	read("", "", http.StatusUnauthorized)
	for _, tc := range []struct {
		role, suffix, want string
		owner              bool
	}{{"user", "&user_id=admin", "user", true}, {"admin", "", "admin", true}, {"admin", "&user_id=admin", "admin", true}, {"admin", "&user_id=user", "user", false}} {
		result := read(tc.role, tc.suffix, http.StatusOK)
		if len(result.Sessions) != 1 || result.Sessions[0].SessionID != tc.want || result.OwnerView != tc.owner || len(result.Sessions[0].Inputs.Items) != 2000 || !result.Sessions[0].Inputs.Truncated {
			t.Fatalf("scope/cap %+v result=%+v", tc, result)
		}
		if result.Sessions[0].Overview == nil || result.Sessions[0].Overview.InputTokens != 123 {
			t.Fatal("real overview absent")
		}
		_, status := result.Sessions[0].Colors["status"]
		if status != tc.owner {
			t.Fatal("owner status visibility mismatch")
		}
	}
	if result := read("admin", "&login_email=shared@example.com", http.StatusOK); len(result.Sessions) != 0 {
		t.Fatal("login-only widened user scope")
	}
	for _, suffix := range []string{"&agent=invalid", "&source=invalid", "&classification=invalid", "&assembled=wrong", "&profile_email=user@example.com"} {
		read("admin", suffix, http.StatusBadRequest)
	}
	for _, query := range []string{
		`UPDATE session_analysis_facts SET user_ids=ARRAY['user','other'],profile_emails=ARRAY['user@example.com','other@example.com'],owner_user_id=NULL,owner_profile_email=NULL,requests=9 WHERE session_id='user'`,
		`UPDATE session_activity_segments SET owner_user_id=NULL,owner_profile_email=NULL WHERE session_id='user'`,
		`INSERT INTO session_records(session_id,user_id,profile_email,record_type) VALUES('user','other','other@example.com','user')`,
	} {
		if _, err := conn.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	mixed := read("admin", "&user_id=user", http.StatusOK)
	if mixed.OwnerView || len(mixed.Sessions) != 1 || mixed.Sessions[0].Facts != "ready" || len(mixed.Sessions[0].Segments) != 1 || len(mixed.Sessions[0].Inputs.Items) != 2000 || mixed.Sessions[0].Requests == nil || *mixed.Sessions[0].Requests != 9 {
		t.Fatalf("admin selected mixed missing=%+v", mixed)
	}
	if _, ok := mixed.Sessions[0].Colors["status"]; ok {
		t.Fatal("admin selected mixed exposed owner status")
	}
	if restricted := read("user", "", http.StatusOK); len(restricted.Sessions) != 0 {
		t.Fatal("restricted user received mixed session")
	}
	if defaults := read("admin", "", http.StatusOK); len(defaults.Sessions) != 1 || defaults.Sessions[0].SessionID != "admin" {
		t.Fatal("admin default widened to selected user's mixed session")
	}
	for _, query := range []string{
		`UPDATE session_analysis_facts SET user_ids=ARRAY['user'],profile_emails=ARRAY['user@example.com'],owner_user_id='user',owner_profile_email='user@example.com' WHERE session_id='user'`,
		`UPDATE session_activity_segments SET owner_user_id='user',owner_profile_email='user@example.com' WHERE session_id='user'`,
		`DELETE FROM session_records WHERE session_id='user' AND user_id='other'`,
	} {
		if _, err := conn.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, `INSERT INTO session_fact_dirty(session_id,generation,removal_generation,reasons) VALUES('user',2,2,128)`); err != nil {
		t.Fatal(err)
	}
	retained := read("user", "", http.StatusOK)
	if len(retained.Sessions) != 1 || retained.Sessions[0].Facts != "building" || len(retained.Sessions[0].Inputs.Items) != 0 {
		t.Fatalf("retention invalidation leaked=%+v", retained)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES('session_fact_copies_v1')`); err != nil {
		t.Fatal(err)
	}
	copied := read("admin", "", http.StatusOK)
	if len(copied.Sessions) != 1 || copied.Sessions[0].Facts != "building" || len(copied.Sessions[0].Segments) != 0 || copied.Sessions[0].Inputs.State != "undeterminable" {
		t.Fatalf("copy gate leaked old inputs=%+v", copied)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO deleted_sessions(session_id) VALUES('user')`); err != nil {
		t.Fatal(err)
	}
	if result := read("user", "", http.StatusOK); len(result.Sessions) != 0 {
		t.Fatal("deleted session exposed")
	}
}

func calendarHTTPTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func TestSessionCalendarRouteRangeAndOwnerScope(t *testing.T) {
	calls := 0
	m := &mockStore{sessionCalendarReadFn: func(f store.SessionCalendarFilter) (store.SessionCalendar, error) {
		calls++
		if !f.Access.User || f.Access.UserID != "uid-owner" || f.UserID != "uid-owner" || f.LoginEmail != "" {
			t.Fatalf("owner scope escaped: %+v", f)
		}
		return store.SessionCalendar{From: f.From.UTC().Format(time.RFC3339Nano), To: f.To.UTC().Format(time.RFC3339Nano), OwnerView: true, Sessions: []store.SessionCalendarSession{}}, nil
	}}
	s := newTestServer(m, nil)
	user := &auth.DashboardUser{ID: 7, Role: "user", Email: "owner@example.com", CctraceUserID: "uid-owner"}
	request := func(query string) *http.Request {
		r := httptest.NewRequest("GET", "/api/session-calendar"+query, nil)
		return r.WithContext(auth.WithUser(r.Context(), user))
	}
	rangeQuery := "?from=2026-03-08T05%3A00%3A00Z&to=2026-03-15T04%3A00%3A00Z&user_id=foreign&login_email=foreign@example.com"
	if w := httptest.NewRecorder(); func() int { s.Handler().ServeHTTP(w, request(rangeQuery)); return w.Code }() != http.StatusNotFound {
		t.Fatal("disabled route must not read")
	}
	s.WithSessionAnalysisEnabled(true)
	for _, query := range []string{"", "?from=2026-03-08T00:00:00Z&to=2026-03-08T00:00:00Z", "?from=2026-03-08T00:00:00Z&to=2026-03-23T00:00:00Z"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, request(query))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("range %s: status=%d", query, w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request(rangeQuery))
	if w.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
	}
	var body store.SessionCalendar
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || !body.OwnerView || body.From != "2026-03-08T05:00:00Z" {
		t.Fatalf("response=%+v err=%v", body, err)
	}
}
