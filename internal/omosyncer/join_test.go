package omosyncer

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

func TestOmoSyncer_JoinSourceSkipsOnlyWhatWasThereBeforeTheJoin(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")
	omoDir := t.TempDir()
	t0 := time.Now()
	old := writeOmoFixture(t, omoDir, "old", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-0000000000c1.jsonl", []string{sessionHeader("/work/p", "old"), userMessage("m1", "", "2026-01-01T00:00:01.000Z", "before the join")})
	fresh := writeOmoFixture(t, omoDir, "fresh", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-0000000000c2.jsonl", []string{sessionHeader("/work/p", "fresh"), userMessage("m1", "", "2026-01-01T00:00:02.000Z", "after the join")})
	setMTime(t, old, t0.Add(-time.Hour))
	setMTime(t, fresh, t0.Add(time.Hour))

	s := New([]string{omoDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := s.JoinSource(context.Background(), omoDir, t0)
	if err != nil || n != 1 {
		t.Fatalf("JoinSource = %d, %v; want one file skipped", n, err)
	}
	if fs := state.Files[old]; fs == nil || fs.SkippedAtFirstSync == 0 {
		t.Fatalf("the file that was there is skipped to its end and says so: %+v", fs)
	}
	if state.HasFile(fresh) {
		t.Fatal("a file newer than the join was skipped")
	}
	if n, err := s.JoinSource(context.Background(), omoDir, t0); err != nil || n != 0 {
		t.Fatalf("a second join must find nothing left to do: %d, %v", n, err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*captured) == 0 {
		t.Fatal("the file created after the join was not collected")
	}
	if got := state.GetOffset(old); got != state.Files[old].SkippedAtFirstSync {
		t.Fatalf("the skipped file was read: offset %d", got)
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
func TestOmoSyncer_JoinSourceReadsWhatIsWrittenAfterItsLook(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	omoDir := t.TempDir()
	t0 := time.Now()
	path := writeOmoFixture(t, omoDir, "s", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-0000000000c3.jsonl", []string{sessionHeader("/work/p", "s"), userMessage("m1", "", "2026-01-01T00:00:01.000Z", "before-the-look")})
	setMTime(t, path, t0.Add(-time.Hour))
	writeAfterJoinLook(t, path, userMessage("m2", "m1", "2026-01-01T00:00:02.000Z", "after-the-look"))

	s := New([]string{omoDir}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	if n, err := s.JoinSource(context.Background(), omoDir, t0); err != nil || n != 1 {
		t.Fatalf("JoinSource = %d, %v; want the file skipped", n, err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sent := sentText(t, *captured); !strings.Contains(sent, "after-the-look") || strings.Contains(sent, "before-the-look") {
		t.Fatalf("want only the line written after the look sent, got %s", sent)
	}
}

// A join saves nothing itself: it runs under the lease and the line locks, and
// the caller saves once after every folder it joins.
func TestOmoSyncer_JoinSourceLeavesTheSaveToTheCaller(t *testing.T) {
	omoDir := t.TempDir()
	t0 := time.Now()
	for _, n := range []string{"d1", "d2"} {
		name := "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-0000000000" + n + ".jsonl"
		setMTime(t, writeOmoFixture(t, omoDir, n, name, []string{sessionHeader("/work/p", n), userMessage("m1", "", "2026-01-01T00:00:01.000Z", "x")}), t0.Add(-time.Hour))
	}
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := syncer.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	s := New([]string{omoDir}, "user@example.com", "uid-001", state, syncer.NewClient("http://127.0.0.1:1", "", ""), nil)
	if n, err := s.JoinSource(context.Background(), omoDir, t0); err != nil || n != 2 {
		t.Fatalf("JoinSource = %d, %v; want both files skipped", n, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the join saved the state itself: %v", err)
	}
}
