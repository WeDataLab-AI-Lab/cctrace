package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/store"
)

// A client that cannot finish a self-update reports it on the next sync (#750).
// The handler's job is only to carry that through to the store untouched --
// except for the one thing a client chooses the size of.
func TestSync_carriesUpdateStallToStore(t *testing.T) {
	// Given
	storage := &clientPlatformStore{mockStore: &mockStore{}}
	srv := newTestServer(storage, nil)
	body := `{"profile_email":"stalled@example.com","user_id":"stalled","records":[],` +
		`"update_stall":{"target_version":"v0.7.56","consecutive":9,` +
		`"first_failed_at":"2026-09-18T01:02:03Z","last_failed_at":"2026-09-21T04:05:06Z",` +
		`"reason":"rename /usr/local/bin/cctrace: permission denied"}}`
	request := httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Cctrace-Version", "v0.7.14")
	response := httptest.NewRecorder()

	// When
	srv.Handler().ServeHTTP(response, request)

	// Then
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if len(storage.updates) != 1 {
		t.Fatalf("client version updates = %d, want 1", len(storage.updates))
	}
	got := storage.updates[0].UpdateStall
	if got == nil {
		t.Fatal("UpdateStall = nil, want the reported stall")
	}
	if got.TargetVersion != "v0.7.56" || got.Consecutive != 9 {
		t.Fatalf("target/consecutive = %q/%d, want v0.7.56/9", got.TargetVersion, got.Consecutive)
	}
	wantFirst := time.Date(2026, 9, 18, 1, 2, 3, 0, time.UTC)
	if !got.FirstFailedAt.Equal(wantFirst) {
		t.Fatalf("FirstFailedAt = %v, want %v", got.FirstFailedAt, wantFirst)
	}
	if got.Reason != "rename /usr/local/bin/cctrace: permission denied" {
		t.Fatalf("Reason = %q, want the client's own message", got.Reason)
	}
}

// A client saying "nothing is wrong" and a client too old to say anything must
// not arrive as the same thing, or the screen counts silence as health.
func TestSync_separatesCleanReportFromNoReport(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		wantSaid bool
	}{
		{
			name:     "clean report",
			body:     `{"profile_email":"a@example.com","user_id":"a","records":[],"update_stall":{}}`,
			wantSaid: true,
		},
		{
			name:     "no report",
			body:     `{"profile_email":"a@example.com","user_id":"a","records":[]}`,
			wantSaid: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &clientPlatformStore{mockStore: &mockStore{}}
			srv := newTestServer(storage, nil)
			request := httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			srv.Handler().ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if len(storage.updates) != 1 {
				t.Fatalf("client version updates = %d, want 1", len(storage.updates))
			}
			said := storage.updates[0].UpdateStall != nil
			if said != tc.wantSaid {
				t.Fatalf("reported = %v, want %v", said, tc.wantSaid)
			}
		})
	}
}

// The reason is the one field whose length the client picks, and it is written
// on every sync. Cap it at the edge rather than trusting the sender.
func TestSync_capsUpdateStallReason(t *testing.T) {
	// Given
	storage := &clientPlatformStore{mockStore: &mockStore{}}
	srv := newTestServer(storage, nil)
	long := strings.Repeat("x", store.ClientUpdateStallReasonMax*3)
	body := fmt.Sprintf(
		`{"profile_email":"a@example.com","user_id":"a","records":[],"update_stall":{"consecutive":1,"reason":%q}}`,
		long,
	)
	request := httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	// When
	srv.Handler().ServeHTTP(response, request)

	// Then
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	got := storage.updates[0].UpdateStall
	if got == nil {
		t.Fatal("UpdateStall = nil, want the reported stall")
	}
	if len([]rune(got.Reason)) != store.ClientUpdateStallReasonMax {
		t.Fatalf("reason length = %d runes, want %d", len([]rune(got.Reason)), store.ClientUpdateStallReasonMax)
	}
}
