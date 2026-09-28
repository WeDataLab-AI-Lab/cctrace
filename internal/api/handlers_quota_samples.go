package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"cctrace/internal/store"
)

// maxQuotaSampleBatch bounds one ingest request. The Codex backfill walks
// thousands of session files at once, so it chunks; this is the ceiling it
// chunks to, and it keeps a malformed or hostile body from being unbounded.
const maxQuotaSampleBatch = 5000

type quotaSamplesRequest struct {
	Samples []*store.QuotaSample `json:"samples"`
}

type quotaSamplesResponse struct {
	Accepted int `json:"accepted"`
	Inserted int `json:"inserted"`
}

// handleQuotaSamplesWrite appends readings to the history table.
//
// This is a separate endpoint from POST /api/quota rather than an extension of
// it. That one keeps a single current row per profile and is what older clients
// speak; leaving it untouched means a client that predates this cannot be
// broken by it, and a new client talking to an older server simply finds no
// route here and carries on feeding the snapshot.
func (s *Server) handleQuotaSamplesWrite(w http.ResponseWriter, r *http.Request) {
	var req quotaSamplesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.Samples) > maxQuotaSampleBatch {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "too many samples in one request (max " + strconv.Itoa(maxQuotaSampleBatch) + ")",
		})
		return
	}

	inserted, err := s.store.InsertQuotaSamples(r.Context(), req.Samples)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Accepted and inserted are reported separately. They differ whenever a
	// reading was already held — the response cache re-offering one, another
	// profile having reported the same instant, a backfill being re-run — and
	// that is a normal outcome, not an error. Collapsing them would make a
	// re-run look like it did work it did not do.
	writeJSON(w, http.StatusOK, quotaSamplesResponse{
		Accepted: len(req.Samples),
		Inserted: inserted,
	})
}

// handleQuotaSamplesRead serves the chart's history query.
func (s *Server) handleQuotaSamplesRead(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.QuotaSampleFilter{
		BillingProvider: q.Get("billing_provider"),
	}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from: " + err.Error()})
			return
		}
		f.From = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to: " + err.Error()})
			return
		}
		f.To = t
	}
	if v := q.Get("window_minutes"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "window_minutes: " + err.Error()})
			return
		}
		f.WindowMinutes = n
	}

	samples, err := s.store.ListQuotaSamples(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	if samples == nil {
		samples = []*store.QuotaSample{}
	}
	writeJSON(w, http.StatusOK, samples)
}
