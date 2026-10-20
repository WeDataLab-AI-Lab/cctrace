package api

import (
	"net/http"

	"cctrace/internal/sessionrecord"
	"cctrace/internal/store"
)

// handleMaintenanceStatus answers where the P1t transition stands (p1t-design
// §4.2): token_rule_pending until its completion marker commits, and step, the
// first step not yet done. Any signed-in user may read it: the note it drives is
// on every dashboard page.
func (s *Server) handleMaintenanceStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.store.RequestSetMigrationStatus(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	facts, err := s.store.SessionFactStatus(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	facts.Enabled = s.sessionAnalysisEnabled
	if !facts.Enabled {
		facts.Mode = "disabled"
	}
	recordWorker := sessionrecord.WorkerStatus{State: "off"}
	if s.sessionRecordsWorker != nil {
		recordWorker = s.sessionRecordsWorker.Status()
	}
	writeJSON(w, http.StatusOK, struct {
		TokenRulePending bool                    `json:"token_rule_pending"`
		Step             string                  `json:"step"`
		SessionAnalysis  store.SessionFactStatus `json:"session_analysis"`
		SessionRecords   map[string]interface{}  `json:"session_records"`
	}{status.TokenRulePending, status.Step, facts, map[string]interface{}{
		"enabled": s.sessionRecordsEnabled, "prompt_version": sessionrecord.PromptVersion,
		"state": recordWorker.State, "paused_reason": recordWorker.PausedReason,
	}})
}
