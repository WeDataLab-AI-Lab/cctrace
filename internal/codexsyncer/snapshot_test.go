package codexsyncer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cctrace/internal/projecthash"
	"cctrace/internal/syncer"
)

// ---- rollout lines, in the shapes measured on local rollouts ----

var turnBase = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func lineAt(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05.000Z") }

func sessionMetaLine(at time.Time, cwd string) string {
	return `{"type":"session_meta","timestamp":"` + lineAt(at) + `","payload":{"id":"x","cwd":"` + cwd + `","originator":"codex-tui","model_provider":"openai"}}`
}

func taskStartedLine(turn string, at time.Time) string {
	return fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"task_started","turn_id":%q,"started_at":%d,"model_context_window":258400,"collaboration_mode_kind":"default"}}`,
		lineAt(at), turn, at.Unix())
}

func turnContextLine(turn string, at time.Time, cwd string) string {
	return fmt.Sprintf(`{"type":"turn_context","timestamp":%q,"payload":{"turn_id":%q,"cwd":%q,"model":"test-model","effort":"high","collaboration_mode":{"mode":"default","settings":{"developer_instructions":"DEVELOPER-TEXT"}}}}`,
		lineAt(at), turn, cwd)
}

func userLine(at time.Time) string {
	return `{"type":"response_item","timestamp":"` + lineAt(at) + `","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"do the thing"}]}}`
}

func taskCompleteLine(turn string, at time.Time) string {
	return fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"task_complete","turn_id":%q,"completed_at":%d,"duration_ms":60000,"time_to_first_token_ms":900,"last_agent_message":"FINAL-REPLY-TEXT"}}`,
		lineAt(at), turn, at.Unix())
}

// turnLines are the lines of one whole turn starting at at.
func turnLines(turn string, at time.Time, cwd string) []string {
	return []string{taskStartedLine(turn, at), turnContextLine(turn, at, cwd), userLine(at.Add(time.Second)), taskCompleteLine(turn, at.Add(time.Minute))}
}

func codexSnapshot(t *testing.T, state *syncer.State, path string) *syncer.SessionSnapshotState {
	t.Helper()
	fs := state.Files[path]
	if fs == nil || fs.SessionSnapshot == nil {
		t.Fatalf("%s has no session snapshot", filepath.Base(path))
	}
	return fs.SessionSnapshot
}

func turnsOf(st *syncer.SessionSnapshotState) []string {
	var ids []string
	for _, turn := range st.Snapshot.CodexTurns {
		ids = append(ids, turn.TurnID+"/"+turn.End)
	}
	return ids
}

// observationsServer accepts record sync and answers the exclusion query from
// excluded, recording each session-observations body.
type observationsServer struct {
	mu     sync.Mutex
	bodies []string
}

func newObservationsServer(t *testing.T, excluded map[string]bool) (*httptest.Server, *observationsServer) {
	t.Helper()
	o := &observationsServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync/capabilities":
			_, _ = w.Write([]byte(`{"reenrich":true}`))
		case "/api/sync/exclusions":
			var q struct {
				Accounts []syncer.AccountRef `json:"accounts"`
			}
			_ = json.Unmarshal(body, &q)
			out := []syncer.AccountRef{}
			for _, a := range q.Accounts {
				if excluded[a.Key()] {
					out = append(out, a)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"accounts": out})
		case "/api/session-observations":
			o.mu.Lock()
			o.bodies = append(o.bodies, string(body))
			o.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]int{"accepted": 1, "stored": 1})
		default:
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1, "updated": 1})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, o
}

func (o *observationsServer) requests(t *testing.T) []syncer.SessionObservationsRequest {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []syncer.SessionObservationsRequest
	for _, b := range o.bodies {
		var req syncer.SessionObservationsRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Fatal(err)
		}
		out = append(out, req)
	}
	return out
}

// flusher is the Claude side of a process: an empty Claude home whose flush
// also sends the Codex snapshots of homes.
func flusher(t *testing.T, client *syncer.Client, codexState *syncer.State, homes ...string) *syncer.Syncer {
	t.Helper()
	claudeState, err := syncer.LoadState(filepath.Join(t.TempDir(), "claude-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := syncer.New(t.TempDir(), "p@example.com", "u1", claudeState, client, nil)
	s.ServeCodexSnapshots(codexState, homes)
	return s
}

// A turn started in one pass and ended in the next is one entry of the
// rollout's snapshot, carrying what each line said; the model's reply and the
// developer instructions stay behind.
func TestCodexTurnsMergeAcrossIncrementalScans(t *testing.T) {
	srv, obs := newObservationsServer(t, nil)
	client := syncer.NewClient(srv.URL, "", "")
	home := t.TempDir()
	const cwd = "/Users/alice/myproject"
	path := writeRollout(t, home, "rollout-2026-10-01T09-00-00-"+uuidA+".jsonl", []string{
		sessionMetaLine(turnBase, cwd), taskStartedLine("turn-1", turnBase), turnContextLine("turn-1", turnBase, cwd), userLine(turnBase.Add(time.Second)),
	})
	state := newTestState(t)
	state.SetOffset(path, 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	cs := New([]string{home}, testProfileEmail, "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := turnsOf(codexSnapshot(t, state, path)); len(got) != 1 || got[0] != "turn-1/" {
		t.Fatalf("after the first pass: turns %v, want turn-1 open", got)
	}

	appendLines(t, path, taskCompleteLine("turn-1", turnBase.Add(time.Minute)))
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := codexSnapshot(t, state, path)
	want := `[{"turn_id":"turn-1","started_at":"2026-10-01T09:00:00Z","ended_at":"2026-10-01T09:01:00Z","timestamps_exact":true,"end":"complete",` +
		`"duration_ms":60000,"time_to_first_token_ms":900,"model_context_window":258400,"collaboration_mode_kind":"default","model":"test-model","effort":"high"}]`
	if got, _ := json.Marshal(st.Snapshot.CodexTurns); string(got) != want || st.Revision != 2 {
		t.Fatalf("after the second pass: turns %s revision %d\nwant %s revision 2", got, st.Revision, want)
	}

	if err := flusher(t, client, state, home).FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := obs.requests(t)
	if len(reqs) != 1 || len(reqs[0].Snapshots) != 1 {
		t.Fatalf("observation requests %+v, want one carrying the snapshot", reqs)
	}
	item := reqs[0].Snapshots[0]
	if item.SessionID != uuidA || item.Agent != "codex" || item.BillingProvider != "openai" || item.ProjectHash != projecthash.FromPath(cwd) || len(item.Snapshot.CodexTurns) != 1 {
		t.Errorf("sent %+v, want the rollout's snapshot as a codex session of its project", item)
	}
	for _, text := range []string{"FINAL-REPLY-TEXT", "DEVELOPER-TEXT", "do the thing"} {
		if strings.Contains(obs.bodies[0], text) {
			t.Errorf("%s left the machine in a snapshot", text)
		}
	}
}

// A rollout sync collected part of before snapshots existed is rebuilt from that
// part when its next lines arrive -- never from the part the first sync skipped.
func TestCodexSnapshotRebuildHonoursSkippedAtFirstSync(t *testing.T) {
	srv, _ := newObservationsServer(t, nil)
	home := t.TempDir()
	const cwd = "/Users/alice/myproject"
	lines := append([]string{sessionMetaLine(turnBase, cwd)}, turnLines("turn-skipped", turnBase, cwd)...)
	path := writeRollout(t, home, "rollout-2026-10-01T09-00-00-"+uuidA+".jsonl", lines)
	skipped, _ := os.Stat(path)
	appendLines(t, path, turnLines("turn-collected", turnBase.Add(time.Hour), cwd)...)
	collected, _ := os.Stat(path)
	state := newTestState(t)
	state.SetOffset(path, collected.Size())
	state.Files[path].SkippedAtFirstSync = skipped.Size()
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	appendLines(t, path, turnLines("turn-new", turnBase.Add(2*time.Hour), cwd)...)
	cs := New([]string{home}, testProfileEmail, "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := codexSnapshot(t, state, path)
	if got := turnsOf(st); len(got) != 2 || got[0] != "turn-collected/complete" || got[1] != "turn-new/complete" {
		t.Fatalf("turns %v, want the collected part's turn and the new one, not the skipped one", got)
	}
	if st.Revision != 1 {
		t.Errorf("revision %d, want 1", st.Revision)
	}
}

// A rollout rewritten shorter keeps what remains of it out of its snapshot, as
// out of its records: collection restarts past the remnant, and the rebuild
// once new lines arrive reads only what was collected after it.
func TestCodexSnapshotRebuildLeavesAShrunkRolloutsRemnantOut(t *testing.T) {
	srv, _ := newObservationsServer(t, nil)
	home := t.TempDir()
	const cwd = "/Users/alice/myproject"
	path := writeRollout(t, home, "rollout-2026-10-01T09-00-00-"+uuidA+".jsonl",
		append([]string{sessionMetaLine(turnBase, cwd)}, turnLines("turn-old", turnBase, cwd)...))
	collected, _ := os.Stat(path)
	state := newTestState(t)
	state.SetOffset(path, collected.Size())
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	writeRollout(t, home, filepath.Base(path), []string{sessionMetaLine(turnBase, cwd), taskStartedLine("turn-remnant", turnBase.Add(time.Minute))})
	cs := New([]string{home}, testProfileEmail, "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	appendLines(t, path, turnLines("turn-new", turnBase.Add(time.Hour), cwd)...)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := turnsOf(codexSnapshot(t, state, path)); len(got) != 1 || got[0] != "turn-new/complete" {
		t.Fatalf("turns %v, want the turn written after the rewrite alone", got)
	}
}

// Re-enrichment backfills a rollout's snapshot from the part sync collected, so
// a historical session gets its turns without new lines; it saves the state and
// says how many it backfilled. The skipped part stays out of it.
func TestCodexReenrichBackfillsTheSnapshot(t *testing.T) {
	srv, _ := newObservationsServer(t, nil)
	home := t.TempDir()
	const cwd = "/Users/alice/myproject"
	lines := append([]string{sessionMetaLine(turnBase, cwd)}, turnLines("turn-skipped", turnBase, cwd)...)
	path := writeRollout(t, home, "rollout-2026-10-01T09-00-00-"+uuidA+".jsonl", lines)
	skipped, _ := os.Stat(path)
	appendLines(t, path, turnLines("turn-collected", turnBase.Add(time.Hour), cwd)...)
	collected, _ := os.Stat(path)
	appendLines(t, path, taskStartedLine("turn-not-yet-synced", turnBase.Add(2*time.Hour)))
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, err := syncer.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffset(path, collected.Size())
	state.Files[path].SkippedAtFirstSync = skipped.Size()
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	cs := New([]string{home}, testProfileEmail, "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	if _, err := cs.ReenrichOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := cs.SnapshotsBackfilled(); n != 1 {
		t.Errorf("backfilled %d, want 1", n)
	}
	reloaded, err := syncer.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	st := codexSnapshot(t, reloaded, path)
	if got := turnsOf(st); len(got) != 1 || got[0] != "turn-collected/complete" || st.Revision != 1 {
		t.Fatalf("backfilled turns %v revision %d on disk, want the collected part's turn alone, unsent", got, st.Revision)
	}
	if fs := reloaded.Files[path]; fs.Offset != collected.Size() {
		t.Errorf("offset moved to %d", fs.Offset)
	}
}

// The Codex homes one process serves share its state file, and each logs in on
// its own. A snapshot goes under the account observed for its own home, an
// excluded account's is consumed without being sent, and a rollout of a home
// this process does not serve is left for the process that does.
func TestCodexSnapshotsGoUnderTheirOwnHomesAccount(t *testing.T) {
	srv, obs := newObservationsServer(t, map[string]bool{"openai:acct-personal": true})
	client := syncer.NewClient(srv.URL, "", "")
	homeWork, homePersonal, homeOther := t.TempDir(), t.TempDir(), t.TempDir()
	writeCodexAuth(t, homeWork, "acct-work")
	writeCodexAuth(t, homePersonal, "acct-personal")
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, err := syncer.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Date(2026, 4, 23, 11, 0, 0, 0, time.UTC)
	state.ObserveCodexAccount(observed, absHome(t, homeWork), "acct-work")
	state.ObserveCodexAccount(observed, absHome(t, homePersonal), "acct-personal")
	const cwd = "/Users/alice/myproject"
	const uuidC = "00000000-0000-0000-0000-000000000003"
	var paths []string
	for _, r := range []struct{ home, uuid string }{{homeWork, uuidA}, {homePersonal, uuidB}, {homeOther, uuidC}} {
		path := writeRollout(t, r.home, "rollout-2026-10-01T09-00-00-"+r.uuid+".jsonl",
			append([]string{sessionMetaLine(turnBase, cwd)}, turnLines("turn-"+r.uuid, turnBase, cwd)...))
		state.SetOffset(path, 0)
		paths = append(paths, path)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	cs := New([]string{homeWork, homePersonal, homeOther}, testProfileEmail, "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := flusher(t, client, state, homeWork, homePersonal).FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := obs.requests(t)
	if len(reqs) != 1 || len(reqs[0].Snapshots) != 1 {
		t.Fatalf("observation requests %v, want one carrying a single snapshot", obs.bodies)
	}
	if item := reqs[0].Snapshots[0]; item.SessionID != uuidA || item.AccountID != "acct-work" || item.BillingProvider != "openai" {
		t.Errorf("sent %s under %s/%s, want %s under openai/acct-work", item.SessionID, item.BillingProvider, item.AccountID, uuidA)
	}
	for _, leaked := range []string{uuidB, uuidC} {
		if strings.Contains(obs.bodies[0], leaked) {
			t.Errorf("session %s was sent", leaked)
		}
	}
	reloaded, err := syncer.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for i, wantDirty := range []bool{false, false, true} {
		st := reloaded.Files[paths[i]].SessionSnapshot
		if dirty := st.Revision > st.SentRevision; dirty != wantDirty {
			t.Errorf("%s: dirty %v on disk, want %v", filepath.Base(paths[i]), dirty, wantDirty)
		}
	}
}

// A repository options.collect_repository_prefixes leaves out gets no snapshot,
// and its lines are consumed without being collected: should it be allowed
// later, what is built from collected lines starts past them.
func TestCodexSnapshotSkipsARepositoryThatIsNotCollected(t *testing.T) {
	srv, _ := newObservationsServer(t, nil)
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "remote", "add", "origin", "https://github.com/org/elsewhere.git")
	home := t.TempDir()
	path := writeRollout(t, home, "rollout-2026-10-01T09-00-00-"+uuidA+".jsonl",
		append([]string{sessionMetaLine(turnBase, repo)}, turnLines("turn-1", turnBase, repo)...))
	state := newTestState(t)
	state.SetOffset(path, 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	cs := New([]string{home}, testProfileEmail, "uid-001", state, syncer.NewClient(srv.URL, "", ""), []string{"github.com/org/allowed"})
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	fs := state.Files[path]
	if fs.SessionSnapshot != nil || fs.CollectedFrom != fi.Size() {
		t.Errorf("snapshot %+v, collected from %d; want none, and collection restarting at %d", fs.SessionSnapshot, fs.CollectedFrom, fi.Size())
	}
}
