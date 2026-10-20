package syncer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/store"
)

func TestClientTransferOutcomesKeepAuthAndRetryPerClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer auth-bad":
			w.WriteHeader(401)
		case "Bearer slow":
			w.Header().Set("Retry-After", "61")
			w.WriteHeader(429)
		default:
			w.Write([]byte(`{"inserted":1}`))
		}
	}))
	defer server.Close()
	for _, token := range []string{"auth-bad", "slow", "good"} {
		client := NewClient(server.URL, token, "dev")
		_, err := client.Send(context.Background(), "claude", "fixture", "fixture", "hash", "project", ProjectIdentity{}, []*store.SessionRecord{{}})
		outcome := client.TransferOutcome()
		if token == "auth-bad" && !errors.Is(err, ErrAuthentication) {
			t.Fatalf("auth error not classified: %v", err)
		}
		if token == "slow" && (outcome.RetryAfter.Seconds() != 61 || outcome.Failed != 1) {
			t.Fatalf("retry outcome: %+v", outcome)
		}
		if token == "good" && (outcome.Sent != 1 || outcome.Failed != 0 || err != nil) {
			t.Fatalf("isolated good client outcome: %+v %v", outcome, err)
		}
		client.ResetTransferOutcome()
		if got := client.TransferOutcome(); got.Sent != 0 || got.Failed != 0 {
			t.Fatalf("outcome not reset: %+v", got)
		}
	}
}
