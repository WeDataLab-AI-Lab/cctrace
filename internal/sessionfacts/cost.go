package sessionfacts

// priceSession sets the session's cost and each turn's (p1b-design §4). The
// session's is the list's: the sum of its api_request events when it has
// telemetry, and the sum of its imputed rows when it has none. The facts read
// those rows and never run the estimator themselves.
//
// A Codex session is always estimated: its cost is its codex_imputed_cost rows
// whatever telemetry it sent.
func (c *computation) priceSession() {
	s := &c.session
	detail := &s.Detail.Cost
	if c.in.HasOTEL && c.in.Agent != "codex" {
		total, attached, unattached := c.attachEvents()
		s.CostUSD = &total
		// Telemetry older than the oldest event still stored may be gone, so a
		// session that began before it shows what is left of its cost, not all
		// of it.
		s.CostSource = "otel"
		if s.FirstActivityAt != nil && s.FirstActivityAt.Before(c.in.OTELOldest) {
			s.CostSource = "otel_partial"
		}
		detail.TurnAttachedUSD, detail.UnattachedTurnUSD = attached, unattached
		for _, t := range c.turns {
			c.pulse()
			t.out.CostUSD, t.out.CostSource, t.out.CostReason = c.measured(t)
		}
		return
	}

	s.CostUSD, s.CostSource = estimated(&c.total)
	if s.CostSource == "no_rate" {
		// The session has no state for requests none of which can be priced:
		// it has no imputed row, which is none.
		s.CostUSD, s.CostSource = nil, "none"
	}
	detail.UnattachedTurnUSD = c.loose.imputedUSD
	for _, t := range c.turns {
		c.pulse()
		t.out.CostUSD, t.out.CostSource = estimated(&t.cost)
		detail.TurnAttachedUSD += t.cost.imputedUSD
	}
	// The session had telemetry once: the server filled a login from it on
	// these rows, and every event has since expired.
	for _, l := range c.lines {
		c.pulse()
		if l.LoginEmailSource == "otel" {
			detail.Note = "otel_expired"
			break
		}
	}
}

// measured is a turn's cost in a session with telemetry (p1b-design §4.4).
//
// Turns split by rule 1-b share a prompt id, so its events cannot be tied to
// either turn even when neither has a request head. They are in the session's
// unattached sum. An ordinary turn that called no API -- no request and no
// event -- has no cost to place: a /clear is none wherever it sits. A pre-P0
// turn with a request has no prompt id to place its cost and no estimate is put
// in its place. Otherwise the cost is
// the events that carry the turn's prompt id -- all of them if the turn began
// at or after the oldest event still stored, what is left of them if it began
// before, and nothing at all once they have all expired. Requests whose
// telemetry has not arrived, with nothing expired, leave the turn at none.
func (c *computation) measured(t *turn) (cost *float64, source, reason string) {
	before := t.out.StartedAt.Before(c.in.OTELOldest)
	switch {
	case t.out.PromptIDShared:
		return nil, "unattributable", "shared_prompt_id"
	case t.cost.requests == 0 && t.cost.events == 0:
		return nil, "none", ""
	case t.promptID == "":
		return nil, "unattributable", "pre_p0"
	case t.cost.events == 0 && before:
		return nil, "expired", ""
	case t.cost.events == 0:
		return nil, "none", ""
	case before:
		return &t.cost.otelUSD, "otel_partial", ""
	}
	return &t.cost.otelUSD, "otel", ""
}

// estimated is the cost of a turn's requests, or a session's, from their
// imputed rows (p1b-design §4.4). The state follows the requests that have no
// row: one that has a rate is waiting for the 30-minute rewrite, so the sum so
// far is not final; one without a rate never gets a row, and when no request
// has one there is no cost to show.
func estimated(s *costSum) (cost *float64, source string) {
	switch {
	case s.requests == 0:
		return nil, "none"
	case s.waiting > 0:
		return &s.imputedUSD, "estimate_pending"
	case s.priced == 0:
		return nil, "no_rate"
	}
	return &s.imputedUSD, "estimate"
}
