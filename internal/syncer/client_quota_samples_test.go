package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/store"
)

func quotaRows(n int) []*store.QuotaSample {
	rows := make([]*store.QuotaSample, n)
	for i := range rows {
		rows[i] = &store.QuotaSample{AccountID: "acct"}
	}
	return rows
}

// quotaChunkServer records the row count of every quota-samples request and
// answers the request numbered failAt (1-based) with failStatus.
func quotaChunkServer(t *testing.T, failAt, failStatus int) (*httptest.Server, *[]int) {
	t.Helper()
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p QuotaSamplesPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("decode: %v", err)
		}
		sizes = append(sizes, len(p.Samples))
		if len(sizes) == failAt {
			w.WriteHeader(failStatus)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &sizes
}

// A send larger than one request allows must arrive whole, split into chunks no
// server limit refuses. Sent as one body, a long Codex session's readings were
// refused on every pass and the file holding them was re-read forever.
func TestSendQuotaSamples_ChunksLargeSend(t *testing.T) {
	srv, sizes := quotaChunkServer(t, 0, 0)
	total := 2*MaxQuotaSamplesPerRequest + 1

	if err := NewClient(srv.URL, "", "").SendQuotaSamples(context.Background(), quotaRows(total)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(*sizes) != 3 {
		t.Fatalf("requests = %d (%v), want 3", len(*sizes), *sizes)
	}
	sum := 0
	for _, n := range *sizes {
		if n > MaxQuotaSamplesPerRequest {
			t.Errorf("chunk of %d rows exceeds %d", n, MaxQuotaSamplesPerRequest)
		}
		sum += n
	}
	if sum != total {
		t.Errorf("rows received = %d, want %d", sum, total)
	}
}

func TestSendQuotaSamples_StopsAtFirstFailedChunk(t *testing.T) {
	srv, sizes := quotaChunkServer(t, 2, http.StatusInternalServerError)

	err := NewClient(srv.URL, "", "").SendQuotaSamples(context.Background(), quotaRows(2*MaxQuotaSamplesPerRequest+1))
	if err == nil {
		t.Fatal("send succeeded, want the failed chunk's error")
	}
	if len(*sizes) != 2 {
		t.Errorf("requests = %d, want 2 (no chunk sent after the failure)", len(*sizes))
	}
}

func TestSendQuotaSamples_NotFoundIsUnsupported(t *testing.T) {
	srv, _ := quotaChunkServer(t, 1, http.StatusNotFound)

	err := NewClient(srv.URL, "", "").SendQuotaSamples(context.Background(), quotaRows(MaxQuotaSamplesPerRequest+1))
	if !errors.Is(err, ErrQuotaSamplesUnsupported) {
		t.Fatalf("err = %v, want ErrQuotaSamplesUnsupported", err)
	}
}

func TestSendQuotaSamples_EmptySendsNothing(t *testing.T) {
	srv, sizes := quotaChunkServer(t, 0, 0)

	if err := NewClient(srv.URL, "", "").SendQuotaSamples(context.Background(), nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(*sizes) != 0 {
		t.Errorf("requests = %d, want 0", len(*sizes))
	}
}
