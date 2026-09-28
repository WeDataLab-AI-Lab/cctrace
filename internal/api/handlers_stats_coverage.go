package api

import (
	"net/http"

	"cctrace/internal/store"
)

// handleCoverageGap answers "how much of the subscription burn did we measure"
// for a range, or says why the question cannot be answered for this request.
//
// Ineligibility is a 200 with eligible:false. The chart polls this beside two
// other queries at POLL_NORMAL; a 4xx would present an ordinary state -- the
// reader narrowed to one project -- as a failure.
func (s *Server) handleCoverageGap(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ineligible := func(reason string) {
		writeJSON(w, http.StatusOK, &store.CoverageGap{
			Reason: reason, Accounts: []store.CoverageGapAccount{}, Unfitted: []string{}, KMethod: store.CoverageKMethod,
		})
	}

	// Quota readings are account-level: no session, project, model or profile
	// attribution exists on the burn side, so any subset on the measured side
	// leaves nothing to compare it with.
	for _, key := range []string{"project_hash", "project_hashes", "user_id", "profile_email", "user_team", "model", "agent"} {
		if q.Get(key) != "" {
			ineligible("coverage is account-level; the " + key + " filter has no counterpart in subscription burn")
			return
		}
	}
	if q.Get("model_category") == "compatible" {
		ineligible("compatible-provider traffic burns no subscription")
		return
	}
	since, until := lenientQueryTimeRange(r)
	if since == nil || until == nil || !until.After(*since) {
		ineligible("since and until are required")
		return
	}
	f := store.CoverageGapFilter{Since: *since, Until: *until, LoginEmail: q.Get("login_email")}
	// The bucket series is opt-in and rides the chart's own bucketing; an
	// unknown granularity yields the range total only rather than a guess.
	switch g := q.Get("granularity"); g {
	case "minute", "hour", "day", "week", "month":
		f.Granularity = g
		if tz := q.Get("tz"); tz != "" && tzRegexp.MatchString(tz) {
			f.Timezone = tz
		}
	}
	// The percentage is an integer and the weekly meter moves about a point an
	// hour at full tilt; over a few hours the range total is quantization, not
	// signal. The bucket series is exempt: it is an overlay the reader asked
	// for, spread from the meter's last movement, and a short window of it is
	// rough rather than wrong. The factor behind it is fitted on 28 days either way.
	// The chart's second scope, so one request answers both the sentence and the
	// overlay. Ignored unless both bounds parse and make a range.
	if hs, hu := lenientQueryTime(q.Get("headline_since")), lenientQueryTime(q.Get("headline_until")); hs != nil && hu != nil && hu.After(*hs) {
		f.HeadlineSince, f.HeadlineUntil = *hs, *hu
	}
	// The percentage is an integer and the weekly meter moves about a point an
	// hour at full tilt; over a few hours the range total is quantization, not
	// signal. The bucket series is exempt: it is an overlay the reader asked
	// for, spread from the meter's last movement, and a short window of it is
	// rough rather than wrong. The factor behind it is fitted on 28 days either way.
	//
	// Applied to whichever scope the SENTENCE is on. With one request carrying
	// both, "3-hour buckets plus a 7-day headline" is an ordinary ask; judging it
	// by the bucket range would refuse it and turn the overlay off on the default
	// view, silently.
	headlineSpan := until.Sub(*since)
	if !f.HeadlineSince.IsZero() {
		headlineSpan = f.HeadlineUntil.Sub(f.HeadlineSince)
	}
	if f.Granularity == "" && headlineSpan < store.CoverageMinRange {
		ineligible("coverage needs a range of at least a day")
		return
	}
	gap, err := s.store.CoverageGapStats(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, gap)
}
