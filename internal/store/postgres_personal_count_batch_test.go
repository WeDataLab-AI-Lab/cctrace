package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The shared fixture is truncated by other store tests, so this is serial.
func TestPersonalClassificationBatchesSourceChunkProbes(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,uuid,record_type,user_id,profile_email,ts,raw)
		SELECT 'batch-'||n,'batch-'||n||'-'||r,'user','uid-owner','owner@example.com',now(),'{}'::jsonb
		FROM generate_series(1,16) n CROSS JOIN generate_series(1,16) r`); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 16)
	for i := range ids {
		ids[i] = fmt.Sprintf("batch-%d", i+1)
	}
	refreshOverviewRollupsForTest(t, s, ids)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Exercise the nested-loop choice seen on the deployed hypertable even on
	// this small fixture; the source chunk must still consume the candidate set once.
	if _, err = tx.Exec(ctx, `SET LOCAL enable_hashjoin=off; SET LOCAL enable_mergejoin=off`); err != nil {
		t.Fatal(err)
	}
	for _, identity := range []SessionOverviewFilter{
		{DashboardUserID: owner, PersonalUserID: "uid-owner"},
		{DashboardUserID: owner, PersonalEmail: "owner@example.com"},
	} {
		query, args := sessionOverviewRollupPersonalQuery(identity, true)
		var raw json.RawMessage
		if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON) `+query, args...).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var plan []struct{ Plan map[string]interface{} }
		if err := json.Unmarshal(raw, &plan); err != nil {
			t.Fatal(err)
		}
		inspected := false
		var visit func(map[string]interface{}, bool)
		visit = func(node map[string]interface{}, basis bool) {
			basis = basis || node["Subplan Name"] == "CTE source_basis"
			if relation, ok := node["Relation Name"].(string); basis && ok && (relation == "session_records" || strings.HasSuffix(relation, "_chunk")) {
				inspected = true
				if loops, _ := node["Actual Loops"].(float64); loops > 1 {
					t.Errorf("source chunk scanned %.0f times for one candidate batch; want at most one scan", loops)
				}
			}
			children, _ := node["Plans"].([]interface{})
			for _, child := range children {
				visit(child.(map[string]interface{}), basis)
			}
		}
		visit(plan[0].Plan, false)
		if !inspected {
			t.Fatal("source chunk plan was not inspected")
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for rows.Next() {
			var class string
			var n int
			if err := rows.Scan(&class, &n); err != nil {
				t.Fatal(err)
			}
			if class != "unsorted" || n != 16 {
				t.Errorf("unsaved owner population changed: %s=%d", class, n)
			}
			count += n
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if count != 16 {
			t.Errorf("owned unsorted count=%d, want 16", count)
		}
	}
}
