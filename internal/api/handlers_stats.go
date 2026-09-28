package api

import (
	"net/http"
	"time"

	"cctrace/internal/projecthash"
	"cctrace/internal/store"
)

// The login_email every handler below filters on is not necessarily an observed
// account: the inference pass (#346) writes login_email on session_records that no
// OTEL ever named, and the gjc/omo arms of these queries read that column without
// consulting login_email_source. See ListLoginAccounts in postgres_cost.go for the
// full argument and for why the totals are still the right answer to report.
func (s *Server) handleDailyStats(w http.ResponseWriter, r *http.Request) {
	f := store.EventFilter{
		ProfileEmail: r.URL.Query().Get("profile_email"),
		LoginEmail:   r.URL.Query().Get("login_email"),
		UserTeam:     r.URL.Query().Get("user_team"),
	}
	if tz := r.URL.Query().Get("tz"); tz != "" && tzRegexp.MatchString(tz) {
		f.Timezone = tz
	}
	f.Since, f.Until = lenientQueryTimeRange(r)
	result, err := s.store.DailyStats(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleTimeSeriesStats(w http.ResponseWriter, r *http.Request) {
	projectHashes, projectHashesPresent := queryProjectHashesWithPresence(r, "project_hashes")
	f := store.EventFilter{
		ProfileEmail:  r.URL.Query().Get("profile_email"),
		LoginEmail:    r.URL.Query().Get("login_email"),
		ProjectHash:   queryProjectHash(r, "project_hash"),
		ProjectHashes: projectHashes, ProjectHashesPresent: projectHashesPresent,
		UserTeam: r.URL.Query().Get("user_team"),
		Agent:    r.URL.Query().Get("agent"),
	}
	if tz := r.URL.Query().Get("tz"); tz != "" && tzRegexp.MatchString(tz) {
		f.Timezone = tz
	}
	f.Since, f.Until = lenientQueryTimeRange(r)
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "minute"
	}
	result, err := s.store.TimeSeriesStats(r.Context(), f, granularity)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleTimeSeriesStatsByModel(w http.ResponseWriter, r *http.Request) {
	projectHashes, projectHashesPresent := queryProjectHashesWithPresence(r, "project_hashes")
	f := store.EventFilter{
		ProfileEmail:  r.URL.Query().Get("profile_email"),
		LoginEmail:    r.URL.Query().Get("login_email"),
		UserID:        r.URL.Query().Get("user_id"),
		ProjectHash:   queryProjectHash(r, "project_hash"),
		ProjectHashes: projectHashes, ProjectHashesPresent: projectHashesPresent,
		UserTeam:      r.URL.Query().Get("user_team"),
		Agent:         r.URL.Query().Get("agent"),
		ModelCategory: r.URL.Query().Get("model_category"),
	}
	if tz := r.URL.Query().Get("tz"); tz != "" && tzRegexp.MatchString(tz) {
		f.Timezone = tz
	}
	f.Since, f.Until = lenientQueryTimeRange(r)
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "minute"
	}
	result, err := s.store.TimeSeriesStatsByModel(r.Context(), f, granularity)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleTimeSeriesStatsByUser(w http.ResponseWriter, r *http.Request) {
	projectHashes, projectHashesPresent := queryProjectHashesWithPresence(r, "project_hashes")
	f := store.EventFilter{
		ProfileEmail:  r.URL.Query().Get("profile_email"),
		LoginEmail:    r.URL.Query().Get("login_email"),
		UserID:        r.URL.Query().Get("user_id"),
		ProjectHash:   queryProjectHash(r, "project_hash"),
		ProjectHashes: projectHashes, ProjectHashesPresent: projectHashesPresent,
		UserTeam:      r.URL.Query().Get("user_team"),
		Agent:         r.URL.Query().Get("agent"),
		ModelCategory: r.URL.Query().Get("model_category"),
		Model:         r.URL.Query().Get("model"),
	}
	if tz := r.URL.Query().Get("tz"); tz != "" && tzRegexp.MatchString(tz) {
		f.Timezone = tz
	}
	f.Since, f.Until = lenientQueryTimeRange(r)
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "minute"
	}
	result, err := s.store.TimeSeriesStatsByUser(r.Context(), f, granularity)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleLatestActivity(w http.ResponseWriter, r *http.Request) {
	projectHashes, projectHashesPresent := queryProjectHashesWithPresence(r, "project_hashes")
	f := store.EventFilter{
		ProfileEmail:  r.URL.Query().Get("profile_email"),
		LoginEmail:    r.URL.Query().Get("login_email"),
		UserID:        r.URL.Query().Get("user_id"),
		ProjectHash:   queryProjectHash(r, "project_hash"),
		ProjectHashes: projectHashes, ProjectHashesPresent: projectHashesPresent,
		UserTeam:      r.URL.Query().Get("user_team"),
		Agent:         r.URL.Query().Get("agent"),
		ModelCategory: r.URL.Query().Get("model_category"),
		Model:         r.URL.Query().Get("model"),
	}
	ts, ok, err := s.store.LatestActivityTs(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := map[string]interface{}{"has_data": ok}
	if ok {
		resp["latest_ts"] = ts.Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusOK, resp)
}

// queryProjectHash reads a project_hash filter and canonicalises it.
//
// Stored hashes were rewritten to one spelling (#303); a filter carrying an older
// spelling has to be translated the same way or it matches nothing at all, which
// looks to the caller like an empty project rather than a stale value.
func queryProjectHash(r *http.Request, name string) string {
	return projecthash.Repair(r.URL.Query().Get(name))
}

// queryProjectHashes is queryProjectHash for the comma-separated forms.
func queryProjectHashes(r *http.Request, name string) []string {
	raw := queryCSV(r, name)
	out := make([]string, 0, len(raw))
	for _, h := range raw {
		if c := projecthash.Repair(h); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func queryProjectHashesWithPresence(r *http.Request, name string) ([]string, bool) {
	return queryProjectHashes(r, name), r.URL.Query().Has(name)
}
