package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// observationServer stands in for cctraced: it accepts sync and re-enrichment
// batches, answers the capability probe and the exclusion query, and records
// every session-observations body. answer, when set, decides the response to an
// observations request and reports whether it wrote one; otherwise it is a 200.
type observationServer struct {
	t   *testing.T
	srv *httptest.Server

	mu              sync.Mutex
	raw             []string
	reqs            []SessionObservationsRequest
	syncRecords     int
	syncStatus      int
	exclusionStatus int
	excluded        map[string]bool
	answer          func(w http.ResponseWriter, req SessionObservationsRequest) bool
}

func newObservationServer(t *testing.T) *observationServer {
	t.Helper()
	o := &observationServer{t: t}
	o.srv = httptest.NewServer(http.HandlerFunc(o.serve))
	t.Cleanup(o.srv.Close)
	return o
}

func (o *observationServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	o.mu.Lock()
	defer o.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/sync/capabilities":
		_ = json.NewEncoder(w).Encode(syncCapabilities{Reenrich: true})
	case "/api/sync/exclusions":
		if o.exclusionStatus != 0 && o.exclusionStatus != http.StatusOK {
			w.WriteHeader(o.exclusionStatus)
			return
		}
		var q struct {
			Accounts []AccountRef `json:"accounts"`
		}
		_ = json.Unmarshal(body, &q)
		out := []AccountRef{}
		for _, a := range q.Accounts {
			if o.excluded[a.Key()] {
				out = append(out, a)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accounts": out})
	case "/api/sync":
		if o.syncStatus != 0 && o.syncStatus != http.StatusOK {
			http.Error(w, "refused", o.syncStatus)
			return
		}
		var p SyncPayload
		_ = json.Unmarshal(body, &p)
		o.syncRecords += len(p.Records)
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: len(p.Records), Updated: len(p.Records)})
	case "/api/session-observations":
		var req SessionObservationsRequest
		if err := json.Unmarshal(body, &req); err != nil {
			o.t.Errorf("decode session observations: %v", err)
		}
		o.raw = append(o.raw, string(body))
		o.reqs = append(o.reqs, req)
		if o.answer != nil && o.answer(w, req) {
			return
		}
		n := len(req.Snapshots) + len(req.LiveStates) + len(req.Events)
		_ = json.NewEncoder(w).Encode(SessionObservationsResponse{Accepted: n, Stored: n})
	default:
		http.NotFound(w, r)
	}
}

func (o *observationServer) URL() string { return o.srv.URL }

func (o *observationServer) bodies() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.raw...)
}

func (o *observationServer) requests() []SessionObservationsRequest {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]SessionObservationsRequest(nil), o.reqs...)
}

func (o *observationServer) records() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.syncRecords
}

func (o *observationServer) setSyncStatus(status int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.syncStatus = status
}

func (o *observationServer) setExclusions(status int, excluded map[string]bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.exclusionStatus, o.excluded = status, excluded
}

func (o *observationServer) setAnswer(answer func(w http.ResponseWriter, req SessionObservationsRequest) bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.answer = answer
}

// dirtyState is a state holding n dirty snapshots, s-1 to s-n, each with title.
func dirtyState(t *testing.T, claudeDir string, n int, fill func(i int, snap *SessionSnapshot)) *State {
	t.Helper()
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		snap := newSessionSnapshot()
		snap.AITitle = snapshotTitle(fmt.Sprintf("title %d", i))
		if fill != nil {
			fill(i, &snap)
		}
		path := filepath.Join(claudeDir, "projects", "-p", fmt.Sprintf("s-%d.jsonl", i))
		state.Files[path] = &FileState{SessionSnapshot: &SessionSnapshotState{
			Snapshot: snap, Revision: 1, ProjectHash: "-p", ObservedAt: t0,
		}}
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	return state
}

func allSent(state *State) bool {
	for _, fs := range state.Files {
		if fs.SessionSnapshot != nil && fs.SessionSnapshot.dirty() {
			return false
		}
	}
	return true
}

func sessionIDsOf(req SessionObservationsRequest) []string {
	var ids []string
	for _, it := range req.Snapshots {
		ids = append(ids, it.SessionID)
	}
	return ids
}

// ---- sender ----

// The status a server answers decides what the caller does next, so each one is
// told apart: a 429 carries its wait, a 404 means an older server, a 400 says
// why, a 413 splits.
func TestSendSessionObservationsStatuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		header map[string]string
		body   string
		check  func(t *testing.T, resp SessionObservationsResponse, err error)
	}{
		{"ok", http.StatusOK, nil, `{"accepted":2,"stored":1,"skipped":1}`, func(t *testing.T, resp SessionObservationsResponse, err error) {
			if err != nil || resp != (SessionObservationsResponse{Accepted: 2, Stored: 1, Skipped: 1}) {
				t.Errorf("resp %+v err %v", resp, err)
			}
		}},
		{"rate limited", http.StatusTooManyRequests, map[string]string{"Retry-After": "7"}, ``, func(t *testing.T, _ SessionObservationsResponse, err error) {
			var re *RetryableError
			if !errors.As(err, &re) || re.RetryAfter != 7*time.Second {
				t.Errorf("err %v, want a RetryableError of 7s", err)
			}
		}},
		{"rate limited without a wait", http.StatusTooManyRequests, nil, ``, func(t *testing.T, _ SessionObservationsResponse, err error) {
			var re *RetryableError
			if !errors.As(err, &re) || re.RetryAfter != 5*time.Second {
				t.Errorf("err %v, want a RetryableError of the 5s default", err)
			}
		}},
		{"too large", http.StatusRequestEntityTooLarge, nil, ``, func(t *testing.T, _ SessionObservationsResponse, err error) {
			if !errors.Is(err, ErrBodyTooLarge) {
				t.Errorf("err %v, want ErrBodyTooLarge", err)
			}
		}},
		{"older server", http.StatusNotFound, nil, `404 page not found`, func(t *testing.T, _ SessionObservationsResponse, err error) {
			if !errors.Is(err, ErrSessionObservationsUnsupported) {
				t.Errorf("err %v, want ErrSessionObservationsUnsupported", err)
			}
		}},
		{"rejected", http.StatusBadRequest, nil, `{"error":"snapshots[3]: snapshot is 70000 bytes (max 65536)"}`, func(t *testing.T, _ SessionObservationsResponse, err error) {
			if !errors.Is(err, ErrSessionObservationsRejected) || !strings.Contains(err.Error(), "snapshots[3]: snapshot is 70000 bytes (max 65536)") {
				t.Errorf("err %v, want ErrSessionObservationsRejected with the server's reason", err)
			}
		}},
		{"server error", http.StatusInternalServerError, nil, `boom`, func(t *testing.T, _ SessionObservationsResponse, err error) {
			var re *RetryableError
			if err == nil || errors.As(err, &re) || errors.Is(err, ErrBodyTooLarge) ||
				errors.Is(err, ErrSessionObservationsUnsupported) || errors.Is(err, ErrSessionObservationsRejected) {
				t.Errorf("err %v, want a plain error", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotAuth = r.Method+" "+r.URL.Path, r.Header.Get("Authorization")
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			now := t0
			resp, err := NewClient(srv.URL, "tok", "v1").SendSessionObservations(context.Background(),
				SessionObservationsRequest{ProfileEmail: "p@example.com", HeartbeatAt: &now})
			if gotPath != "POST /api/session-observations" || gotAuth != "Bearer tok" {
				t.Errorf("request %q auth %q", gotPath, gotAuth)
			}
			tc.check(t, resp, err)
		})
	}
}

// ---- flush ----

// The collector is running whether or not a session has anything new, so a
// flush with nothing dirty still sends the heartbeat, alone, under the identity
// /api/sync uses.
func TestFlushSendsTheHeartbeatAloneWhenNothingIsDirty(t *testing.T) {
	srv := newObservationServer(t)
	s, _ := newSnapshotSyncer(t, t.TempDir(), srv.URL())
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	bodies := srv.bodies()
	if len(bodies) != 1 {
		t.Fatalf("%d requests, want 1", len(bodies))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(bodies[0]), &raw); err != nil {
		t.Fatal(err)
	}
	var at time.Time
	if err := json.Unmarshal(raw["heartbeat_at"], &at); err != nil || at.IsZero() {
		t.Errorf("heartbeat_at = %s, want a time", raw["heartbeat_at"])
	}
	if _, ok := raw["snapshots"]; ok {
		t.Errorf("a heartbeat-only request carried snapshots: %s", bodies[0])
	}
	if string(raw["profile_email"]) != `"p@example.com"` || string(raw["user_id"]) != `"u1"` || string(raw["login_email"]) != `""` {
		t.Errorf("identity = %s / %s / %s", raw["profile_email"], raw["user_id"], raw["login_email"])
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A 200 marks sent the revision the request carried, not the one the state holds
// when the answer arrives: a change made while the request was in flight has not
// reached the server and stays dirty.
func TestFlushMarksSentOnlyTheRevisionItCarried(t *testing.T) {
	claudeDir := t.TempDir()
	state := dirtyState(t, claudeDir, 1, nil)
	var st *SessionSnapshotState
	for _, fs := range state.Files {
		st = fs.SessionSnapshot
	}
	client := NewClient("http://observations.invalid", "", "")
	var carried []SessionObservationsRequest
	client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var req SessionObservationsRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		carried = append(carried, req)
		if len(carried) == 1 {
			// A sync pass lands while this request is on the wire.
			st.Snapshot.AITitle = snapshotTitle("changed in flight")
			st.Revision++
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Request: r,
			Body: io.NopCloser(strings.NewReader(`{"accepted":1,"stored":1}`))}, nil
	})}
	s := New(claudeDir, "p@example.com", "u1", state, client, nil)

	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.SentRevision != 1 || st.Revision != 2 || !st.dirty() {
		t.Fatalf("after the first flush: revision %d sent %d, want 2/1 and still dirty", st.Revision, st.SentRevision)
	}
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(carried) != 2 || len(carried[1].Snapshots) != 1 || deref(carried[1].Snapshots[0].Snapshot.AITitle) != "changed in flight" {
		t.Fatalf("second flush carried %+v, want the change made in flight", carried)
	}
	if st.dirty() {
		t.Errorf("revision %d sent %d after the second flush", st.Revision, st.SentRevision)
	}
	reloaded, err := LoadState(state.path)
	if err != nil {
		t.Fatal(err)
	}
	if !allSent(reloaded) {
		t.Error("the sent revision was not saved")
	}
}

// An older server answers 404. Observations wait an hour, the snapshots stay
// dirty, and record sync goes on as if nothing happened.
func TestFlushParksAnHourOnNotFoundWhileSyncGoesOn(t *testing.T) {
	advance := fakeClock(t)
	srv := newObservationServer(t)
	srv.setAnswer(func(w http.ResponseWriter, _ SessionObservationsRequest) bool {
		w.WriteHeader(http.StatusNotFound)
		return true
	})
	claudeDir := t.TempDir()
	const sid = "sess-404"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", t0), aiTitleLine(sid, "Older server"))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)

	if err := s.FlushObservations(context.Background()); !errors.Is(err, ErrSessionObservationsUnsupported) {
		t.Fatalf("flush err = %v, want ErrSessionObservationsUnsupported", err)
	}
	before := srv.records()
	appendLines(t, path, convLine(sid, "u-2", t0.Add(time.Minute)))
	mustSync(t, s)
	if srv.records() != before+1 {
		t.Errorf("records sent while observations were parked: %d, want %d", srv.records(), before+1)
	}
	for _, step := range []time.Duration{0, observationsUnsupportedPark - time.Second} {
		advance(step)
		if err := s.FlushObservations(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := len(srv.requests()); n != 1 {
			t.Fatalf("%d observation requests within the park, want 1", n)
		}
	}
	if !snapshotState(t, state, path).dirty() {
		t.Error("a 404 marked the snapshot sent")
	}
	advance(2 * time.Second)
	_ = s.FlushObservations(context.Background())
	if n := len(srv.requests()); n != 2 {
		t.Errorf("%d observation requests once the park expired, want 2", n)
	}
}

// Any other failure is not a decision about the snapshots: they stay dirty and
// the flush does not park, so the next interval sends them.
func TestFlushKeepsSnapshotsDirtyOnOtherFailures(t *testing.T) {
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
	claudeDir := t.TempDir()
	state := dirtyState(t, claudeDir, 2, nil)
	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL(), "", ""), nil)

	if err := s.FlushObservations(context.Background()); err == nil {
		t.Fatal("a 503 was not reported")
	}
	if allSent(state) {
		t.Fatal("a 503 marked snapshots sent")
	}
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reqs := srv.requests(); len(reqs) != 2 || len(reqs[1].Snapshots) != 2 || !allSent(state) {
		t.Errorf("%d requests, all sent %v: want the retry to carry both snapshots", len(reqs), allSent(state))
	}
}

// A 429 says how long to stay away, and the flush stays away that long.
func TestFlushHonoursRetryAfter(t *testing.T) {
	advance := fakeClock(t)
	srv := newObservationServer(t)
	refused := false
	srv.setAnswer(func(w http.ResponseWriter, _ SessionObservationsRequest) bool {
		if refused {
			return false
		}
		refused = true
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	})
	s, _ := newSnapshotSyncer(t, t.TempDir(), srv.URL())

	var re *RetryableError
	if err := s.FlushObservations(context.Background()); !errors.As(err, &re) || re.RetryAfter != 120*time.Second {
		t.Fatalf("flush err = %v, want a 429 asking for 120s", err)
	}
	advance(119 * time.Second)
	_ = s.FlushObservations(context.Background())
	if n := len(srv.requests()); n != 1 {
		t.Fatalf("%d requests before Retry-After ran out, want 1", n)
	}
	advance(2 * time.Second)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(srv.requests()); n != 2 {
		t.Errorf("%d requests after Retry-After, want 2", n)
	}
}

// One snapshot the server refuses must not hold the others back. A heartbeat
// alone first shows the envelope is fine; then the chunk is halved until the bad
// snapshot is alone -- about two requests per halving. The others are stored,
// and it is logged once with the server's reason and marked sent, so it is not
// retried forever. The flush reports how many it refused.
func TestFlushIsolatesTheSnapshotTheServerRefuses(t *testing.T) {
	logs := captureSyncLog(t)
	srv := newObservationServer(t)
	const n, bad = 200, "s-137"
	const reason = "snapshots[136]: snapshot is 70000 bytes (max 65536)"
	srv.setAnswer(func(w http.ResponseWriter, req SessionObservationsRequest) bool {
		if slices.Contains(sessionIDsOf(req), bad) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": reason})
			return true
		}
		return false
	})
	claudeDir := t.TempDir()
	state := dirtyState(t, claudeDir, n, nil)
	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL(), "", ""), nil)

	err := s.FlushObservations(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refused 1 session observation") {
		t.Fatalf("flush err = %v, want the count of refused snapshots", err)
	}
	stored := map[string]int{}
	for _, req := range srv.requests() {
		if ids := sessionIDsOf(req); !slices.Contains(ids, bad) {
			for _, id := range ids {
				stored[id]++
			}
		}
	}
	if len(stored) != n-1 || stored[bad] != 0 {
		t.Errorf("stored %d distinct snapshots, want the %d the server accepts", len(stored), n-1)
	}
	for id, times := range stored {
		if times != 1 {
			t.Errorf("%s stored %d times", id, times)
		}
	}
	t.Logf("%d requests isolated one refused snapshot among %d", len(srv.requests()), n)
	if most := 2*int(math.Ceil(math.Log2(n))) + 2; len(srv.requests()) > most {
		t.Errorf("%d requests to isolate one snapshot among %d, want at most %d", len(srv.requests()), n, most)
	}
	if !allSent(state) {
		t.Error("a snapshot was left dirty")
	}
	if c := strings.Count(logs.String(), "session "+bad+" "); c != 1 || !strings.Contains(logs.String(), reason) {
		t.Errorf("log names %s %d times, want once with the reason:\n%s", bad, c, logs.String())
	}

	before := len(srv.requests())
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reqs := srv.requests()[before:]; len(reqs) != 1 || len(reqs[0].Snapshots) != 0 {
		t.Errorf("the next flush sent %d requests, want the heartbeat alone", len(reqs))
	}
}

// A server enforcing a rule this client does not know -- a newer server's
// stricter item check -- refuses every snapshot, one by one once halved. Past
// maxLoneRefusals the flush stops rather than spend two requests per snapshot
// marking them all sent: the rest stay dirty, and the flush waits an hour.
func TestFlushStopsAfterTooManyRefusedSnapshots(t *testing.T) {
	advance := fakeClock(t)
	srv := newObservationServer(t)
	srv.setAnswer(func(w http.ResponseWriter, req SessionObservationsRequest) bool {
		if len(req.Snapshots) == 0 {
			return false // the envelope is fine
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "snapshots[0]: snapshot holds a field this server does not take"})
		return true
	})
	claudeDir := t.TempDir()
	state := dirtyState(t, claudeDir, 450, nil)
	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL(), "", ""), nil)

	err := s.FlushObservations(context.Background())
	if !errors.Is(err, errRefusingItems) {
		t.Fatalf("flush err = %v, want it stopped at the refusal past %d", err, maxLoneRefusals)
	}
	reqs := len(srv.requests())
	t.Logf("%d requests before stopping, for 450 snapshots", reqs)
	if most := int(math.Ceil(math.Log2(maxObservationChunkItems))) + 2*(maxLoneRefusals+1) + 2; reqs > most {
		t.Errorf("%d requests, want at most %d", reqs, most)
	}
	sent := 0
	for _, fs := range state.Files {
		if !fs.SessionSnapshot.dirty() {
			sent++
		}
	}
	if sent != maxLoneRefusals {
		t.Errorf("%d snapshots marked sent, want only the %d refused alone", sent, maxLoneRefusals)
	}
	advance(observationsRefusingPark - time.Second)
	if err := s.FlushObservations(context.Background()); err != nil || len(srv.requests()) != reqs {
		t.Errorf("within the hour: err %v, %d requests; want it parked", err, len(srv.requests())-reqs)
	}
}

// A server that refuses the envelope refuses every request, whatever it carries.
// Halving down to single snapshots would send twice as many requests as there
// are snapshots and mark every one sent; the heartbeat alone tells the two apart
// in one more request. Nothing is marked sent, and the flush waits an hour.
func TestFlushStopsWhenTheServerRefusesTheEnvelope(t *testing.T) {
	advance := fakeClock(t)
	srv := newObservationServer(t)
	srv.setAnswer(func(w http.ResponseWriter, _ SessionObservationsRequest) bool {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "user_id is 300 bytes (max 256)"})
		return true
	})
	claudeDir := t.TempDir()
	state := dirtyState(t, claudeDir, 450, nil)
	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL(), "", ""), nil)

	err := s.FlushObservations(context.Background())
	if !errors.Is(err, errEnvelopeRejected) || !strings.Contains(err.Error(), "user_id is 300 bytes") {
		t.Fatalf("flush err = %v, want the refused envelope with the server's reason", err)
	}
	reqs := srv.requests()
	if len(reqs) != 2 || len(reqs[0].Snapshots) != maxObservationChunkItems || len(reqs[1].Snapshots) != 0 {
		t.Fatalf("%d requests, want the first chunk and a heartbeat alone", len(reqs))
	}
	for _, fs := range state.Files {
		if fs.SessionSnapshot.SentRevision != 0 {
			t.Fatal("a snapshot was marked sent although the server refused the envelope")
		}
	}
	advance(observationsEnvelopePark - time.Second)
	if err := s.FlushObservations(context.Background()); err != nil || len(srv.requests()) != 2 {
		t.Errorf("within the hour: err %v, %d requests; want it parked", err, len(srv.requests()))
	}
}

// An identity over the server's cap is refused whole, so the client does not
// send it at all -- not even the heartbeat -- and says so once.
func TestFlushDoesNotSendAnEnvelopeOverTheServersCap(t *testing.T) {
	logs := captureSyncLog(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	state := dirtyState(t, claudeDir, 3, nil)
	long := strings.Repeat("p", maxObservationEnvelopeBytes) + "@example.com"
	s := New(claudeDir, long, "u1", state, NewClient(srv.URL(), "", ""), nil)

	for i := 0; i < 2; i++ {
		if err := s.FlushObservations(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(srv.requests()); n != 0 {
		t.Errorf("%d requests sent with a profile_email over the cap, want none", n)
	}
	if allSent(state) {
		t.Error("snapshots were marked sent")
	}
	if n := strings.Count(logs.String(), "profile_email is"); n != 1 {
		t.Errorf("said so %d times, want once:\n%s", n, logs.String())
	}
}

// A request carries at most 200 snapshots, the server's cap, and about 4 MiB of
// them, half its body cap.
func TestFlushChunksByCountAndBytes(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		srv := newObservationServer(t)
		claudeDir := t.TempDir()
		state := dirtyState(t, claudeDir, 450, nil)
		s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL(), "", ""), nil)
		if err := s.FlushObservations(context.Background()); err != nil {
			t.Fatal(err)
		}
		var counts []int
		for _, req := range srv.requests() {
			counts = append(counts, len(req.Snapshots))
		}
		if fmt.Sprint(counts) != "[200 200 50]" || !allSent(state) {
			t.Errorf("chunks %v (all sent %v), want [200 200 50]", counts, allSent(state))
		}
	})
	t.Run("bytes", func(t *testing.T) {
		srv := newObservationServer(t)
		claudeDir := t.TempDir()
		state := dirtyState(t, claudeDir, 120, func(i int, snap *SessionSnapshot) {
			for n := 1; n <= maxSnapshotPRLinks; n++ {
				snap.addPRLink(SnapshotPRLink{URL: fmt.Sprintf("https://github.com/o/r/pull/%d/%d?%s", i, n, strings.Repeat("q", 400)), Number: int64(n), Repository: "o/r", FirstSeenAt: t0})
			}
		})
		s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL(), "", ""), nil)
		if err := s.FlushObservations(context.Background()); err != nil {
			t.Fatal(err)
		}
		total := 0
		for i, body := range srv.bodies() {
			n := len(srv.requests()[i].Snapshots)
			total += n
			if len(body) > maxObservationChunkBytes+1<<10 || n > maxObservationChunkItems {
				t.Errorf("request %d: %d bytes, %d snapshots", i, len(body), n)
			}
		}
		if len(srv.bodies()) < 2 || total != 120 || !allSent(state) {
			t.Errorf("%d requests carrying %d snapshots, want the 120 split by size", len(srv.bodies()), total)
		}
	})
}

// An excluded account's snapshot never leaves the machine and is consumed as
// its records are: the sent revision catches up, so it is not retried.
func TestFlushConsumesAnExcludedAccountsSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		local  []string
		server map[string]bool
	}{
		{"excluded locally", []string{"anthropic:acct-personal"}, map[string]bool{}},
		{"excluded by the server", nil, map[string]bool{"anthropic:acct-personal": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newObservationServer(t)
			srv.setExclusions(http.StatusOK, tc.server)
			claudeDir := t.TempDir()
			writeClaudeConfig(t, claudeDir, "acct-personal")
			const sid = "sess-personal"
			path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", time.Now().Add(time.Hour)), aiTitleLine(sid, "PERSONAL-TITLE"))
			state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := state.Save(); err != nil {
				t.Fatal(err)
			}
			client := NewClient(srv.URL(), "", "")
			client.SetExcludedAccounts(tc.local)
			s := New(claudeDir, "p@example.com", "u1", state, client, nil)
			mustSync(t, s)
			if st := snapshotState(t, state, path); !st.dirty() {
				t.Fatal("fixture: the snapshot is not dirty")
			}

			if err := s.FlushObservations(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, body := range srv.bodies() {
				if strings.Contains(body, "PERSONAL-TITLE") || strings.Contains(body, sid) {
					t.Errorf("an excluded account's snapshot was sent: %s", body)
				}
			}
			if len(srv.bodies()) != 1 {
				t.Errorf("%d requests, want the heartbeat", len(srv.bodies()))
			}
			if st := snapshotState(t, state, path); st.dirty() {
				t.Errorf("revision %d sent %d: an excluded snapshot must be consumed", st.Revision, st.SentRevision)
			}
		})
	}
}

// The metadata lines carry no timestamp. A pass that brings only them, to a file
// that has shown no timestamped record since its snapshot began, attributes the
// snapshot at the time it changed -- when those lines were read -- rather than
// at no time, which would read as no account and let an excluded one through.
func TestFlushAttributesASnapshotWithoutARecordTimestamp(t *testing.T) {
	srv := newObservationServer(t)
	srv.setExclusions(http.StatusOK, map[string]bool{})
	claudeDir := t.TempDir()
	writeClaudeConfig(t, claudeDir, "acct-personal")
	const sid = "sess-untimed"
	path, _ := writeTranscript(t, claudeDir, sid, aiTitleLine(sid, "UNTIMED-TITLE"))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	s.client.SetExcludedAccounts([]string{"anthropic:acct-personal"})
	mustSync(t, s)
	if st := snapshotState(t, state, path); !st.NewestRecordAt.IsZero() || !st.dirty() {
		t.Fatalf("fixture: newest record at %v, dirty %v", st.NewestRecordAt, st.dirty())
	}

	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, body := range srv.bodies() {
		if strings.Contains(body, "UNTIMED-TITLE") {
			t.Errorf("an excluded account's snapshot went out unattributed: %s", body)
		}
	}
	if snapshotState(t, state, path).dirty() {
		t.Error("the excluded snapshot was not consumed")
	}
}

// Sending on a guess would put an excluded account's titles on the wire, so a
// failed exclusion query holds the snapshot: dirty, not sent, until an answer
// comes. The heartbeat still goes.
func TestFlushHoldsSnapshotsWhileTheExclusionQueryFails(t *testing.T) {
	advance := fakeClock(t)
	srv := newObservationServer(t)
	srv.setExclusions(http.StatusOK, map[string]bool{})
	claudeDir := t.TempDir()
	writeClaudeConfig(t, claudeDir, "acct-work")
	const sid = "sess-held"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", time.Now().Add(time.Hour)), aiTitleLine(sid, "WORK-TITLE"))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)

	srv.setExclusions(http.StatusInternalServerError, nil)
	advance(exclusionAnswerTTL + time.Second)
	if err := s.FlushObservations(context.Background()); err == nil {
		t.Error("a held snapshot was not reported")
	}
	if bodies := srv.bodies(); len(bodies) != 1 || strings.Contains(bodies[0], "WORK-TITLE") {
		t.Fatalf("while the query failed: %v, want the heartbeat alone", bodies)
	}
	if !snapshotState(t, state, path).dirty() {
		t.Fatal("a held snapshot was marked sent")
	}

	srv.setExclusions(http.StatusOK, map[string]bool{})
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests()
	last := reqs[len(reqs)-1]
	if len(last.Snapshots) != 1 || last.Snapshots[0].AccountID != "acct-work" || deref(last.Snapshots[0].Snapshot.AITitle) != "WORK-TITLE" {
		t.Errorf("once answered: sent %+v, want the snapshot for acct-work", last.Snapshots)
	}
	if snapshotState(t, state, path).dirty() {
		t.Error("the snapshot stayed dirty after it was sent")
	}
}

// The titles, the agent name and a live session's name are what the prompts
// policy hides, on the bytes that leave the machine; the tools policy leaves
// them, and no policy touches the PR links or the cost, which are outcome
// metadata, or a live session's status. An empty title stays empty.
func TestSessionSnapshotRedactionMatrixOnTheWire(t *testing.T) {
	titles := []string{"S-AITITLE", "S-CUSTOMTITLE", "S-AGENTNAME", "S-LIVENAME"}
	for _, p := range []struct {
		name   string
		policy RedactPolicy
	}{
		{"off", RedactPolicy{}},
		{"prompts", RedactPolicy{UserPrompts: true}},
		{"tools", RedactPolicy{ToolDetails: true}},
		{"both", RedactPolicy{UserPrompts: true, ToolDetails: true}},
	} {
		t.Run(p.name, func(t *testing.T) {
			srv := newObservationServer(t)
			claudeDir := t.TempDir()
			writeTranscript(t, claudeDir, "sess-r1",
				convLine("sess-r1", "u-1", t0),
				aiTitleLine("sess-r1", titles[0]),
				customTitleLine("sess-r1", titles[1]),
				agentNameLine("sess-r1", titles[2]),
				prLinkLine("sess-r1", 3, "https://github.com/o/r/pull/S-PR", "o/r", t0),
				costStateLine("sess-r1", 1759300000000, 3.75, 2, 1, false, "claude-opus-5-5[1m]"),
			)
			writeTranscript(t, claudeDir, "sess-r2",
				convLine("sess-r2", "u-2", t0),
				customTitleLine("sess-r2", ""),
				prLinkLine("sess-r2", 4, "https://github.com/o/r/pull/S-PR2", "o/r", t0),
			)
			live := sessionFileFields(9001, "sess-r1")
			live["name"], live["status"], live["waitingFor"] = titles[3], "waiting", "dialog open"
			writeSessionFile(t, claudeDir, live)
			state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := state.Save(); err != nil {
				t.Fatal(err)
			}
			client := NewClient(srv.URL(), "", "")
			client.SetRedactPolicy(p.policy)
			s := New(claudeDir, "p@example.com", "u1", state, client, nil)
			mustSync(t, s)
			if err := s.FlushObservations(context.Background()); err != nil {
				t.Fatal(err)
			}

			bodies := srv.bodies()
			if len(bodies) != 1 {
				t.Fatalf("%d requests, want 1", len(bodies))
			}
			body := bodies[0]
			for _, title := range titles {
				if strings.Contains(body, title) == p.policy.UserPrompts {
					t.Errorf("%s present=%v under %s", title, !p.policy.UserPrompts, p.name)
				}
			}
			wantMarkers := 0
			if p.policy.UserPrompts {
				wantMarkers = len(titles)
			}
			if got := strings.Count(body, redactedMarker); got != wantMarkers {
				t.Errorf("%d markers, want %d: %s", got, wantMarkers, body)
			}
			for _, kept := range []string{"pull/S-PR", "pull/S-PR2", `"total_cost_usd":3.75`, `"custom_title":""`,
				`"status":"waiting"`, `"waiting_kind":"dialog"`, `"name_source":"user"`} {
				if !strings.Contains(body, kept) {
					t.Errorf("%s missing under %s: %s", kept, p.name, body)
				}
			}
			// The state keeps what the file said; only the request is redacted.
			for _, fs := range state.Files {
				if st := fs.SessionSnapshot; st != nil && deref(st.Snapshot.AITitle) == redactedMarker {
					t.Error("redaction reached the stored snapshot")
				}
			}
			for _, seen := range s.obs.live.seen {
				if seen.item.Name != titles[3] {
					t.Errorf("the remembered live state holds name %q", seen.item.Name)
				}
			}
		})
	}
}

// Several Claude homes can share one state file, each synced by its own Syncer.
// A snapshot belongs to its home: one home's flush must not send another home's
// snapshot under its own account -- that is how an excluded account's titles went
// out -- and leaves it for that home's Syncer, which applies that home's account.
func TestFlushSendsOnlyItsOwnHomesSnapshots(t *testing.T) {
	srv := newObservationServer(t)
	srv.setExclusions(http.StatusOK, map[string]bool{})
	homeA, homeB := t.TempDir(), t.TempDir()
	writeClaudeConfig(t, homeA, "acct-work")
	writeClaudeConfig(t, homeB, "acct-personal")
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	newClient := func() *Client {
		c := NewClient(srv.URL(), "", "")
		c.SetExcludedAccounts([]string{"anthropic:acct-personal"})
		return c
	}
	const sidB = "sess-personal-b"
	pathB, _ := writeTranscript(t, homeB, sidB, convLine(sidB, "u-1", time.Now().Add(time.Hour)), aiTitleLine(sidB, "PERSONAL-TITLE"))
	const sidA = "sess-work-a"
	writeTranscript(t, homeA, sidA, convLine(sidA, "u-2", time.Now().Add(time.Hour)), aiTitleLine(sidA, "WORK-TITLE"))
	sB := New(homeB, "p@example.com", "u1", state, newClient(), nil)
	mustSync(t, sB)
	sA := New(homeA, "p@example.com", "u1", state, newClient(), nil)
	mustSync(t, sA)

	if err := sA.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests()
	if len(reqs) != 1 || len(reqs[0].Snapshots) != 1 || reqs[0].Snapshots[0].SessionID != sidA || reqs[0].Snapshots[0].AccountID != "acct-work" {
		t.Fatalf("home A's flush sent %s, want its own snapshot alone, as acct-work", srv.bodies())
	}
	if !snapshotState(t, state, pathB).dirty() {
		t.Fatal("home A's flush consumed home B's snapshot")
	}

	if err := sB.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, body := range srv.bodies() {
		if strings.Contains(body, "PERSONAL-TITLE") {
			t.Errorf("the excluded account's title was sent: %s", body)
		}
	}
	if snapshotState(t, state, pathB).dirty() {
		t.Error("home B's own flush did not consume its excluded snapshot")
	}
}

// A title read while redact_user_prompts was on stays hidden after the option is
// turned off: the snapshot goes again when anything else in it changes, and the
// title must not go with it in plain text. A title read after that goes as the
// option then says.
func TestSnapshotTitleReadUnderRedactionStaysRedacted(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-relax"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", t0), customTitleLine(sid, "SECRET-FROM-STRICT-PERIOD"))
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	strict := NewClient(srv.URL(), "", "")
	strict.SetRedactPolicy(RedactPolicy{UserPrompts: true})
	s1 := New(claudeDir, "p@example.com", "u1", state, strict, nil)
	mustSync(t, s1)
	if err := s1.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The option is turned off; the daemon restarts with the state on disk.
	reloaded, err := LoadState(state.path)
	if err != nil {
		t.Fatal(err)
	}
	s2 := New(claudeDir, "p@example.com", "u1", reloaded, NewClient(srv.URL(), "", ""), nil)
	appendLines(t, path, customTitleLine(sid, "SECRET-FROM-STRICT-PERIOD"), costStateLine(sid, 1759300000000, 0.5, 1, 0, false, "m"))
	mustSync(t, s2)
	if err := s2.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests()
	if len(reqs) != 2 || len(reqs[1].Snapshots) != 1 {
		t.Fatalf("%d requests, want the strict flush and the relaxed one carrying the changed snapshot", len(reqs))
	}
	if got := deref(reqs[1].Snapshots[0].Snapshot.CustomTitle); got != redactedMarker {
		t.Errorf("after relaxing, a title read under redaction went as %q", got)
	}

	appendLines(t, path, customTitleLine(sid, "TITLE-AFTER-RELAXING"))
	mustSync(t, s2)
	if err := s2.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs = srv.requests()
	if got := deref(reqs[len(reqs)-1].Snapshots[0].Snapshot.CustomTitle); got != "TITLE-AFTER-RELAXING" {
		t.Errorf("a title read after relaxing went as %q, want it in plain text", got)
	}
}

// The other direction: a title read while redact_user_prompts was off is hidden
// once the option is turned on, by the policy in force when it goes out.
func TestSnapshotTitleReadBeforeRedactionIsRedactedAfter(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-tighten"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", t0), customTitleLine(sid, "READ-WHILE-OFF"))
	s1, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s1)
	if err := s1.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}

	strict := NewClient(srv.URL(), "", "")
	strict.SetRedactPolicy(RedactPolicy{UserPrompts: true})
	s2 := New(claudeDir, "p@example.com", "u1", state, strict, nil)
	appendLines(t, path, costStateLine(sid, 1759300000000, 0.5, 1, 0, false, "m"))
	mustSync(t, s2)
	if err := s2.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests()
	if len(reqs) != 2 || len(reqs[1].Snapshots) != 1 {
		t.Fatalf("%d requests, want the first flush and the one after the option was turned on", len(reqs))
	}
	if got := deref(reqs[1].Snapshots[0].Snapshot.CustomTitle); got != redactedMarker {
		t.Errorf("with the option on, the title went as %q", got)
	}
}

// The server counts what it would not store -- a blocked project, a deleted
// session, an excluded account, or an item dated past its clock -- and a clock
// running ahead looks exactly like that from here. Said once, not every minute.
func TestFlushLogsSkippedItemsOnce(t *testing.T) {
	logs := captureSyncLog(t)
	srv := newObservationServer(t)
	srv.setAnswer(func(w http.ResponseWriter, req SessionObservationsRequest) bool {
		_ = json.NewEncoder(w).Encode(SessionObservationsResponse{Accepted: len(req.Snapshots), Skipped: len(req.Snapshots)})
		return true
	})
	claudeDir := t.TempDir()
	state := dirtyState(t, claudeDir, 2, nil)
	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL(), "", ""), nil)
	for i := 0; i < 2; i++ {
		for _, fs := range state.Files {
			fs.SessionSnapshot.Revision++
		}
		if err := s.FlushObservations(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(logs.String(), "the server skipped 2 of 2"); n != 1 {
		t.Errorf("skipped items logged %d times, want once:\n%s", n, logs.String())
	}
}

// A server that accepted the exclusion query and never answers must not hold the
// watch loop for the client's five-minute ceiling: the query has its own
// deadline, and running out of it is a failed query.
func TestQueryExcludedAccountsHasADeadline(t *testing.T) {
	previous := exclusionQueryTimeout
	exclusionQueryTimeout = 50 * time.Millisecond
	t.Cleanup(func() { exclusionQueryTimeout = previous })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(6 * time.Second): // past the assertion below, short of the client's ceiling
		}
	}))
	defer srv.Close()
	defer close(release) // before Close, which waits for the handler

	start := time.Now()
	_, err := NewClient(srv.URL, "", "").QueryExcludedAccounts(context.Background(), []AccountRef{{BillingProvider: "anthropic", AccountID: "a"}})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Errorf("err %v after %s, want a failure at the deadline", err, time.Since(start).Round(time.Millisecond))
	}
}
