package main

import (
	"math"
	"sort"
	"time"

	"cctrace/internal/store"
)

type spend struct {
	at   time.Time
	cost float64
}

// accounts are one persona's billing accounts: the Anthropic login behind Claude
// Code and the ChatGPT login behind Codex.
type accounts struct {
	ClaudeAccount, ClaudeOrg, CodexAccount string
}

func personaAccounts(seed uint64, i int) accounts {
	r := sessionRand("accounts", seed+uint64(i)*7919)
	return accounts{ClaudeAccount: uuid4(r), ClaudeOrg: uuid4(r), CodexAccount: uuid4(r)}
}

// quotaSamples reads the plan-limit meters the way the clients do: every five
// minutes while the person's machine is awake (Claude Code), or while a Codex
// session is running (Codex). A meter moves in proportion to what was spent in
// its window, so measured cost and meter movement agree, which is what the
// coverage figure on the Overview fits.
func quotaSamples(pl *plan, p *persona, idx int, acct accounts) []*store.QuotaSample {
	var claude, codex []spend
	var awake, codexOn [][2]time.Time
	byDay := map[string][2]time.Time{}
	for _, s := range pl.Sessions {
		if s.P != p {
			continue
		}
		day := s.Start.In(kst).Format("2006-01-02")
		w, ok := byDay[day]
		if !ok || s.Start.Before(w[0]) {
			w[0] = s.Start
		}
		if !ok || s.End.After(w[1]) {
			w[1] = s.End
		}
		byDay[day] = w
		for _, tr := range s.Turns {
			for _, req := range tr.Requests {
				if s.Agent == "claude" {
					claude = append(claude, spend{req.End, req.Cost})
				} else {
					codex = append(codex, spend{req.End, req.Cost})
				}
			}
		}
		for _, bg := range s.Background {
			claude = append(claude, spend{bg.End, bg.Cost})
		}
		if s.Agent == "codex" {
			codexOn = append(codexOn, [2]time.Time{s.Start, s.End.Add(5 * time.Minute)})
		}
	}
	for _, w := range byDay {
		awake = append(awake, [2]time.Time{w[0].Add(-10 * time.Minute), w[1].Add(30 * time.Minute)})
	}
	sort.Slice(awake, func(i, j int) bool { return awake[i][0].Before(awake[j][0]) })
	sort.Slice(claude, func(i, j int) bool { return claude[i].at.Before(claude[j].at) })
	sort.Slice(codex, func(i, j int) bool { return codex[i].at.Before(codex[j].at) })

	// Each account's weekly window resets at its own fixed moment, as real ones do.
	weeklyBase := time.Date(2026, 1, 5, 9, 0, 0, 0, kst).Add(time.Duration(idx*29) * time.Hour)
	var out []*store.QuotaSample
	if len(claude) > 0 {
		m := meter{provider: "anthropic", account: acct.ClaudeAccount, plan: p.ClaudePlan, email: p.Email,
			weekly: p.ClaudePctUSD, shortKey: "session", weeklyKey: "weekly_all", weeklyBase: weeklyBase}
		out = append(out, m.read(claude, awake, pl.Anchor)...)
	}
	if len(codex) > 0 {
		m := meter{provider: "openai", account: acct.CodexAccount, plan: p.CodexPlan, email: p.Email,
			weekly: p.CodexPctUSD, shortKey: "300", weeklyKey: "10080", weeklyBase: weeklyBase.Add(31 * time.Hour)}
		out = append(out, m.read(codex, codexOn, pl.Anchor)...)
	}
	return out
}

type meter struct {
	provider, account, plan, email string
	weekly                         float64 // dollars per weekly point
	shortKey, weeklyKey            string
	weeklyBase                     time.Time
}

func (m meter) read(spends []spend, on [][2]time.Time, anchor time.Time) []*store.QuotaSample {
	var out []*store.QuotaSample
	short, week := 300, 10080
	var shortStart time.Time
	seen := map[time.Time]bool{}
	for _, w := range on {
		for t := w[0].Truncate(5 * time.Minute); t.Before(w[1]) && !t.After(anchor); t = t.Add(5 * time.Minute) {
			if seen[t] {
				continue
			}
			seen[t] = true
			// Weekly window.
			n := int(math.Floor(t.Sub(m.weeklyBase).Hours() / (7 * 24)))
			wStart := m.weeklyBase.Add(time.Duration(n) * 7 * 24 * time.Hour)
			wReset := wStart.Add(7 * 24 * time.Hour)
			wPct := math.Min(100, math.Floor(sumBetween(spends, wStart, t)/m.weekly))
			// Five-hour window: opens with the first spend after the last one closed.
			if shortStart.IsZero() || !t.Before(shortStart.Add(5*time.Hour)) {
				shortStart = time.Time{}
				if first, ok := firstAfter(spends, t.Add(-5*time.Minute), t); ok {
					shortStart = first
				}
			}
			sPct := 0.0
			var sReset *time.Time
			if !shortStart.IsZero() {
				sPct = math.Min(100, math.Floor(sumBetween(spends, shortStart, t)/(m.weekly/7)))
				rs := shortStart.Add(5 * time.Hour)
				sReset = &rs
			}
			wr := wReset
			out = append(out,
				&store.QuotaSample{BillingProvider: m.provider, AccountID: m.account, WindowKey: m.shortKey, SampledAt: t.UTC(),
					UsedPct: sPct, ResetsAt: sReset, WindowMinutes: &short, Plan: m.plan, LoginEmail: m.email, ProfileEmail: m.email, Attribution: "observed"},
				&store.QuotaSample{BillingProvider: m.provider, AccountID: m.account, WindowKey: m.weeklyKey, SampledAt: t.UTC(),
					UsedPct: wPct, ResetsAt: &wr, WindowMinutes: &week, Plan: m.plan, LoginEmail: m.email, ProfileEmail: m.email, Attribution: "observed"})
		}
	}
	return out
}

func sumBetween(spends []spend, from, to time.Time) float64 {
	i := sort.Search(len(spends), func(i int) bool { return !spends[i].at.Before(from) })
	total := 0.0
	for ; i < len(spends) && !spends[i].at.After(to); i++ {
		total += spends[i].cost
	}
	return total
}

func firstAfter(spends []spend, from, to time.Time) (time.Time, bool) {
	i := sort.Search(len(spends), func(i int) bool { return spends[i].at.After(from) })
	if i < len(spends) && !spends[i].at.After(to) {
		return spends[i].at, true
	}
	return time.Time{}, false
}
