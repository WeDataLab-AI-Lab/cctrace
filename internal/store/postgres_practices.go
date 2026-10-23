package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Practices compares people who show a practice in the window with people who do
// not. It names practices and never people.
type Practices struct {
	WindowStart time.Time     `json:"window_start"`
	WindowEnd   time.Time     `json:"window_end"`
	Rows        []PracticeRow `json:"rows"`
	Self        PracticeRates `json:"self"`
	Caveats     []string      `json:"caveats"`
}

// PracticeRow carries UsersWithout and Metrics only when both groups have
// practiceMinimumContributors people with a decision in the window.
type PracticeRow struct {
	Kind         string           `json:"kind"`
	Name         string           `json:"name"`
	UsersWith    int64            `json:"users_with"`
	UsersWithout *int64           `json:"users_without,omitempty"`
	Metrics      *PracticeMetrics `json:"metrics,omitempty"`
}

type PracticeMetrics struct {
	ManualApprovalsPer1000 PracticeComparison `json:"manual_approvals_per_1000"`
	HumanRejectionsPer1000 PracticeComparison `json:"human_rejections_per_1000"`
}

// PracticeComparison holds the median of per-person rates in each group.
type PracticeComparison struct {
	With    float64 `json:"with"`
	Without float64 `json:"without"`
}

// PracticeRates is one person's own rates; nil when they made no decision.
type PracticeRates struct {
	ManualApprovalsPer1000 *float64 `json:"manual_approvals_per_1000"`
	HumanRejectionsPer1000 *float64 `json:"human_rejections_per_1000"`
}

const practiceMinimumContributors = 3

var practiceCaveats = []string{
	"A practice is one or more plugin_loaded, skill_activated or hook_registered events in the window; no event can mean the practice was not observed rather than not used.",
	"Metrics are medians of per-person rates over Claude tool_decision events, shown only when both groups have at least 3 people with a decision in the window.",
	"Differences are correlation, not the effect of a practice.",
	"No person is identified in this response, but it is not an anonymity guarantee.",
}

// practiceDecisionRatesCTE is one row per person: their Claude tool decisions in
// [$1, $2) reduced to two rates. Reducing to one row per person before anything
// joins to it keeps a person's repeated membership events from counting their
// decisions again, and keeps a heavy user from setting a group's value.
const practiceDecisionRatesCTE = `
decision_rates AS (
  SELECT user_id,
    1000 * (COUNT(*) FILTER (WHERE attrs->>'source' IN ('user_temporary', 'user_permanent')))::double precision / COUNT(*) AS manual_approvals,
    1000 * (COUNT(*) FILTER (WHERE attrs->>'source' IN ('user_reject', 'user_abort')))::double precision / COUNT(*) AS human_rejections
  FROM visible_events
  WHERE ts >= $1 AND ts < $2
    AND event_name = 'tool_decision' AND agent = 'claude' AND user_id <> ''
  GROUP BY user_id
)`

// practicesQuery scans visible_events once per CTE, each bounded by the window,
// and joins per-person rows only, never events. Membership is tested with
// = ANY over each practice's member array rather than a join back to the
// membership CTE: CTE row estimates come out as 1 here (EXPLAIN on prod,
// 2026-10-21), which turns that join into a nested loop over every
// (practice, person, membership) triple.
const practicesQuery = `
WITH ` + practiceDecisionRatesCTE + `,
membership AS (
  SELECT DISTINCT kind, name, user_id
  FROM (
    SELECT user_id,
      CASE event_name WHEN 'plugin_loaded' THEN 'plugin' WHEN 'skill_activated' THEN 'skill' ELSE 'hook' END AS kind,
      CASE event_name
        WHEN 'plugin_loaded' THEN attrs->>'plugin.name'
        WHEN 'skill_activated' THEN attrs->>'skill.name'
        ELSE concat_ws('/', attrs->>'hook_event', attrs->>'hook_source', attrs->>'hook_type', NULLIF(attrs->>'plugin.name', ''))
      END AS name
    FROM visible_events
    WHERE ts >= $1 AND ts < $2
      AND event_name IN ('plugin_loaded', 'skill_activated', 'hook_registered')
      AND user_id <> ''
  ) observed
  WHERE name <> ''
),
practices AS (
  SELECT kind, name, array_agg(user_id) AS members FROM membership GROUP BY kind, name
),
people AS (
  SELECT u.user_id, d.manual_approvals, d.human_rejections
  FROM (SELECT user_id FROM decision_rates UNION SELECT user_id FROM membership) u
  LEFT JOIN decision_rates d ON d.user_id = u.user_id
),
grid AS (
  SELECT p.kind, p.name, u.user_id = ANY (p.members) AS uses, u.manual_approvals, u.human_rejections
  FROM practices p CROSS JOIN people u
)
SELECT kind, name,
  COUNT(*) FILTER (WHERE uses) AS users_with,
  COUNT(*) FILTER (WHERE NOT uses) AS users_without,
  COUNT(manual_approvals) FILTER (WHERE uses) AS contributors_with,
  COUNT(manual_approvals) FILTER (WHERE NOT uses) AS contributors_without,
  COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY manual_approvals) FILTER (WHERE uses), 0),
  COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY manual_approvals) FILTER (WHERE NOT uses), 0),
  COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY human_rejections) FILTER (WHERE uses), 0),
  COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY human_rejections) FILTER (WHERE NOT uses), 0)
FROM grid
GROUP BY kind, name
ORDER BY kind, users_with DESC, name`

// Practices reads every person's practice membership and decision rates in
// [since, until) from visible_events, so account exclusions apply. People are
// keyed by the event's user_id; events without one are left out. Self is
// selfUserID's own rates.
func (s *PgStore) Practices(ctx context.Context, since, until time.Time, selfUserID string) (*Practices, error) {
	result := &Practices{WindowStart: since, WindowEnd: until, Rows: []PracticeRow{}, Caveats: practiceCaveats}
	rows, err := s.pool.Query(ctx, practicesQuery, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			row                         PracticeRow
			usersWithout, with, without int64
			// The SQL turns a group with no contributor's NULL median into 0;
			// the threshold below never lets such a group through.
			manual, rejected PracticeComparison
		)
		if err := rows.Scan(&row.Kind, &row.Name, &row.UsersWith, &usersWithout, &with, &without,
			&manual.With, &manual.Without, &rejected.With, &rejected.Without); err != nil {
			return nil, err
		}
		if with >= practiceMinimumContributors && without >= practiceMinimumContributors {
			row.UsersWithout = &usersWithout
			row.Metrics = &PracticeMetrics{ManualApprovalsPer1000: manual, HumanRejectionsPer1000: rejected}
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	err = s.pool.QueryRow(ctx, `WITH `+practiceDecisionRatesCTE+`
SELECT manual_approvals, human_rejections FROM decision_rates WHERE user_id = $3`,
		since, until, selfUserID).Scan(&result.Self.ManualApprovalsPer1000, &result.Self.HumanRejectionsPer1000)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return result, nil
}
