package openinsights

import (
	"math"
	"sort"
	"time"
)

// CostWindow summarises one side of a cost comparison.
type CostWindow struct {
	Window
	Sessions       int      `json:"sessions"`
	CostUSD        float64  `json:"cost_usd"`
	CostPerSession *float64 `json:"cost_per_session"`
}

// CostDelta is one model or project's cost in both windows.
type CostDelta struct {
	Axis        string  `json:"axis"`
	Key         string  `json:"key"`
	PreviousUSD float64 `json:"previous_usd"`
	CurrentUSD  float64 `json:"current_usd"`
	DeltaUSD    float64 `json:"delta_usd"`
}

// CostSession is a costly session in the current window.
type CostSession struct {
	SessionID string    `json:"session_id"`
	StartTime time.Time `json:"start_time"`
	Model     string    `json:"model"`
	CostUSD   float64   `json:"cost_usd"`
}

// CostResult answers "why did my cost change between two windows".
type CostResult struct {
	Kind               string        `json:"kind"`
	Previous           CostWindow    `json:"previous"`
	Current            CostWindow    `json:"current"`
	DeltaCostUSD       float64       `json:"delta_cost_usd"`
	VolumeEffectUSD    float64       `json:"volume_effect_usd"`
	IntensityEffectUSD float64       `json:"intensity_effect_usd"`
	DominantEffect     string        `json:"dominant_effect"`
	TopDriver          *CostDelta    `json:"top_driver"`
	ByModel            []CostDelta   `json:"by_model"`
	ByProject          []CostDelta   `json:"by_project,omitempty"`
	TopSessions        []CostSession `json:"top_sessions"`
	Truncated          bool          `json:"truncated"`
	Caveats            []Caveat      `json:"caveats"`
}

const maxCostDeltas = 5

// ModelCost is one row of GET /api/open/v1/usage?group_by=model.
type ModelCost struct {
	Model   string  `json:"key"`
	CostUSD float64 `json:"cost_usd"`
}

// AggregateCost compares the current window against the previous one.
//
// The total change splits exactly into a volume effect (more or fewer sessions
// at the previous cost per session) and an intensity effect (every current
// session costing more or less than before). Model deltas come from
// per-request model costs because a session's model field names only one of
// the models it used. The project breakdown uses each session's project name
// and is omitted when no session carries one. Session and project detail is
// withheld as withholdDetail decides for scope.
func AggregateCost(prevWin, curWin Window, prev, cur []Session, prevModels, curModels []ModelCost, topN int, scope Scope) CostResult {
	res := CostResult{
		Kind: "cost", Previous: costWindow(prevWin, prev), Current: costWindow(curWin, cur),
		TopSessions: []CostSession{},
	}

	res.DeltaCostUSD = round4(res.Current.CostUSD - res.Previous.CostUSD)
	var prevRate float64
	if res.Previous.Sessions > 0 {
		prevRate = res.Previous.CostUSD / float64(res.Previous.Sessions)
	}
	res.VolumeEffectUSD = round4(float64(res.Current.Sessions-res.Previous.Sessions) * prevRate)
	// Derived from the rounded delta so the two effects always add up to it.
	res.IntensityEffectUSD = round4(res.DeltaCostUSD - res.VolumeEffectUSD)
	switch {
	case res.Previous.Sessions == 0 || (res.VolumeEffectUSD == 0 && res.IntensityEffectUSD == 0):
		// Without a baseline every dollar would read as intensity.
		res.DominantEffect = "undetermined"
	case math.Abs(res.IntensityEffectUSD) > math.Abs(res.VolumeEffectUSD):
		res.DominantEffect = "intensity"
	default:
		res.DominantEffect = "volume"
	}

	withhold, withheldBecause := withholdDetail(scope, append(append([]Session{}, prev...), cur...), func(s Session) string { return s.UserID })
	if withhold {
		topN = 0
	}

	res.ByModel = rankDeltas(modelDeltas(prevModels, curModels))
	if !withhold && anyProjectName(prev, cur) {
		res.ByProject = rankDeltas(projectDeltas(prev, cur))
	}
	for _, d := range append(append([]CostDelta{}, res.ByModel...), res.ByProject...) {
		if d.Key == unknownKey || d.DeltaUSD == 0 {
			continue
		}
		if res.TopDriver == nil || math.Abs(d.DeltaUSD) > math.Abs(res.TopDriver.DeltaUSD) {
			d := d
			res.TopDriver = &d
		}
	}

	sorted := append([]Session{}, cur...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CostUSD > sorted[j].CostUSD })
	for i := 0; i < len(sorted) && i < topN; i++ {
		s := sorted[i]
		res.TopSessions = append(res.TopSessions, CostSession{s.SessionID, s.StartTime.UTC(), s.Model, round4(s.CostUSD)})
	}

	res.Caveats = costCaveats(res, prev, cur, prevModels, curModels)
	if withheldBecause != nil {
		res.Caveats = append(res.Caveats, *withheldBecause)
	} else if res.Current.Sessions+res.Previous.Sessions > 0 && res.ByProject == nil {
		res.Caveats = append(res.Caveats, Caveat{"project_unavailable", "no session carries a project name (sessions without synced records, or a server older than this client), so cost is not broken down by project"})
	}
	return res
}

func costWindow(w Window, rows []Session) CostWindow {
	cw := CostWindow{Window: w, Sessions: len(rows)}
	for _, s := range rows {
		cw.CostUSD += s.CostUSD
	}
	cw.CostPerSession = ratio(cw.CostUSD, float64(cw.Sessions))
	cw.CostUSD = round4(cw.CostUSD)
	return cw
}

func modelDeltas(prev, cur []ModelCost) map[string]*CostDelta {
	byKey := map[string]*CostDelta{}
	for i, rows := range [][]ModelCost{prev, cur} {
		for _, m := range rows {
			d := deltaFor(byKey, "model", m.Model)
			if i == 0 {
				d.PreviousUSD += m.CostUSD
			} else {
				d.CurrentUSD += m.CostUSD
			}
		}
	}
	return byKey
}

func anyProjectName(windows ...[]Session) bool {
	for _, rows := range windows {
		for _, s := range rows {
			if s.ProjectName != "" {
				return true
			}
		}
	}
	return false
}

func projectDeltas(prev, cur []Session) map[string]*CostDelta {
	byKey := map[string]*CostDelta{}
	for i, rows := range [][]Session{prev, cur} {
		for _, s := range rows {
			d := deltaFor(byKey, "project", s.ProjectName)
			if i == 0 {
				d.PreviousUSD += s.CostUSD
			} else {
				d.CurrentUSD += s.CostUSD
			}
		}
	}
	return byKey
}

func deltaFor(byKey map[string]*CostDelta, axis, key string) *CostDelta {
	if key == "" {
		key = unknownKey
	}
	d := byKey[key]
	if d == nil {
		d = &CostDelta{Axis: axis, Key: key}
		byKey[key] = d
	}
	return d
}

func rankDeltas(byKey map[string]*CostDelta) []CostDelta {
	out := make([]CostDelta, 0, len(byKey))
	for _, d := range byKey {
		d.PreviousUSD, d.CurrentUSD = round4(d.PreviousUSD), round4(d.CurrentUSD)
		d.DeltaUSD = round4(d.CurrentUSD - d.PreviousUSD)
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		if math.Abs(out[i].DeltaUSD) != math.Abs(out[j].DeltaUSD) {
			return math.Abs(out[i].DeltaUSD) > math.Abs(out[j].DeltaUSD)
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > maxCostDeltas {
		out = out[:maxCostDeltas]
	}
	return out
}

func costCaveats(res CostResult, prev, cur []Session, prevModels, curModels []ModelCost) []Caveat {
	cs := []Caveat{{"observational_only", "differences between windows are correlations, not causes"}}
	if res.Previous.Sessions == 0 {
		cs = append(cs, Caveat{"window_empty:previous", "the previous window has no sessions to compare against"})
	}
	if res.Current.Sessions == 0 {
		cs = append(cs, Caveat{"window_empty:current", "the current window has no sessions"})
	}
	if res.Previous.Sessions < lowVolumeSessions || res.Current.Sessions < lowVolumeSessions {
		cs = append(cs, Caveat{"low_volume", "a window has fewer than 5 sessions"})
	}
	if len(prevModels) > 0 || len(curModels) > 0 {
		if modelTotalsDiffer(prev, prevModels) || modelTotalsDiffer(cur, curModels) {
			cs = append(cs, Caveat{"model_totals_differ", "by_model is aggregated per request and includes cost the session totals exclude (deleted or session-less rows, or rows past a truncated download); compare models with each other, not with delta_cost_usd"})
		}
	}
	prevIDs := map[string]bool{}
	for _, s := range prev {
		prevIDs[s.SessionID] = true
	}
	for _, s := range cur {
		if prevIDs[s.SessionID] {
			cs = append(cs, Caveat{"boundary_sessions", "some sessions span both windows and are counted in each, which inflates volume_effect_usd"})
			break
		}
	}
	return cs
}

// modelTotalsDiffer reports whether per-request model cost and the session list
// disagree by more than a cent or 1%, whichever is larger.
func modelTotalsDiffer(sessions []Session, models []ModelCost) bool {
	var sessionTotal, modelTotal float64
	for _, s := range sessions {
		sessionTotal += s.CostUSD
	}
	for _, m := range models {
		modelTotal += m.CostUSD
	}
	return math.Abs(sessionTotal-modelTotal) > math.Max(0.01, 0.01*sessionTotal)
}
