package openinsights

import (
	"math"
	"testing"
	"time"
)

var (
	t0 = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	t1 = t0.Add(7 * 24 * time.Hour)
	t2 = t1.Add(7 * 24 * time.Hour)
)

func sess(id, model string, cost float64) Session {
	return Session{SessionID: id, UserID: "u1", Model: model, StartTime: t1, EndTime: t1.Add(time.Hour), CostUSD: cost}
}

func inProject(s Session, name string) Session {
	s.ProjectName = name
	return s
}

func hasCaveat(cs []Caveat, code string) bool {
	for _, c := range cs {
		if c.Code == code {
			return true
		}
	}
	return false
}

func TestAggregateCostSplitsDeltaIntoVolumeAndIntensity(t *testing.T) {
	prev := []Session{sess("a", "opus", 2), sess("b", "opus", 2)}
	cur := []Session{sess("c", "opus", 4), sess("d", "opus", 4), sess("e", "opus", 4)}

	got := AggregateCost(Window{t0, t1}, Window{t1, t2}, prev, cur, nil, nil, 5, ScopeUser)

	if got.DeltaCostUSD != 8 {
		t.Fatalf("delta = %v, want 8", got.DeltaCostUSD)
	}
	// one more session at the old $2 rate, then three sessions each $2 dearer
	if got.VolumeEffectUSD != 2 || got.IntensityEffectUSD != 6 {
		t.Fatalf("effects = %v/%v, want 2/6", got.VolumeEffectUSD, got.IntensityEffectUSD)
	}
	if got.DominantEffect != "intensity" {
		t.Fatalf("dominant effect = %q", got.DominantEffect)
	}
	if math.Abs(got.VolumeEffectUSD+got.IntensityEffectUSD-got.DeltaCostUSD) > 1e-9 {
		t.Fatal("effects do not sum to delta")
	}
}

func TestAggregateCostRanksModelsAndProjectsByAbsoluteDelta(t *testing.T) {
	prev := []Session{inProject(sess("a", "", 5), "web"), sess("b", "", 1)}
	cur := []Session{inProject(sess("c", "", 1), "web"), inProject(sess("d", "", 2), "api"), sess("e", "", 1)}
	// Model cost comes from per-request aggregation, not from a session's
	// representative model, so a session that mixed models is split exactly.
	prevModels := []ModelCost{{Model: "opus", CostUSD: 5}, {Model: "sonnet", CostUSD: 1}}
	curModels := []ModelCost{{Model: "opus", CostUSD: 0.5}, {Model: "sonnet", CostUSD: 3.5}}

	got := AggregateCost(Window{t0, t1}, Window{t1, t2}, prev, cur, prevModels, curModels, 5, ScopeUser)

	if got.ByModel[0].Key != "opus" || got.ByModel[0].DeltaUSD != -4.5 {
		t.Fatalf("by_model[0] = %+v", got.ByModel[0])
	}
	if got.ByProject[0].Key != "web" || got.ByProject[0].DeltaUSD != -4 {
		t.Fatalf("by_project[0] = %+v", got.ByProject[0])
	}
	var unmapped bool
	for _, d := range got.ByProject {
		unmapped = unmapped || d.Key == unknownKey
	}
	if !unmapped {
		t.Fatalf("unmapped project not bucketed: %+v", got.ByProject)
	}
	if got.TopDriver == nil || got.TopDriver.Axis != "model" || got.TopDriver.Key != "opus" {
		t.Fatalf("top driver = %+v", got.TopDriver)
	}
}

func TestAggregateCostTopSessionsAreCurrentWindowByCost(t *testing.T) {
	cur := []Session{sess("low", "opus", 1), sess("high", "opus", 9), sess("mid", "opus", 3)}

	got := AggregateCost(Window{t0, t1}, Window{t1, t2}, nil, cur, nil, nil, 2, ScopeUser)

	if len(got.TopSessions) != 2 || got.TopSessions[0].SessionID != "high" || got.TopSessions[1].SessionID != "mid" {
		t.Fatalf("top sessions = %+v", got.TopSessions)
	}
}

func TestAggregateCostCaveats(t *testing.T) {
	got := AggregateCost(Window{t0, t1}, Window{t1, t2}, nil, []Session{sess("a", "opus", 1)}, nil, nil, 5, ScopeAdmin)

	for _, code := range []string{"observational_only", "window_empty:previous", "low_volume", "admin_scope"} {
		if !hasCaveat(got.Caveats, code) {
			t.Errorf("missing caveat %q in %+v", code, got.Caveats)
		}
	}
	if got.Previous.CostPerSession != nil {
		t.Errorf("empty window cost_per_session = %v, want null", *got.Previous.CostPerSession)
	}
	if got.ByProject != nil {
		t.Errorf("by_project should be omitted when no session names a project: %+v", got.ByProject)
	}
}

func TestAggregateCostEffectsSumToDeltaAfterRounding(t *testing.T) {
	prev := []Session{sess("a", "", 0.00002), sess("b", "", 0.00003)}
	cur := []Session{sess("c", "", 0.00003)}

	got := AggregateCost(Window{t0, t1}, Window{t1, t2}, prev, cur, nil, nil, 5, ScopeUser)

	if sum := round4(got.VolumeEffectUSD + got.IntensityEffectUSD); sum != got.DeltaCostUSD {
		t.Fatalf("volume %v + intensity %v = %v, delta %v", got.VolumeEffectUSD, got.IntensityEffectUSD, sum, got.DeltaCostUSD)
	}
}

func TestAggregateCostDominantEffectUndeterminedWithoutBaseline(t *testing.T) {
	got := AggregateCost(Window{t0, t1}, Window{t1, t2}, nil, []Session{sess("a", "", 3)}, nil, nil, 5, ScopeUser)
	if got.DominantEffect != "undetermined" {
		t.Fatalf("no previous sessions: dominant = %q", got.DominantEffect)
	}
	same := []Session{sess("a", "", 1)}
	if got := AggregateCost(Window{t0, t1}, Window{t1, t2}, same, same, nil, nil, 5, ScopeUser); got.DominantEffect != "undetermined" {
		t.Fatalf("no change: dominant = %q", got.DominantEffect)
	}
}

func TestAggregateCostTopDriverSkipsUnattributedCost(t *testing.T) {
	prev := []Session{inProject(sess("a", "", 1), "web")}
	cur := []Session{sess("b", "", 10), inProject(sess("c", "", 3), "web")}

	got := AggregateCost(Window{t0, t1}, Window{t1, t2}, prev, cur, nil, nil, 5, ScopeUser)

	if got.TopDriver == nil || got.TopDriver.Key != "web" {
		t.Fatalf("top driver = %+v", got.TopDriver)
	}
}

func TestAggregateCostFlagsDifferingBasesAndBoundarySessions(t *testing.T) {
	prev := []Session{sess("a", "", 2), sess("long", "", 1)}
	cur := []Session{sess("long", "", 1), sess("b", "", 4)}
	prevModels := []ModelCost{{Model: "opus", CostUSD: 3}}
	curModels := []ModelCost{{Model: "opus", CostUSD: 9}} // includes cost the session list does not

	got := AggregateCost(Window{t0, t1}, Window{t1, t2}, prev, cur, prevModels, curModels, 5, ScopeUser)

	if !hasCaveat(got.Caveats, "model_totals_differ") || !hasCaveat(got.Caveats, "boundary_sessions") {
		t.Fatalf("caveats = %+v", got.Caveats)
	}
	matching := AggregateCost(Window{t0, t1}, Window{t1, t2}, prev, cur, prevModels, []ModelCost{{Model: "opus", CostUSD: 5}}, 5, ScopeUser)
	if hasCaveat(matching.Caveats, "model_totals_differ") {
		t.Fatalf("matching totals flagged: %+v", matching.Caveats)
	}
}

func TestAdminScopeWithholdsSessionAndProjectDetail(t *testing.T) {
	other := inProject(sess("x", "", 5), "their-repo")
	other.UserID = "u2"
	cur := []Session{inProject(sess("a", "", 1), "web"), other}

	cost := AggregateCost(Window{t0, t1}, Window{t1, t2}, nil, cur, nil, nil, 5, ScopeAdmin)
	if len(cost.TopSessions) != 0 || cost.ByProject != nil || cost.TopDriver != nil {
		t.Fatalf("cost leaks per-user detail: %+v %+v %+v", cost.TopSessions, cost.ByProject, cost.TopDriver)
	}
	if cost.Current.CostUSD != 6 || !hasCaveat(cost.Caveats, "admin_scope") {
		t.Fatalf("aggregates or caveat lost: %+v %+v", cost.Current, cost.Caveats)
	}

	// Every row ownerless (data from before user_id existed) is still withheld:
	// the role, not the rows, decides.
	a, b := sess("a", "", 1), sess("b", "", 2)
	a.UserID, b.UserID = "", ""
	if got := AggregateCost(Window{t0, t1}, Window{t1, t2}, nil, []Session{a, b}, nil, nil, 5, ScopeAdmin); len(got.TopSessions) != 0 {
		t.Fatalf("ownerless rows under an admin token leaked: %+v", got.TopSessions)
	}

	e := req("y", 0, 10, 150_000, 0, 1)
	e.UserID = ""
	ctx := AggregateContext(Window{t1, t2}, []Event{req("s", 0, 10, 150_000, 0, 1), e}, 5, ScopeAdmin)
	if len(ctx.BloatedSessions) != 0 || ctx.Bloated.Requests != 2 || !hasCaveat(ctx.Caveats, "admin_scope") {
		t.Fatalf("context: %+v", ctx)
	}
}

// A regular token is scoped by the server, so rows are the caller's own even when
// some predate user_id (a server that scopes by email returns both kinds).
func TestUserScopeKeepsDetailForOwnRowsWithAndWithoutUserID(t *testing.T) {
	legacy := inProject(sess("old", "", 5), "web")
	legacy.UserID = ""
	cost := AggregateCost(Window{t0, t1}, Window{t1, t2}, nil, []Session{sess("new", "", 1), legacy}, nil, nil, 5, ScopeUser)
	if len(cost.TopSessions) != 2 || cost.ByProject == nil || hasCaveat(cost.Caveats, "admin_scope") {
		t.Fatalf("own detail withheld: %+v %+v", cost.TopSessions, cost.Caveats)
	}
}

func TestDetailWithheldWhenRoleUnknownOrRowsSpanUsers(t *testing.T) {
	mine := inProject(sess("a", "", 1), "web")

	unknown := AggregateCost(Window{t0, t1}, Window{t1, t2}, nil, []Session{mine}, nil, nil, 5, ScopeUnknown)
	if len(unknown.TopSessions) != 0 || unknown.ByProject != nil || !hasCaveat(unknown.Caveats, "role_unknown") {
		t.Fatalf("unknown role kept detail: %+v %+v", unknown.TopSessions, unknown.Caveats)
	}

	// A regular-user verdict that is wrong (a proxy 403 mistaken for the server's)
	// still cannot leak other people's rows: two named owners withhold detail.
	other := inProject(sess("x", "", 5), "their-repo")
	other.UserID = "u2"
	mixed := AggregateCost(Window{t0, t1}, Window{t1, t2}, nil, []Session{mine, other}, nil, nil, 5, ScopeUser)
	if len(mixed.TopSessions) != 0 || mixed.ByProject != nil || !hasCaveat(mixed.Caveats, "multiple_users") {
		t.Fatalf("rows from two users kept detail: %+v %+v", mixed.TopSessions, mixed.Caveats)
	}

	e2 := req("y", 0, 10, 150_000, 0, 1)
	e2.UserID = "u2"
	ctx := AggregateContext(Window{t1, t2}, []Event{req("s", 0, 10, 150_000, 0, 1), e2}, 5, ScopeUser)
	if len(ctx.BloatedSessions) != 0 || !hasCaveat(ctx.Caveats, "multiple_users") {
		t.Fatalf("context rows from two users kept detail: %+v", ctx)
	}
}

func TestProjectUnavailableCaveatWhenNoSessionNamesAProject(t *testing.T) {
	got := AggregateCost(Window{t0, t1}, Window{t1, t2}, nil, []Session{sess("a", "", 1)}, nil, nil, 5, ScopeUser)
	if got.ByProject != nil || !hasCaveat(got.Caveats, "project_unavailable") {
		t.Fatalf("by_project=%+v caveats=%+v", got.ByProject, got.Caveats)
	}
	if empty := AggregateCost(Window{t0, t1}, Window{t1, t2}, nil, nil, nil, nil, 5, ScopeUser); hasCaveat(empty.Caveats, "project_unavailable") {
		t.Fatalf("empty windows flagged: %+v", empty.Caveats)
	}
}
