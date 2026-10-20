package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"cctrace/internal/emailalias"
	"cctrace/internal/profile"
	"cctrace/internal/store"
)

type clientPolicyReportStore struct {
	*mockStore
	reports []store.ClientPolicyReport
}

func (s *clientPolicyReportStore) UpsertClientPolicyReport(_ context.Context, report store.ClientPolicyReport) error {
	s.reports = append(s.reports, report)
	return nil
}

// validClientPolicyReport is a report as a client sends it, as a map so a test
// can break one field at a time.
func validClientPolicyReport() map[string]any {
	return map[string]any{
		"profile_email":    "alice@example.com",
		"user_id":          "alice",
		"installation_id":  uuid.NewString(),
		"profile":          "work",
		"sync_endpoint":    "http://cctrace.company.example:4318",
		"otel_endpoint":    "http://cctrace.company.example:4317",
		"protocol":         "grpc",
		"applied_sequence": 0,
		"check_result":     "deferred",
		"check_sequence":   3,
		"check_reason":     `shared Codex settings with profile "home"; policy deferred`,
	}
}

func postClientPolicyReport(t *testing.T, srv *Server, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/client-policy-report", strings.NewReader(string(raw)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Cctrace-Version", "v0.7.62")
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, request)
	return response
}

func TestClientPolicyReport_storesWhatTheClientSaid(t *testing.T) {
	// Given
	storage := &clientPolicyReportStore{mockStore: &mockStore{}}
	srv := newTestServer(storage, nil)
	body := validClientPolicyReport()

	// When
	response := postClientPolicyReport(t, srv, body)

	// Then
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body %s", response.Code, http.StatusNoContent, response.Body.String())
	}
	if len(storage.reports) != 1 {
		t.Fatalf("stored reports = %d, want 1", len(storage.reports))
	}
	want := store.ClientPolicyReport{
		ProfileEmail: "alice@example.com", UserID: "alice",
		InstallationID: body["installation_id"].(string), Profile: "work",
		SyncEndpoint: "http://cctrace.company.example:4318", OTELEndpoint: "http://cctrace.company.example:4317", Protocol: "grpc",
		CheckResult: "deferred", CheckSequence: 3,
		CheckReason:   `shared Codex settings with profile "home"; policy deferred`,
		ClientVersion: "v0.7.62",
	}
	if storage.reports[0] != want {
		t.Fatalf("stored report = %+v, want %+v", storage.reports[0], want)
	}
}

// The row is keyed by the policy target, so a report without an account still
// identifies something a policy can name. client_versions drops such requests
// only because the account is its key.
func TestClientPolicyReport_storesAReportWithoutAnAccount(t *testing.T) {
	// Given
	storage := &clientPolicyReportStore{mockStore: &mockStore{}}
	srv := newTestServer(storage, nil)
	body := validClientPolicyReport()
	delete(body, "profile_email")
	body["profile"] = "" // the default profile

	// When
	response := postClientPolicyReport(t, srv, body)

	// Then
	if response.Code != http.StatusNoContent || len(storage.reports) != 1 {
		t.Fatalf("status = %d, stored = %d; want %d and 1", response.Code, len(storage.reports), http.StatusNoContent)
	}
	if got := storage.reports[0]; got.ProfileEmail != "" || got.Profile != "" {
		t.Fatalf("stored report = %+v, want empty account and the default profile", got)
	}
}

// The operator's list joins reports to client_versions by account, and sync
// files its rows under the canonical address. A report kept under the alias the
// client happened to send would sit beside that account instead of on it.
func TestClientPolicyReport_filesTheReportUnderTheCanonicalAccount(t *testing.T) {
	// Given
	storage := &clientPolicyReportStore{mockStore: &mockStore{}}
	srv := newTestServer(storage, nil)
	srv.aliases = emailalias.Aliases{"legacy@example.com": "alice@example.com"}
	body := validClientPolicyReport()
	body["profile_email"] = "Legacy@example.com"

	// When
	response := postClientPolicyReport(t, srv, body)

	// Then
	if response.Code != http.StatusNoContent || len(storage.reports) != 1 {
		t.Fatalf("status = %d, stored = %d; want %d and 1", response.Code, len(storage.reports), http.StatusNoContent)
	}
	if got := storage.reports[0].ProfileEmail; got != "alice@example.com" {
		t.Fatalf("stored account = %q, want the canonical alice@example.com", got)
	}
}

// A value that cannot be used as written is refused whole. Truncating instead
// would store something worse than nothing: a cut profile name merges two targets
// into one row, and a cut endpoint pasted into a policy's `from` list never
// matches the client it came from.
func TestClientPolicyReport_refusesWhatItCannotStoreAsWritten(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"installation id is not a UUID":     func(b map[string]any) { b["installation_id"] = "not-a-uuid" },
		"installation id is not canonical":  func(b map[string]any) { b["installation_id"] = strings.ToUpper(uuid.NewString()) },
		"installation id missing":           func(b map[string]any) { delete(b, "installation_id") },
		"profile name breaks the name rule": func(b map[string]any) { b["profile"] = "../etc" },
		"profile name is the reserved one":  func(b map[string]any) { b["profile"] = "default" },
		"profile name is too long":          func(b map[string]any) { b["profile"] = strings.Repeat("p", 64) },
		"sync endpoint is too long": func(b map[string]any) {
			b["sync_endpoint"] = "http://cctrace.company.example:4318/" + strings.Repeat("a", 512)
		},
		"otel endpoint carries a line break": func(b map[string]any) { b["otel_endpoint"] = "http://cctrace.company.example:4317\nx" },
		"protocol is too long":               func(b map[string]any) { b["protocol"] = strings.Repeat("g", 33) },
		"check result is not one we know":    func(b map[string]any) { b["check_result"] = "maybe" },
		"applied sequence beyond the column": func(b map[string]any) { b["applied_sequence"] = uint64(math.MaxInt64) + 1 },
		"check sequence beyond the column":   func(b map[string]any) { b["check_sequence"] = uint64(math.MaxInt64) + 1 },
		"account is too long":                func(b map[string]any) { b["profile_email"] = strings.Repeat("a", 321) },
	} {
		t.Run(name, func(t *testing.T) {
			// Given
			storage := &clientPolicyReportStore{mockStore: &mockStore{}}
			srv := newTestServer(storage, nil)
			body := validClientPolicyReport()
			mutate(body)

			// When
			response := postClientPolicyReport(t, srv, body)

			// Then
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body %s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			if len(storage.reports) != 0 {
				t.Fatalf("stored %d report(s) from a refused request", len(storage.reports))
			}
		})
	}
}

// The reason is the client's error text and the one field it chooses the length
// of. It is cut rather than refused -- losing the tail of a message is better
// than losing the device from the list -- and cut on runes, because a path under
// a non-ASCII home directory cut mid-character is invalid UTF-8 and Postgres
// refuses the row. Line breaks are flattened: the text ends up in a table an
// operator reads.
func TestClientPolicyReport_flattensAndCutsTheReason(t *testing.T) {
	// Given
	storage := &clientPolicyReportStore{mockStore: &mockStore{}}
	srv := newTestServer(storage, nil)
	body := validClientPolicyReport()
	body["check_result"] = "failed"
	body["check_reason"] = "open /Users/alice/.cctrace/profile.json:\n\tpermission denied " + strings.Repeat("가", store.ClientPolicyReasonMax)

	// When
	response := postClientPolicyReport(t, srv, body)

	// Then
	if response.Code != http.StatusNoContent || len(storage.reports) != 1 {
		t.Fatalf("status = %d, stored = %d; want %d and 1", response.Code, len(storage.reports), http.StatusNoContent)
	}
	reason := storage.reports[0].CheckReason
	if !utf8.ValidString(reason) {
		t.Fatalf("stored reason is not valid UTF-8: %q", reason)
	}
	if n := utf8.RuneCountInString(reason); n != store.ClientPolicyReasonMax {
		t.Fatalf("stored reason is %d runes, want %d", n, store.ClientPolicyReasonMax)
	}
	if strings.ContainsAny(reason, "\n\t\r") {
		t.Fatalf("stored reason keeps control characters: %q", reason)
	}
	if !strings.HasPrefix(reason, "open /Users/alice/.cctrace/profile.json: permission denied") {
		t.Fatalf("stored reason lost its head: %q", reason)
	}
}

// The profile-name rule is the client's (internal/profile). The server keeps its
// own copy rather than import a package that reads the local home directory, so
// this ties the two: a name one accepts and the other refuses would either hide a
// real profile from the list or let through a name no client can have.
func TestClientPolicyReport_profileRuleAgreesWithTheClient(t *testing.T) {
	for _, name := range []string{"work", "a", "A1_b-c", strings.Repeat("p", 63), strings.Repeat("p", 64), "default", "-lead", "has space", "한글", ""} {
		clientAccepts := name == "" || profile.ValidateName(name) == nil
		if got := validClientPolicyProfile(name); got != clientAccepts {
			t.Errorf("profile %q: server accepts = %v, client accepts = %v", name, got, clientAccepts)
		}
	}
}

// Without the CLI's credentials nothing is read and nothing is stored: the list
// this feeds is what an operator writes a policy from.
func TestClientPolicyReport_isBehindCLIAuthentication(t *testing.T) {
	// Given
	deny := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	}
	storage := &clientPolicyReportStore{mockStore: &mockStore{}}
	srv := newTestServer(storage, nil, deny)

	// When
	response := postClientPolicyReport(t, srv, validClientPolicyReport())

	// Then
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if len(storage.reports) != 0 {
		t.Fatalf("stored %d report(s) from an unauthenticated request", len(storage.reports))
	}
}
