package codexsyncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/gitctx"
	"cctrace/internal/syncer"
)

// The cwd and model saved with a file's offset are where the next pass starts
// its carry: a record written after the offset has no cwd or model of its own
// until the next turn_context. So what is saved must be the scanner's state at
// the consumed boundary. These tests end a pass on a turn_context that no
// record follows yet, which is where the last record's values and the
// scan-end values differ.

const (
	scanEndRepoA = "github.com/example-org/cctrace"
	scanEndRepoB = "github.com/org/repo"
)

func scanEndTurnContext(ts, cwd, model string) string {
	return `{"type":"turn_context","timestamp":"` + ts + `","payload":{"cwd":"` + cwd + `","model":"` + model + `"}}`
}

func scanEndUserTurn(ts string) string {
	return `{"type":"response_item","timestamp":"` + ts + `","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`
}

// scanEndHead is a session opened in /work/a on gpt-5.
func scanEndHead() []string {
	return []string{
		`{"type":"session_meta","timestamp":"2026-08-24T09:00:00.000Z","payload":{"cwd":"/work/a","model_provider":"openai","originator":"codex-tui"}}`,
		scanEndTurnContext("2026-08-24T09:00:01.000Z", "/work/a", "gpt-5"),
	}
}

// newScanEndSyncer writes lines as one rollout that the first pass scans from
// the start, with /work/a and /work/b resolving to two different repositories.
//
// It replaces the package-level resolveGit, so the tests built on it are not
// parallel.
func newScanEndSyncer(t *testing.T, srvURL string, collectPrefixes, lines []string) (*CodexSyncer, *syncer.State, string) {
	t.Helper()
	prevResolve := resolveGit
	resolveGit = func(cwd string) gitctx.Context {
		// No repository root: sendProjectRules returns before any rule scan.
		switch cwd {
		case "/work/a":
			return gitctx.Context{RepositoryID: scanEndRepoA}
		case "/work/b":
			return gitctx.Context{RepositoryID: scanEndRepoB}
		}
		return gitctx.Context{RepositoryID: "local:" + cwd}
	}
	t.Cleanup(func() { resolveGit = prevResolve })

	state := newTestState(t)
	home := t.TempDir()
	path := writeRollout(t, home, "rollout-2026-08-24T09-00-00-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl", lines)
	// Known at offset 0, so the first pass reads the rollout instead of skipping
	// it as history that predates the first sync.
	state.SetOffset(path, 0)
	cs := New([]string{home}, testProfileEmail, "uid-001", state, syncer.NewClient(srvURL, "", ""), collectPrefixes)
	return cs, state, path
}

func appendScanEndLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func scanEndPass(t *testing.T, cs *CodexSyncer, want int) {
	t.Helper()
	n, err := cs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != want {
		t.Fatalf("SyncOnce sent %d records, want %d", n, want)
	}
}

// assertScanEndEnvelope checks the identity one captured sync request went out
// under and the record types and models it carried, in order.
func assertScanEndEnvelope(t *testing.T, body map[string]interface{}, projectHash, repositoryID string, typesAndModels ...string) {
	t.Helper()
	if body["project_hash"] != projectHash {
		t.Errorf("project_hash = %v, want %s", body["project_hash"], projectHash)
	}
	if body["repository_id"] != repositoryID {
		t.Errorf("repository_id = %v, want %s", body["repository_id"], repositoryID)
	}
	records, _ := body["records"].([]interface{})
	if len(records)*2 != len(typesAndModels) {
		t.Fatalf("request carried %d records, want %d", len(records), len(typesAndModels)/2)
	}
	for i, r := range records {
		rec, _ := r.(map[string]interface{})
		if rec["record_type"] != typesAndModels[2*i] || rec["model"] != typesAndModels[2*i+1] {
			t.Errorf("record %d = %v on %v, want %s on %s", i, rec["record_type"], rec["model"], typesAndModels[2*i], typesAndModels[2*i+1])
		}
	}
}

func TestSyncFile_recordAfterTrailingTurnContextIsSentUnderItsCWD(t *testing.T) {
	srv, captured := newTestServer(t)
	cs, state, path := newScanEndSyncer(t, srv.URL, nil, append(scanEndHead(),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
		scanEndTurnContext("2026-08-24T09:00:03.000Z", "/work/b", "gpt-5"),
	))

	scanEndPass(t, cs, 1)
	if len(*captured) != 1 {
		t.Fatalf("sync requests after the first pass = %d, want 1", len(*captured))
	}
	// The record of this pass was written in /work/a and still goes out as such.
	assertScanEndEnvelope(t, (*captured)[0], "-work-a", scanEndRepoA, "user", "gpt-5")
	if got := state.Files[path].CWD; got != "/work/b" {
		t.Errorf("cwd saved with the offset = %q, want /work/b, where the scan ended", got)
	}

	appendScanEndLines(t, path, scanEndUserTurn("2026-08-24T09:00:04.000Z"))
	scanEndPass(t, cs, 1)
	if len(*captured) != 2 {
		t.Fatalf("sync requests after the second pass = %d, want 2", len(*captured))
	}
	assertScanEndEnvelope(t, (*captured)[1], "-work-b", scanEndRepoB, "user", "gpt-5")
}

func TestSyncFile_recordAfterTrailingTurnContextCarriesItsModel(t *testing.T) {
	srv, captured := newTestServer(t)
	cs, state, path := newScanEndSyncer(t, srv.URL, nil, append(scanEndHead(),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
		scanEndTurnContext("2026-08-24T09:00:03.000Z", "/work/a", "gpt-5.5"),
	))

	scanEndPass(t, cs, 1)
	if len(*captured) != 1 {
		t.Fatalf("sync requests after the first pass = %d, want 1", len(*captured))
	}
	assertScanEndEnvelope(t, (*captured)[0], "-work-a", scanEndRepoA, "user", "gpt-5")
	if got := state.Files[path].Model; got != "gpt-5.5" {
		t.Errorf("model saved with the offset = %q, want gpt-5.5, where the scan ended", got)
	}

	appendScanEndLines(t, path, scanEndUserTurn("2026-08-24T09:00:04.000Z"))
	scanEndPass(t, cs, 1)
	if len(*captured) != 2 {
		t.Fatalf("sync requests after the second pass = %d, want 2", len(*captured))
	}
	assertScanEndEnvelope(t, (*captured)[1], "-work-a", scanEndRepoA, "user", "gpt-5.5")
}

// The session moved from an allowed repository to one outside the allowlist.
// Saving the allowed cwd would send the excluded repository's next record
// under the allowed identity.
func TestSyncFile_recordAfterMoveToExcludedRepositoryIsNotSent(t *testing.T) {
	srv, captured := newTestServer(t)
	cs, state, path := newScanEndSyncer(t, srv.URL, []string{scanEndRepoA}, append(scanEndHead(),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
		scanEndTurnContext("2026-08-24T09:00:03.000Z", "/work/b", "gpt-5"),
	))

	scanEndPass(t, cs, 1)
	if len(*captured) != 1 {
		t.Fatalf("sync requests after the first pass = %d, want 1", len(*captured))
	}
	assertScanEndEnvelope(t, (*captured)[0], "-work-a", scanEndRepoA, "user", "gpt-5")

	appendScanEndLines(t, path, scanEndUserTurn("2026-08-24T09:00:04.000Z"))
	scanEndPass(t, cs, 0)
	if len(*captured) != 1 {
		t.Fatalf("sync requests after the second pass = %d, want 1: the record written in %s was sent as %v",
			len(*captured), scanEndRepoB, (*captured)[len(*captured)-1]["repository_id"])
	}
	assertConsumed(t, state, path)
}

// The mirror case, through the allowlist drop's own save: the session moved
// from an excluded repository into an allowed one. Saving the excluded cwd
// would drop the allowed repository's next record.
func TestSyncFile_recordAfterMoveToAllowedRepositoryIsSent(t *testing.T) {
	srv, captured := newTestServer(t)
	cs, state, path := newScanEndSyncer(t, srv.URL, []string{scanEndRepoB}, append(scanEndHead(),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
		scanEndTurnContext("2026-08-24T09:00:03.000Z", "/work/b", "gpt-5"),
	))

	scanEndPass(t, cs, 0)
	if len(*captured) != 0 {
		t.Fatalf("sync requests after the first pass = %d, want 0: its record was written in %s", len(*captured), scanEndRepoA)
	}
	assertConsumed(t, state, path)
	if got := state.Files[path].CWD; got != "/work/b" {
		t.Errorf("cwd saved with the offset = %q, want /work/b, where the scan ended", got)
	}

	appendScanEndLines(t, path, scanEndUserTurn("2026-08-24T09:00:04.000Z"))
	scanEndPass(t, cs, 1)
	if len(*captured) != 1 {
		t.Fatalf("sync requests after the second pass = %d, want 1", len(*captured))
	}
	assertScanEndEnvelope(t, (*captured)[0], "-work-b", scanEndRepoB, "user", "gpt-5")
}

// The excluded-account drop saves the offset at its own site.
func TestSyncFile_excludedAccountDropSavesScanEndCWDAndModel(t *testing.T) {
	srv, captured := exclusionServer(t, nil)
	cs, state, path := newScanEndSyncer(t, srv.URL, nil, append(scanEndHead(),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
		scanEndTurnContext("2026-08-24T09:00:03.000Z", "/work/b", "gpt-5.5"),
	))
	cs.client.SetExcludedAccounts([]string{"openai:acct-personal"})
	// Logged in as that account too, so the observation the pass itself makes
	// agrees with the one below whatever the wall clock says.
	writeCodexAuth(t, cs.homeForFile(path), "acct-personal")
	state.ObserveCodexAccount(time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC), cs.homeForFile(path), "acct-personal")

	scanEndPass(t, cs, 0)
	if len(*captured) != 0 {
		t.Fatalf("sync requests = %d, want 0: the account is excluded", len(*captured))
	}
	assertConsumed(t, state, path)
	if fs := state.Files[path]; fs.CWD != "/work/b" || fs.Model != "gpt-5.5" {
		t.Errorf("saved with the offset: cwd=%q model=%q, want /work/b and gpt-5.5, where the scan ended", fs.CWD, fs.Model)
	}
}

// A failed quota send holds the offset so the same bytes are read again. The
// consumed boundary is then where the pass started, and the metadata that
// belongs to it is what the pass started with -- not the scan-end state of
// bytes the offset does not cover. The tests below hold an offset that way and
// read the same tail again.

// scanEndQuotaServer records each /api/sync body and answers the quota-history
// route with a 500 until quotaUp is set.
func scanEndQuotaServer(t *testing.T) (*httptest.Server, *[]map[string]interface{}, *atomic.Bool) {
	t.Helper()
	var captured []map[string]interface{}
	quotaUp := &atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/quota-samples"):
			if !quotaUp.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		case r.URL.Path == "/api/sync":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			captured = append(captured, body)
		}
		_, _ = w.Write([]byte(`{"inserted":1,"skipped":0}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &captured, quotaUp
}

// newScanEndQuotaSyncer is newScanEndSyncer in a logged-in home, which is what
// gives a rate-limit reading an account to be sent for.
func newScanEndQuotaSyncer(t *testing.T, srvURL string, collectPrefixes, lines []string) (*CodexSyncer, *syncer.State, string) {
	t.Helper()
	cs, state, path := newScanEndSyncer(t, srvURL, collectPrefixes, lines)
	writeCodexAuth(t, cs.homeForFile(path), "acct-quota")
	return cs, state, path
}

func scanEndTotalUsage(in, cached, out int) string {
	return fmt.Sprintf(`{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d}}`, in, cached, out)
}

// scanEndRateLimit is a token_count event carrying a rate-limit reading, the
// thing a failed quota send is about. info is its "info" member as JSON.
func scanEndRateLimit(ts, info string) string {
	return `{"type":"event_msg","timestamp":"` + ts + `","payload":{"type":"token_count","info":` + info +
		`,"rate_limits":{"primary":{"used_percent":22.0,"window_minutes":10080,"resets_at":1786850006},"secondary":null,"plan_type":"pro"}}}`
}

// assertScanEndSaved checks the offset and the cwd and model saved with it.
func assertScanEndSaved(t *testing.T, state *syncer.State, path string, offset int64, cwd, model string) {
	t.Helper()
	fs := state.Files[path]
	if fs.Offset != offset {
		t.Fatalf("offset = %d, want %d", fs.Offset, offset)
	}
	if fs.CWD != cwd || fs.Model != model {
		t.Errorf("saved with offset %d: cwd=%q model=%q, want %s and %s", offset, fs.CWD, fs.Model, cwd, model)
	}
}

func scanEndFileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// Saving the scan-end cwd and model at a held offset would stamp the re-read
// record with a turn_context that comes after it.
func TestSyncFile_heldOffsetKeepsTheCWDAndModelItWasReachedWith(t *testing.T) {
	srv, captured, _ := scanEndQuotaServer(t)
	cs, state, path := newScanEndQuotaSyncer(t, srv.URL, nil, append(scanEndHead(),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
	))
	scanEndPass(t, cs, 1)
	settled := state.GetOffset(path)

	appendScanEndLines(t, path,
		scanEndUserTurn("2026-08-24T09:00:03.000Z"),
		scanEndTurnContext("2026-08-24T09:00:04.000Z", "/work/b", "gpt-5.5"),
		scanEndRateLimit("2026-08-24T09:00:05.000Z", "null"),
	)
	// Twice: the pass that holds the offset, then the pass that reads the same
	// bytes again from it.
	for range 2 {
		scanEndPass(t, cs, 1)
		assertScanEndSaved(t, state, path, settled, "/work/a", "gpt-5")
	}
	if len(*captured) != 3 {
		t.Fatalf("sync requests = %d, want 3", len(*captured))
	}
	for _, body := range *captured {
		assertScanEndEnvelope(t, body, "-work-a", scanEndRepoA, "user", "gpt-5")
	}
}

// The held tail opens with its own turn_context. The re-read meets it again,
// so the record goes out under /work/b each time while the held offset keeps
// the cwd and model of the bytes in front of it.
func TestSyncFile_heldOffsetRetryRereadsItsOwnTurnContext(t *testing.T) {
	srv, captured, quotaUp := scanEndQuotaServer(t)
	cs, state, path := newScanEndQuotaSyncer(t, srv.URL, nil, append(scanEndHead(),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
	))
	scanEndPass(t, cs, 1)
	settled := state.GetOffset(path)

	appendScanEndLines(t, path,
		scanEndTurnContext("2026-08-24T09:00:03.000Z", "/work/b", "gpt-5.5"),
		scanEndUserTurn("2026-08-24T09:00:04.000Z"),
		scanEndRateLimit("2026-08-24T09:00:05.000Z", "null"),
	)
	for range 2 {
		scanEndPass(t, cs, 1)
		assertScanEndSaved(t, state, path, settled, "/work/a", "gpt-5")
	}
	quotaUp.Store(true)
	scanEndPass(t, cs, 1)
	assertScanEndSaved(t, state, path, scanEndFileSize(t, path), "/work/b", "gpt-5.5")

	if len(*captured) != 4 {
		t.Fatalf("sync requests = %d, want 4", len(*captured))
	}
	for _, body := range (*captured)[1:] {
		assertScanEndEnvelope(t, body, "-work-b", scanEndRepoB, "user", "gpt-5.5")
	}
}

// The allowlist drop saves at its own site. Held there with the scan-end cwd,
// the re-read would stamp the excluded repository's record with the allowed
// cwd that follows it and send it.
func TestSyncFile_heldOffsetAfterAllowlistDropDoesNotSendOnRetry(t *testing.T) {
	srv, captured, _ := scanEndQuotaServer(t)
	cs, state, path := newScanEndQuotaSyncer(t, srv.URL, []string{scanEndRepoB}, append(scanEndHead(),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
	))
	scanEndPass(t, cs, 0)
	settled := state.GetOffset(path)

	appendScanEndLines(t, path,
		scanEndUserTurn("2026-08-24T09:00:03.000Z"),
		scanEndTurnContext("2026-08-24T09:00:04.000Z", "/work/b", "gpt-5.5"),
		scanEndRateLimit("2026-08-24T09:00:05.000Z", "null"),
	)
	for range 2 {
		scanEndPass(t, cs, 0)
		assertScanEndSaved(t, state, path, settled, "/work/a", "gpt-5")
	}
	if len(*captured) != 0 {
		t.Fatalf("sync requests = %d, want 0: every record was written in %s, sent as %v",
			len(*captured), scanEndRepoA, (*captured)[0]["repository_id"])
	}
}

// The excluded-account drop saves at its own site too. Nothing of the file is
// consumed while the offset is held at its start, so nothing is saved for it.
func TestSyncFile_heldOffsetAfterExcludedAccountDropSavesNothingAhead(t *testing.T) {
	srv, captured, _ := scanEndQuotaServer(t)
	cs, state, path := newScanEndQuotaSyncer(t, srv.URL, nil, append(scanEndHead(),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
		scanEndTurnContext("2026-08-24T09:00:03.000Z", "/work/b", "gpt-5.5"),
		scanEndRateLimit("2026-08-24T09:00:04.000Z", "null"),
	))
	cs.client.SetExcludedAccounts([]string{"openai:acct-quota"})
	state.ObserveCodexAccount(time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC), cs.homeForFile(path), "acct-quota")

	for range 2 {
		scanEndPass(t, cs, 0)
		assertScanEndSaved(t, state, path, 0, "", "")
		if fs := state.Files[path]; fs.TokenUsageScanned || fs.CodexForkMetadataScanned {
			t.Errorf("scan-end flags saved at offset 0: %+v", fs)
		}
	}
	if len(*captured) != 0 {
		t.Fatalf("sync requests = %d, want 0: the account is excluded", len(*captured))
	}
}

// A fork rollout copies its parent's history ahead of a trigger_turn marker,
// and the scanner emits nothing until it has passed that marker. Saving
// "boundary reached" at an offset in front of the marker opens the gate for
// the re-read, which then sends the parent's history as the child's records.
func TestSyncFile_heldOffsetDoesNotSaveAForkBoundaryItHasNotPassed(t *testing.T) {
	srv, captured, quotaUp := scanEndQuotaServer(t)
	cs, state, path := newScanEndQuotaSyncer(t, srv.URL, nil, []string{
		`{"type":"session_meta","timestamp":"2026-08-24T09:00:00.000Z","payload":{"id":"child-session","forked_from_id":"parent-session","thread_source":"subagent","cwd":"/work/a","model_provider":"openai","originator":"codex-tui"}}`,
		`{"type":"session_meta","timestamp":"2026-08-24T09:00:00.000Z","payload":{"id":"parent-session","thread_source":"user","cwd":"/work/a","model_provider":"openai","originator":"codex-tui"}}`,
		scanEndTurnContext("2026-08-24T09:00:01.000Z", "/work/a", "gpt-5"),
		// The parent's turn, copied in.
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
		`{"type":"inter_agent_communication_metadata","timestamp":"2026-08-24T09:00:03.000Z","payload":{"trigger_turn":true}}`,
		// The child's own turn.
		scanEndUserTurn("2026-08-24T09:00:05.000Z"),
		scanEndRateLimit("2026-08-24T09:00:06.000Z", "null"),
	})
	forkFlags := func() [4]bool {
		fs := state.Files[path]
		return [4]bool{fs.CodexSubagentFork, fs.CodexForkHistoryCopied, fs.CodexForkHasTriggerTurn, fs.CodexForkBoundaryReached}
	}

	for range 2 {
		scanEndPass(t, cs, 1)
		assertScanEndSaved(t, state, path, 0, "", "")
		if got := forkFlags(); got != [4]bool{} {
			t.Errorf("fork flags saved at offset 0 = %v, want none: nothing of the file is consumed", got)
		}
	}
	quotaUp.Store(true)
	scanEndPass(t, cs, 1)
	assertScanEndSaved(t, state, path, scanEndFileSize(t, path), "/work/a", "gpt-5")
	if got := forkFlags(); got != [4]bool{true, true, true, true} {
		t.Errorf("fork flags saved at the end of the file = %v, want all set", got)
	}

	if len(*captured) != 3 {
		t.Fatalf("sync requests = %d, want 3", len(*captured))
	}
	for i, body := range *captured {
		assertScanEndEnvelope(t, body, "-work-a", scanEndRepoA, "user", "gpt-5")
		rec, _ := body["records"].([]interface{})[0].(map[string]interface{})
		if ts, _ := rec["ts"].(string); !strings.HasPrefix(ts, "2026-08-24T09:00:05") {
			t.Errorf("request %d sent the record of %v, want the child's own turn at 09:00:05", i, rec["ts"])
		}
		if rec["entrypoint"] != "codex-subagent" {
			t.Errorf("request %d entrypoint = %v, want codex-subagent", i, rec["entrypoint"])
		}
	}
}

// scanEndUsage is the tokens of each record in a request: input, cached,
// output.
func scanEndUsage(body map[string]interface{}) [][3]interface{} {
	var out [][3]interface{}
	records, _ := body["records"].([]interface{})
	for _, r := range records {
		rec, _ := r.(map[string]interface{})
		out = append(out, [3]interface{}{rec["input_tokens"], rec["cache_read_tokens"], rec["output_tokens"]})
	}
	return out
}

// Usage is the difference between a cumulative total and the last one seen.
// Saving the scan-end total at a held offset makes the re-read measure the
// same events against a total that already includes them.
func TestSyncFile_heldOffsetRetryDerivesTheSameUsage(t *testing.T) {
	srv, captured, quotaUp := scanEndQuotaServer(t)
	cs, state, path := newScanEndQuotaSyncer(t, srv.URL, nil, append(scanEndHead(),
		`{"type":"event_msg","timestamp":"2026-08-24T09:00:02.000Z","payload":{"type":"token_count","info":`+scanEndTotalUsage(100, 40, 20)+`}}`,
	))
	savedTotals := func() [3]int {
		fs := state.Files[path]
		return [3]int{fs.TotalInputTokens, fs.TotalCachedInputTokens, fs.TotalOutputTokens}
	}

	scanEndPass(t, cs, 1)
	settled := state.GetOffset(path)
	if got := savedTotals(); got != [3]int{100, 40, 20} {
		t.Fatalf("totals after the first pass = %v, want 100/40/20", got)
	}

	appendScanEndLines(t, path,
		`{"type":"event_msg","timestamp":"2026-08-24T09:00:03.000Z","payload":{"type":"token_count","info":`+scanEndTotalUsage(175, 70, 25)+`}}`,
		scanEndRateLimit("2026-08-24T09:00:04.000Z", scanEndTotalUsage(250, 100, 30)),
	)
	for range 2 {
		scanEndPass(t, cs, 2)
		assertScanEndSaved(t, state, path, settled, "/work/a", "gpt-5")
		if got := savedTotals(); got != [3]int{100, 40, 20} {
			t.Errorf("totals saved with the held offset = %v, want 100/40/20, the total at that offset", got)
		}
	}
	quotaUp.Store(true)
	scanEndPass(t, cs, 2)
	assertScanEndSaved(t, state, path, scanEndFileSize(t, path), "/work/a", "gpt-5")
	if got := savedTotals(); got != [3]int{250, 100, 30} {
		t.Errorf("totals saved at the end of the file = %v, want 250/100/30", got)
	}

	if len(*captured) != 4 {
		t.Fatalf("sync requests = %d, want 4", len(*captured))
	}
	// What an uninterrupted pass over the same two events sends.
	want := fmt.Sprint([][3]interface{}{{float64(75), float64(30), float64(5)}, {float64(75), float64(30), float64(5)}})
	for i, body := range (*captured)[1:] {
		if got := fmt.Sprint(scanEndUsage(body)); got != want {
			t.Errorf("usage in request %d of the held tail = %s, want %s", i, got, want)
		}
	}
}

// Two flags decide how a token_count event is read, apart from the totals
// themselves. Without a known total its usage is the event's own
// last_token_usage; with one it is the difference from that total. And once a
// token_usage_record has been seen, token_count events are its mirror and are
// not counted at all. Held at the start of the file, neither is known yet:
// saved there, the first would turn the re-read event's usage into its whole
// total, and the second would drop the event.
func TestSyncFile_heldOffsetSavesNoUsageBaselineItHasNotPassed(t *testing.T) {
	srv, captured, quotaUp := scanEndQuotaServer(t)
	cs, state, path := newScanEndQuotaSyncer(t, srv.URL, nil, append(scanEndHead(),
		`{"type":"event_msg","timestamp":"2026-08-24T09:00:02.000Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":60,"cached_input_tokens":20,"output_tokens":10}}}}`,
		`{"type":"token_usage_record","timestamp":"2026-08-24T09:00:03.000Z","payload":{"usage":{"input_tokens":75,"cached_input_tokens":30,"output_tokens":5},"thread_token_usage":{"input_tokens":175,"cached_input_tokens":70,"output_tokens":25}}}`,
		scanEndRateLimit("2026-08-24T09:00:04.000Z", "null"),
	))
	baseline := func() [2]bool {
		fs := state.Files[path]
		return [2]bool{fs.HasTotalTokenUsage, fs.CodexHasTokenUsageRecord}
	}

	for range 2 {
		scanEndPass(t, cs, 2)
		assertScanEndSaved(t, state, path, 0, "", "")
		if got := baseline(); got != [2]bool{} {
			t.Errorf("saved at offset 0: has total=%v, has usage record=%v, want neither: nothing of the file is consumed", got[0], got[1])
		}
	}
	quotaUp.Store(true)
	scanEndPass(t, cs, 2)
	assertScanEndSaved(t, state, path, scanEndFileSize(t, path), "/work/a", "gpt-5")
	if got := baseline(); got != [2]bool{true, true} {
		t.Errorf("saved at the end of the file: has total=%v, has usage record=%v, want both", got[0], got[1])
	}

	if len(*captured) != 3 {
		t.Fatalf("sync requests = %d, want 3", len(*captured))
	}
	// What an uninterrupted pass over the same two lines sends.
	want := fmt.Sprint([][3]interface{}{{float64(60), float64(20), float64(10)}, {float64(75), float64(30), float64(5)}})
	for i, body := range *captured {
		if got := fmt.Sprint(scanEndUsage(body)); got != want {
			t.Errorf("usage in request %d = %s, want %s", i, got, want)
		}
	}
}

// A state file written before these fields existed has an offset and nothing
// else, and the consumed prefix is rescanned to rebuild them. scanEndLegacy
// leaves a rollout in that condition.
func scanEndLegacy(t *testing.T, srvURL string) (*CodexSyncer, *syncer.State, string) {
	t.Helper()
	cs, state, path := newScanEndQuotaSyncer(t, srvURL, nil, append(scanEndHead(),
		`{"type":"event_msg","timestamp":"2026-08-24T09:00:02.000Z","payload":{"type":"token_count","info":`+scanEndTotalUsage(100, 40, 20)+`}}`,
	))
	state.SetOffset(path, scanEndFileSize(t, path))
	if fs := state.Files[path]; fs.CWD != "" || fs.TokenUsageScanned {
		t.Fatalf("fixture is not a legacy state entry: %+v", fs)
	}
	return cs, state, path
}

func assertScanEndRebuilt(t *testing.T, state *syncer.State, path string) {
	t.Helper()
	fs := state.Files[path]
	if !fs.TokenUsageScanned || !fs.CodexForkMetadataScanned || !fs.HasTotalTokenUsage ||
		fs.TotalInputTokens != 100 || fs.TotalCachedInputTokens != 40 || fs.TotalOutputTokens != 20 {
		t.Errorf("rebuilt prefix metadata was not saved: %+v", fs)
	}
}

// The offset can also stay where it is because there is nothing new. The
// metadata rebuilt from the prefix is the scanner's at that offset and is
// saved.
func TestSyncFile_unchangedOffsetSavesTheRebuiltPrefixMetadata(t *testing.T) {
	srv, _, _ := scanEndQuotaServer(t)
	cs, state, path := scanEndLegacy(t, srv.URL)
	settled := state.GetOffset(path)

	scanEndPass(t, cs, 0)
	assertScanEndSaved(t, state, path, settled, "/work/a", "gpt-5")
	assertScanEndRebuilt(t, state, path)
}

// The same rebuild in a pass whose tail has no record and whose quota send
// fails: what is saved is still the rebuilt prefix metadata, not the
// turn_context of the tail the offset is held in front of.
func TestSyncFile_heldRecordlessTailSavesTheRebuiltPrefixMetadata(t *testing.T) {
	srv, captured, quotaUp := scanEndQuotaServer(t)
	cs, state, path := scanEndLegacy(t, srv.URL)
	settled := state.GetOffset(path)

	appendScanEndLines(t, path,
		scanEndTurnContext("2026-08-24T09:00:03.000Z", "/work/b", "gpt-5.5"),
		scanEndRateLimit("2026-08-24T09:00:04.000Z", "null"),
	)
	scanEndPass(t, cs, 0)
	assertScanEndSaved(t, state, path, settled, "/work/a", "gpt-5")
	assertScanEndRebuilt(t, state, path)

	quotaUp.Store(true)
	scanEndPass(t, cs, 0)
	assertScanEndSaved(t, state, path, scanEndFileSize(t, path), "/work/b", "gpt-5.5")
	if len(*captured) != 0 {
		t.Fatalf("sync requests = %d, want 0: the tail has no record", len(*captured))
	}
}

// A tail that holds a turn_context and no record takes the no-records return,
// which has always saved the scan-end values. Pinned so that return keeps
// agreeing with the ones that follow a record.
func TestSyncFile_recordlessTailSavesItsTurnContext(t *testing.T) {
	srv, captured := newTestServer(t)
	cs, state, path := newScanEndSyncer(t, srv.URL, nil, append(scanEndHead(),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
	))
	scanEndPass(t, cs, 1)

	appendScanEndLines(t, path, scanEndTurnContext("2026-08-24T09:00:03.000Z", "/work/b", "gpt-5.5"))
	scanEndPass(t, cs, 0)
	assertConsumed(t, state, path)
	if fs := state.Files[path]; fs.CWD != "/work/b" || fs.Model != "gpt-5.5" {
		t.Errorf("saved with the offset: cwd=%q model=%q, want /work/b and gpt-5.5", fs.CWD, fs.Model)
	}

	appendScanEndLines(t, path, scanEndUserTurn("2026-08-24T09:00:04.000Z"))
	scanEndPass(t, cs, 1)
	if len(*captured) != 2 {
		t.Fatalf("sync requests = %d, want 2", len(*captured))
	}
	assertScanEndEnvelope(t, (*captured)[1], "-work-b", scanEndRepoB, "user", "gpt-5.5")
}

// A session that never leaves its cwd or model is unaffected: the last
// record's values and the scan-end values are the same thing.
func TestSyncFile_singleCWDSessionSendsAndSavesAsBefore(t *testing.T) {
	srv, captured := newTestServer(t)
	cs, state, path := newScanEndSyncer(t, srv.URL, []string{scanEndRepoA}, append(scanEndHead(),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
		`{"type":"response_item","timestamp":"2026-08-24T09:00:03.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"world"}]}}`,
	))

	scanEndPass(t, cs, 2)
	if len(*captured) != 1 {
		t.Fatalf("sync requests after the first pass = %d, want 1", len(*captured))
	}
	assertScanEndEnvelope(t, (*captured)[0], "-work-a", scanEndRepoA, "user", "gpt-5", "assistant", "gpt-5")
	assertConsumed(t, state, path)
	if fs := state.Files[path]; fs.CWD != "/work/a" || fs.Model != "gpt-5" {
		t.Errorf("saved with the offset: cwd=%q model=%q, want /work/a and gpt-5", fs.CWD, fs.Model)
	}

	appendScanEndLines(t, path, scanEndUserTurn("2026-08-24T09:00:04.000Z"))
	scanEndPass(t, cs, 1)
	if len(*captured) != 2 {
		t.Fatalf("sync requests after the second pass = %d, want 2", len(*captured))
	}
	assertScanEndEnvelope(t, (*captured)[1], "-work-a", scanEndRepoA, "user", "gpt-5")
	assertConsumed(t, state, path)
	if fs := state.Files[path]; fs.CWD != "/work/a" || fs.Model != "gpt-5" {
		t.Errorf("saved with the offset after the second pass: cwd=%q model=%q, want /work/a and gpt-5", fs.CWD, fs.Model)
	}
}
