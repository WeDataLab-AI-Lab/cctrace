package gjcsyncer

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

// A source handed over without the previous holder's cursor is joined: what is
// on disk when the join begins is skipped, as on a first run, and what appears
// after it is read from its start.
func TestGjcSyncer_JoinSourceSkipsOnlyWhatWasThereBeforeTheJoin(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")
	gjcDir := t.TempDir()
	t0 := time.Now()
	oldUUID, newUUID := "019f0000-0000-7000-8000-0000000000a1", "019f0000-0000-7000-8000-0000000000a2"
	old := writeGjcFixture(t, gjcDir, "old", oldUUID, []string{headerLine(oldUUID, "/work/p"), userLine("m1", "", "2026-08-14T10:00:01.000Z", "before the join")})
	fresh := writeGjcFixture(t, gjcDir, "fresh", newUUID, []string{headerLine(newUUID, "/work/p"), userLine("m1", "", "2026-08-14T10:00:02.000Z", "after the join")})
	setMTime(t, old, t0.Add(-time.Hour))
	setMTime(t, fresh, t0.Add(time.Hour))

	gs := New([]string{gjcDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := gs.JoinSource(context.Background(), gjcDir, t0)
	if err != nil || n != 1 {
		t.Fatalf("JoinSource = %d, %v; want one file skipped", n, err)
	}
	if fs := state.Files[old]; fs == nil || fs.SkippedAtFirstSync == 0 {
		t.Fatalf("the file that was there is skipped to its end and says so: %+v", fs)
	}
	if state.HasFile(fresh) {
		t.Fatal("a file newer than the join was skipped")
	}
	if n, err := gs.JoinSource(context.Background(), gjcDir, t0); err != nil || n != 0 {
		t.Fatalf("a second join must find nothing left to do: %d, %v", n, err)
	}

	// The orchestration saves the state after a join; with the first-run window
	// closed, the newer file is read whole.
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := gs.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*captured) == 0 {
		t.Fatal("the file created after the join was not collected")
	}
	if got := state.GetOffset(old); got != state.Files[old].SkippedAtFirstSync {
		t.Fatalf("the skipped file was read: offset %d", got)
	}
	// Files that appear later are read whole too: nothing about the join lingers.
	later := "019f0000-0000-7000-8000-0000000000a3"
	writeGjcFixture(t, gjcDir, "later", later, []string{headerLine(later, "/work/p"), userLine("m1", "", "2026-08-14T10:00:03.000Z", "much later")})
	before := len(*captured)
	if _, err := gs.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*captured) == before {
		t.Fatal("a session created after the join was not collected")
	}
}

func TestGjcSyncer_JoinSourceSkipsSubagentTranscriptsToo(t *testing.T) {
	_, _ = newTestServer(t)
	state := newTestState(t)
	gjcDir := t.TempDir()
	parent := "019f0000-0000-7000-8000-0000000000b1"
	sub := writeGjcSubagentFixture(t, gjcDir, "sub", parent, "task-1", []string{headerLine("019f0000-0000-7000-8000-0000000000b2", "/work/p"), userLine("m1", "", "2026-08-14T10:00:01.000Z", "x")})
	setMTime(t, sub, time.Now().Add(-time.Hour))
	gs := New([]string{gjcDir}, "user@example.com", "uid-001", state, syncer.NewClient("http://127.0.0.1:1", "", ""), nil)
	if n, err := gs.JoinSource(context.Background(), gjcDir, time.Now()); err != nil || n != 1 || !state.HasFile(sub) {
		t.Fatalf("JoinSource = %d, %v", n, err)
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
func TestGjcSyncer_JoinSourceReadsWhatIsWrittenAfterItsLook(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	gjcDir := t.TempDir()
	t0 := time.Now()
	id := "019f0000-0000-7000-8000-0000000000a4"
	path := writeGjcFixture(t, gjcDir, "s", id, []string{headerLine(id, "/work/p"), userLine("m1", "", "2026-08-14T10:00:01.000Z", "before-the-look")})
	setMTime(t, path, t0.Add(-time.Hour))
	writeAfterJoinLook(t, path, userLine("m2", "m1", "2026-08-14T10:00:02.000Z", "after-the-look"))

	gs := New([]string{gjcDir}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	if n, err := gs.JoinSource(context.Background(), gjcDir, t0); err != nil || n != 1 {
		t.Fatalf("JoinSource = %d, %v; want the file skipped", n, err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := gs.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sent := sentText(t, *captured); !strings.Contains(sent, "after-the-look") || strings.Contains(sent, "before-the-look") {
		t.Fatalf("want only the line written after the look sent, got %s", sent)
	}
}

// A join saves nothing itself: it runs under the lease and the line locks, and
// the caller saves once after every folder it joins.
func TestGjcSyncer_JoinSourceLeavesTheSaveToTheCaller(t *testing.T) {
	gjcDir := t.TempDir()
	t0 := time.Now()
	for _, id := range []string{"019f0000-0000-7000-8000-0000000000d1", "019f0000-0000-7000-8000-0000000000d2"} {
		setMTime(t, writeGjcFixture(t, gjcDir, id[len(id)-2:], id, []string{headerLine(id, "/work/p"), userLine("m1", "", "2026-08-14T10:00:01.000Z", "x")}), t0.Add(-time.Hour))
	}
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := syncer.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	gs := New([]string{gjcDir}, "user@example.com", "uid-001", state, syncer.NewClient("http://127.0.0.1:1", "", ""), nil)
	if n, err := gs.JoinSource(context.Background(), gjcDir, t0); err != nil || n != 2 {
		t.Fatalf("JoinSource = %d, %v; want both files skipped", n, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the join saved the state itself: %v", err)
	}
}
