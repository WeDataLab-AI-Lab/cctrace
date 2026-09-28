// Package activitylabel derives a work segment's activity_type from tool-call
// evidence -- never prompt text -- so it works regardless of language, needs no
// text corpus, and stays legible for a lone one-character acknowledgment turn.
//
// Scope note: the design this implements (swirling-tickling-unicorn plan,
// Stage 5) specifies three additional signals this package does not compute --
// ship_signal (a Bash command matching git commit/git push/gh pr create),
// test_signal's file-pattern half (a touched *_test.*/*.spec.* file), and
// doc_signal (a touched *.md file or docs/ path). Checked on dev (2026-08-24):
// otel_events.attrs for tool_result events carries tool_input_size_bytes and
// tool_result_size_bytes (byte counts) but never the command text or file path
// -- Claude Code's OTEL exporter does not emit those as event attributes.
// Deriving them would mean parsing tool_use blocks out of session_records.raw
// instead, a separate and larger change (new raw-JSON parsing, cross-agent
// shape handling) that deserves its own review. The four signals below are the
// ones Stage 3's clustering actually validated against real data (see
// Classify's doc comment), so diagnose/ops/author/explore are still on solid
// ground; a session that would have been "ship" lands in ops or author
// instead. That degradation has empirical cover too -- Stage 3's clustering
// found four clusters in the real sample, not five, so "ship" was never an
// observed cluster to begin with, only a reasoned addition on top.
package activitylabel

// Version tags every row this package labels, so a future rule-set change can
// tell which rows still reflect an older version.
const Version = "behavior-v1"

// Signals summarizes one segment's tool-call evidence within its
// [start_ts, end_ts) window. Every field comes from tool_result events
// (visible_events); never from prompt or command text.
type Signals struct {
	WriteCalls  int  // tool_name IN (Edit, Write, NotebookEdit)
	ReadCalls   int  // tool_name IN (Read, Grep, Glob)
	BashCalls   int  // tool_name = Bash
	TotalCalls  int  // every tool_result in the window (denominator for the bash ratio)
	FailPresent bool // any tool_result in the window has tool_success = false
}

// Classify returns the segment's activity_type, first-match-wins. A segment
// that matches nothing -- no tool calls at all, or a mix that clears no rule --
// returns "unknown": the safe degradation, not a guess.
//
// Source: swirling-tickling-unicorn plan, Stage 3 cluster centroids (dev,
// 30-day sample, dated 2026-08-24):
//
//	cluster    write  read  bash  fail   -> label
//	0 (43.3%)  0.16   0.15  0.69  100%   -> diagnose (fail is decisive; bash
//	                                        ratio overlaps ops but fail splits it)
//	1 (10.7%)  0.34   0.37  0.21  0%     -> author (balanced write/read)
//	2 (19.2%)  0.20   0.19  0.61  0%     -> author (ops threshold not met)
//	3 (26.8%)  0.02   0.03  0.95  0%     -> ops (near-pure shell)
//
// The ops threshold (bash ratio > 0.8) sits between clusters 2 (0.61) and 3
// (0.95), splitting the two. The sample is 30 days of dev data; widen the
// window before moving this threshold.
func Classify(s Signals) string {
	switch {
	case s.FailPresent:
		return "diagnose"
	case s.WriteCalls == 0 && s.TotalCalls > 0 && float64(s.BashCalls)/float64(s.TotalCalls) > 0.8:
		return "ops"
	case s.WriteCalls > 0:
		return "author"
	case s.ReadCalls > 0:
		return "explore"
	default:
		return "unknown"
	}
}
