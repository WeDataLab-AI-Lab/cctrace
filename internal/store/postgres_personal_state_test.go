package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPersonalVisibleSourceQueryDeduplicatesSourceRowsBeforeOwnershipProof(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	// Unique UUIDs ensure the fixture inserts every identical identity row rather
	// than hitting any database idempotency path.
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(uuid,session_id,user_id,profile_email,billing_provider,record_type,raw)
		SELECT 'dedupe-uuid-'||n,'dedupe','uid-owner','owner@example.com','fixture','user','{}'::jsonb
		FROM generate_series(1,16) n`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(uuid,session_id,user_id,profile_email,billing_provider,record_type,raw) VALUES
		('dedupe-empty','dedupe','','','fixture','user','{}'::jsonb),
		('dedupe-partial','dedupe','uid-owner','','fixture','user','{}'::jsonb),
		('mixed-owner','mixed','uid-owner','owner@example.com','fixture','user','{}'::jsonb),
		('mixed-foreign','mixed','uid-foreign','foreign@example.com','fixture','user','{}'::jsonb),
		('allowed-owner','blocked','uid-owner','owner@example.com','fixture','user','{}'::jsonb),
		('disallowed-owner','blocked','uid-owner','owner@example.com','fixture','user','{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET billing_provider='fixture',account_id='blocked-account'
		WHERE uuid='disallowed-owner'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_billing_accounts(billing_provider,account_id,reason,created_by)
		VALUES('fixture','blocked-account','synthetic fixture','test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot,identity_provenance)
		VALUES('dedupe','uid-owner','owner@example.com','claude',now(),'{}'::jsonb,'personal_token')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_live_states(session_id,user_id,profile_email,agent,pid,proc_start,status,heartbeat_at,identity_provenance)
		VALUES ('dedupe','uid-owner','owner@example.com','claude',101,'a','idle',now(),'personal_token'),
		       ('dedupe','uid-owner','owner@example.com','claude',102,'b','idle',now(),'personal_token')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_observation_events(session_id,user_id,profile_email,agent,event_id,kind,observed_at,identity_provenance)
		VALUES ('dedupe','uid-owner','owner@example.com','claude','event-a','state_change',now(),'personal_token'),
		       ('dedupe','uid-owner','owner@example.com','claude','event-b','state_change',now(),'personal_token')`); err != nil {
		t.Fatal(err)
	}

	query := personalVisibleSourceQuery(" AND session_id IN (SELECT session_id FROM candidate)")
	buildQuery := func(sourceSQL string) string {
		return `WITH candidate(session_id) AS (VALUES ('dedupe'),('mixed'),('blocked')),
			` + strings.TrimPrefix(sourceSQL, "WITH ") + `
			SELECT c.session_id,(SELECT count(*) FROM sources s WHERE s.session_id=c.session_id),
				EXISTS(SELECT 1 FROM valid_sources v WHERE v.session_id=c.session_id)
			FROM candidate c ORDER BY c.session_id`
	}
	type proof struct {
		sources int
		valid   bool
	}
	read := func(sourceSQL string) map[string]proof {
		t.Helper()
		rows, err := s.pool.Query(ctx, buildQuery(sourceSQL), int64(0), "uid-owner", "owner@example.com")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := make(map[string]proof, 3)
		for rows.Next() {
			var id string
			var p proof
			if err := rows.Scan(&id, &p.sources, &p.valid); err != nil {
				t.Fatal(err)
			}
			got[id] = p
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}
	canonical := read(query)
	grouped := read(personalVisibleSourceQueryWithGrouping(" AND session_id IN (SELECT session_id FROM candidate)", true))
	if !reflect.DeepEqual(grouped, canonical) {
		t.Fatalf("grouped source cardinality/owner proof=%v, want canonical %v", grouped, canonical)
	}
	legacy := read(strings.ReplaceAll(query, "SELECT DISTINCT ", "SELECT "))
	want := map[string]proof{"dedupe": {sources: 6, valid: true}, "mixed": {sources: 2, valid: false}, "blocked": {sources: 2, valid: false}}
	if !reflect.DeepEqual(canonical, want) {
		t.Fatalf("canonical source cardinality/owner proof=%v, want %v", canonical, want)
	}
	legacyWant := map[string]proof{"dedupe": {sources: 23, valid: true}, "mixed": {sources: 2, valid: false}, "blocked": {sources: 2, valid: false}}
	if !reflect.DeepEqual(legacy, legacyWant) {
		t.Fatalf("local SQL with deduplication removed=%v, want %v", legacy, legacyWant)
	}
	if reflect.DeepEqual(legacy, want) {
		t.Fatalf("fixture failed to detect old non-deduplicating behavior: %v", legacy)
	}
	for id, expected := range canonical {
		if legacy[id].valid != expected.valid {
			t.Fatalf("source tuple collapse changed owner proof for %s: canonical=%v legacy=%v", id, canonical, legacy)
		}
	}
	if legacy["dedupe"].sources <= canonical["dedupe"].sources {
		t.Fatalf("old non-deduplicating SQL must retain redundant raw rows without changing owner proof: canonical=%v legacy=%v", canonical, legacy)
	}
}

func TestPersonalVisibleSourceBasisDeduplicatesRawAllowedMetadata(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(uuid,session_id,user_id,profile_email,login_email,billing_provider,account_id,project_hash,record_type,raw)
		SELECT 'basis-identical-'||n,'basis','uid-owner','owner@example.com','owner-login@example.com','','','','user','{}'::jsonb
		FROM generate_series(1,16) n`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(uuid,session_id,user_id,profile_email,login_email,billing_provider,account_id,project_hash,record_type,raw) VALUES
		('basis-login','basis','uid-owner','owner@example.com','blocked@example.com','','','','user','{}'::jsonb),
		('basis-account','basis','uid-owner','owner@example.com','owner-login@example.com','fixture','basis-excluded','','user','{}'::jsonb),
		('basis-project','basis','uid-owner','owner@example.com','owner-login@example.com','','','basis-blocked','user','{}'::jsonb),
		('basis-allowed-variant','basis','uid-owner','owner@example.com','other-login@example.com','fixture','basis-other','basis-other','user','{}'::jsonb),
		('basis-outside-candidate','outside','uid-owner','owner@example.com','owner-login@example.com','','','','user','{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_accounts(login_email) VALUES('blocked@example.com')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_billing_accounts(billing_provider,account_id) VALUES('fixture','basis-excluded')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO blocked_projects(project_hash) VALUES('basis-blocked')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot,identity_provenance)
		VALUES('basis','uid-owner','owner@example.com','claude',now(),'{}'::jsonb,'personal_token')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_live_states(session_id,user_id,profile_email,agent,pid,proc_start,status,heartbeat_at,identity_provenance)
		VALUES('basis','uid-owner','owner@example.com','claude',201,'basis','idle',now(),'personal_token')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_observation_events(session_id,user_id,profile_email,agent,event_id,kind,observed_at,identity_provenance)
		VALUES('basis','uid-owner','owner@example.com','claude','basis-shared','state_change',now(),'shared_key_claim')`); err != nil {
		t.Fatal(err)
	}

	query := personalVisibleSourceQuery(" AND session_id IN (SELECT session_id FROM candidate)")
	query = `WITH candidate(session_id) AS (VALUES ('basis')),
		` + strings.TrimPrefix(query, "WITH ") + `
		SELECT (SELECT count(*) FROM source_basis WHERE session_id='basis'),
			(SELECT count(*) FROM sources WHERE session_id='basis'),
			(SELECT count(*) FROM source_basis WHERE session_id='basis' AND login_email='blocked@example.com'),
			(SELECT count(*) FROM source_basis WHERE session_id='basis' AND billing_provider='fixture' AND account_id='basis-excluded'),
			(SELECT count(*) FROM source_basis WHERE session_id='basis' AND project_hash='basis-blocked'),
			(SELECT count(*) FROM source_basis WHERE session_id='basis' AND provenance='personal_token'),
			(SELECT count(*) FROM source_basis WHERE session_id='basis' AND provenance='shared_key_claim'),
			(SELECT count(*) FROM source_basis WHERE session_id='outside'),
			EXISTS(SELECT 1 FROM valid_sources WHERE session_id='basis')`
	var basisRows, sourceRows, loginVariants, billingVariants, projectVariants int
	var personalTokenVariants, sharedClaimVariants, outsideRows int
	var valid bool
	if err := s.pool.QueryRow(ctx, query, int64(0), "uid-owner", "owner@example.com").Scan(
		&basisRows, &sourceRows, &loginVariants, &billingVariants, &projectVariants,
		&personalTokenVariants, &sharedClaimVariants, &outsideRows, &valid); err != nil {
		t.Fatal(err)
	}
	if basisRows != 8 || sourceRows != 5 || loginVariants != 1 || billingVariants != 1 || projectVariants != 1 ||
		personalTokenVariants != 2 || sharedClaimVariants != 1 || outsideRows != 0 || valid {
		t.Fatalf("raw allowed-basis projection rows=%d sources=%d login=%d billing=%d project=%d personal-token=%d shared-claim=%d outside=%d valid=%t",
			basisRows, sourceRows, loginVariants, billingVariants, projectVariants,
			personalTokenVariants, sharedClaimVariants, outsideRows, valid)
	}
}

func personalFixture(t *testing.T, s *PgStore, email, uid string) int64 {
	t.Helper()
	var id int64
	if err := s.pool.QueryRow(context.Background(), `INSERT INTO dashboard_users (email,password_hash,role,cctrace_user_id)
		VALUES($1,'fixture','user',$2) RETURNING id`, email, uid).Scan(&id); err != nil {
		t.Fatalf("dashboard user: %v", err)
	}
	return id
}

func personalSource(t *testing.T, s *PgStore, session, email, uid string) {
	t.Helper()
	personalSourceAt(t, s, session, email, uid, time.Now())
}

func personalSourceAt(t *testing.T, s *PgStore, session, email, uid string, at time.Time) {
	t.Helper()
	if err := s.InsertSessionRecords(context.Background(), []*SessionRecord{{Ts: at, SessionID: session, RecordType: "user", ProfileEmail: email, UserID: uid, UUID: "r-" + session, Raw: json.RawMessage(`{"text":"fixture"}`)}}); err != nil {
		t.Fatalf("source: %v", err)
	}
}

func TestPersonalOverviewValuesPageOwnershipAndProof(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := personalFixture(t, s, "owner@example.com", "uid-owner")
	other := personalFixture(t, s, "other@example.com", "uid-other")
	for _, sid := range []string{"own", "class-only", "no-state", "mixed", "excluded", "expired", "rebound"} {
		personalSource(t, s, sid, "owner@example.com", "uid-owner")
	}
	text := strings.Repeat("한", 123) + "\nprivate second line"
	class := "working"
	for _, sid := range []string{"own", "mixed", "excluded", "expired", "rebound"} {
		if _, err := s.PutPersonalSession(ctx, id, sid, "", "uid-owner", &class, &text, 0); err != nil {
			t.Fatalf("save %s: %v", sid, err)
		}
	}
	if _, err := s.PutPersonalSession(ctx, id, "class-only", "", "uid-owner", &class, nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,record_type,user_id,profile_email) VALUES ('mixed','user','uid-other','other@example.com')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_sessions(session_id) VALUES ('excluded')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_personal_state SET expires_at=now()-interval '1 second' WHERE session_id='expired'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_personal_state SET source_user_id='uid-other' WHERE session_id='rebound'`); err != nil {
		t.Fatal(err)
	}
	ids := []string{"own", "class-only", "no-state", "mixed", "excluded", "expired", "rebound", "missing"}
	for _, access := range []struct{ email, uid string }{{"", "uid-owner"}, {"owner@example.com", ""}} {
		values, err := s.PersonalSessionOverviewValues(ctx, id, access.email, access.uid, ids)
		if err != nil || len(values) != 2 || values["own"].Classification != "working" || values["own"].FirstLine == nil || *values["own"].FirstLine != strings.Repeat("한", 120) || values["class-only"].Classification != "working" || values["class-only"].FirstLine != nil {
			t.Fatalf("page values (%+v): %+v, %v", access, values, err)
		}
		values, err = s.PersonalSessionOverviewValues(ctx, other, access.email, access.uid, ids)
		if err != nil || len(values) != 0 {
			t.Fatalf("other dashboard values: %+v, %v", values, err)
		}
	}
	values, err := s.PersonalSessionOverviewValues(ctx, id, "", "uid-other", ids)
	if err != nil || len(values) != 0 {
		t.Fatalf("other source values: %+v, %v", values, err)
	}
	values, err = s.PersonalSessionOverviewValues(ctx, id, "", "uid-owner", []string{"own", "own"})
	if err != nil || len(values) != 1 || values["own"].FirstLine == nil {
		t.Fatalf("duplicate page IDs: %+v, %v", values, err)
	}
	values, err = s.PersonalSessionOverviewValues(ctx, id, "", "uid-owner", []string{"missing"})
	if err != nil || len(values) != 0 {
		t.Fatalf("non-page values: %+v, %v", values, err)
	}
}

func TestPersonalSessionPersistenceAndOwnership(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := personalFixture(t, s, "owner@example.com", "uid-one")
	other := personalFixture(t, s, "other@example.com", "uid-other")
	personalSource(t, s, "owned", "owner@example.com", "uid-one")
	class, note := "working", "<script>alert(1)</script>\nprivate second line"
	created, err := s.PutPersonalSession(ctx, id, "owned", "", "uid-one", &class, &note, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Revision != 1 || created.Classification != class {
		t.Fatalf("unexpected create: %+v", created)
	}
	loaded, err := s.GetPersonalSession(ctx, id, "owned", "", "uid-one")
	if err != nil || loaded.Note == nil || loaded.Note.Text != note {
		t.Fatalf("durable note = %+v, %v", loaded, err)
	}
	if _, err = s.GetPersonalSession(ctx, other, "owned", "", "uid-other"); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("other user read: %v", err)
	}
	if _, err = s.GetPersonalSession(ctx, other, "owned", "", "uid-one"); err != nil {
		t.Fatalf("same source but other dashboard id can see empty default: %v", err)
	}
	if _, err = s.PutPersonalSession(ctx, id, "owned", "", "uid-one", nil, &note, 0); !errors.Is(err, ErrPersonalConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	if _, err = s.PutPersonalSession(ctx, id, "owned", "", "uid-one", nil, &note, 1); err != nil {
		t.Fatalf("edit: %v", err)
	}
	page, err := s.PersonalSessionNotes(ctx, id, "", "uid-one", 10, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].FirstLine != "<script>alert(1)</script>" {
		t.Fatalf("note list: %+v, %v", page, err)
	}
	counts, err := s.PersonalSessionCounts(ctx, id, "", "uid-one", PersonalCountFilter{})
	if err != nil || counts.Total != 1 || counts.Counts["working"] != 1 {
		t.Fatalf("counts: %+v, %v", counts, err)
	}
	personalSource(t, s, "unsorted-owned", "owner@example.com", "uid-one")
	secondNote := "second"
	if _, err = s.PutPersonalSession(ctx, id, "unsorted-owned", "", "uid-one", nil, &secondNote, 0); err != nil {
		t.Fatalf("second note: %v", err)
	}
	firstPage, err := s.PersonalSessionNotes(ctx, id, "", "uid-one", 1, "")
	if err != nil || len(firstPage.Items) != 1 || !firstPage.Truncated || firstPage.NextCursor == "" {
		t.Fatalf("first note page: %+v, %v", firstPage, err)
	}
	if _, err = s.PutPersonalSession(ctx, id, "unsorted-owned", "", "uid-one", nil, &secondNote, 1); err != nil {
		t.Fatalf("move note ahead of cursor: %v", err)
	}
	secondPage, err := s.PersonalSessionNotes(ctx, id, "", "uid-one", 1, firstPage.NextCursor)
	if err != nil || len(secondPage.Items) != 1 || secondPage.Items[0].SessionID != "owned" {
		t.Fatalf("stable note cursor: %+v, %v", secondPage, err)
	}
	if _, err = s.PersonalSessionNotes(ctx, id, "", "uid-one", 1, "bad-cursor"); !errors.Is(err, ErrPersonalInvalid) {
		t.Fatalf("invalid cursor: %v", err)
	}
	counts, err = s.PersonalSessionCounts(ctx, id, "", "uid-one", PersonalCountFilter{})
	if err != nil || counts.Total != 2 || counts.Counts["unsorted"] != 1 {
		t.Fatalf("full-set classification counts: %+v, %v", counts, err)
	}
	if _, err = s.PutPersonalSession(ctx, id, "owned", "", "uid-one", nil, &note, 2); err != nil {
		t.Fatalf("edit for delete CAS: %v", err)
	}
	deleted, err := s.DeletePersonalSession(ctx, id, "owned", "", "uid-one", 3)
	if err != nil || deleted.Revision != 4 || deleted.Note != nil || deleted.Classification != "unsorted" {
		t.Fatalf("delete: %+v, %v", deleted, err)
	}
}

func TestPersonalOverviewUsesOwnedRollupOutsidePage(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := personalFixture(t, s, "owner@example.com", "uid-owner")
	other := personalFixture(t, s, "other@example.com", "uid-other")
	for _, sid := range []string{"outside-page", "no-note", "other-owned", "reused"} {
		uid, email := "uid-owner", "owner@example.com"
		if sid == "other-owned" {
			uid, email = "uid-other", "other@example.com"
		}
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: time.Now(), SessionID: sid,
			UserID: uid, ProfileEmail: email, RecordType: "user", Agent: "codex", ProjectHash: "actual-project",
			UUID: "r-" + sid, Raw: json.RawMessage(`{}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	note := "private navigation note"
	for _, sid := range []string{"outside-page", "other-owned", "reused"} {
		uid, ownerID := "uid-owner", id
		if sid == "other-owned" {
			uid, ownerID = "uid-other", other
		}
		if _, err := s.PutPersonalSession(ctx, ownerID, sid, "", uid, nil, &note, 0); err != nil {
			t.Fatal(err)
		}
	}
	for _, access := range []struct {
		name, email, uid string
	}{
		{"personal-token", "", "uid-owner"},
		{"legacy-profile", "owner@example.com", ""},
	} {
		t.Run(access.name, func(t *testing.T) {
			got, err := s.GetPersonalSessionOverview(ctx, id, "outside-page", access.email, access.uid)
			if err != nil || got.SessionID != "outside-page" || got.Agent != "codex" || got.ProjectHash != "actual-project" || !got.HasSync || got.EventCount < 1 {
				t.Fatalf("owned actual rollup = %+v, %v", got, err)
			}
		})
	}
	for _, sid := range []string{"no-note", "other-owned", "missing"} {
		if got, err := s.GetPersonalSessionOverview(ctx, id, sid, "", "uid-owner"); !errors.Is(err, ErrPersonalNotFound) {
			t.Fatalf("%s exposed overview %+v, %v", sid, got, err)
		}
	}
	if got, err := s.GetPersonalSessionOverview(ctx, other, "outside-page", "", "uid-owner"); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("another dashboard account exposed overview %+v, %v", got, err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_overview_rollups WHERE session_id='outside-page'`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetPersonalSessionOverview(ctx, id, "outside-page", "", "uid-owner"); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("missing collected overview invented metadata %+v, %v", got, err)
	}
	_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{{
		SessionID: "observation-only", UserID: "uid-owner", ProfileEmail: "owner@example.com",
		IdentityProvenance: "personal_token", ObservedAt: time.Now(), Snapshot: json.RawMessage(`{}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutPersonalSession(ctx, id, "observation-only", "", "uid-owner", nil, &note, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups
		(scope_type,scope_value,session_id,start_time,end_time,has_sync)
		VALUES ('user','uid-owner','observation-only',now(),now(),true)`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetPersonalSessionOverview(ctx, id, "observation-only", "", "uid-owner"); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("observation-only proof promoted to raw overview access %+v, %v", got, err)
	}
	personalSource(t, s, "observation-only", "owner@example.com", "uid-owner")
	if got, err := s.GetPersonalSessionOverview(ctx, id, "observation-only", "", "uid-owner"); err != nil || got == nil || got.SessionID != "observation-only" || !got.HasSync {
		t.Fatalf("verified observation note with owned raw overview = %+v, %v", got, err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_records WHERE session_id='reused'`); err != nil {
		t.Fatal(err)
	}
	personalSource(t, s, "reused", "owner@example.com", "uid-other")
	if got, err := s.GetPersonalSessionOverview(ctx, id, "reused", "", "uid-other"); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("rebound owner exposed old overview %+v, %v", got, err)
	}
	if _, err := s.DeleteSession(ctx, "outside-page", "admin", "erase", false, false); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetPersonalSessionOverview(ctx, id, "outside-page", "", "uid-owner"); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("deleted overview exposed %+v, %v", got, err)
	}
}

func TestPersonalSessionMixedAndObservationProvenance(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := personalFixture(t, s, "owner@example.com", "uid-one")
	note := "verified"
	insert := func(sid, provenance, uid string) {
		t.Helper()
		_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{{SessionID: sid, UserID: uid, ProfileEmail: "owner@example.com", IdentityProvenance: provenance, ObservedAt: time.Now(), Snapshot: json.RawMessage(`{}`)}}})
		if err != nil {
			t.Fatalf("ingest %s: %v", sid, err)
		}
	}
	insert("shared-only", "shared_key_claim", "uid-one")
	if _, err := s.PutPersonalSession(ctx, id, "shared-only", "", "uid-one", nil, &note, 0); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("shared key granted ownership: %v", err)
	}
	insert("unknown-only", "", "uid-one")
	if _, err := s.GetPersonalSession(ctx, id, "unknown-only", "", "uid-one"); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("legacy unknown granted ownership: %v", err)
	}
	insert("trusted-only", "personal_token", "uid-one")
	if _, err := s.PutPersonalSession(ctx, id, "trusted-only", "", "uid-one", nil, &note, 0); err != nil {
		t.Fatalf("verified observation rejected: %v", err)
	}
	insert("mixed", "personal_token", "uid-one")
	personalSource(t, s, "mixed", "other@example.com", "uid-other")
	if _, err := s.GetPersonalSession(ctx, id, "mixed", "", "uid-one"); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("mixed owner read: %v", err)
	}
}

func TestPersonalSharedClaimCannotCompleteVerifiedOwner(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	victim := personalFixture(t, s, "victim@example.com", "uid-victim")
	personalSource(t, s, "partial", "", "uid-real")
	_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{{
		SessionID: "partial", ProfileEmail: "victim@example.com", IdentityProvenance: "shared_key_claim",
		ObservedAt: time.Now(), Snapshot: json.RawMessage(`{}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	note := "must stay private"
	if _, err = s.PutPersonalSession(ctx, victim, "partial", "victim@example.com", "", nil, &note, 0); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("shared-key claim promoted to verified owner: %v", err)
	}
	if countRows(t, s, "session_personal_state", "partial") != 0 {
		t.Fatal("unverified owner created personal row")
	}
}

func TestPersonalRepresentativeUsesOneVerifiedSourceRow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := personalFixture(t, s, "owner@example.com", "uid-a")
	personalSource(t, s, "split", "", "uid-a")
	_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{{
		SessionID: "split", ProfileEmail: "owner@example.com", IdentityProvenance: "personal_token",
		ObservedAt: time.Now(), Snapshot: json.RawMessage(`{}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	note := "verified pairs only"
	if _, err = s.PutPersonalSession(ctx, id, "split", "", "uid-a", nil, &note, 0); err != nil {
		t.Fatal(err)
	}
	var uid, email, proof string
	if err := s.pool.QueryRow(ctx, `SELECT source_user_id,source_profile_email,owner_proof
		FROM session_personal_state WHERE dashboard_user_id=$1 AND session_id='split'`, id).Scan(&uid, &email, &proof); err != nil {
		t.Fatal(err)
	}
	if uid != "uid-a" || email != "" || proof != "session_record" {
		t.Fatalf("representative must be a matching verified row, got (%q,%q,%q)", uid, email, proof)
	}
	if got, err := s.GetPersonalSession(ctx, id, "split", "", "uid-a"); err != nil || got.Note == nil || got.Note.Text != note {
		t.Fatalf("verified split identity cannot read its note: %+v, %v", got, err)
	}
}

func TestPersonalSourceRebindDoesNotRevealStoredNote(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := personalFixture(t, s, "owner@example.com", "uid-a")
	personalSource(t, s, "reused", "owner@example.com", "uid-a")
	note, class := "private old source", "working"
	if _, err := s.PutPersonalSession(ctx, id, "reused", "", "uid-a", &class, &note, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_records WHERE session_id='reused'`); err != nil {
		t.Fatal(err)
	}
	personalSource(t, s, "reused", "owner@example.com", "uid-b")
	if _, err := s.pool.Exec(ctx, `UPDATE dashboard_users SET cctrace_user_id='uid-b' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	for _, action := range []struct {
		name string
		run  func() error
	}{
		{"get", func() error { _, err := s.GetPersonalSession(ctx, id, "reused", "", "uid-b"); return err }},
		{"put", func() error {
			_, err := s.PutPersonalSession(ctx, id, "reused", "", "uid-b", nil, &note, 1)
			return err
		}},
		{"delete", func() error { _, err := s.DeletePersonalSession(ctx, id, "reused", "", "uid-b", 1); return err }},
	} {
		if err := action.run(); !errors.Is(err, ErrPersonalNotFound) {
			t.Errorf("%s after source rebind: %v", action.name, err)
		}
	}
	page, err := s.PersonalSessionNotes(ctx, id, "", "uid-b", 10, "")
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("old note in list: %+v, %v", page, err)
	}
	counts, err := s.PersonalSessionCounts(ctx, id, "", "uid-b", PersonalCountFilter{})
	if err != nil || counts.Counts["working"] != 0 || counts.Total != 0 {
		t.Fatalf("old classification in counts: %+v, %v", counts, err)
	}
	var stored string
	if err := s.pool.QueryRow(ctx, `SELECT note FROM session_personal_state WHERE dashboard_user_id=$1 AND session_id='reused'`, id).Scan(&stored); err != nil || stored != note {
		t.Fatalf("original private note was overwritten: %v", err)
	}
}

func TestPersonalFilteredCountsAndBoundedNotes(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := personalFixture(t, s, "owner@example.com", "uid-one")
	// Keep the two-hour fixture UPDATE away from UTC chunk boundaries.
	now := time.Now().UTC().Truncate(24 * time.Hour).Add(-12 * time.Hour)
	for _, source := range []struct {
		sid, agent, project, entry, class, note string
	}{
		{"a-owned", "claude", "project-a", "cli", "working", "first\nprivate body"},
		{"b-owned", "codex", "project-b", "codex_exec", "done", "second\nprivate body"},
		{"c-owned", "gjc", "project-a", "", "consulting", "third"},
	} {
		record := &SessionRecord{Ts: now, SessionID: source.sid, RecordType: "user", ProfileEmail: "owner@example.com",
			UserID: "uid-one", UUID: "r-" + source.sid, Raw: json.RawMessage(`{}`), Agent: source.agent,
			ProjectHash: source.project, Entrypoint: source.entry, PromptSource: "typed", SourceFile: "fixture.jsonl"}
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{record}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutPersonalSession(ctx, id, source.sid, "", "uid-one", &source.class, &source.note, 0); err != nil {
			t.Fatal(err)
		}
	}
	checks := []struct {
		filter PersonalCountFilter
		want   string
	}{
		{PersonalCountFilter{Agent: "other", ProjectHashes: []string{"project-a"}}, "consulting"},
		{PersonalCountFilter{Agent: "codex", Source: "headless"}, "done"},
		{PersonalCountFilter{Agent: "claude", Source: "interactive", Classification: "working"}, "working"},
		{PersonalCountFilter{FoldLineage: true}, ""},
	}
	for _, check := range checks {
		counts, err := s.PersonalSessionCounts(ctx, id, "", "uid-one", check.filter)
		if err != nil {
			t.Fatalf("filtered counts %+v: %v", check.filter, err)
		}
		if check.want != "" && (counts.Total != 1 || counts.Counts[check.want] != 1) {
			t.Fatalf("filtered counts %+v: %+v", check.filter, counts)
		}
		if check.want == "" && counts.Total != 3 {
			t.Fatalf("unfiltered full count: %+v", counts)
		}
	}
	outside := now.Add(-24 * time.Hour)
	counts, err := s.PersonalSessionCounts(ctx, id, "", "uid-one", PersonalCountFilter{Until: &outside})
	if err != nil || counts.Total != 0 {
		t.Fatalf("outside time range: %+v, %v", counts, err)
	}
	// A session spanning the range is not in it unless an actual source row is.
	since, until := now.Add(-time.Hour), now.Add(time.Hour)
	counts, err = s.PersonalSessionCounts(ctx, id, "", "uid-one", PersonalCountFilter{Since: &since, Until: &until})
	if err != nil || counts.Total != 3 {
		t.Fatalf("inside time range: %+v, %v", counts, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET ts=ts-interval '2 hours' WHERE session_id='b-owned'`); err != nil {
		t.Fatal(err)
	}
	counts, err = s.PersonalSessionCounts(ctx, id, "", "uid-one", PersonalCountFilter{Since: &since, Until: &until})
	if err != nil || counts.Total != 2 || counts.Counts["done"] != 0 {
		t.Fatalf("out-of-window source excluded: %+v, %v", counts, err)
	}
	page, err := s.PersonalSessionNotes(ctx, id, "", "uid-one", 1, "")
	if err != nil || len(page.Items) != 1 || !page.Truncated || page.NextCursor == "" ||
		strings.Contains(page.Items[0].FirstLine, "private body") {
		t.Fatalf("bounded note page: %+v, %v", page, err)
	}
	seen := map[string]bool{page.Items[0].SessionID: true}
	for page.NextCursor != "" {
		page, err = s.PersonalSessionNotes(ctx, id, "", "uid-one", 1, page.NextCursor)
		if err != nil || len(page.Items) != 1 || seen[page.Items[0].SessionID] {
			t.Fatalf("cursor duplicate or lost item: %+v, %v", page, err)
		}
		seen[page.Items[0].SessionID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("cursor omitted notes: %+v", seen)
	}
}

func TestPersonalSharedClaimCannotExtendVerifiedSourceRetention(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	t.Cleanup(func() { forceRetention(t, s, "session_records", nil) })
	forceRetention(t, s, "session_records", nil)
	id := personalFixture(t, s, "owner@example.com", "uid-one")
	old := time.Now().Add(-10 * 24 * time.Hour)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: old, SessionID: "stale-verified-source",
		RecordType: "user", ProfileEmail: "owner@example.com", UserID: "uid-one", UUID: "old-verified",
		Raw: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{{
		SessionID: "stale-verified-source", UserID: "uid-one", ProfileEmail: "owner@example.com",
		IdentityProvenance: "shared_key_claim", ObservedAt: time.Now(), Snapshot: json.RawMessage(`{}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	forceRetention(t, s, "session_records", ptrInt(7))
	note := "private late write"
	if _, err := s.PutPersonalSession(ctx, id, "stale-verified-source", "", "uid-one", nil, &note, 0); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("shared claim extended old verified proof: %v", err)
	}
	if countRows(t, s, "session_personal_state", "stale-verified-source") != 0 {
		t.Fatal("late private state was saved")
	}
}

func TestPersonalOtherVerifiedProofCannotExtendSelectedSourceRetention(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	t.Cleanup(func() { forceRetention(t, s, "session_records", nil) })
	forceRetention(t, s, "session_records", nil)
	id := personalFixture(t, s, "owner@example.com", "uid-one")
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: time.Now().Add(-10 * 24 * time.Hour),
		SessionID: "other-verified-proof", RecordType: "user", UserID: "uid-one", UUID: "old-proof",
		Raw: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{{
		SessionID: "other-verified-proof", UserID: "uid-one", ProfileEmail: "owner@example.com",
		IdentityProvenance: "personal_token", ObservedAt: time.Now(), Snapshot: json.RawMessage(`{}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	forceRetention(t, s, "session_records", ptrInt(7))
	note := "old record proof must not borrow observation age"
	if _, err := s.PutPersonalSession(ctx, id, "other-verified-proof", "", "uid-one", nil, &note, 0); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("different verified proof extended old representative: %v", err)
	}
	if countRows(t, s, "session_personal_state", "other-verified-proof") != 0 {
		t.Fatal("expired representative proof saved private state")
	}
}

func TestPersonalPruneAfterVerifiedSourceRebind(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := personalFixture(t, s, "owner@example.com", "uid-a")
	personalSource(t, s, "rebound-prune", "owner@example.com", "uid-a")
	note := "private old owner"
	if _, err := s.PutPersonalSession(ctx, id, "rebound-prune", "", "uid-a", nil, &note, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_records WHERE session_id='rebound-prune'`); err != nil {
		t.Fatal(err)
	}
	personalSource(t, s, "rebound-prune", "owner@example.com", "uid-b")
	if _, err := s.PrunePersonalState(ctx); err != nil {
		t.Fatal(err)
	}
	if countRows(t, s, "session_personal_state", "rebound-prune") != 0 ||
		countRows(t, s, "session_personal_identity_keys", "rebound-prune") != 0 {
		t.Fatal("old private row or deletion keys survived verified source rebind")
	}
}

func TestPersonalStoredStateRaceWithSessionDeletion(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := personalFixture(t, s, "owner@example.com", "uid-one")
	personalSource(t, s, "race-delete", "owner@example.com", "uid-one")
	note := "race private"
	if _, err := s.PutPersonalSession(ctx, id, "race-delete", "", "uid-one", nil, &note, 0); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	writes := make(chan error, 1)
	deletes := make(chan error, 1)
	go func() {
		<-start
		_, err := s.PutPersonalSession(ctx, id, "race-delete", "", "uid-one", nil, &note, 1)
		writes <- err
	}()
	go func() {
		<-start
		_, err := s.DeleteSession(ctx, "race-delete", "admin", "erase", false, false)
		deletes <- err
	}()
	close(start)
	writeErr, deleteErr := <-writes, <-deletes
	if deleteErr != nil {
		t.Fatalf("delete racing write: %v", deleteErr)
	}
	if writeErr != nil && !errors.Is(writeErr, ErrPersonalNotFound) {
		t.Fatalf("write racing deletion: %v", writeErr)
	}
	if _, err := s.GetPersonalSession(ctx, id, "race-delete", "", "uid-one"); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("tombstone did not hide saved note: %v", err)
	}
	if _, err := s.SweepDeletedSessions(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if countRows(t, s, "session_personal_state", "race-delete") != 0 {
		t.Fatal("sweep kept personal state after racing write")
	}
}

func TestPersonalStoredStateRaceWithRawIdentityDeletion(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := personalFixture(t, s, "owner@example.com", "uid-one")
	personalSource(t, s, "race-raw-delete", "owner@example.com", "uid-one")
	note := "private raw identity"
	if _, err := s.PutPersonalSession(ctx, id, "race-raw-delete", "", "uid-one", nil, &note, 0); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	writes := make(chan error, 1)
	deletes := make(chan error, 1)
	go func() {
		<-start
		_, err := s.PutPersonalSession(ctx, id, "race-raw-delete", "", "uid-one", nil, &note, 1)
		writes <- err
	}()
	go func() {
		<-start
		deletes <- s.DeleteUserData(ctx, "owner@example.com", "")
	}()
	close(start)
	writeErr, deleteErr := <-writes, <-deletes
	if deleteErr != nil {
		t.Fatalf("identity deletion racing write: %v", deleteErr)
	}
	if writeErr != nil && !errors.Is(writeErr, ErrPersonalNotFound) {
		t.Fatalf("write racing identity deletion: %v", writeErr)
	}
	if countRows(t, s, "session_personal_state", "race-raw-delete") != 0 ||
		countRows(t, s, "session_personal_identity_keys", "race-raw-delete") != 0 {
		t.Fatal("raw identity deletion left personal state or deletion keys")
	}
}

func TestPersonalStateRaceWithObservationRetention(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	t.Cleanup(func() { forceRetention(t, s, "session_records", nil) })
	forceRetention(t, s, "session_records", nil)
	id := personalFixture(t, s, "owner@example.com", "uid-one")
	old := time.Now().Add(-10 * 24 * time.Hour)
	_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{{
		SessionID: "retained-observation", UserID: "uid-one", ProfileEmail: "owner@example.com",
		IdentityProvenance: "personal_token", ObservedAt: old, Snapshot: json.RawMessage(`{}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	note := "private retained state"
	if _, err := s.PutPersonalSession(ctx, id, "retained-observation", "", "uid-one", nil, &note, 0); err != nil {
		t.Fatal(err)
	}
	_, err = s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{{
		SessionID: "retained-with-claim", UserID: "uid-one", ProfileEmail: "owner@example.com",
		IdentityProvenance: "personal_token", ObservedAt: old, Snapshot: json.RawMessage(`{}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutPersonalSession(ctx, id, "retained-with-claim", "", "uid-one", nil, &note, 0); err != nil {
		t.Fatal(err)
	}
	_, err = s.IngestSessionObservations(ctx, SessionObservationBatch{Events: []*SessionObservationEvent{{
		SessionID: "retained-with-claim", UserID: "uid-one", ProfileEmail: "owner@example.com",
		IdentityProvenance: "shared_key_claim", EventID: "claim", Kind: "session_end",
		ObservedAt: time.Now(), Payload: json.RawMessage(`{}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	forceRetention(t, s, "session_records", ptrInt(7))
	start := make(chan struct{})
	writes := make(chan error, 1)
	prunes := make(chan error, 1)
	go func() {
		<-start
		_, err := s.PutPersonalSession(ctx, id, "retained-observation", "", "uid-one", nil, &note, 1)
		writes <- err
	}()
	go func() {
		<-start
		_, err := s.PruneSessionObservations(ctx, time.Now().Add(-SessionObservationEventRetention))
		prunes <- err
	}()
	close(start)
	writeErr, pruneErr := <-writes, <-prunes
	if pruneErr != nil {
		t.Fatalf("observation prune: %v", pruneErr)
	}
	if writeErr != nil && !errors.Is(writeErr, ErrPersonalNotFound) {
		t.Fatalf("write racing retention: %v", writeErr)
	}
	if _, err := s.GetPersonalSession(ctx, id, "retained-observation", "", "uid-one"); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("source retention exposed note: %v", err)
	}
	for _, sid := range []string{"retained-observation", "retained-with-claim"} {
		if countRows(t, s, "session_personal_state", sid) != 0 ||
			countRows(t, s, "session_personal_identity_keys", sid) != 0 {
			t.Fatalf("personal state survived source retention transaction: %s", sid)
		}
	}
}

func TestPersonalDeletionMergeAndGlobalRetention(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := personalFixture(t, s, "source@example.com", "uid-src")
	personalSource(t, s, "merge", "source@example.com", "uid-src")
	personalSource(t, s, "erase", "source@example.com", "uid-src")
	note := "sensitive"
	for _, sid := range []string{"merge", "erase"} {
		if _, err := s.PutPersonalSession(ctx, id, sid, "", "uid-src", nil, &note, 0); err != nil {
			t.Fatalf("write %s: %v", sid, err)
		}
	}
	global, err := s.PutPersonalGlobalNote(ctx, id, "all sessions", 0, false)
	if err != nil || global.Revision != 1 {
		t.Fatalf("global write: %+v, %v", global, err)
	}
	if _, err = s.PutPersonalGlobalNote(ctx, id, "stale", 0, false); !errors.Is(err, ErrPersonalConflict) {
		t.Fatalf("global stale: %v", err)
	}
	global, err = s.GetPersonalGlobalNote(ctx, id)
	if err != nil || global.Text != "all sessions" {
		t.Fatalf("global reload: %+v, %v", global, err)
	}
	if err = s.MergeUsers(ctx, "source@example.com", "", "dest@example.com"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	for _, sid := range []string{"merge", "erase"} {
		if _, err = s.GetPersonalSession(ctx, id, sid, "dest@example.com", ""); !errors.Is(err, ErrPersonalNotFound) {
			t.Fatalf("merge exposed %s: %v", sid, err)
		}
		if _, err = s.PutPersonalSession(ctx, id, sid, "dest@example.com", "", nil, &note, 0); !errors.Is(err, ErrPersonalNotFound) {
			t.Fatalf("merge recreated %s: %v", sid, err)
		}
	}
	if _, err = s.PrunePersonalState(ctx); err != nil {
		t.Fatalf("prune invalidated source: %v", err)
	}
	for _, sid := range []string{"merge", "erase"} {
		if countRows(t, s, "session_personal_state", sid) != 1 {
			t.Fatalf("prune lost invalidated row %s while merged source exists", sid)
		}
	}
	// Retention can leave only an immutable deletion key: DeleteUserData must
	// still find and erase the personal row after the raw source has vanished.
	if _, err = s.pool.Exec(ctx, `DELETE FROM session_records WHERE session_id='erase'`); err != nil {
		t.Fatalf("retire raw erase source: %v", err)
	}
	if err = s.DeleteUserData(ctx, "source@example.com", ""); err != nil {
		t.Fatalf("delete original identity: %v", err)
	}
	for _, sid := range []string{"merge", "erase"} {
		if n := countRows(t, s, "session_personal_state", sid); n != 0 {
			t.Fatalf("personal after source deletion %s: %d", sid, n)
		}
	}
	if global, err = s.GetPersonalGlobalNote(ctx, id); err != nil || global.Text != "all sessions" {
		t.Fatalf("source delete erased global: %+v, %v", global, err)
	}
	if _, err = s.PutPersonalGlobalNote(ctx, id, "", 1, true); err != nil {
		t.Fatalf("global delete: %v", err)
	}
	if _, err = s.PutPersonalGlobalNote(ctx, id, "stale", 1, false); !errors.Is(err, ErrPersonalConflict) {
		t.Fatalf("global deleted revision conflict: %v", err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE dashboard_personal_notes SET updated_at=now()-interval '91 days' WHERE dashboard_user_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PrunePersonalState(ctx); err != nil {
		t.Fatalf("prune global: %v", err)
	}
	if global, err = s.GetPersonalGlobalNote(ctx, id); err != nil || global.Revision != 0 {
		t.Fatalf("expired global: %+v, %v", global, err)
	}
}

func TestPersonalNoteLengthAndTombstone(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := personalFixture(t, s, "owner@example.com", "uid-one")
	personalSource(t, s, "deleted", "owner@example.com", "uid-one")
	tooLong := strings.Repeat("x", 16385)
	if _, err := s.PutPersonalSession(ctx, id, "deleted", "", "uid-one", nil, &tooLong, 0); !errors.Is(err, ErrPersonalInvalid) {
		t.Fatalf("oversized note: %v", err)
	}
	note := "private"
	if _, err := s.PutPersonalSession(ctx, id, "deleted", "", "uid-one", nil, &note, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.DeleteSession(ctx, "deleted", "admin", "erase", false, false); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if _, err := s.GetPersonalSession(ctx, id, "deleted", "", "uid-one"); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("tombstone revealed note: %v", err)
	}
	if _, err := s.PutPersonalSession(ctx, id, "deleted", "", "uid-one", nil, &note, 1); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("tombstone accepted write: %v", err)
	}
	if _, err := s.SweepDeletedSessions(ctx, 10); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if countRows(t, s, "session_personal_state", "deleted") != 0 || countRows(t, s, "session_personal_identity_keys", "deleted") != 0 {
		t.Fatal("sweep kept personal rows")
	}
}
