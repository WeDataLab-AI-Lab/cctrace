package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/sessionlog"
)

func commitCallLine(id, command, cwd string, at time.Time) string {
	b, _ := json.Marshal(map[string]any{"type": "assistant", "uuid": "use-" + id, "sessionId": "s", "timestamp": at, "cwd": cwd,
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": command}}}}})
	return string(b)
}

func commitResultLine(id, output string, at time.Time) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "uuid": "result-" + id, "sessionId": "s", "timestamp": at,
		"toolUseResult": map[string]any{"gitOperation": map[string]any{"commit": map[string]any{"sha": "abcdef1234567890"}}},
		"message":       map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": output}}}})
	return string(b)
}

func scanCommitFixture(t *testing.T, path string) ([]*sessionlog.Record, int64) {
	t.Helper()
	r, end, err := sessionlog.ScanFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	return r, end
}

func TestSessionCommitsSourceIdentityBoundsAndPrivacy(t *testing.T) {
	home := t.TempDir()
	s, state := newSnapshotSyncer(t, home, "http://unused.invalid")
	main, _ := writeTranscript(t, home, "s", commitCallLine("same", "git commit -m private-subject", "/no-filesystem-access", t0))
	sub := writeSubagentTranscript(t, home, "s", "a", commitCallLine("same", "git -C /other commit", "/no-filesystem-access", t0), commitResultLine("same", "[main abcdef1] private-output", t0.Add(time.Second)))
	r, end := scanCommitFixture(t, main)
	state.SetOffset(main, end)
	s.noteSessionCommits(main, 20, r)
	r, end = scanCommitFixture(t, sub)
	state.SetOffset(sub, end)
	s.noteSessionCommits(sub, 0, r)
	got := state.Files[main].SessionCommits
	if len(got.Open) != 1 || len(got.Done) != 1 || got.Open[0].Transcript == got.Done[0].Transcript || !got.Partial {
		t.Fatalf("source merge: %+v", got)
	}
	if !reflect.DeepEqual(got.Done[0].OutputHashes, []string{"abcdef1"}) || got.Done[0].StructuredHash != "abcdef1234567890" {
		t.Fatalf("hash proof lost: %+v", got.Done)
	}
	s.noteSessionCommits(sub, 0, r)
	if len(got.Done) != 1 {
		t.Fatal("replayed call duplicated")
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(state.path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Files[main].SessionCommits, got) || *loaded.Files[main].CallsFrom != 20 {
		t.Fatal("offset and call state did not round trip")
	}
	wire, _ := json.Marshal(got)
	if strings.Contains(string(wire), "private-") || strings.Contains(string(wire), "git commit") {
		t.Fatal("raw command or output persisted")
	}
	other := filepath.Join(t.TempDir(), "projects", "p", "s.jsonl")
	state.SetOffset(other, 100)
	s.noteSessionCommits(other, 0, r)
	if state.Files[other].CallsFrom != nil {
		t.Fatal("another home's prefix collected")
	}
}

func TestSessionCommitsBoundedAndAccountExcluded(t *testing.T) {
	home := t.TempDir()
	s, state := newSnapshotSyncer(t, home, "http://unused.invalid")
	var lines []string
	for i := 0; i < maxDoneCommitCalls+2; i++ {
		id := fmt.Sprint(i)
		lines = append(lines, commitCallLine(id, "git commit -m private", "/work/"+id, t0), commitResultLine(id, "nothing to commit, working tree clean", t0.Add(time.Second)))
	}
	for i := 0; i < maxOpenCommitCalls+2; i++ {
		lines = append(lines, commitCallLine("open"+fmt.Sprint(i), "git commit", "/work", t0))
	}
	path, _ := writeTranscript(t, home, "s", lines...)
	r, end := scanCommitFixture(t, path)
	state.SetOffset(path, end)
	s.noteSessionCommits(path, 0, r)
	got := state.Files[path].SessionCommits
	if !got.Truncated || len(got.Open) != maxOpenCommitCalls || len(got.Done) != maxDoneCommitCalls || len(got.Dirs) != maxCommitDirs {
		t.Fatalf("unbounded: open=%d done=%d dirs=%d truncated=%v", len(got.Open), len(got.Done), len(got.Dirs), got.Truncated)
	}
	state.Files[path].SessionCommits = nil
	state.ObserveClaudeAccount(t0.Add(-time.Second), home, "excluded")
	s.client.SetExcludedAccounts([]string{"anthropic:excluded"})
	s.noteSessionCommits(path, 0, r)
	if state.Files[path].SessionCommits != nil {
		t.Fatal("excluded account collected")
	}
}

func TestSessionCommitsExtremeInputAndUnknownResults(t *testing.T) {
	for _, tc := range []struct {
		name, id, command, cwd string
		dropped                bool
	}{
		{"ops", "c", strings.Repeat("git commit;", 1000), "/work", true},
		{"identifier", strings.Repeat("x", 513), "git commit", "/work", true},
		{"directory", "c", "git commit", "/" + strings.Repeat("x", 4097), false},
		{"multiple ops", "c", "git -C /a commit; git -C /b commit", "/work", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			s, state := newSnapshotSyncer(t, home, "http://unused.invalid")
			path, _ := writeTranscript(t, home, "s", commitCallLine(tc.id, tc.command, tc.cwd, t0))
			r, end := scanCommitFixture(t, path)
			state.SetOffset(path, end)
			s.noteSessionCommits(path, 0, r)
			got := state.Files[path].SessionCommits
			if tc.dropped {
				if !got.Truncated || len(got.Open) != 0 {
					t.Fatalf("unbounded input retained: %+v", got)
				}
			} else if len(got.Open) != 1 || !got.Open[0].Incomplete {
				t.Fatalf("ambiguous input accepted: %+v", got)
			}
		})
	}
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`{}`)} {
		if _, complete := commitResultText(raw); complete {
			t.Fatalf("unknown result considered complete: %s", raw)
		}
	}
	if _, complete := commitResultText(json.RawMessage(`""`)); !complete {
		t.Fatal("observed empty output is not missing")
	}
}

func TestSessionCommitsConcurrentCallsAndReadPolicy(t *testing.T) {
	home := t.TempDir()
	s, state := newSnapshotSyncer(t, home, "http://unused.invalid")
	s.client.redact = RedactPolicy{ToolDetails: true}
	for _, sid := range []string{"one", "two"} {
		path, _ := writeTranscript(t, home, sid, commitCallLine(sid, "git commit", "/same-worktree", t0), commitResultLine(sid, "[main abcdef1] private", t0.Add(time.Second)))
		r, end := scanCommitFixture(t, path)
		state.SetOffset(path, end)
		s.noteSessionCommits(path, 0, r)
	}
	for _, fs := range state.Files {
		call := fs.SessionCommits.Done[0]
		if !call.Ambiguous || !call.ReadRedacted {
			t.Fatalf("concurrent/policy evidence lost: %+v", call)
		}
	}
}

func TestSessionCommitsRepositoryAllowlist(t *testing.T) {
	srv := newObservationServer(t)
	home := t.TempDir()
	path, _ := writeTranscript(t, home, "s", commitCallLine("c", "git commit", "/repo-denied", t0))
	s, state := newSnapshotSyncer(t, home, srv.URL())
	s.collectPrefixes = []string{"github.com/allowed/"}
	s.cwdCache["/repo-denied"] = &projectMeta{repositoryID: "github.com/denied/repo", cachedAt: time.Now()}
	mustSync(t, s)
	if fs := state.Files[path]; fs != nil && (fs.SessionCommits != nil || fs.CallsFrom != nil) {
		t.Fatal("denied repository collected calls")
	}
}

func TestSessionCommitsOnlyAdvanceWithAcknowledgedOffset(t *testing.T) {
	var refused atomic.Bool
	refused.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync" && refused.Load() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"inserted":1,"excluded_accounts":[]}`))
	}))
	defer srv.Close()
	home := t.TempDir()
	path, _ := writeTranscript(t, home, "s", commitCallLine("c", "git commit -m private", "/nonexistent", t0), commitResultLine("c", "[main abcdef1] private", t0.Add(time.Second)))
	s, state := newSnapshotSyncer(t, home, srv.URL)
	_, _ = s.SyncOnce(context.Background())
	if fs := state.Files[path]; fs != nil && (fs.Offset != 0 || fs.SessionCommits != nil || fs.CallsFrom != nil) {
		t.Fatalf("failed send advanced call state: %+v", fs)
	}
	if s.LastPass().FilesFailed == 0 {
		t.Fatal("fixture did not refuse the record send")
	}
	refused.Store(false)
	mustSync(t, s)
	fs := state.Files[path]
	if fs == nil || fs.Offset == 0 || fs.CallsFrom == nil || fs.SessionCommits == nil || len(fs.SessionCommits.Done) != 1 {
		t.Fatalf("ack did not persist evidence: %+v", fs)
	}
}
