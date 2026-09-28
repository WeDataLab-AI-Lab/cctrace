package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The server's read deadline was a constant while the body limit became
// configurable, so raising CCTRACE_MAX_SYNC_BODY_BYTES bought an acceptance the
// server would not wait for: the operator is told 64 MiB is allowed and the read
// still ends at 30 s. What the client then sees is a dropped connection, not a
// 413, so it cannot tell a size problem from an unreachable server.
func TestSyncReadDeadline(t *testing.T) {
	small := syncReadDeadline(maxSyncRequestBodyBytes)
	if small < 30*time.Second {
		t.Errorf("the default limit got %s, less than the fixed deadline it replaces", small)
	}

	raised := syncReadDeadline(64 << 20)
	if raised <= small {
		t.Errorf("a raised limit got %s, no more than the default's %s", raised, small)
	}

	// Bounded: a slow sender holds a connection for at most this, having sent at
	// most the limit, so the exposure is stated by two numbers rather than open.
	ceiling := syncReadDeadline(MaxConfigurableSyncBodyBytes)
	if ceiling > 10*time.Minute {
		t.Errorf("the configurable ceiling got %s; the exposure has to stay bounded", ceiling)
	}
}

// The read deadline was raised and the write deadline was not, which bought
// nothing: Go arms WriteTimeout when the request header has been read, so that
// one clock covers the body transfer, the work and the reply. Production ran
// POST /api/sync 500 at 30,000 ms between 90 and 1,146 times a day while the
// body limit said 192 MiB and syncReadDeadline said 222 s (#621).
func TestSyncWriteDeadlineOutlastsTheRead(t *testing.T) {
	for _, limit := range []int64{maxSyncRequestBodyBytes, 64 << 20, MaxConfigurableSyncBodyBytes} {
		read, write := syncReadDeadline(limit), syncWriteDeadline(limit)
		if write <= read {
			t.Errorf("limit %d: write deadline %s does not outlast the read deadline %s, so the read budget is unusable",
				limit, write, read)
		}
	}

	// The server-wide WriteTimeout this replaces. Anything at or below it leaves
	// the defect in place for the default limit.
	if got := syncWriteDeadline(maxSyncRequestBodyBytes); got <= 30*time.Second {
		t.Errorf("the default limit got %s, no more than the 30s it has to replace", got)
	}

	// Still bounded, on the same terms as the read deadline: one connection, at
	// most the limit sent, for at most this long.
	if got := syncWriteDeadline(MaxConfigurableSyncBodyBytes); got > 15*time.Minute {
		t.Errorf("the configurable ceiling got %s; the exposure has to stay bounded", got)
	}
}

// TestSyncBodyLimitOutlivesTheServerWriteTimeout is the end-to-end half: the
// deadline arithmetic above is worth nothing if the middleware never applies it.
//
// The server is given a WriteTimeout far shorter than the handler takes, which
// is the production shape in miniature -- there, a 30 s WriteTimeout cut
// requests the 222 s read deadline had already accepted.
func TestSyncBodyLimitOutlivesTheServerWriteTimeout(t *testing.T) {
	const handlerWork = 300 * time.Millisecond

	s := &Server{}
	handler := s.withSyncBodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(handlerWork)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"inserted":1}`))
	}))

	srv := httptest.NewUnstartedServer(handler)
	srv.Config.WriteTimeout = handlerWork / 3
	srv.Start()
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/api/sync", "application/json", strings.NewReader(`{"records":[]}`))
	if err != nil {
		t.Fatalf("request failed after outliving the server WriteTimeout: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 -- the write deadline cut a request the body limit accepted", resp.StatusCode)
	}
	if string(body) != `{"inserted":1}` {
		t.Fatalf("body %q, want the handler's reply", body)
	}
}
