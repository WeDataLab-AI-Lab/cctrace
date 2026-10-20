package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func TestSessionOverviewClassificationSummaryOption(t *testing.T) {
	t.Parallel()
	for _, option := range []string{"", "&include_classification_counts=1", "&include_classification_counts=0"} {
		t.Run(option, func(t *testing.T) {
			t.Parallel()
			skip := option == "&include_classification_counts=0"
			calls := 0
			m := &mockStore{
				countSessionOverviewsFn: func(ctx context.Context, f store.SessionOverviewFilter) (int, error) {
					if f.Classification != "working" || f.DashboardUserID != 14 || f.PersonalUserID != "owner" || f.UserID != "owner" || f.Agent != "codex" || !f.FoldLineage || f.Source != "interactive" || len(f.ProjectHashes) != 1 || f.ProjectHashes[0] != "-project" {
						t.Errorf("filtered count lost ownership/population: %+v", f)
					}
					if _, ok := ctx.Deadline(); !ok {
						t.Error("count lost deadline")
					}
					return 3, nil
				},
				sessionOverviewClassificationCountsFn: func(_ context.Context, f store.SessionOverviewFilter) (map[string]int, error) {
					calls++
					if skip {
						return nil, errors.New("summary must not block opted-out total")
					}
					if f.Classification != "" {
						t.Error("summary must cover all classifications")
					}
					return map[string]int{"working": 3}, nil
				},
			}
			srv := newTestServer(m, nil)
			srv.useUserIDAccessControl = true
			user := &auth.DashboardUser{ID: 14, Role: "user", CctraceUserID: "owner"}
			r := personalAPIRequest(http.MethodGet, "/api/session-overview/count?classification=working&agent=codex&assembled=1&source=interactive&project_hashes=project"+option, "", user, false)
			w := httptest.NewRecorder()
			srv.handleCountSessionOverview(w, r)
			var result map[string]json.RawMessage
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || string(result["count"]) != "3" {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			_, included := result["classification_counts"]
			if included == skip || calls != map[bool]int{false: 1, true: 0}[skip] {
				t.Fatalf("summary included=%v calls=%d skip=%v", included, calls, skip)
			}
		})
	}
}

func TestSessionOverviewClassificationSummaryRejectsInvalidOption(t *testing.T) {
	t.Parallel()
	for _, option := range []string{"", "false", "2", "0&include_classification_counts=1"} {
		t.Run(option, func(t *testing.T) {
			t.Parallel()
			m := &mockStore{countSessionOverviewsFn: func(context.Context, store.SessionOverviewFilter) (int, error) {
				t.Error("invalid option reached store")
				return 0, nil
			}}
			w := httptest.NewRecorder()
			newTestServer(m, nil).handleCountSessionOverview(w, httptest.NewRequest(http.MethodGet, "/api/session-overview/count?include_classification_counts="+option, nil))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSessionOverviewClassificationSummaryKeepsAnalysisContract(t *testing.T) {
	t.Parallel()
	calls := 0
	m := &mockStore{sessionOverviewAnalysisCountFn: func(_ context.Context, f store.SessionOverviewFilter, _ bool) (int, map[string]int, int, error) {
		calls++
		if !f.IncludeAnalysis {
			t.Error("analysis scope lost")
		}
		return 9, map[string]int{"working": 3}, 0, nil
	}}
	srv := newTestServer(m, nil)
	srv.WithSessionAnalysisEnabled(true)
	w := httptest.NewRecorder()
	user := &auth.DashboardUser{ID: 14, Role: "user", CctraceUserID: "owner"}
	srv.handleCountSessionOverview(w, personalAPIRequest(http.MethodGet, "/api/session-overview/count?include=analysis&include_classification_counts=0", "", user, false))
	var result map[string]json.RawMessage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result["classification_counts"] == nil || calls != 1 {
		t.Fatalf("analysis contract: calls=%d status=%d body=%s", calls, w.Code, w.Body.String())
	}
}
