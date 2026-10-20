package syncer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"cctrace/internal/sessionlog"
	"cctrace/internal/store"
)

// reenrichScopeServer answers the capability probe and the exclusion query
// (excluded names the accounts it reports as excluded, keyed provider:id), and
// records every reenrich batch. A plain sync batch is accepted and not recorded.
func reenrichScopeServer(t *testing.T, excluded map[string]bool) (*httptest.Server, func() []*store.SessionRecord) {
	t.Helper()
	var mu sync.Mutex
	var sent []*store.SessionRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/sync/capabilities":
			_ = json.NewEncoder(w).Encode(syncCapabilities{Reenrich: true})
		case "/api/sync/exclusions":
			var q struct {
				Accounts []AccountRef `json:"accounts"`
			}
			_ = json.Unmarshal(body, &q)
			out := []AccountRef{}
			for _, a := range q.Accounts {
				if excluded[a.Key()] {
					out = append(out, a)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"accounts": out})
		case "/api/sync":
			var p SyncPayload
			if err := json.Unmarshal(body, &p); err != nil {
				t.Errorf("decode sync body: %v", err)
			}
			if !p.Reenrich {
				_ = json.NewEncoder(w).Encode(syncResponse{Inserted: len(p.Records)})
				return
			}
			mu.Lock()
			sent = append(sent, p.Records...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(syncResponse{Updated: len(p.Records)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []*store.SessionRecord {
		mu.Lock()
		defer mu.Unlock()
		return append([]*store.SessionRecord(nil), sent...)
	}
}

func writeScopeSession(t *testing.T, claudeDir string, lines []string) (path string, starts []int64) {
	t.Helper()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-reenrich-scope")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	for _, line := range lines {
		starts = append(starts, int64(content.Len()))
		content.WriteString(line + "\n")
	}
	path = filepath.Join(sessionDir, "session.jsonl")
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, starts
}

func uuidsOf(records []*store.SessionRecord) []string {
	out := []string{}
	for _, r := range records {
		out = append(out, r.UUID)
	}
	return out
}

// The bytes the first sync skipped were deliberately never collected. Re-
// enrichment reads every file from byte zero, and now that its update carries raw
// content it must not reach back into that prefix -- including the line the skip
// cut through, which sync could not parse from the middle and so never sent.
func TestReenrichStaysOutOfTheFirstSyncSkip(t *testing.T) {
	lines := []string{
		`{"type":"user","uuid":"u-1","timestamp":"2026-10-01T00:00:00Z","sessionId":"scope-1","message":{"role":"user","content":"before install"}}`,
		`{"type":"assistant","uuid":"u-2","timestamp":"2026-10-01T00:00:01Z","sessionId":"scope-1","message":{"role":"assistant"}}`,
		`{"type":"user","uuid":"u-3","timestamp":"2026-10-01T00:00:02Z","sessionId":"scope-1","message":{"role":"user","content":"after install"}}`,
	}
	for _, tc := range []struct {
		name    string
		skipped func(starts []int64) int64
		want    []string
	}{
		{"nothing skipped", func([]int64) int64 { return 0 }, []string{"u-1", "u-2", "u-3"}},
		{"skip ends at a line start", func(s []int64) int64 { return s[2] }, []string{"u-3"}},
		{"skip ends inside a line", func(s []int64) int64 { return s[1] + 5 }, []string{"u-3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, sent := reenrichScopeServer(t, nil)
			claudeDir := t.TempDir()
			path, starts := writeScopeSession(t, claudeDir, lines)
			state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			fi, _ := os.Stat(path)
			state.SetOffset(path, fi.Size())
			state.Files[path].SkippedAtFirstSync = tc.skipped(starts)

			s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
			if _, err := s.ReenrichOnce(context.Background(), false); err != nil {
				t.Fatalf("ReenrichOnce: %v", err)
			}
			if got := uuidsOf(sent()); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("reenriched %v, want %v", got, tc.want)
			}
		})
	}
}

// A file found shrunk has its offset reset to the size left on disk, and sync
// collects only what is appended after that. The remnant in front of it was never
// read by sync -- it may be the start of a rewrite whose rows were stored before
// it, or may not -- so re-enrichment, which reads from byte zero, leaves it
// alone as it leaves the first-sync skip alone.
func TestReenrichStaysOutOfAShrunkFilesRemnant(t *testing.T) {
	line := func(uuid string) string {
		return `{"type":"user","uuid":"` + uuid + `","timestamp":"2026-10-01T00:00:00Z","sessionId":"scope-3","message":{"role":"user","content":"` + uuid + `"}}`
	}
	srv, sent := reenrichScopeServer(t, nil)
	claudeDir := t.TempDir()
	path, _ := writeScopeSession(t, claudeDir, []string{line("u-old-1"), line("u-old-2"), line("u-old-3")})
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	state.SetOffset(path, fi.Size())
	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)

	// Rewritten smaller, then resumed.
	remnant := line("u-remnant") + "\n"
	if err := os.WriteFile(path, []byte(remnant), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce after the shrink: %v", err)
	}
	if got := state.Files[path].CollectedFrom; got != int64(len(remnant)) {
		t.Fatalf("CollectedFrom = %d, want the size left on disk, %d", got, len(remnant))
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line("u-new") + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce after the regrowth: %v", err)
	}

	if _, err := s.ReenrichOnce(context.Background(), false); err != nil {
		t.Fatalf("ReenrichOnce: %v", err)
	}
	if got := uuidsOf(sent()); !reflect.DeepEqual(got, []string{"u-new"}) {
		t.Errorf("reenriched %v, want only what sync collected after the shrink: [u-new]", got)
	}
}

// A re-enrichment batch carries each line's collected fields and nothing else:
// no message, no key outside sessionlog.CollectedKeys, and a top-level content
// only on system records, only when content was asked for. By default the
// content fields stay behind, named in cctraceWithheld.
func TestReenrichSendsOnlyTheCollectedFields(t *testing.T) {
	lines := []string{
		`{"type":"user","uuid":"u-1","timestamp":"2026-10-01T00:00:00Z","sessionId":"scope-4","promptId":"p-1","message":{"role":"user","content":"PROMPT"},` +
			`"toolUseResult":{"answers":{"Q":"ANSWER"},"status":"completed","stdout":"OUT"}}`,
		`{"type":"system","uuid":"s-1","timestamp":"2026-10-01T00:00:01Z","sessionId":"scope-4","subtype":"away_summary","content":"RECAP"}`,
		`{"type":"system","uuid":"s-2","timestamp":"2026-10-01T00:00:02Z","sessionId":"scope-4","subtype":"turn_duration","content":"NOT-A-RECAP","durationMs":5}`,
		`{"type":"attachment","uuid":"a-1","timestamp":"2026-10-01T00:00:03Z","sessionId":"scope-4","attachment":{"type":"queued_command","commandMode":"prompt","prompt":"QUEUED"}}`,
	}
	collectedKey := map[string]bool{}
	for _, key := range sessionlog.CollectedKeys {
		collectedKey[key] = true
	}
	for _, includeContent := range []bool{false, true} {
		srv, sent := reenrichScopeServer(t, nil)
		claudeDir := t.TempDir()
		path, _ := writeScopeSession(t, claudeDir, lines)
		state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		fi, _ := os.Stat(path)
		state.SetOffset(path, fi.Size())
		s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
		if _, err := s.ReenrichOnce(context.Background(), includeContent); err != nil {
			t.Fatalf("ReenrichOnce: %v", err)
		}
		raws := map[string]map[string]json.RawMessage{}
		for _, r := range sent() {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(r.Raw, &raw); err != nil {
				t.Fatalf("%s: raw %s: %v", r.UUID, r.Raw, err)
			}
			for key := range raw {
				if !collectedKey[key] {
					t.Errorf("includeContent=%v %s: raw carries %q: %s", includeContent, r.UUID, key, r.Raw)
				}
			}
			for _, text := range []string{"PROMPT", "OUT"} {
				if strings.Contains(string(r.Raw), text) {
					t.Errorf("includeContent=%v %s: %s left the machine: %s", includeContent, r.UUID, text, r.Raw)
				}
			}
			raws[r.UUID] = raw
		}
		if len(raws) != len(lines) {
			t.Fatalf("includeContent=%v: sent %d records, want %d", includeContent, len(raws), len(lines))
		}
		if _, ok := raws["s-1"]["content"]; ok != includeContent {
			t.Errorf("includeContent=%v: recap sent=%v", includeContent, ok)
		}
		if _, ok := raws["s-2"]["content"]; ok != includeContent {
			t.Errorf("includeContent=%v: system message sent=%v", includeContent, ok)
		}
		for uuid, want := range map[string]string{"u-1": `["toolUseResult.answers"]`, "s-1": `["content"]`, "s-2": `["content"]`, "a-1": `["attachment.prompt"]`} {
			if includeContent {
				want = ""
			}
			if got := string(raws[uuid]["cctraceWithheld"]); got != want {
				t.Errorf("includeContent=%v %s: cctraceWithheld = %s, want %s", includeContent, uuid, got, want)
			}
		}
	}
}

// An excluded account's rows are not touched by an update any more than they are
// inserted: records are stamped with the account active at their own timestamp
// and dropped before the batch is built, by the local list and by the server's
// answer alike -- the same filter a sync pass applies.
func TestReenrichDropsExcludedAccounts(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	lines := []string{
		`{"type":"user","uuid":"u-work","timestamp":"2026-10-01T01:00:00Z","sessionId":"scope-2","message":{"role":"user","content":"work"}}`,
		`{"type":"user","uuid":"u-personal","timestamp":"2026-10-01T03:00:00Z","sessionId":"scope-2","message":{"role":"user","content":"personal"}}`,
	}
	for _, tc := range []struct {
		name           string
		local          []string
		serverExcludes map[string]bool
	}{
		{"excluded locally", []string{"anthropic:acct-personal"}, nil},
		{"excluded by the server", nil, map[string]bool{"anthropic:acct-personal": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, sent := reenrichScopeServer(t, tc.serverExcludes)
			claudeDir := t.TempDir()
			path, _ := writeScopeSession(t, claudeDir, lines)
			state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			fi, _ := os.Stat(path)
			state.SetOffset(path, fi.Size())
			home := normalizeHome(claudeDir)
			state.ObserveClaudeAccount(t0, home, "acct-work")
			state.ObserveClaudeAccount(t0.Add(2*time.Hour), home, "acct-personal")

			client := NewClient(srv.URL, "", "")
			client.SetExcludedAccounts(tc.local)
			s := New(claudeDir, "p@example.com", "u1", state, client, nil)
			if _, err := s.ReenrichOnce(context.Background(), false); err != nil {
				t.Fatalf("ReenrichOnce: %v", err)
			}
			got := sent()
			if len(got) != 1 || got[0].UUID != "u-work" || got[0].AccountID != "acct-work" {
				t.Fatalf("reenriched %v (accounts %v), want only u-work stamped acct-work", uuidsOf(got), accountsOf(got))
			}
		})
	}
}

func accountsOf(records []*store.SessionRecord) []string {
	out := []string{}
	for _, r := range records {
		out = append(out, r.AccountID)
	}
	return out
}
