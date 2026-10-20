package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cctrace/internal/gitcmd"
	"cctrace/internal/gitctx"
)

func TestCommitReceiptStrictProof(t *testing.T) {
	h := strings.Repeat("a", 40)
	other := "aaaaaaa" + strings.Repeat("b", 33)
	call := GitCall{Start: time.Unix(100, 0), End: time.Unix(101, 0), OutputHashes: []string{h[:7]}}
	log := gitctx.CommitLog{Items: []gitctx.Commit{{Hash: h, At: 100, ReflogAt: 100}}}
	if got, why := matchCommitReceipt(call, log); len(got) != 1 || why != "" {
		t.Fatalf("unique receipt: %v %s", got, why)
	}
	log.Items = append(log.Items, gitctx.Commit{Hash: other, At: 100, ReflogAt: 100})
	if _, why := matchCommitReceipt(call, log); why == "" {
		t.Fatal("ambiguous short hash accepted")
	}
	call.OutputHashes = []string{h}
	if got, why := matchCommitReceipt(call, log); len(got) != 1 || why != "" {
		t.Fatal("full receipt not disambiguated")
	}
	call.StructuredHash = other
	if _, why := matchCommitReceipt(call, log); why != "conflicting_hash_evidence" {
		t.Fatalf("conflict: %s", why)
	}
	call.OutputHashes = nil
	call.StructuredHash = ""
	if _, why := matchCommitReceipt(call, log); why != "no_call_hash" {
		t.Fatal("time alone became proof")
	}
	call.OutputHashes = []string{h}
	log.Truncated = true
	if _, why := matchCommitReceipt(call, log); why != "incomplete_reflog" {
		t.Fatal("partial log accepted")
	}
}

func TestCommitTruncatedWithoutRetainedCalls(t *testing.T) {
	home := t.TempDir()
	s, state := newSnapshotSyncer(t, home, "http://unused.invalid")
	path, _ := writeTranscript(t, home, "s", commitCallLine("c", "git commit", "/repo", t0))
	state.SetOffset(path, 100)
	fs := state.Files[path]
	fs.SessionSnapshot = &SessionSnapshotState{Snapshot: newSessionSnapshot()}
	fs.SessionCommits = &SessionCommitsState{Truncated: true}
	if !s.collectSessionCommits(context.Background(), t0) || fs.SessionSnapshot.Snapshot.Commits == nil || !slices.Contains(fs.SessionSnapshot.Snapshot.Truncated, "commits") {
		t.Fatal("lost calls became observed zero")
	}
	if s.collectSessionCommits(context.Background(), t0) {
		t.Fatal("truncation repeats revisions")
	}
}

func TestCommitCallConcurrentEvidence(t *testing.T) {
	hash := strings.Repeat("a", 40)
	call := GitCall{ID: "a", Start: t0, End: t0.Add(time.Second), Ops: []gitcmd.Operation{{Dir: "/a", DirKnown: true}}, OutputHashes: []string{hash}}
	other := call
	other.ID = "b"
	other.Ops = []gitcmd.Operation{{Dir: "/nested", DirKnown: true}}
	loc := map[string]commitLocation{"/a": {root: "/root"}, "/nested": {root: "/root"}}
	items := []gitctx.Commit{{Hash: hash}}
	if !commitCallConflicts(call, "/root", loc, []GitCall{call, other}, items) {
		t.Fatal("same worktree receipt conflict missed")
	}
	other.OutputHashes = nil
	if commitCallConflicts(call, "/root", loc, []GitCall{other}, items) {
		t.Fatal("missing evidence in other call defeats valid own receipt")
	}
	other.OutputHashes = []string{hash}
	loc["/nested"] = commitLocation{root: "/different"}
	if commitCallConflicts(call, "/root", loc, []GitCall{other}, items) {
		t.Fatal("independent worktree treated as conflict")
	}
}

func TestCommitSnapshotBoundsClonePrivacy(t *testing.T) {
	snap := newSessionSnapshot()
	content := strings.Repeat("한", 90)
	in := &SnapshotCommits{Trees: []CommitTree{{Repository: "github.com/test/repo"}}}
	for i := 0; i < 120; i++ {
		in.Items = append(in.Items, SnapshotCommit{Call: fmt.Sprint(i), Hash: fmt.Sprintf("%040x", i), At: int64(i), Subject: &content})
		in.Unresolved = append(in.Unresolved, UnresolvedCommit{Call: "u" + fmt.Sprint(i), At: int64(i)})
	}
	snap.mergeCommits(in)
	if len(snap.Commits.Items) != 100 || len(snap.Commits.Unresolved) != 100 || len(*snap.Commits.Items[0].Subject) > 200 {
		t.Fatal("snapshot bounds")
	}
	for _, policy := range []RedactPolicy{{UserPrompts: true}, {ToolDetails: true}} {
		redacted := redactSnapshot(snap, policy, TitleFlags{})
		if *redacted.Commits.Items[0].Subject != redactedMarker || *snap.Commits.Items[0].Subject == redactedMarker {
			t.Fatal("wire redaction mutates state or misses subject")
		}
	}
	copy := snap.clone()
	copy.Commits.Items[0].Hash = "changed"
	if snap.Commits.Items[0].Hash == "changed" {
		t.Fatal("clone aliases items")
	}
	marker := redactedMarker
	prior := newSessionSnapshot()
	prior.mergeCommits(&SnapshotCommits{Trees: in.Trees, Items: []SnapshotCommit{{Call: "x", Hash: "h", Subject: &marker}}})
	prior.mergeCommits(&SnapshotCommits{Trees: in.Trees, Items: []SnapshotCommit{{Call: "x", Hash: "h", Subject: &content}}})
	if *prior.Commits.Items[0].Subject != marker {
		t.Fatal("read-time marker restored")
	}
	snap.fit()
	wire, _ := json.Marshal(snap)
	if len(wire) > maxSnapshotBytes {
		t.Fatal("snapshot exceeds wire budget")
	}
}

func TestCommitCollectorRealReceiptAndDefaultReenrich(t *testing.T) {
	repo := gitStateRepo(t)
	start := time.Now().UTC()
	raw, err := exec.Command("git", "-C", repo, "commit", "--allow-empty", "-m", "private matched subject").CombinedOutput()
	if err != nil {
		t.Fatalf("fixture commit: %v", err)
	}
	receipt := string(raw)
	end := time.Now().UTC()
	parsed := gitcmd.ReadOutput(receipt)
	if len(parsed.Hashes) != 1 {
		t.Fatalf("fixture receipt: %q", receipt)
	}
	home := t.TempDir()
	s, state := newSnapshotSyncer(t, home, "http://unused.invalid")
	path, _ := writeTranscript(t, home, "s", commitCallLine("c", "git commit", repo, start))
	state.SetOffset(path, 100)
	state.Files[path].SessionSnapshot = &SessionSnapshotState{Snapshot: newSessionSnapshot()}
	state.Files[path].SessionCommits = &SessionCommitsState{Done: []GitCall{{ID: "c", Transcript: "s.jsonl", Start: start, End: end, ResultSeen: true, Ops: []gitcmd.Operation{{Kind: "commit", Dir: repo, DirKnown: true}}, OutputHashes: parsed.Hashes, WithholdSubject: true}}}
	if !s.collectSessionCommits(context.Background(), end) {
		t.Fatal("collector unchanged")
	}
	got := state.Files[path].SessionSnapshot.Snapshot.Commits
	if got == nil || len(got.Items) != 1 || got.Items[0].Subject != nil {
		t.Fatalf("default withheld: %+v", got)
	}
	rev := state.Files[path].SessionSnapshot.Revision
	if s.collectSessionCommits(context.Background(), end) || state.Files[path].SessionSnapshot.Revision != rev {
		t.Fatal("settled call changes revision")
	}
	wire, _ := json.Marshal(state.Files[path])
	if strings.Contains(string(wire), "private matched subject") {
		t.Fatal("withheld content leaked")
	}
}

func TestCommitBackfillCollectedPrefixAndReadRedaction(t *testing.T) {
	home := t.TempDir()
	s, state := newSnapshotSyncer(t, home, "http://unused.invalid")
	old := time.Now().Add(-30 * 24 * time.Hour)
	path, _ := writeTranscript(t, home, "s", commitCallLine("skipped", "git commit", "/repo", old), commitResultLine("skipped", "[main abcdef1] secret", old.Add(time.Second)), commitCallLine("kept", "git commit", "/repo", old.Add(2*time.Second)), commitResultLine("kept", "[main abcdef1] secret", old.Add(3*time.Second)), commitCallLine("unacked", "git commit", "/repo", old.Add(4*time.Second)))
	records, _ := scanCommitFixture(t, path)
	state.SetOffset(path, records[4].Offset)
	from := records[2].Offset
	state.Files[path].CollectedFrom = from
	s.client.redact = RedactPolicy{ToolDetails: true}
	if !s.backfillSessionCommits(path, records, false) {
		t.Fatal("old collected calls not backfilled")
	}
	cs := state.Files[path].SessionCommits
	if len(cs.Done) != 1 || len(cs.Open) != 0 || cs.Done[0].ID != "kept" || !cs.Done[0].WithholdSubject || !cs.Done[0].ReadRedacted {
		t.Fatalf("collected prefix: %+v", cs)
	}
	s.client.redact = RedactPolicy{}
	s.backfillSessionCommits(path, records, true)
	if !cs.Done[0].ReadRedacted {
		t.Fatal("policy toggle erased read-time privacy")
	}
	cs.Done = nil // settled calls leave the queue, but not their privacy latch
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(state.path)
	if err != nil {
		t.Fatal(err)
	}
	s.state = loaded
	cs = loaded.Files[path].SessionCommits
	s.backfillSessionCommits(path, records, true)
	if len(cs.Done) != 1 || !cs.Done[0].ReadRedacted {
		t.Fatal("settled read-time privacy restored by include-content")
	}
}

func TestCommitSettledReadPolicyPersistsThroughReenrichAndWire(t *testing.T) {
	for _, policy := range []RedactPolicy{{UserPrompts: true}, {ToolDetails: true}} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) {
			repo := gitStateRepo(t)
			start := time.Now().UTC()
			output, err := exec.Command("git", "-C", repo, "commit", "--allow-empty", "-m", "private settled subject").CombinedOutput()
			if err != nil {
				t.Fatalf("fixture commit: %v: %s", err, output)
			}
			end := time.Now().UTC()
			result, err := json.Marshal(map[string]any{"type": "user", "uuid": "result-c", "sessionId": "s", "timestamp": end, "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "c", "content": string(output)}}}})
			if err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			srv := newObservationServer(t)
			s, state := newSnapshotSyncer(t, home, srv.URL())
			path, _ := writeTranscript(t, home, "s", commitCallLine("c", "git commit --allow-empty -m private", repo, start), string(result))
			records, offset := scanCommitFixture(t, path)
			state.SetOffset(path, offset)
			state.Files[path].SessionSnapshot = &SessionSnapshotState{Snapshot: newSessionSnapshot()}
			s.client.redact = policy
			s.noteSessionCommits(path, 0, records)
			cs := state.Files[path].SessionCommits
			if cs == nil || len(cs.Done) != 1 || !cs.Done[0].ReadRedacted {
				t.Fatalf("read-time latch: %+v", cs)
			}
			if !s.collectSessionCommits(context.Background(), end) || len(cs.Done) != 0 {
				t.Fatalf("call did not settle: %+v", cs)
			}
			items := state.Files[path].SessionSnapshot.Snapshot.Commits.Items
			if len(items) != 1 || items[0].Subject == nil || *items[0].Subject != redactedMarker {
				t.Fatalf("read-time subject: %+v", items)
			}
			if err := state.Save(); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadState(state.path)
			if err != nil {
				t.Fatal(err)
			}
			s.state = loaded
			s.client.redact = RedactPolicy{}
			if !s.backfillSessionCommits(path, records, true) {
				t.Fatal("include-content did not backfill collected calls")
			}
			if len(loaded.Files[path].SessionCommits.Done) != 1 || !loaded.Files[path].SessionCommits.Done[0].ReadRedacted {
				t.Fatal("settled call lost read-time privacy latch")
			}
			s.collectSessionCommits(context.Background(), end.Add(time.Second))
			items = loaded.Files[path].SessionSnapshot.Snapshot.Commits.Items
			if len(items) != 1 || items[0].Subject == nil || *items[0].Subject != redactedMarker {
				t.Fatalf("reenrich restored subject: %+v", items)
			}
			if err := s.FlushObservations(context.Background()); err != nil {
				t.Fatal(err)
			}
			bodies := srv.bodies()
			if len(bodies) == 0 || !strings.Contains(bodies[len(bodies)-1], redactedMarker) || strings.Contains(bodies[len(bodies)-1], "private settled subject") {
				t.Fatalf("reenrich wire privacy: %q", bodies)
			}
		})
	}
}

func TestCommitReadPrivacyOverflowRemainsConservative(t *testing.T) {
	repo := gitStateRepo(t)
	start := time.Now().UTC()
	output, err := exec.Command("git", "-C", repo, "commit", "--allow-empty", "-m", "private overflow subject").CombinedOutput()
	if err != nil {
		t.Fatalf("fixture commit: %v: %s", err, output)
	}
	end := time.Now().UTC()
	result, err := json.Marshal(map[string]any{"type": "user", "uuid": "result-200", "sessionId": "s", "timestamp": end, "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "200", "content": string(output)}}}})
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	srv := newObservationServer(t)
	s, state := newSnapshotSyncer(t, home, srv.URL())
	s.client.redact = RedactPolicy{ToolDetails: true}
	lines := make([]string, 0, 2*(maxDoneCommitCalls+1))
	for i := 0; i < maxDoneCommitCalls; i++ {
		id := fmt.Sprint(i)
		lines = append(lines, commitCallLine(id, "git commit -m private", "/work", t0), commitResultLine(id, "nothing to commit, working tree clean", t0.Add(time.Second)))
	}
	lines = append(lines, commitCallLine("200", "git commit --allow-empty -m private", repo, start), string(result))
	path, _ := writeTranscript(t, home, "s", lines...)
	records, offset := scanCommitFixture(t, path)
	state.SetOffset(path, offset)
	state.Files[path].SessionSnapshot = &SessionSnapshotState{Snapshot: newSessionSnapshot()}
	s.noteSessionCommits(path, 0, records)
	cs := state.Files[path].SessionCommits
	if cs == nil || !cs.RedactedOverflow || len(cs.RedactedCalls) != maxDoneCommitCalls || len(cs.Done) != maxDoneCommitCalls {
		t.Fatalf("overflow not latched: %+v", cs)
	}
	last := cs.Done[len(cs.Done)-1]
	if last.ID != "200" || !last.ReadRedacted {
		t.Fatalf("overflow call not conservatively redacted: %+v", last)
	}
	// The earlier fixture calls establish overflow; only the real receipt needs git lookup.
	cs.Done = []GitCall{last}
	if !s.collectSessionCommits(context.Background(), end) || len(cs.Done) != 0 {
		t.Fatalf("overflow call not settled: %+v", cs)
	}
	items := state.Files[path].SessionSnapshot.Snapshot.Commits.Items
	if len(items) != 1 || items[0].Subject == nil || *items[0].Subject != redactedMarker {
		t.Fatalf("overflow subject at collection: %+v", items)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(state.path)
	if err != nil {
		t.Fatal(err)
	}
	s.state = loaded
	s.client.redact = RedactPolicy{}
	if !s.backfillSessionCommits(path, records, true) {
		t.Fatal("include-content did not restore settled calls")
	}
	cs = loaded.Files[path].SessionCommits
	if !cs.RedactedOverflow || len(cs.Done) != maxDoneCommitCalls {
		t.Fatalf("overflow lost after backfill: %+v", cs)
	}
	for _, call := range cs.Done {
		if !call.ReadRedacted || call.WithholdSubject {
			t.Fatalf("overflow restored plaintext permission: %+v", call)
		}
	}
	cs.Done = []GitCall{cs.Done[len(cs.Done)-1]}
	s.collectSessionCommits(context.Background(), end.Add(time.Second))
	items = loaded.Files[path].SessionSnapshot.Snapshot.Commits.Items
	if len(items) != 1 || items[0].Subject == nil || *items[0].Subject != redactedMarker {
		t.Fatalf("overflow reenrich restored subject: %+v", items)
	}
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	bodies := srv.bodies()
	if len(bodies) == 0 || !strings.Contains(bodies[len(bodies)-1], redactedMarker) || strings.Contains(bodies[len(bodies)-1], "private overflow subject") {
		t.Fatalf("overflow wire privacy: %q", bodies)
	}
}

func TestCommitSnapshotRetryPreservesRevisionAndRedaction(t *testing.T) {
	srv := newObservationServer(t)
	failed := false
	srv.setAnswer(func(w http.ResponseWriter, _ SessionObservationsRequest) bool {
		if failed {
			return false
		}
		failed = true
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return true
	})
	home := t.TempDir()
	state := dirtyState(t, home, 1, nil)
	for _, fs := range state.Files {
		if fs.SessionSnapshot != nil {
			subject := "private commit"
			fs.SessionSnapshot.Snapshot.mergeCommits(&SnapshotCommits{Trees: []CommitTree{{Repository: "github.com/test/repo"}}, Items: []SnapshotCommit{{Call: "c", Hash: strings.Repeat("a", 40), Subject: &subject, Proof: "output_hash"}}})
		}
	}
	client := NewClient(srv.URL(), "", "")
	client.redact = RedactPolicy{ToolDetails: true}
	s := New(home, "p@example.com", "u1", state, client, nil)
	if err := s.FlushObservations(context.Background()); err == nil {
		t.Fatal("503 not reported")
	}
	if allSent(state) {
		t.Fatal("failed commit snapshot acknowledged")
	}
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests()
	if len(reqs) != 2 || !allSent(state) {
		t.Fatal("commit snapshot not retried")
	}
	for _, req := range reqs {
		wire, _ := json.Marshal(req)
		if strings.Contains(string(wire), "private commit") || !strings.Contains(string(wire), redactedMarker) {
			t.Fatal("retry subject privacy")
		}
	}
}

func TestCommitCollectorAllowlistAndNoAnswer(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprint(denied), func(t *testing.T) {
			repo := namedRepo(t, "repo", "https://github.com/private/repo.git")
			home := t.TempDir()
			s, state := newSnapshotSyncer(t, home, "http://unused.invalid")
			path, _ := writeTranscript(t, home, "s", commitCallLine("c", "git commit", repo, t0))
			state.SetOffset(path, 100)
			state.Files[path].SessionSnapshot = &SessionSnapshotState{Snapshot: newSessionSnapshot()}
			state.Files[path].SessionCommits = &SessionCommitsState{Done: []GitCall{{ID: "c", Start: t0, End: t0.Add(time.Second), ResultSeen: true, Ops: []gitcmd.Operation{{Kind: "commit", Dir: repo, DirKnown: true}}, OutputHashes: []string{"abcdef1"}}}}
			if denied {
				s.collectPrefixes = []string{"github.com/allowed/"}
			}
			calls := installGitWrapper(t, "case \"$*\" in *' log -g '*) sleep 1;; esac\n")
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			started := time.Now()
			s.collectSessionCommits(ctx, t0.Add(2*time.Second))
			elapsed := time.Since(started)
			if elapsed > time.Second {
				t.Fatalf("budget overrun %s", elapsed)
			}
			cs := state.Files[path].SessionCommits
			if denied {
				if len(cs.Done) != 0 || callsWith(calls(), "log", "-g") != 0 {
					t.Fatal("denied repository inspected")
				}
			} else {
				if len(cs.Done) != 1 || cs.NoAnswers != 1 || state.Files[path].SessionSnapshot.Snapshot.Commits != nil {
					t.Fatal("no answer became a zero or lost pending call")
				}
			}
			waitGitJobs(t, s)
		})
	}
}

func TestCommitFallbackExploresButRequiresWorktreeWitness(t *testing.T) {
	repo := gitStateRepo(t)
	start := time.Now().UTC()
	runGit(t, repo, "commit", "--allow-empty", "-m", "fallback subject")
	end := time.Now().UTC()
	log, err := gitctx.ReadCommitLog(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	hash := log.Items[0].Hash
	other := filepath.Join(t.TempDir(), "other")
	runGit(t, repo, "worktree", "add", "--detach", other, "HEAD")
	// Two real worktrees claim the same object through creation-shaped reflogs.
	// Hash discovery must not manufacture the unknown execution directory.
	runGit(t, other, "update-ref", "--create-reflog", "-m", "commit: duplicate candidate", "HEAD", hash, hash)
	for _, dynamic := range []bool{false, true} {
		t.Run(fmt.Sprint(dynamic), func(t *testing.T) {
			home := t.TempDir()
			s, state := newSnapshotSyncer(t, home, "http://unused.invalid")
			gone := filepath.Join(repo, "removed-child")
			if err := os.MkdirAll(gone, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(gone); err != nil {
				t.Fatal(err)
			}
			path, _ := writeTranscript(t, home, "s", commitCallLine("c", "git commit", repo, start))
			state.SetOffset(path, 100)
			fs := state.Files[path]
			fs.SessionSnapshot = &SessionSnapshotState{Snapshot: newSessionSnapshot()}
			op := gitcmd.Operation{Kind: "commit", Dir: gone, DirKnown: true}
			if dynamic {
				op.Dir = ""
				op.DirKnown = false
			}
			fs.SessionCommits = &SessionCommitsState{Dirs: []string{other}, Done: []GitCall{{ID: "c", Transcript: "s.jsonl", Start: start, End: end, Ops: []gitcmd.Operation{op}, OutputHashes: []string{hash}, ResultSeen: true}}}
			fs.SessionGit = &SessionGitState{Edited: []string{filepath.Join(repo, "modified.txt")}}
			calls := installGitWrapper(t, "")
			if !s.collectSessionCommits(context.Background(), end) {
				t.Fatal("unchanged")
			}
			got := fs.SessionSnapshot.Snapshot.Commits
			if dynamic {
				if got == nil || len(got.Items) != 0 || len(got.Unresolved) != 1 {
					t.Fatalf("dynamic path attributed: %+v", got)
				}
				if callsWith(calls(), "log", "-g") < 2 {
					t.Fatal("alternative worktree exploration skipped")
				}
			} else {
				if got == nil || len(got.Items) != 1 || got.Items[0].Hash != hash {
					t.Fatalf("same-root alternative missed: %+v", got)
				}
			}
		})
	}
}

func TestCommitJobTimeoutDoesNotDuplicate(t *testing.T) {
	var jobs gitJobs
	started := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		_, ok := commitJob(ctx, &jobs, "same", func() int { close(started); <-release; return 1 })
		done <- ok
	}()
	<-started
	cancel()
	if <-done {
		t.Fatal("cancel returned answer")
	}
	if _, ok := commitJob(context.Background(), &jobs, "same", func() int { t.Error("duplicate job"); return 2 }); ok {
		t.Fatal("hung key restarted")
	}
	close(release)
}
