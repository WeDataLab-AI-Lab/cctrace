package syncer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"cctrace/internal/store"
)

func testPolicyReport() store.ClientPolicyReport {
	return store.ClientPolicyReport{
		ProfileEmail: "alice@example.com", UserID: "alice",
		InstallationID: uuid.NewString(), Profile: "work",
		SyncEndpoint: "http://cctrace.company.example:4318", OTELEndpoint: "http://cctrace.company.example:4317", Protocol: "grpc",
		AppliedSequence: 2, CheckResult: store.ClientPolicyCheckDeferred, CheckSequence: 3,
		CheckReason: `shared Codex settings with profile "home"; policy deferred`,
	}
}

func TestSendPolicyReport_postsTheReportAsThisClient(t *testing.T) {
	t.Parallel()
	// Given
	type seen struct {
		method, path, auth, version string
		body                        map[string]any
	}
	got := make(chan seen, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		got <- seen{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Cctrace-Version"), body}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	report := testPolicyReport()

	// When
	err := NewClient(ts.URL, "cli-token", "v0.7.62").SendPolicyReport(context.Background(), report)

	// Then
	if err != nil {
		t.Fatalf("SendPolicyReport: %v", err)
	}
	req := <-got
	if req.method != http.MethodPost || req.path != "/api/client-policy-report" {
		t.Fatalf("request = %s %s, want POST /api/client-policy-report", req.method, req.path)
	}
	if req.auth != "Bearer cli-token" || req.version != "v0.7.62" {
		t.Fatalf("auth = %q, version = %q; the report must go out as this client", req.auth, req.version)
	}
	for field, want := range map[string]any{
		"installation_id":  report.InstallationID,
		"profile":          "work",
		"sync_endpoint":    "http://cctrace.company.example:4318",
		"otel_endpoint":    "http://cctrace.company.example:4317",
		"protocol":         "grpc",
		"applied_sequence": float64(2),
		"check_result":     "deferred",
		"check_sequence":   float64(3),
		"check_reason":     `shared Codex settings with profile "home"; policy deferred`,
		"profile_email":    "alice@example.com",
		"user_id":          "alice",
	} {
		if req.body[field] != want {
			t.Errorf("body[%q] = %v, want %v", field, req.body[field], want)
		}
	}
}

// A server older than the route answers 404. That is the normal state for a
// client that updated before its server did, and for one pointed at a server
// that will never have it; reporting it every five minutes would fill the log
// with something nobody can act on.
func TestSendPolicyReport_treatsAnOlderServerAsNothingToReport(t *testing.T) {
	t.Parallel()
	// Given
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()

	// When
	err := NewClient(ts.URL, "cli-token", "v0.7.62").SendPolicyReport(context.Background(), testPolicyReport())

	// Then
	if err != nil {
		t.Fatalf("SendPolicyReport against a server without the route: %v, want nil", err)
	}
}

func TestSendPolicyReport_returnsTheServersRefusal(t *testing.T) {
	t.Parallel()
	// Given
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"profile is not a valid profile name"}`)
	}))
	defer ts.Close()

	// When
	err := NewClient(ts.URL, "cli-token", "v0.7.62").SendPolicyReport(context.Background(), testPolicyReport())

	// Then
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "profile is not a valid profile name") {
		t.Fatalf("err = %v, want the status and the server's reason", err)
	}
}
