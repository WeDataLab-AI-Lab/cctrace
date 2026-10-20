package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestTurnContextFullTupleAndAccountProjection(t *testing.T) {
	// Serial: synthetic membership isolates source tuple paging from the engine.
	s := acquireTestStore(t)
	id, _ := seedTurnContext(t, s)
	publishTurnContext(t, s, id)
	ctx := context.Background()
	base := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(id,ts,session_id,uuid,record_type,user_id,profile_email,agent,raw)
 SELECT 1,$1,$2,'same',CASE WHEN g%3=0 THEN 'user' WHEN g%3=1 THEN 'assistant' ELSE 'custom' END,'turn-owner',
 CASE WHEN g%2=0 THEN 'A@example.test' ELSE 'a@example.test' END,'custom-agent','{"message":{"content":"synthetic"}}'::jsonb
 FROM generate_series(0,5) g`, base, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_timeline_members(session_id,source_ts,source_uuid,source_record_id,source_record_type,source_profile_email,turn_key)
 SELECT session_id,ts,uuid,id,record_type,profile_email,'tuple-turn' FROM session_records WHERE session_id=$1 AND uuid='same'`, id); err != nil {
		t.Fatal(err)
	}
	a := SessionAnalysisAccess{DashboardUserID: 991}
	seen := map[string]bool{}
	q := TurnContextRequest{Limit: 1}
	for pass := 0; pass < 6; pass++ {
		v, err := s.TurnContext(ctx, id, "tuple-turn", a, q)
		if err != nil || v.State != "ready" || len(v.Records) != 1 {
			t.Fatalf("page=%d result=%+v err=%v", pass, v, err)
		}
		r := v.Records[0]
		key := fmt.Sprintf("%s/%s/%s", r.UUID, r.RecordType, r.ProfileEmail)
		if seen[key] {
			t.Fatalf("duplicate tuple=%s", key)
		}
		seen[key] = true
		q.Revision = v.Revision
		if pass < 5 {
			if v.NextCursor == nil {
				t.Fatalf("premature end at page %d", pass)
			}
			q.Cursor = *v.NextCursor
		} else if v.NextCursor != nil {
			t.Fatal("spurious extra page")
		}
	}
	a.ProfileEmail = "A@example.test"
	v, err := s.TurnContext(ctx, id, "tuple-turn", a, TurnContextRequest{Limit: 100})
	if err != nil || len(v.Records) != 3 {
		t.Fatalf("account page=%+v err=%v", v, err)
	}
	for _, r := range v.Records {
		if r.ProfileEmail != a.ProfileEmail {
			t.Fatal("cross-account body")
		}
	}
}
