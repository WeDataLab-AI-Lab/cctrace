package syncer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cctrace/internal/store"
)

// syncTestEmail is the profile these cases sync as; the address itself carries
// no meaning here.
const syncTestEmail = "alice@example.test"

// The client is the only place that knows its own update state, and the daemon
// is the only thing that talks to the server, so the report rides the sync it
// was already making (#750).
func TestClient_carriesUpdateStallOnEverySend(t *testing.T) {
	// Given
	var bodies []map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]json.RawMessage
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"inserted":0}`))
	}))
	defer server.Close()

	failedAt := time.Date(2026, 9, 21, 4, 5, 6, 0, time.UTC)
	client := NewClient(server.URL, "token", "v0.7.14")
	client.SetUpdateStallReporter(func() *store.ClientUpdateStall {
		return &store.ClientUpdateStall{
			TargetVersion: "v0.7.56",
			Consecutive:   9,
			FirstFailedAt: failedAt.Add(-48 * time.Hour),
			LastFailedAt:  failedAt,
			Reason:        "rename /usr/local/bin/cctrace: permission denied",
		}
	})

	// When
	if _, err := client.Send(context.Background(), "claude", syncTestEmail, "alice", "hash", "proj", ProjectIdentity{}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Then
	if len(bodies) != 1 {
		t.Fatalf("requests = %d, want 1", len(bodies))
	}
	raw, ok := bodies[0]["update_stall"]
	if !ok {
		t.Fatal("update_stall missing from the payload")
	}
	var got store.ClientUpdateStall
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode update_stall: %v", err)
	}
	if got.TargetVersion != "v0.7.56" || got.Consecutive != 9 {
		t.Fatalf("target/consecutive = %q/%d, want v0.7.56/9", got.TargetVersion, got.Consecutive)
	}
	if !got.LastFailedAt.Equal(failedAt) {
		t.Fatalf("LastFailedAt = %v, want %v", got.LastFailedAt, failedAt)
	}
}

// Without a reporter installed the field must stay absent, so a build that does
// not know about this reads as "said nothing" rather than "said it is fine".
func TestClient_omitsUpdateStallWithoutReporter(t *testing.T) {
	// Given
	var body map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"inserted":0}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", "v0.7.14")

	// When
	if _, err := client.Send(context.Background(), "claude", syncTestEmail, "alice", "hash", "proj", ProjectIdentity{}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Then
	if _, ok := body["update_stall"]; ok {
		t.Fatal("update_stall present without a reporter, want absent")
	}
}
