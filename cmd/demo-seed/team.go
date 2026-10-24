package main

import "time"

// kst is the team's clock. Work days, lunch and evenings are laid out on it, so a
// session that "starts at 9:30" shows at 9:30 on a dashboard read in Seoul.
var kst = time.FixedZone("KST", 9*60*60)

type weighted struct {
	v string
	w float64
}

// persona is one fictitious team member. The five names, addresses and user ids
// are the ones the public docs screenshots already show (#794), so the demo and
// the documentation describe the same team.
type persona struct {
	UserID string // cctrace_user_id, also the dashboard's "ID"
	Home   string // home directory name inside the seeding container
	Name   string
	Email  string // dashboard, profile and login address
	Team   string
	Admin  bool

	Projects     []string // project keys, most-used first
	ClaudeShare  float64  // share of sessions run in Claude Code; the rest are Codex
	ClaudeModels []weighted
	CodexModels  []weighted
	ClaudePlan   string // quota plan key, see web/lib/plan-prices.ts
	CodexPlan    string
	// ClaudePctUSD and CodexPctUSD are how many dollars of usage move the weekly
	// meter by one point; the five-hour meter moves seven times faster.
	ClaudePctUSD float64
	CodexPctUSD  float64

	DayStart      int // minutes after local midnight
	DayEnd        int
	ActiveWeekday float64
	ActiveWeekend float64
	Evening       float64
	Korean        float64 // share of prompts written in Korean
	Headless      float64 // share of Claude sessions run as `claude -p`
	PluginUse     float64 // share of turns that use a plugin where one fits, see plugins.go
}

var team = []*persona{
	{
		UserID: "ivynonrealton", Home: "ivy", Name: "Ivy Nonrealton", Email: "ivy.nonrealton@example.com", Team: "Platform", Admin: true,
		Projects:     []string{"nebula-ingest", "nebula-cli", "nebula-docs"},
		ClaudeShare:  0.8,
		ClaudeModels: []weighted{{"claude-opus-5-5", 0.7}, {"claude-fable-5-1", 0.2}, {"claude-sonnet-5-5", 0.1}},
		CodexModels:  []weighted{{"gpt-6.1-sol", 0.7}, {"gpt-6-astra", 0.3}},
		ClaudePlan:   "default_claude_max_20x", CodexPlan: "plus",
		ClaudePctUSD: 1.4, CodexPctUSD: 0.3,
		DayStart: 9*60 + 30, DayEnd: 18*60 + 30, ActiveWeekday: 0.93, ActiveWeekend: 0.15, Evening: 0.35, Korean: 0.45, Headless: 0.05, PluginUse: 0.5,
	},
	{
		UserID: "marcusfablewright", Home: "marcus", Name: "Marcus Fablewright", Email: "marcus.fablewright@example.com", Team: "Data",
		Projects:     []string{"nebula-ingest", "orbit-scheduler"},
		ClaudeShare:  0.35,
		ClaudeModels: []weighted{{"claude-sonnet-5-5", 0.6}, {"claude-opus-5-5", 0.4}},
		CodexModels:  []weighted{{"gpt-6.1-sol", 0.6}, {"gpt-6-astra", 0.3}, {"gpt-6-luna", 0.1}},
		ClaudePlan:   "default_claude_max_5x", CodexPlan: "pro",
		ClaudePctUSD: 0.55, CodexPctUSD: 1.2,
		DayStart: 10 * 60, DayEnd: 19 * 60, ActiveWeekday: 0.9, ActiveWeekend: 0.05, Evening: 0.2, Korean: 0.2, Headless: 0.1, PluginUse: 0.25,
	},
	{
		UserID: "priyanotarealsen", Home: "priya", Name: "Priya Notarealsen", Email: "priya.notarealsen@example.com", Team: "Platform",
		Projects:     []string{"orbit-scheduler", "nebula-ingest"},
		ClaudeShare:  0.92,
		ClaudeModels: []weighted{{"claude-opus-5-5", 0.8}, {"claude-sonnet-5-5", 0.2}},
		CodexModels:  []weighted{{"gpt-6.1-sol", 1}},
		ClaudePlan:   "default_claude_max_20x", CodexPlan: "plus",
		ClaudePctUSD: 1.6, CodexPctUSD: 0.1,
		DayStart: 9 * 60, DayEnd: 17*60 + 30, ActiveWeekday: 0.95, ActiveWeekend: 0.05, Evening: 0.1, Korean: 0.6, Headless: 0.03, PluginUse: 0.6,
	},
	{
		UserID: "tomasficticio", Home: "tomas", Name: "Tomás Ficticio", Email: "tomas.ficticio@example.com", Team: "Product",
		Projects:     []string{"nebula-docs", "nebula-cli"},
		ClaudeShare:  0.75,
		ClaudeModels: []weighted{{"claude-sonnet-5-5", 0.5}, {"claude-opus-5-5", 0.5}},
		CodexModels:  []weighted{{"gpt-6.1-sol", 0.7}, {"gpt-6-luna", 0.3}},
		ClaudePlan:   "default_claude_max_5x", CodexPlan: "plus",
		ClaudePctUSD: 0.7, CodexPctUSD: 0.15,
		DayStart: 10*60 + 30, DayEnd: 18*60 + 30, ActiveWeekday: 0.85, ActiveWeekend: 0.12, Evening: 0.25, Korean: 0.15, Headless: 0.02, PluginUse: 0.35,
	},
	{
		UserID: "yukitestonaga", Home: "yuki", Name: "Yuki Testonaga", Email: "yuki.testonaga@example.com", Team: "Data",
		Projects:     []string{"orbit-scheduler", "nebula-cli"},
		ClaudeShare:  0.5,
		ClaudeModels: []weighted{{"claude-opus-5-5", 0.5}, {"claude-sonnet-5-5", 0.3}, {"claude-fable-5-1", 0.2}},
		CodexModels:  []weighted{{"gpt-6.1-sol", 0.5}, {"gpt-6-astra", 0.3}, {"gpt-6-luna", 0.2}},
		ClaudePlan:   "default_claude_max_5x", CodexPlan: "prolite",
		ClaudePctUSD: 0.75, CodexPctUSD: 0.8,
		DayStart: 8*60 + 30, DayEnd: 17 * 60, ActiveWeekday: 0.9, ActiveWeekend: 0.08, Evening: 0.15, Korean: 0.5, Headless: 0.08, PluginUse: 0.45,
	},
}

// claudePrice is dollars per million tokens. Claude Code reports cost_usd itself,
// so these only have to be plausible, not exact.
type claudePrice struct{ in, out, cacheRead, cacheWrite float64 }

var claudePrices = map[string]claudePrice{
	"claude-fable-5-1":  {10, 50, 0.25, 12.5},
	"claude-opus-5-5":   {5, 25, 0.5, 6.25},
	"claude-sonnet-5-5": {2, 10, 0.2, 2.5},
	"claude-haiku-5-5":  {1, 5, 0.1, 1.25},
}

// codexPrice is dollars per million tokens from the public OpenAI pricing page
// (2026-10-24). The server imputes Codex cost from its own codex_model_rates;
// the seeder writes these rows there (seedCodexRates) and uses the same numbers
// to move the quota meters.
type codexPrice struct{ in, out, cached float64 }

var codexPrices = map[string]codexPrice{
	"gpt-6-astra": {10, 50, 1},
	"gpt-6.1-sol": {2, 10, 0.1},
	"gpt-6-luna":  {0.1, 0.5, 0.01},
}

const (
	claudeCodeVersion = "2.3.12"
	codexVersion      = "0.161.0"
)
