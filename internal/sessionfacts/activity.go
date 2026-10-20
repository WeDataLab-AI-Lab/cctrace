package sessionfacts

import (
	"sort"
	"time"
)

// segmentGap is the silence that ends an activity segment.
const segmentGap = 15 * time.Minute

// span widens to take in every instant it is shown.
type span struct{ first, last *time.Time }

func (s *span) add(t time.Time) {
	// One instant has one value here whatever zone its row came in, so that of
	// two rows of the same instant it does not matter which is seen first.
	t = t.UTC()
	if s.first == nil || t.Before(*s.first) {
		s.first = &t
	}
	if s.last == nil || t.After(*s.last) {
		s.last = &t
	}
}

// claudeActivity sets the session's activity range and first record, and
// returns its activity segments (p1b-design §3.17, §3.19).
//
// The range covers every JSONL line with a timestamp of its own, subagents
// included, and the api_request events. A segment is main-thread lines only,
// abandoned branches included, with no gap of 15 minutes between neighbours:
// what a person at the session would call one sitting. Both leave out the
// copies of another session's lines and the away_summary line, which Claude
// Code writes on its own after the person has been away.
//
// first_record_at is for the retention reconcile alone and so counts copies
// too: it has to see the oldest row the facts were read from.
func (c *computation) claudeActivity() []Segment {
	var activity, records span
	var main []*line
	for _, l := range c.lines {
		c.pulse()
		if !l.dated() {
			continue
		}
		records.add(l.TS)
		if l.copied || (l.RecordType == "system" && l.Subtype == "away_summary") {
			continue
		}
		activity.add(l.TS)
		if !l.IsSidechain && !l.digested() {
			main = append(main, l)
		}
	}
	for i := range c.in.OTEL {
		c.pulse()
		activity.add(c.in.OTEL[i].TS)
	}
	s := &c.session
	s.FirstActivityAt, s.LastActivityAt, s.FirstRecordAt = activity.first, activity.last, records.first

	// c.lines is in ts order, so main is too.
	var segments []Segment
	var length time.Duration
	for _, l := range main {
		c.pulse()
		if n := len(segments); n == 0 || l.TS.Sub(segments[n-1].SegEnd) >= segmentGap {
			segments = append(segments, Segment{SegStart: l.TS, SegEnd: l.TS})
		}
		seg := &segments[len(segments)-1]
		length += l.TS.Sub(seg.SegEnd)
		seg.SegEnd = l.TS
		seg.Records++
		if l.human {
			seg.HumanInputs++
		}
	}
	s.ActiveSeconds = int(length / time.Second)
	return segments
}

// codexActivity sets a Codex session's activity range and first record. Copy
// marks sit on usage rows only, so the copied part of a Codex session is taken
// to be its prefix: every row up to the last copied usage row (p1b-design
// §3.16).
func (c *computation) codexPrefixEnd() time.Time {
	var prefixEnd time.Time
	for _, row := range c.in.CodexRows {
		c.pulse()
		if c.copied(row.ID) && row.TS.After(prefixEnd) {
			prefixEnd = row.TS
		}
	}
	return prefixEnd
}

func (c *computation) codexActivity() {
	rows := c.in.CodexRows
	prefixEnd := c.codexPrefixEnd()
	var activity, records span
	for i := range rows {
		c.pulse()
		if ts := rows[i].TS; ts.After(epoch) {
			records.add(ts)
			if ts.After(prefixEnd) {
				activity.add(ts)
			}
		}
	}
	for i := range c.in.OTEL {
		c.pulse()
		activity.add(c.in.OTEL[i].TS)
	}
	s := &c.session
	s.FirstActivityAt, s.LastActivityAt, s.FirstRecordAt = activity.first, activity.last, records.first
}

// sortedSet is the distinct values of a list, sorted.
func sortedSet(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for i, v := range values {
		if i == 0 || v != values[i-1] {
			out = append(out, v)
		}
	}
	return out
}
