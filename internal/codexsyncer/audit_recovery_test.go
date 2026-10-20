package codexsyncer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

func TestAuditCodexContextRecoveryIsConsentBounded(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                                              string
		include, known, skip, excluded, shrunk, truncated bool
		want                                              int
	}{
		{name: "default withholds", known: true},
		{name: "explicit recovery", include: true, known: true, want: 2},
		{name: "unknown source", include: true},
		{name: "first sync prefix", include: true, known: true, skip: true, want: 1},
		{name: "excluded range", include: true, known: true, excluded: true},
		{name: "shrunk remnant", include: true, known: true, shrunk: true},
		{name: "shrink not yet detected by daemon", include: true, known: true, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var restored []*store.SessionRecord
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/sync/capabilities" {
					_, _ = w.Write([]byte(`{"reenrich":true}`))
					return
				}
				var p syncer.SyncPayload
				if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
					t.Error(err)
				}
				for _, record := range p.Records {
					if record.RecordType == "context" {
						if p.Reenrich {
							t.Error("new context must use idempotent insert, not an update of a nonexistent row")
						}
						mu.Lock()
						restored = append(restored, record)
						mu.Unlock()
					}
				}
				_, _ = w.Write([]byte(`{"inserted":2,"updated":1}`))
			}))
			defer srv.Close()
			home := t.TempDir()
			dir := filepath.Join(home, "sessions")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			lines := []string{
				`{"type":"session_meta","timestamp":"2026-10-17T00:00:00Z","payload":{"base_instructions":{"text":"ALLOWED-BASE"}}}`,
				`{"type":"turn_context","timestamp":"2026-10-17T00:00:01Z","payload":{"developer_instructions":"ALLOWED-DEV"}}`,
				`{"type":"response_item","timestamp":"2026-10-17T00:00:02Z","payload":{"type":"message","role":"user","content":"already collected"}}`,
			}
			path := writeCodexFixture(t, dir, "rollout-2026-10-17T00-00-00-aaaaaaaa-0000-0000-0000-000000000001.jsonl", lines)
			state := newTestState(t)
			if tc.known {
				state.SetOffset(path, int64(len(strings.Join(lines, "\n")+"\n")))
				if tc.skip {
					state.Files[path].SkippedAtFirstSync = int64(len(lines[0]) + 1)
				}
				state.Files[path].ExcludedSeen = tc.excluded
				if tc.shrunk {
					state.Files[path].CollectedFrom = state.Files[path].Offset
				}
			}
			// A newer, not-yet-collected tail must stay on the normal sync path.
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString(`{"type":"turn_context","timestamp":"2026-10-17T00:00:03Z","payload":{"developer_instructions":"UNSYNCED-TAIL"}}` + "\n")
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if tc.truncated {
				state.Files[path].Offset += 1000
			}
			s := New([]string{home}, "", "", state, syncer.NewClient(srv.URL, "", ""), nil)
			if _, err := s.ReenrichOnce(context.Background(), tc.include); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(restored) != tc.want {
				t.Fatalf("restored %d context records, want %d", len(restored), tc.want)
			}
			for _, r := range restored {
				if r.UUID == "" || strings.Contains(string(r.Raw), "UNSYNCED-TAIL") {
					t.Errorf("unsafe recovery: %+v", r)
				}
			}
		})
	}
}

func TestAuditCodexContextRecoveryKeepsLiveRecordIdentity(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var payloads []syncer.SyncPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/capabilities" {
			_, _ = w.Write([]byte(`{"reenrich":true}`))
			return
		}
		var p syncer.SyncPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		mu.Lock()
		payloads = append(payloads, p)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]int{"inserted": len(p.Records), "updated": len(p.Records)})
	}))
	defer srv.Close()
	home := t.TempDir()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeCodexFixture(t, dir, "rollout-2026-10-17T00-00-00-aaaaaaaa-0000-0000-0000-000000000001.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-10-17T00:00:00Z","payload":{"base_instructions":{"text":"base"}}}`,
		`{"type":"turn_context","timestamp":"2026-10-17T00:00:00Z","payload":{"developer_instructions":"developer"}}`,
		`{"type":"response_item","timestamp":"2026-10-17T00:00:00Z","payload":{"type":"message","role":"user","content":"first"}}`,
		`{"type":"response_item","timestamp":"2026-10-17T00:00:00Z","payload":{"type":"message","role":"user","content":"second"}}`,
	})
	state := newTestState(t)
	state.SetOffset(path, 0)
	s := New([]string{home}, "", "", state, syncer.NewClient(srv.URL, "", ""), nil)
	if _, err := s.syncFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReenrichOnce(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 3 {
		t.Fatalf("got %d payloads, want live, context recovery, existing-row enrichment", len(payloads))
	}
	live := map[string]time.Time{}
	var users []*store.SessionRecord
	for _, r := range payloads[0].Records {
		if r.RecordType == "context" {
			live[r.UUID] = r.Ts
		} else {
			users = append(users, r)
		}
	}
	if len(live) != 2 || len(users) != 2 {
		t.Fatalf("live records: contexts=%d users=%d", len(live), len(users))
	}
	if delta := users[1].Ts.Sub(users[0].Ts); delta != time.Microsecond {
		t.Errorf("context records changed user timestamp assignment: %s", delta)
	}
	for _, r := range payloads[1].Records {
		if r.UUID == "" || !r.Ts.Equal(live[r.UUID]) {
			t.Errorf("recovery changed the context key: uuid=%s ts=%s", r.UUID, r.Ts)
		}
	}
}
