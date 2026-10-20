package codexsyncer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cctrace/internal/codexlog"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

func supplementalRecoveryServer(t *testing.T) (*httptest.Server, func() []syncer.SyncPayload) {
	t.Helper()
	var mu sync.Mutex
	var payloads []syncer.SyncPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync/capabilities":
			_, _ = w.Write([]byte(`{"reenrich":true}`))
		case "/api/sync/exclusions":
			_, _ = w.Write([]byte(`{"accounts":[]}`))
		case "/api/sync":
			var p syncer.SyncPayload
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Error(err)
			}
			mu.Lock()
			payloads = append(payloads, p)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted": len(p.Records), "updated": len(p.Records)})
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []syncer.SyncPayload {
		mu.Lock()
		defer mu.Unlock()
		return append([]syncer.SyncPayload(nil), payloads...)
	}
}

func supplementalFixture(t *testing.T) (string, string, []*codexlog.Record, *syncer.State) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeCodexFixture(t, dir, "rollout-2026-10-18T00-00-00-aaaaaaaa-0000-0000-0000-000000000001.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-10-18T00:00:00Z","payload":{"cwd":` + fmt.Sprintf("%q", t.TempDir()) + `,"model":"fixture-model"}}`,
		`{"type":"token_usage_record","timestamp":"2026-10-18T00:00:01Z","payload":{"turn_id":"turn-1","response_id":"response-write","usage":{"cache_write_input_tokens":7}}}`,
		`{"type":"token_usage_record","timestamp":"2026-10-18T00:00:01Z","payload":{"turn_id":"turn-1","response_id":"response-normal","usage":{"input_tokens":3}}}`,
		`{"type":"token_usage_record","timestamp":"2026-10-18T00:00:01Z","payload":{"turn_id":"turn-1","response_id":"response-reasoning","usage":{"reasoning_output_tokens":4}}}`,
		`{"type":"event_msg","timestamp":"2026-10-18T00:00:01Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":3}}}}`,
	})
	records, end, err := codexlog.ScanFile(path, 0, codexlog.SessionIDFromPath(path))
	if err != nil {
		t.Fatal(err)
	}
	state := newTestState(t)
	state.SetOffset(path, end)
	// Reproduce the ledger written by the previous parser: newly supported
	// write/reasoning-only rows occupied no timestamp ordinal at all.
	nudger := syncer.NewConflictNudger(nil)
	for _, r := range records {
		if r.RecordType == "usage" && r.InputTokens != nil && *r.InputTokens > 0 {
			nudger.Apply(toStoreRecord(r, "", "", codexlog.SessionIDFromPath(path), filepath.Base(path), ""))
		}
	}
	state.AdvanceConflictTail(path, 0, end, true, nudger)
	return home, path, records, state
}

func TestSupplementalUsageRecoveryRetainsLegacyKeys(t *testing.T) {
	t.Parallel()
	srv, payloads := supplementalRecoveryServer(t)
	home, path, _, state := supplementalFixture(t)
	originalOffset := state.GetOffset(path)
	s := New([]string{home}, "", "", state, syncer.NewClient(srv.URL, "", ""), nil)
	for i := 0; i < 2; i++ {
		if _, err := s.ReenrichOnce(context.Background(), true); err != nil {
			t.Fatal(err)
		}
	}
	var restored, existing []*store.SessionRecord
	for _, p := range payloads() {
		if p.Reenrich {
			existing = append(existing, p.Records...)
		} else {
			restored = append(restored, p.Records...)
		}
	}
	if len(restored) != 4 {
		t.Fatalf("restored %d rows across two runs, want two supplemental rows per run", len(restored))
	}
	seen := map[string]string{}
	for _, r := range restored {
		if !strings.HasPrefix(r.UUID, "usage-") || r.RecordType != "usage" || len(r.Raw) == 0 {
			t.Errorf("supplemental row lacks stable source identity: %+v", r)
		}
		var source map[string]any
		_ = json.Unmarshal(r.Raw, &source)
		response := source["payload"].(map[string]any)["response_id"].(string)
		key := r.UUID + "|" + r.Ts.Format(time.RFC3339Nano)
		if old, ok := seen[response]; ok && old != key {
			t.Errorf("repeat recovery moved %s from %s to %s", response, old, key)
		}
		seen[response] = key
	}
	if len(existing) != 2 {
		t.Fatalf("metadata update contained %d rows, want one existing normal usage per run", len(existing))
	}
	want := time.Date(2026, 10, 18, 0, 0, 1, 0, time.UTC)
	for _, r := range existing {
		if r.UUID != "" || !r.Ts.Equal(want) || (len(r.Raw) != 0 && string(r.Raw) != "null") {
			t.Errorf("supplemental recovery changed legacy usage key or body: %+v", r)
		}
	}
	if state.GetOffset(path) != originalOffset {
		t.Error("recovery advanced the ordinary sync offset")
	}
	// Live resync and recovery must agree on each source's identity and keep
	// normal usage at its pre-upgrade timestamp.
	state.SetOffset(path, 0)
	if _, err := s.syncFile(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	latest := payloads()[len(payloads())-1]
	for _, r := range latest.Records {
		if r.RecordType != "usage" {
			continue
		}
		if r.InputTokens != nil && *r.InputTokens > 0 {
			if !r.Ts.Equal(want) || r.UUID != "" {
				t.Errorf("live path moved existing usage: %+v", r)
			}
			continue
		}
		var source map[string]any
		_ = json.Unmarshal(r.Raw, &source)
		response := source["payload"].(map[string]any)["response_id"].(string)
		key := r.UUID + "|" + r.Ts.Format(time.RFC3339Nano)
		if !strings.HasPrefix(r.UUID, "usage-") || seen[response] != key {
			t.Errorf("live/recovery supplemental key differs for %s: %s vs %s", response, key, seen[response])
		}
	}
}

func TestContextMetadataExpansionRetainsPreviousInstructionIdentity(t *testing.T) {
	t.Parallel()
	const oldRaw = `{"type":"session_meta","timestamp":"2026-10-18T00:00:00Z","payload":{"base_instructions":{"text":"synthetic instructions"},"model_provider":"openai"}}`
	const newSource = `{"type":"session_meta","timestamp":"2026-10-18T00:00:00Z","payload":{"base_instructions":{"text":"synthetic instructions"},"model_provider":"openai","parent_thread_id":"parent-1","history_mode":"copy"}}`
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(newSource+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	records, _, err := codexlog.ScanFile(path, 0, uuidA)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%d err=%v", len(records), err)
	}
	record := toStoreRecord(records[0], "", "", uuidA, "rollout.jsonl", "")
	digest := sha256.Sum256([]byte(oldRaw))
	if want := fmt.Sprintf("context-%x", digest[:16]); record.UUID != want {
		t.Errorf("metadata expansion moved existing context UUID from %s to %s", want, record.UUID)
	}
	if !strings.Contains(string(record.Raw), "parent-1") {
		t.Error("stable identity discarded newly preserved lineage metadata")
	}
}

func TestSupplementalUsageRecoveryRequiresCollectedLedgerRange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                                                                                                                string
		include, missingLedger, staleLedger, skipFirst, unsentTail, excluded, shrunk, truncated, excludeAccount, disallowed bool
		want                                                                                                                int
	}{
		{name: "explicit trusted recovery", include: true, want: 2},
		{name: "default metadata only"},
		{name: "missing ledger", include: true, missingLedger: true},
		{name: "stale source anchor", include: true, staleLedger: true},
		{name: "first sync consent prefix", include: true, skipFirst: true, want: 1},
		{name: "uncollected tail", include: true, unsentTail: true, want: 1},
		{name: "excluded source interval", include: true, excluded: true},
		{name: "shrunk remnant", include: true, shrunk: true},
		{name: "unobserved shrink", include: true, truncated: true},
		{name: "excluded account", include: true, excludeAccount: true},
		{name: "repository outside allowlist", include: true, disallowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, payloads := supplementalRecoveryServer(t)
			home, path, records, state := supplementalFixture(t)
			fs := state.Files[path]
			if tc.missingLedger {
				fs.ConflictTail = nil
			}
			if tc.staleLedger {
				fs.ConflictTail.Anchor = "different source"
			}
			if tc.skipFirst {
				fs.SkippedAtFirstSync = records[1].Offset
			}
			if tc.unsentTail {
				fs.Offset = records[2].Offset
				fs.ConflictTail.Offset = fs.Offset
				fs.ConflictTail.Anchor = syncer.ConflictAnchor(path, fs.Offset)
			}
			fs.ExcludedSeen = tc.excluded
			if tc.shrunk {
				fs.CollectedFrom = fs.Offset
			}
			if tc.truncated {
				fs.Offset += 1000
			}
			client := syncer.NewClient(srv.URL, "", "")
			if tc.excludeAccount {
				state.ObserveCodexAccount(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), absHome(t, home), "account-1")
				client.SetExcludedAccounts([]string{"openai:account-1"})
			}
			var allowlist []string
			if tc.disallowed {
				allowlist = []string{"github.com/allowed/"}
			}
			s := New([]string{home}, "", "", state, client, allowlist)
			if _, err := s.ReenrichOnce(context.Background(), tc.include); err != nil {
				t.Fatal(err)
			}
			var restored []*store.SessionRecord
			for _, p := range payloads() {
				if !p.Reenrich {
					restored = append(restored, p.Records...)
				}
				if p.Reenrich {
					for _, r := range p.Records {
						if len(r.Raw) != 0 && string(r.Raw) != "null" {
							t.Errorf("default existing-row metadata update restored a source body: %s", r.Raw)
						}
					}
				}
			}
			if len(restored) != tc.want {
				t.Fatalf("restored %d usage rows, want %d", len(restored), tc.want)
			}
		})
	}
}

func TestSupplementalUsageUUIDDeduplicatesReformattedMirrors(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	source := `{"type":"token_usage_record","timestamp":"2026-10-18T00:00:01Z","payload":{"response_id":"response-1","usage":{"cache_write_input_tokens":7,"reasoning_output_tokens":4}}}` + "\n" +
		`{"payload":{"usage":{"reasoning_output_tokens":4,"cache_write_input_tokens":7},"response_id":"response-1"},"timestamp":"2026-10-18T00:00:01+00:00","type":"token_usage_record"}` + "\n"
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	records, _, err := codexlog.ScanFile(path, 0, uuidA)
	if err != nil || len(records) != 2 {
		t.Fatalf("records=%d err=%v", len(records), err)
	}
	first := toStoreRecord(records[0], "", "", uuidA, "first.jsonl", "")
	second := toStoreRecord(records[1], "", "", uuidA, "mirror.jsonl", "")
	if first.UUID == "" || first.UUID != second.UUID || !first.Ts.Equal(second.Ts) {
		t.Errorf("reformatted mirror changed supplemental key: %s/%s vs %s/%s", first.UUID, first.Ts, second.UUID, second.Ts)
	}
	if string(first.Raw) == string(second.Raw) {
		t.Error("mirror fixture did not exercise distinct original serializations")
	}
}
