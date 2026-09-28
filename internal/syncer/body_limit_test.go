package syncer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cctrace/internal/store"
)

// limitedSyncServer refuses any /api/sync body over limit, the way the server's
// withRequestBodyLimit does, and records the body sizes it accepted.
func limitedSyncServer(t *testing.T, limit int) (*httptest.Server, *[]int) {
	t.Helper()
	var mu sync.Mutex
	accepted := []int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/sync") {
			w.WriteHeader(http.StatusOK)
			return
		}
		body := make([]byte, 0, limit+1)
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Body.Read(buf)
			body = append(body, buf[:n]...)
			if len(body) > limit {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				_, _ = w.Write([]byte(`{"error":"request body too large"}`))
				return
			}
			if err != nil {
				break
			}
		}
		mu.Lock()
		accepted = append(accepted, len(body))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1, "skipped": 0})
	}))
	t.Cleanup(srv.Close)
	return srv, &accepted
}

func recordOfSize(sessionID string, raw int) *store.SessionRecord {
	return &store.SessionRecord{
		Ts:         time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		SessionID:  sessionID,
		RecordType: "assistant",
		Raw:        json.RawMessage(`{"pad":"` + strings.Repeat("a", raw) + `"}`),
	}
}

// The client cuts batches by record count alone, so a run of large records
// builds a body the server refuses whole. Nothing in the batch reaches the
// server and the caller cannot advance, which is the permanent stall #565
// left open. Splitting by size keeps every record and costs only more requests.
func TestSend_splitsBatchesThatExceedTheServerLimit(t *testing.T) {
	const limit = 1 << 20 // 1 MiB, small enough to hit with a handful of records
	srv, accepted := limitedSyncServer(t, limit)
	client := NewClient(srv.URL, "token", "")

	records := make([]*store.SessionRecord, 8)
	for i := range records {
		records[i] = recordOfSize("split-session", 300*1024) // ~300 KiB each
	}

	n, err := client.Send(context.Background(), "claude", "me@example.test", "u1", "-tmp-x", "x", ProjectIdentity{}, records)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if n == 0 {
		t.Fatal("no records reported inserted")
	}
	if len(*accepted) < 2 {
		t.Fatalf("accepted request count = %d, want the batch split across several", len(*accepted))
	}
	for _, size := range *accepted {
		if size > limit {
			t.Errorf("accepted body of %d bytes, over the %d limit", size, limit)
		}
	}
}

// A record that is over the limit on its own cannot be split. The send must fail
// rather than silently drop it -- the bytes are still on disk and a raised limit
// recovers them, which is only true while the offset stays put.
func TestSend_singleOversizedRecordFailsRatherThanDropping(t *testing.T) {
	const limit = 1 << 20
	srv, accepted := limitedSyncServer(t, limit)
	client := NewClient(srv.URL, "token", "")

	records := []*store.SessionRecord{recordOfSize("huge-session", 2<<20)}

	if _, err := client.Send(context.Background(), "claude", "me@example.test", "u1", "-tmp-x", "x", ProjectIdentity{}, records); err == nil {
		t.Fatal("Send reported success for a record the server refused")
	}
	if len(*accepted) != 0 {
		t.Fatalf("server accepted %d bodies, want 0", len(*accepted))
	}
}
