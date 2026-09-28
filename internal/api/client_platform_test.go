package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

type clientPlatformStore struct {
	*mockStore
	updates  []store.ClientVersionUpdate
	versions []*store.ClientVersionRecord
}

func (s *clientPlatformStore) UpsertClientVersion(_ context.Context, update store.ClientVersionUpdate) error {
	s.updates = append(s.updates, update)
	return nil
}

func (s *clientPlatformStore) ListClientVersions(context.Context) ([]*store.ClientVersionRecord, error) {
	return s.versions, nil
}

func TestSync_ingestsClientPlatformHeaders(t *testing.T) {
	// Given
	storage := &clientPlatformStore{mockStore: &mockStore{}}
	srv := newTestServer(storage, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(
		`{"profile_email":"alice@example.com","user_id":"alice","records":[]}`,
	))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Cctrace-Version", "v0.8.0")
	request.Header.Set("X-Cctrace-Os", "windows")
	request.Header.Set("X-Cctrace-Arch", "amd64")
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
	want := store.ClientVersionUpdate{
		UserID:        "alice",
		ProfileEmail:  "alice@example.com",
		ClientVersion: "v0.8.0",
		ClientOS:      "windows",
		ClientArch:    "amd64",
	}
	if storage.updates[0] != want {
		t.Fatalf("client version update = %+v, want %+v", storage.updates[0], want)
	}
}

func TestSync_ingestsEmptyClientPlatform_whenLegacyHeadersMissing(t *testing.T) {
	// Given
	storage := &clientPlatformStore{mockStore: &mockStore{}}
	srv := newTestServer(storage, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(
		`{"profile_email":"legacy@example.com","user_id":"legacy","records":[]}`,
	))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Cctrace-Version", "v0.7.30")
	response := httptest.NewRecorder()

	// When
	srv.Handler().ServeHTTP(response, request)

	// Then
	if len(storage.updates) != 1 {
		t.Fatalf("client version updates = %d, want 1", len(storage.updates))
	}
	if storage.updates[0].ClientOS != "" || storage.updates[0].ClientArch != "" {
		t.Fatalf("legacy client platform = %q/%q, want empty values", storage.updates[0].ClientOS, storage.updates[0].ClientArch)
	}
}

func TestListClientVersions_returnsClientPlatformFields(t *testing.T) {
	// Given
	seenAt := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	storage := &clientPlatformStore{
		mockStore: &mockStore{},
		versions: []*store.ClientVersionRecord{{
			ProfileEmail:  "alice@example.com",
			ClientVersion: "v0.8.0",
			ClientOS:      "darwin",
			ClientArch:    "arm64",
			LastSeenAt:    seenAt,
		}},
	}
	srv := newTestServer(storage, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/client-versions", nil)
	request = request.WithContext(auth.WithUser(request.Context(), &auth.DashboardUser{Role: "admin"}))
	response := httptest.NewRecorder()

	// When
	srv.Handler().ServeHTTP(response, request)

	// Then
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var body []struct {
		ClientOS   string `json:"client_os"`
		ClientArch string `json:"client_arch"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body) != 1 || body[0].ClientOS != "darwin" || body[0].ClientArch != "arm64" {
		t.Fatalf("client platform response = %+v, want darwin/arm64", body)
	}
}

// A server with no synced clients yet answered `null`; the admin Clients tab spreads
// the list and crashed the whole admin page ("rows is not iterable").
func TestListClientVersions_returnsEmptyArray_whenNoClients(t *testing.T) {
	// Given
	storage := &clientPlatformStore{mockStore: &mockStore{}}
	srv := newTestServer(storage, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/client-versions", nil)
	request = request.WithContext(auth.WithUser(request.Context(), &auth.DashboardUser{Role: "admin"}))
	response := httptest.NewRecorder()

	// When
	srv.Handler().ServeHTTP(response, request)

	// Then
	if got := strings.TrimSpace(response.Body.String()); got != "[]" {
		t.Fatalf("body = %s, want []", got)
	}
}

// A client that sends no X-Cctrace-Version header still synced, and the row is
// the only place that fact is recorded. Skipping the upsert made "no row" mean
// two different things - never synced, or synced without a version header - and
// that ambiguity produced a wrong call in prod: an account with 358k session
// records was read as "the sync client is not running" purely because
// client_versions had no row for it (#455).
func TestSync_recordsClientVersion_whenVersionHeaderMissing(t *testing.T) {
	// Given
	storage := &clientPlatformStore{mockStore: &mockStore{}}
	srv := newTestServer(storage, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(
		`{"profile_email":"headerless@example.com","user_id":"headerless","records":[]}`,
	))
	request.Header.Set("Content-Type", "application/json")
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
	want := store.ClientVersionUpdate{
		UserID:       "headerless",
		ProfileEmail: "headerless@example.com",
	}
	if storage.updates[0] != want {
		t.Fatalf("client version update = %+v, want %+v", storage.updates[0], want)
	}
}

// The row's primary key is profile_email, so a request without one would write
// a single shared row that every such client overwrites. Making the upsert
// unconditional removed the header check that used to keep those requests out
// by accident, so the guard has to be stated: no identity, no row.
func TestSync_skipsClientVersion_whenProfileEmailMissing(t *testing.T) {
	// Given
	storage := &clientPlatformStore{mockStore: &mockStore{}}
	srv := newTestServer(storage, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(
		`{"profile_email":"","user_id":"anon","records":[]}`,
	))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Cctrace-Version", "v0.7.32")
	response := httptest.NewRecorder()

	// When
	srv.Handler().ServeHTTP(response, request)

	// Then
	if len(storage.updates) != 0 {
		t.Fatalf("client version updates = %d, want 0 -- an empty profile_email would key a shared junk row: %+v",
			len(storage.updates), storage.updates)
	}
	// Skipping the row must not skip the reply. Asserting the status code alone
	// proves nothing here: httptest.NewRecorder starts at 200, so a handler that
	// returns without writing anything passes that check. The body is what
	// separates "answered" from "fell out of the handler".
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d -- the sync still has to be answered", response.Code, http.StatusOK)
	}
	var reply map[string]int
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
		t.Fatalf("reply is not the sync result JSON (%q): %v -- an early return would leave it empty",
			response.Body.String(), err)
	}
	if _, ok := reply["inserted"]; !ok {
		t.Fatalf("reply %q has no inserted count; the handler returned before answering", response.Body.String())
	}
}
