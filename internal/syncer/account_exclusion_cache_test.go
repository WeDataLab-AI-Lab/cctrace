package syncer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"cctrace/internal/store"
)

// queryServer answers /api/sync/exclusions with status, excluding the keys in
// excluded when status is 200, and counts the queries and accounts it saw.
type queryServer struct {
	mu       sync.Mutex
	status   int
	excluded map[string]bool
	queries  [][]AccountRef
}

func (q *queryServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		defer q.mu.Unlock()
		var body struct {
			Accounts []AccountRef `json:"accounts"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		q.queries = append(q.queries, body.Accounts)
		if q.status != http.StatusOK {
			w.WriteHeader(q.status)
			return
		}
		out := []AccountRef{}
		for _, a := range body.Accounts {
			if q.excluded[a.Key()] {
				out = append(out, a)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accounts": out})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (q *queryServer) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queries)
}

// fakeClock pins nowFn for the test and returns a way to move it.
func fakeClock(t *testing.T) func(time.Duration) {
	t.Helper()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	prev := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = prev })
	return func(d time.Duration) { now = now.Add(d) }
}

func newTestFilter(t *testing.T, url string) *AccountFilter {
	t.Helper()
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return NewAccountFilter(NewClient(url, "", ""), state)
}

func personalRecords() []*store.SessionRecord {
	return []*store.SessionRecord{{AccountID: "acct-personal"}}
}

// A 404 during a rolling deploy can come from one old instance. Latching it for
// the life of the daemon left server exclusions unapplied until a restart; it
// is asked again once the park expires.
func TestAccountFilterAsksAgainOnceNotFoundExpires(t *testing.T) {
	advance := fakeClock(t)
	q := &queryServer{status: http.StatusNotFound, excluded: map[string]bool{"anthropic:acct-personal": true}}
	f := newTestFilter(t, q.start(t).URL)

	f.BeginPass()
	if kept, err := f.Drop(context.Background(), "anthropic", personalRecords()); err != nil || len(kept) != 1 {
		t.Fatalf("404 pass: kept %d, err %v; want the record sent", len(kept), err)
	}
	f.BeginPass()
	if _, err := f.Drop(context.Background(), "anthropic", personalRecords()); err != nil {
		t.Fatal(err)
	}
	if n := q.count(); n != 1 {
		t.Fatalf("asked %d times within the park, want 1", n)
	}

	q.mu.Lock()
	q.status = http.StatusOK
	q.mu.Unlock()
	advance(exclusionsUnsupportedPark + time.Second)
	f.BeginPass()
	kept, err := f.Drop(context.Background(), "anthropic", personalRecords())
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 0 {
		t.Fatal("the server's exclusion was not applied once the park expired")
	}
}

// Watch mode polls every second, and every pass with new records would ask
// again, from the bucket /api/sync shares. An answer is reused for a few
// minutes instead; a lifted exclusion arriving late is covered by ingest,
// which still refuses the account.
func TestAccountFilterReusesAnAnswerAcrossPasses(t *testing.T) {
	advance := fakeClock(t)
	q := &queryServer{status: http.StatusOK, excluded: map[string]bool{"anthropic:acct-personal": true}}
	f := newTestFilter(t, q.start(t).URL)

	for i := 0; i < 3; i++ {
		f.BeginPass()
		kept, err := f.Drop(context.Background(), "anthropic", personalRecords())
		if err != nil || len(kept) != 0 {
			t.Fatalf("pass %d: kept %d, err %v", i, len(kept), err)
		}
		advance(time.Second)
	}
	if n := q.count(); n != 1 {
		t.Fatalf("asked %d times in three passes, want 1", n)
	}

	advance(exclusionAnswerTTL)
	f.BeginPass()
	if _, err := f.Drop(context.Background(), "anthropic", personalRecords()); err != nil {
		t.Fatal(err)
	}
	if n := q.count(); n != 2 {
		t.Fatalf("asked %d times after the answer expired, want 2", n)
	}
}

// A failed query holds every file of the pass that needs its answer, but asking
// again for each of those files only multiplies the failure. The next pass
// tries again.
func TestAccountFilterAsksOncePerPassAfterAFailure(t *testing.T) {
	fakeClock(t)
	q := &queryServer{status: http.StatusInternalServerError}
	f := newTestFilter(t, q.start(t).URL)

	f.BeginPass()
	for i := 0; i < 3; i++ {
		if _, err := f.Drop(context.Background(), "anthropic", personalRecords()); err == nil {
			t.Fatalf("file %d was released without an answer", i)
		}
	}
	if n := q.count(); n != 1 {
		t.Fatalf("asked %d times in one failing pass, want 1", n)
	}
	f.BeginPass()
	_, _ = f.Drop(context.Background(), "anthropic", personalRecords())
	if n := q.count(); n != 2 {
		t.Fatalf("asked %d times after a new pass began, want 2", n)
	}
}

// The server answers at most maxExclusionQueryAccounts per query and refuses a
// larger one outright, so a pass that saw more asks in chunks.
func TestAccountFilterSplitsLargeQueries(t *testing.T) {
	fakeClock(t)
	q := &queryServer{status: http.StatusOK, excluded: map[string]bool{"anthropic:acct-149": true}}
	f := newTestFilter(t, q.start(t).URL)

	var records []*store.SessionRecord
	for i := 0; i < 150; i++ {
		records = append(records, &store.SessionRecord{AccountID: "acct-" + strconv.Itoa(i)})
	}
	f.BeginPass()
	kept, err := f.Drop(context.Background(), "anthropic", records)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 149 {
		t.Errorf("kept %d, want 149: the excluded account in the last chunk was missed", len(kept))
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	asked := 0
	for _, batch := range q.queries {
		if len(batch) > maxExclusionQueryAccounts {
			t.Errorf("one query named %d accounts, over the server's %d", len(batch), maxExclusionQueryAccounts)
		}
		asked += len(batch)
	}
	if asked != 150 {
		t.Errorf("asked about %d accounts, want 150", asked)
	}
}
