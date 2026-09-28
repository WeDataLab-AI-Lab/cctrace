package syncer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/store"
)

// Redact() being correct is not the property that matters. The property is that
// nothing reaches the wire unredacted -- and those are different claims, separated
// by whether Send actually calls it.
//
// That gap is where the flag this replaces lived: log_user_prompts had a value, a
// CLI, and a config listing, and no code path between them. A test on the helper
// alone would have passed just as happily.
//
// So this one reads the bytes off an HTTP server.
func TestSendRedactsBeforeTheRequestLeaves(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"synced":1}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "", "test")
	client.SetRedactPolicy(RedactPolicy{UserPrompts: true})

	records := []*store.SessionRecord{{
		RecordType: "user",
		Raw:        json.RawMessage(`{"message":{"role":"user","content":[{"type":"text","text":"rotate the prod key"}]}}`),
	}}
	if _, err := client.Send(context.Background(), "claude", "", "", "", "", ProjectIdentity{}, records); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if strings.Contains(string(body), "rotate the prod key") {
		t.Errorf("the prompt reached the server:\n%s", body)
	}
	if !strings.Contains(string(body), redactedMarker) {
		t.Errorf("no marker in the uploaded payload:\n%s", body)
	}
}

// The same call with no policy must carry the text, or the test above would pass
// against a client that uploads nothing at all.
func TestSendCarriesContentWhenNoPolicySet(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"synced":1}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "", "test")

	records := []*store.SessionRecord{{
		RecordType: "user",
		Raw:        json.RawMessage(`{"message":{"role":"user","content":[{"type":"text","text":"rotate the prod key"}]}}`),
	}}
	if _, err := client.Send(context.Background(), "claude", "", "", "", "", ProjectIdentity{}, records); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if !strings.Contains(string(body), "rotate the prod key") {
		t.Errorf("default client dropped content it was not asked to drop:\n%s", body)
	}
}
