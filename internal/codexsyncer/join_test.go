package codexsyncer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/syncer"
)

func setMTime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestCodexSyncer_JoinSourceSkipsOnlyWhatWasThereBeforeTheJoin(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")
	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	lines := []string{`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`}
	old := writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-0000000000d1.jsonl", lines)
	fresh := writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-11-bbbbbbbb-0000-0000-0000-0000000000d2.jsonl", lines)
	setMTime(t, old, t0.Add(-time.Hour))
	setMTime(t, fresh, t0.Add(time.Hour))

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := cs.JoinSource(context.Background(), codexDir, t0)
	if err != nil || n != 1 {
		t.Fatalf("JoinSource = %d, %v; want one file skipped", n, err)
	}
	if fs := state.Files[old]; fs == nil || fs.SkippedAtFirstSync == 0 {
		t.Fatalf("the file that was there is skipped to its end and says so: %+v", fs)
	}
	if state.HasFile(fresh) {
		t.Fatal("a file newer than the join was skipped")
	}
	if n, err := cs.JoinSource(context.Background(), codexDir, t0); err != nil || n != 0 {
		t.Fatalf("a second join must find nothing left to do: %d, %v", n, err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*captured) == 0 {
		t.Fatal("the file created after the join was not collected")
	}
	if got := state.GetOffset(old); got != state.Files[old].SkippedAtFirstSync {
		t.Fatalf("the skipped file was read: offset %d", got)
	}
}

// Only the home that is joined is touched: another home's sessions are not the
// join's business.
func TestCodexSyncer_JoinSourceLeavesOtherHomesAlone(t *testing.T) {
	_, _ = newTestServer(t)
	state := newTestState(t)
	joined, other := t.TempDir(), t.TempDir()
	for _, dir := range []string{joined, other} {
		if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	line := []string{`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`}
	a := writeCodexFixture(t, filepath.Join(joined, "sessions"), "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-0000000000e1.jsonl", line)
	b := writeCodexFixture(t, filepath.Join(other, "sessions"), "rollout-2026-04-23T11-30-10-bbbbbbbb-0000-0000-0000-0000000000e2.jsonl", line)
	setMTime(t, a, time.Now().Add(-time.Hour))
	setMTime(t, b, time.Now().Add(-time.Hour))
	cs := New([]string{joined, other}, "user@example.com", "uid-001", state, syncer.NewClient("http://127.0.0.1:1", "", ""), nil)
	if n, err := cs.JoinSource(context.Background(), joined, time.Now()); err != nil || n != 1 {
		t.Fatalf("JoinSource = %d, %v", n, err)
	}
	if !state.HasFile(a) || state.HasFile(b) {
		t.Fatalf("joined home known=%v other home known=%v", state.HasFile(a), state.HasFile(b))
	}
}

// writeAfterJoinLook makes the join's look at path append more to the file right
// after it: an agent writing between the join's time check and its skip.
func writeAfterJoinLook(t *testing.T, path, more string) {
	t.Helper()
	prev := joinStat
	t.Cleanup(func() { joinStat = prev })
	written := false
	joinStat = func(name string) (os.FileInfo, error) {
		fi, err := prev(name)
		if name == path && !written {
			written = true
			f, ferr := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
			if ferr != nil {
				t.Fatal(ferr)
			}
			if _, ferr := f.WriteString(more + "\n"); ferr != nil {
				t.Fatal(ferr)
			}
			f.Close()
		}
		return fi, err
	}
}

// sentText is every request the test server captured, as one string.
func sentText(t *testing.T, captured []map[string]interface{}) string {
	t.Helper()
	b, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The join skips a file to where it was when it judged the file old. A line the
// agent appends after that look is the passes' to read, not skipped with the rest.
func TestCodexSyncer_JoinSourceReadsWhatIsWrittenAfterItsLook(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := func(text string) string {
		return `{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}}`
	}
	t0 := time.Now()
	path := writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-0000000000f1.jsonl", []string{line("before-the-look")})
	setMTime(t, path, t0.Add(-time.Hour))
	writeAfterJoinLook(t, path, line("after-the-look"))

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	if n, err := cs.JoinSource(context.Background(), codexDir, t0); err != nil || n != 1 {
		t.Fatalf("JoinSource = %d, %v; want the file skipped", n, err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sent := sentText(t, *captured); !strings.Contains(sent, "after-the-look") || strings.Contains(sent, "before-the-look") {
		t.Fatalf("want only the line written after the look sent, got %s", sent)
	}
}

// A join saves nothing itself: it runs under the lease and the line locks, and
// the caller saves once after every folder it joins.
func TestCodexSyncer_JoinSourceLeavesTheSaveToTheCaller(t *testing.T) {
	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	line := []string{`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`}
	for _, name := range []string{"rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-0000000000a1.jsonl", "rollout-2026-04-23T11-30-11-aaaaaaaa-0000-0000-0000-0000000000a2.jsonl"} {
		setMTime(t, writeCodexFixture(t, sessDir, name, line), t0.Add(-time.Hour))
	}
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := syncer.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, syncer.NewClient("http://127.0.0.1:1", "", ""), nil)
	if n, err := cs.JoinSource(context.Background(), codexDir, t0); err != nil || n != 2 {
		t.Fatalf("JoinSource = %d, %v; want both files skipped", n, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the join saved the state itself: %v", err)
	}
}
