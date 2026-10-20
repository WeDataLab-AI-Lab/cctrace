package store

import (
	"context"
	"testing"
	"time"

	"cctrace/internal/sessionfacts"
)

func TestSessionFactLoadCopyGateAndFieldMax(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-copy-gate"
	ts := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	var first, second int64
	err = s.pool.QueryRow(ctx, `INSERT INTO session_records(session_id,ts,agent,record_type,uuid,model,message_id,input_tokens,output_tokens)
 VALUES ($1,$2,'claude','assistant','first','no-rate-model','shared',11,1) RETURNING id`, id, ts).Scan(&first)
	if err != nil {
		t.Fatal(err)
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO session_records(session_id,ts,agent,record_type,uuid,model,message_id,input_tokens,output_tokens)
 VALUES ($1,$2,'claude','assistant','second','no-rate-model','shared',5,30) RETURNING id`, id, ts.Add(time.Second)).Scan(&second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_record_copies(srec_id,session_id,original_session_id,record_type,ts) VALUES ($1,$2,'original','assistant',$3)`, second, id, ts.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if before.Input.CopyGate || len(before.Input.CopiedIDs) != 1 || len(before.Input.Heads) != 1 || before.Input.Heads[0].OutputTokens != 30 || before.Input.Heads[0].InputTokens != 11 || before.Input.Heads[0].HasRate || before.Input.Heads[0].Imputed {
		t.Fatalf("gate off: %+v", before.Input)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES ('session_fact_copies_v1') ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM schema_backfills WHERE name = 'session_fact_copies_v1'`)
	})
	after, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if !after.Input.CopyGate || len(after.Input.Heads) != 1 || after.Input.Heads[0].ID != first || after.Input.Heads[0].OutputTokens != 1 || len(after.Input.Rows) != 2 {
		t.Fatalf("gate on: %+v", after.Input)
	}
}

func TestSessionFactLoadCodexUsageAndSnapshot(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-codex-usage"
	ts := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	var userID int64
	err = s.pool.QueryRow(ctx, `INSERT INTO session_records(session_id,ts,agent,record_type,raw,user_id,profile_email)
 VALUES ($1,$2,'codex','user',$3::jsonb,'owner','profile') RETURNING id`, id, ts, `{"type":"response_item","payload":{"type":"message","role":"user","turn_id":"child","root_turn_id":"root","content":[{"type":"input_text","text":"make a feature"}]}}`).Scan(&userID)
	if err != nil {
		t.Fatal(err)
	}
	var usageID int64
	err = s.pool.QueryRow(ctx, `INSERT INTO session_records(session_id,ts,agent,record_type,model,input_tokens,cache_read_tokens,output_tokens,raw,user_id,profile_email)
 VALUES ($1,$2,'codex','usage','gpt-5.2-codex',100,30,5,$3::jsonb,'owner','profile') RETURNING id`, id, ts.Add(time.Second), `{"type":"token_usage_record","payload":{"turn_id":"child","root_turn_id":"root","info":{"model_context_window":258400}}}`).Scan(&usageID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
 VALUES ($1,'owner','profile','codex',$2,$3::jsonb)`, id, ts, `{"codex_turns":[{"turn_id":"child","root_turn_id":"root","end":"complete","model_context_window":258400}]}`)
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(load.Input.Heads) != 0 || len(load.Input.CodexRows) != 2 {
		t.Fatalf("unimputed codex sources: %+v", load.Input)
	}
	// Input slices are unordered; both orders must preserve each source's fields.
	for name, rows := range map[string][]sessionfacts.CodexRow{
		"loaded":   load.Input.CodexRows,
		"reversed": {load.Input.CodexRows[1], load.Input.CodexRows[0]},
	} {
		t.Run(name, func(t *testing.T) {
			seenUser, seenUsage := false, false
			for _, row := range rows {
				switch row.ID {
				case userID:
					if seenUser || row.RecordType != "user" || row.Role != "user" || row.RawType != "response_item" || row.PayloadType != "message" || row.TextHead != "make a feature" || !row.TS.Equal(ts) || row.TurnID != "child" || row.RootTurnID != "root" {
						t.Fatalf("codex user source: %+v", row)
					}
					seenUser = true
				case usageID:
					if seenUsage || row.RecordType != "usage" || row.Role != "" || row.RawType != "token_usage_record" || row.TextHead != "" || !row.TS.Equal(ts.Add(time.Second)) || row.TurnID != "child" || row.RootTurnID != "root" || row.Model != "gpt-5.2-codex" || row.InputTokens != 100 || row.CacheReadTokens != 30 || row.OutputTokens != 5 {
						t.Fatalf("codex usage source: %+v", row)
					}
					seenUsage = true
				default:
					t.Fatalf("unexpected codex source: %+v", row)
				}
			}
			if !seenUser || !seenUsage {
				t.Fatalf("missing codex source: %+v", rows)
			}
		})
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO codex_imputed_cost(srec_id,session_id,ts,model,input_tokens,cache_read_tokens,output_tokens,cost_usd)
 VALUES ($1,$2,$3,'gpt-5.2-codex',70,30,5,0.12)`, usageID, id, ts.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	load, err = s.LoadSessionFactsInput(ctx, id, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(load.Input.CodexRows) != 2 || len(load.Input.Heads) != 1 || load.Input.Heads[0].ID != usageID || load.Input.Heads[0].InputTokens != 70 || !load.Input.Heads[0].HasRate || !load.Input.Heads[0].Imputed || load.Input.Heads[0].CostUSD != 0.12 {
		t.Fatalf("codex usage: %+v", load.Input)
	}
	if len(load.Input.Snapshots) != 1 || len(load.Input.Snapshots[0].CodexTurns) != 1 || load.Input.Snapshots[0].CodexTurns[0].ContextWindow == nil || *load.Input.Snapshots[0].CodexTurns[0].ContextWindow != 258400 {
		t.Fatalf("codex lifecycle: %+v", load.Input.Snapshots)
	}
	fallback, err := s.loadSessionFactsInput(ctx, id, "codex", 0)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Truncated == nil || fallback.Truncated.Session.FirstActivityAt == nil || !fallback.Truncated.Session.FirstActivityAt.Equal(ts) || fallback.Truncated.Session.LastActivityAt == nil || !fallback.Truncated.Session.LastActivityAt.Equal(ts.Add(time.Second)) {
		t.Fatalf("codex fallback without copies: %+v", fallback)
	}
	if string(fallback.Truncated.Hash) != string(sessionfacts.HashOutput(*fallback.Truncated)) {
		t.Fatalf("fallback hash is not normalized")
	}
}

func TestSessionFactLoadCodexFlatText(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-codex-flat"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_records(session_id,ts,agent,record_type,raw)
 VALUES ($1,now(),'codex','user','{"type":"message","role":"user","content":"legacy human input"}'::jsonb),
 ($1,now() + interval '1 second','codex','user','{"type":"message","role":"user","content":[{"type":"input_text","text":"flat text block"}]}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(load.Input.CodexRows) != 2 {
		t.Fatalf("flat codex rows: %+v", load.Input.CodexRows)
	}
	texts := map[string]bool{}
	for _, row := range load.Input.CodexRows {
		if row.Role != "user" {
			t.Fatalf("flat codex role: %+v", row)
		}
		texts[row.TextHead] = true
	}
	if !texts["legacy human input"] || !texts["flat text block"] {
		t.Fatalf("flat codex content: %+v", load.Input.CodexRows)
	}
}

func TestSessionFactLoadCodexCopyPrefixVisibleOnly(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-copy-visible"
	ts := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES ('session_fact_copies_v1') ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM schema_backfills WHERE name = 'session_fact_copies_v1'`)
	})
	_, err = s.pool.Exec(ctx, `INSERT INTO excluded_accounts(login_email) VALUES ('hidden@example.com')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_records(session_id,ts,agent,record_type,login_email)
 VALUES ($1,$2,'codex','user','visible@example.com')`, id, ts)
	if err != nil {
		t.Fatal(err)
	}
	var copied int64
	err = s.pool.QueryRow(ctx, `INSERT INTO session_records(session_id,ts,agent,record_type,login_email)
 VALUES ($1,$2,'codex','usage','hidden@example.com') RETURNING id`, id, ts.Add(time.Second)).Scan(&copied)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_record_copies(srec_id,session_id,original_session_id,record_type,ts)
 VALUES ($1,$2,'original','usage',$3)`, copied, id, ts.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.loadSessionFactsInput(ctx, id, "codex", 0)
	if err != nil {
		t.Fatal(err)
	}
	if load.Truncated == nil || load.Truncated.Session.FirstActivityAt == nil || !load.Truncated.Session.FirstActivityAt.Equal(ts) || load.Truncated.Session.LastActivityAt == nil || !load.Truncated.Session.LastActivityAt.Equal(ts) {
		t.Fatalf("invisible copied prefix changed activity: %+v", load)
	}
}

func TestSessionFactLoadExcludedSnapshotAndOTELSource(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-excluded-snapshot"
	ts := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO excluded_accounts(login_email) VALUES ('hidden@example.com')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,login_email,observed_at,snapshot)
 VALUES ($1,'excluded','profile','hidden@example.com',$2,'{"custom_title":"secret"}'::jsonb),
 ($1,'owner','profile','allowed@example.com',$2,'{"custom_title":""}'::jsonb)`, id, ts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO otel_events(session_id,ts,event_name,login_email,cost_usd)
 VALUES ($1,$2,'api_request','hidden@example.com',1),($1,$3,'tool_result','allowed@example.com',null)`, id, ts, ts.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if !load.Input.HasOTEL || len(load.Input.OTEL) != 0 || len(load.Input.Snapshots) != 1 || load.Input.Snapshots[0].UserID != "owner" || load.Input.Snapshots[0].CustomTitle == nil || *load.Input.Snapshots[0].CustomTitle != "" {
		t.Fatalf("visibility: %+v", load.Input)
	}
	fallback, err := s.loadSessionFactsInput(ctx, id, "claude", -1)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Truncated == nil || len(fallback.Truncated.Session.UserIDs) != 1 || fallback.Truncated.Session.UserIDs[0] != "owner" || fallback.Truncated.Session.FirstActivityAt != nil || fallback.Truncated.Session.Transcript {
		t.Fatalf("fallback visibility: %+v", fallback)
	}
}
