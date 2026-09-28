package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// The unpriced list names models and totals only, but it is an admin tool: what
// to price and what to declare free is an admin decision.
func TestUnpricedModels_adminOnly(t *testing.T) {
	srv := newTestServer(&mockStore{}, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/admin/unpriced-models", nil)
	r = r.WithContext(auth.WithUser(r.Context(), owner()))
	rec := httptest.NewRecorder()
	srv.handleListUnpricedModels(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want 403", rec.Code)
	}
}

func TestUnpricedModels_listsBothUnpricedAndFlatRate(t *testing.T) {
	now := time.Now().UTC()
	m := &mockStore{
		unpricedModels: []store.UnpricedModel{{Agent: "codex", Model: "codex-auto-review", Rows: 2, FirstTs: now, LastTs: now}},
		flatRateModels: []store.FlatRateModel{{Agent: "codex", Model: "gemma4:12b"}},
	}
	srv := newTestServer(m, nil)
	rec := httptest.NewRecorder()
	srv.handleListUnpricedModels(rec, adminJSON(http.MethodGet, "/api/admin/unpriced-models", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Unpriced []store.UnpricedModel `json:"unpriced"`
		FlatRate []store.FlatRateModel `json:"flat_rate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Unpriced) != 1 || body.Unpriced[0].Model != "codex-auto-review" {
		t.Errorf("unpriced = %+v", body.Unpriced)
	}
	if len(body.FlatRate) != 1 || body.FlatRate[0].Model != "gemma4:12b" {
		t.Errorf("flat_rate = %+v", body.FlatRate)
	}
}

// The model is the raw id the list showed, so its case is kept; the agent is a
// fixed lowercase label and is folded.
func TestMarkFlatRateModel_keepsTheRawModelID(t *testing.T) {
	m := &mockStore{}
	srv := newTestServer(m, nil)
	rec := httptest.NewRecorder()
	srv.handleMarkFlatRateModel(rec, adminJSON(http.MethodPost, "/api/admin/flat-rate-models",
		`{"agent":" Codex ","model":" Gemma4:12B ","reason":"local"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(m.flatRateModels) != 1 {
		t.Fatalf("store got %d marks, want 1", len(m.flatRateModels))
	}
	got := m.flatRateModels[0]
	if got.Agent != "codex" || got.Model != "Gemma4:12B" || got.Reason != "local" || got.CreatedBy != "admin@example.com" {
		t.Errorf("mark = %+v", got)
	}
}

func TestMarkFlatRateModel_requiresAgentAndModel(t *testing.T) {
	for _, body := range []string{`{"agent":"codex","model":"  "}`, `{"agent":"","model":"x"}`} {
		m := &mockStore{}
		rec := httptest.NewRecorder()
		newTestServer(m, nil).handleMarkFlatRateModel(rec, adminJSON(http.MethodPost, "/api/admin/flat-rate-models", body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, rec.Code)
		}
		if len(m.flatRateModels) != 0 {
			t.Errorf("%s: a blank key reached the store", body)
		}
	}
}

func TestUnmarkFlatRateModel(t *testing.T) {
	m := &mockStore{}
	rec := httptest.NewRecorder()
	newTestServer(m, nil).handleUnmarkFlatRateModel(rec, adminJSON(http.MethodDelete,
		"/api/admin/flat-rate-models?agent=codex&model=gemma4%3A12b", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(m.unmarkedFlatRate) != 1 || m.unmarkedFlatRate[0] != "codex/gemma4:12b" {
		t.Errorf("unmarked = %v", m.unmarkedFlatRate)
	}
}

// The routes are registered, so the admin UI reaches the handlers at all.
func TestUnpricedModels_routesRegistered(t *testing.T) {
	srv := newTestServer(&mockStore{}, nil)
	for _, pattern := range []string{
		"GET /api/admin/unpriced-models",
		"POST /api/admin/flat-rate-models",
		"DELETE /api/admin/flat-rate-models",
	} {
		method, path, _ := strings.Cut(pattern, " ")
		r := httptest.NewRequest(method, path, nil)
		if _, got := srv.mux.Handler(r); got != pattern {
			t.Errorf("%s resolved to %q", pattern, got)
		}
	}
}
