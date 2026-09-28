package openinsights

import (
	"sort"
	"time"
)

const (
	// rebuildMinContextTokens ignores small requests whose cache writes cost
	// little even when the cache missed.
	rebuildMinContextTokens = 20_000
	// rebuildMinGap is the prompt cache lifetime. A cache write after a shorter
	// gap is a parallel subagent or a new prefix, not a pause that let the
	// cache expire.
	rebuildMinGap = 5 * time.Minute
	// bloatedContextTokens marks a request carrying a context large enough that
	// splitting the session or clearing it is worth considering.
	bloatedContextTokens = 100_000
	maxModels            = 5
)

// ContextTotals is token and cost volume over a set of model requests.
type ContextTotals struct {
	Requests          int      `json:"requests"`
	InputTokens       int64    `json:"input_tokens"`
	CacheReadTokens   int64    `json:"cache_read_tokens"`
	CacheCreateTokens int64    `json:"cache_create_tokens"`
	ContextTokens     int64    `json:"context_tokens"`
	HitRate           *float64 `json:"hit_rate"`
	CostUSD           float64  `json:"cost_usd"`
}

// ContextModel is ContextTotals for one model.
type ContextModel struct {
	Model string `json:"model"`
	ContextTotals
}

// ContextRebuilds counts requests that rewrote more cache than they read after
// the session had been idle at least MinGapSeconds, so the cache had expired.
type ContextRebuilds struct {
	ThresholdTokens  int      `json:"threshold_tokens"`
	MinGapSeconds    int      `json:"min_gap_seconds"`
	Count            int      `json:"count"`
	Tokens           int64    `json:"tokens"`
	MedianGapSeconds *float64 `json:"median_gap_seconds"`
}

// ContextBloated measures requests at or above the bloated-context threshold.
type ContextBloated struct {
	ThresholdTokens int      `json:"threshold_tokens"`
	Requests        int      `json:"requests"`
	CostUSD         float64  `json:"cost_usd"`
	CostShare       *float64 `json:"cost_share"`
}

// ContextSession is a session with bloated-context requests.
type ContextSession struct {
	SessionID        string    `json:"session_id"`
	FirstTs          time.Time `json:"first_ts"`
	LastTs           time.Time `json:"last_ts"`
	Requests         int       `json:"requests"`
	BloatedRequests  int       `json:"bloated_requests"`
	MaxContextTokens int64     `json:"max_context_tokens"`
	CostUSD          float64   `json:"cost_usd"`
}

// ContextResult answers "am I wasting context".
type ContextResult struct {
	Kind            string           `json:"kind"`
	Window          Window           `json:"window"`
	Project         string           `json:"project,omitempty"`
	Sessions        int              `json:"sessions"`
	Overall         ContextTotals    `json:"overall"`
	ByModel         []ContextModel   `json:"by_model"`
	Rebuilds        ContextRebuilds  `json:"rebuilds"`
	Bloated         ContextBloated   `json:"bloated"`
	BloatedSessions []ContextSession `json:"bloated_sessions"`
	Truncated       bool             `json:"truncated"`
	CoveredSince    *time.Time       `json:"covered_since,omitempty"`
	Caveats         []Caveat         `json:"caveats"`
}

type tokenRow struct {
	Event
	input, read, create, context int64
	cost                         float64
}

// AggregateContext summarises cache efficiency, cache rebuilds, and bloated
// contexts. A row counts as a model request when any token field is present,
// which covers every agent's usage event name.
func AggregateContext(w Window, events []Event, topN int, scope Scope) ContextResult {
	rows := tokenRows(events)
	withhold, withheldBecause := withholdDetail(scope, rows, func(r tokenRow) string { return r.UserID })
	if withhold {
		topN = 0
	}
	res := ContextResult{
		Kind: "context", Window: w,
		Rebuilds: ContextRebuilds{ThresholdTokens: rebuildMinContextTokens, MinGapSeconds: int(rebuildMinGap / time.Second)},
		Bloated:  ContextBloated{ThresholdTokens: bloatedContextTokens},
		ByModel:  []ContextModel{}, BloatedSessions: []ContextSession{},
	}

	models := map[string]*ContextTotals{}
	sessions := map[string]*ContextSession{}
	lastTs := map[string]time.Time{}
	var gaps []float64
	for _, r := range rows {
		add(&res.Overall, r)
		model := r.Model
		if model == "" {
			model = unknownKey
		}
		if models[model] == nil {
			models[model] = &ContextTotals{}
		}
		add(models[model], r)

		prev, seen := lastTs[r.SessionID]
		if seen && r.Ts.Sub(prev) >= rebuildMinGap && r.create > r.read && r.context >= rebuildMinContextTokens {
			res.Rebuilds.Count++
			res.Rebuilds.Tokens += r.create
			gaps = append(gaps, r.Ts.Sub(prev).Seconds())
		}
		lastTs[r.SessionID] = r.Ts

		s := sessions[r.SessionID]
		if s == nil {
			s = &ContextSession{SessionID: r.SessionID, FirstTs: r.Ts}
			sessions[r.SessionID] = s
		}
		s.LastTs = r.Ts
		s.Requests++
		s.CostUSD += r.cost
		if r.context > s.MaxContextTokens {
			s.MaxContextTokens = r.context
		}
		if r.context >= bloatedContextTokens {
			s.BloatedRequests++
			res.Bloated.Requests++
			res.Bloated.CostUSD += r.cost
		}
	}

	res.Sessions = len(sessions)
	res.Rebuilds.MedianGapSeconds = median(gaps)
	res.Bloated.CostShare = ratio(res.Bloated.CostUSD, res.Overall.CostUSD)
	res.Bloated.CostUSD = round4(res.Bloated.CostUSD)
	finish(&res.Overall)

	for name, m := range models {
		finish(m)
		res.ByModel = append(res.ByModel, ContextModel{Model: name, ContextTotals: *m})
	}
	sort.Slice(res.ByModel, func(i, j int) bool {
		if res.ByModel[i].ContextTokens != res.ByModel[j].ContextTokens {
			return res.ByModel[i].ContextTokens > res.ByModel[j].ContextTokens
		}
		return res.ByModel[i].Model < res.ByModel[j].Model
	})
	if len(res.ByModel) > maxModels {
		res.ByModel = res.ByModel[:maxModels]
	}

	for _, s := range sessions {
		if s.BloatedRequests > 0 {
			s.CostUSD = round4(s.CostUSD)
			res.BloatedSessions = append(res.BloatedSessions, *s)
		}
	}
	sort.Slice(res.BloatedSessions, func(i, j int) bool {
		a, b := res.BloatedSessions[i], res.BloatedSessions[j]
		if a.BloatedRequests != b.BloatedRequests {
			return a.BloatedRequests > b.BloatedRequests
		}
		if a.MaxContextTokens != b.MaxContextTokens {
			return a.MaxContextTokens > b.MaxContextTokens
		}
		return a.SessionID < b.SessionID
	})
	if len(res.BloatedSessions) > max(topN, 0) {
		res.BloatedSessions = res.BloatedSessions[:max(topN, 0)]
	}

	res.Caveats = contextCaveats(res, rows)
	if withheldBecause != nil {
		res.Caveats = append(res.Caveats, *withheldBecause)
	}
	return res
}

func tokenRows(events []Event) []tokenRow {
	var rows []tokenRow
	for _, e := range events {
		if e.InputTokens == nil && e.CacheReadTokens == nil && e.CacheCreateTokens == nil {
			continue
		}
		e.Ts = e.Ts.UTC()
		r := tokenRow{Event: e, input: deref(e.InputTokens), read: deref(e.CacheReadTokens), create: deref(e.CacheCreateTokens)}
		r.context = r.input + r.read + r.create
		if e.CostUSD != nil {
			r.cost = *e.CostUSD
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Ts.Before(rows[j].Ts) })
	return rows
}

func deref(p *int) int64 {
	if p == nil {
		return 0
	}
	return int64(*p)
}

func add(t *ContextTotals, r tokenRow) {
	t.Requests++
	t.InputTokens += r.input
	t.CacheReadTokens += r.read
	t.CacheCreateTokens += r.create
	t.ContextTokens += r.context
	t.CostUSD += r.cost
}

func finish(t *ContextTotals) {
	t.HitRate = ratio(float64(t.CacheReadTokens), float64(t.ContextTokens))
	t.CostUSD = round4(t.CostUSD)
}

func median(vs []float64) *float64 {
	if len(vs) == 0 {
		return nil
	}
	sort.Float64s(vs)
	m := vs[len(vs)/2]
	if len(vs)%2 == 0 {
		m = (vs[len(vs)/2-1] + m) / 2
	}
	m = round4(m)
	return &m
}

func contextCaveats(res ContextResult, rows []tokenRow) []Caveat {
	cs := []Caveat{}
	if len(rows) == 0 {
		cs = append(cs, Caveat{"window_empty", "no model requests with token counts in the window"})
	}
	if res.Sessions < lowVolumeSessions {
		cs = append(cs, Caveat{"low_volume", "fewer than 5 sessions in the window"})
	}
	return cs
}
