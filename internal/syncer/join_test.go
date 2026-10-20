package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncer_JoinSourceSkipsOnlyWhatWasThereBeforeTheJoin(t *testing.T) {
	var syncCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/sync" {
			syncCalls++
		}
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
	defer srv.Close()
	cwd := t.TempDir()
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-work-p")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"s","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"hi"}}` + "\n"
	old, fresh := filepath.Join(sessionDir, "old.jsonl"), filepath.Join(sessionDir, "fresh.jsonl")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Now()
	if err := os.Chtimes(old, t0.Add(-time.Hour), t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, t0.Add(time.Hour), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	n, err := s.JoinSource(context.Background(), t0)
	if err != nil || n != 1 {
		t.Fatalf("JoinSource = %d, %v; want one file skipped", n, err)
	}
	if fs := state.Files[old]; fs == nil || fs.SkippedAtFirstSync == 0 || fs.Offset != fs.SkippedAtFirstSync {
		t.Fatalf("the file that was there is skipped to its end and says so: %+v", fs)
	}
	if state.HasFile(fresh) {
		t.Fatal("a file newer than the join was skipped")
	}
	if n, err := s.JoinSource(context.Background(), t0); err != nil || n != 0 {
		t.Fatalf("a second join must find nothing left to do: %d, %v", n, err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if syncCalls == 0 {
		t.Fatal("the file created after the join was not collected")
	}
}

func TestSyncer_AdoptedCursorSendsOnlyWhatWasWrittenSince(t *testing.T) {
	run := func(adopt bool) int {
		var posted int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/sync" {
				var body struct {
					Records []json.RawMessage `json:"records"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				posted += len(body.Records)
			}
			_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
		}))
		defer srv.Close()
		cwd := t.TempDir()
		claudeDir := t.TempDir()
		sessionDir := filepath.Join(claudeDir, "projects", "-work-p")
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			t.Fatal(err)
		}
		line := func(id string) string {
			return `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"s","uuid":"` + id + `","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"hi"}}` + "\n"
		}
		path := filepath.Join(sessionDir, "s.jsonl")
		if err := os.WriteFile(path, []byte(line("u1")), 0o644); err != nil {
			t.Fatal(err)
		}
		prev, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := prev.Save(); err != nil {
			t.Fatal(err)
		}
		holder := New(claudeDir, "user@example.com", "u1", prev, NewClient(srv.URL, "", ""), nil)
		if _, err := holder.SyncOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(line("u2"))
		f.Close()
		next := holder
		if adopt {
			state, err := LoadState(filepath.Join(t.TempDir(), "next.json"))
			if err != nil {
				t.Fatal(err)
			}
			state.AdoptSource(prev, Adoption{Path: func(p string) (string, bool) { return p, true }})
			if err := state.Save(); err != nil {
				t.Fatal(err)
			}
			next = New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
		}
		posted = 0
		if _, err := next.SyncOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		return posted
	}
	control, adopted := run(false), run(true)
	if control != 1 || adopted != control {
		t.Fatalf("holder sent %d records, adopter sent %d; both should send only the line written since", control, adopted)
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
			if _, ferr := f.WriteString(more); ferr != nil {
				t.Fatal(ferr)
			}
			f.Close()
		}
		return fi, err
	}
}

// The join skips a file to where it was when it judged the file old. A line the
// agent appends after that look is the passes' to read, not skipped with the rest.
func TestSyncer_JoinSourceReadsWhatIsWrittenAfterItsLook(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/sync" {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
		}
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
	defer srv.Close()
	cwd := t.TempDir()
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-work-p")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := func(id string) string {
		return `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"s","uuid":"` + id + `","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"hi"}}` + "\n"
	}
	path := filepath.Join(sessionDir, "s.jsonl")
	if err := os.WriteFile(path, []byte(line("before-the-look")), 0o644); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if err := os.Chtimes(path, t0.Add(-time.Hour), t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	writeAfterJoinLook(t, path, line("after-the-look"))
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	if n, err := s.JoinSource(context.Background(), t0); err != nil || n != 1 {
		t.Fatalf("JoinSource = %d, %v; want the file skipped", n, err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	sent := strings.Join(bodies, "\n")
	if !strings.Contains(sent, "after-the-look") || strings.Contains(sent, "before-the-look") {
		t.Fatalf("want only the line written after the look sent, got %q", sent)
	}
}

// A join saves nothing itself: it runs under the lease and the line locks, and
// the caller saves once after every folder it joins.
func TestSyncer_JoinSourceLeavesTheSaveToTheCaller(t *testing.T) {
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-work-p")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	for _, name := range []string{"a.jsonl", "b.jsonl"} {
		p := filepath.Join(sessionDir, name)
		if err := os.WriteFile(p, []byte(`{"type":"user","sessionId":"s"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, t0.Add(-time.Hour), t0.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	s := New(claudeDir, "user@example.com", "u1", state, NewClient("http://127.0.0.1:1", "", ""), nil)
	if n, err := s.JoinSource(context.Background(), t0); err != nil || n != 2 {
		t.Fatalf("JoinSource = %d, %v; want both files skipped", n, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the join saved the state itself: %v", err)
	}
}
