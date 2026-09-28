// Package omolog parses omo (senpi) session JSONL logs for session identity,
// thread structure, and utterances.
//
// It deliberately excludes usage/token/cost data. See the Record doc comment
// and docs/design/design-gjc-omo-usage-monitoring.md section 7.2d for why.
package omolog
