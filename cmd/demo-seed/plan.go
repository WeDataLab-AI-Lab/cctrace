package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

// action is one tool call in agent-neutral terms; claude.go and codex.go render
// it as each agent would.
type action struct {
	Kind string // read | grep | edit | write | test | test-fail | git-status | git-diff | git-log | git-commit | git-push | todo | skill
	File string // the skill's name for a skill
}

type request struct {
	Start, End time.Time
	Model      string
	Say        string
	Final      bool
	Actions    []action
	ToolTook   []time.Duration
	ToolIDs    []string
	Approval   []string // per action: "" when allowed by config, else the user's decision source

	In, Out, CacheRead, CacheWrite int // Claude Code
	Cached                         int // Codex cached input
	Cost                           float64
	MsgID, ReqID                   string
}

type turn struct {
	At       time.Time
	Prompt   string
	PromptID string
	Task     task
	NonTask  bool
	Requests []*request
	Compact  bool   // a compact boundary precedes this turn
	Command  string // a plugin command the person typed instead of the prompt
	Skill    string // a skill the turn runs under, see plugins.go
}

type session struct {
	P          *persona
	Proj       *project
	Agent      string // claude | codex
	ID         string
	Model      string
	Headless   bool
	Start, End time.Time
	Title      string
	Turns      []*turn
	Background []*request // Claude Code's small side calls that never reach the transcript
}

type plan struct {
	Anchor   time.Time
	Days     int
	Sessions []*session
}

var taskWeights = []weighted{
	{"implementation", 0.25}, {"debugging", 0.2}, {"testing", 0.12}, {"question", 0.12},
	{"review", 0.08}, {"documentation", 0.07}, {"planning", 0.06}, {"drafting", 0.04},
	{"status", 0.03}, {"operations", 0.03},
}

func pick(r *rand.Rand, ws []weighted) string {
	total := 0.0
	for _, w := range ws {
		total += w.w
	}
	x := r.Float64() * total
	for _, w := range ws {
		if x < w.w {
			return w.v
		}
		x -= w.w
	}
	return ws[len(ws)-1].v
}

func pickStr(r *rand.Rand, s []string) string { return s[r.IntN(len(s))] }

func between(r *rand.Rand, lo, hi time.Duration) time.Duration {
	return lo + time.Duration(r.Int64N(int64(hi-lo)+1))
}

// idChars is the confusable-free set internal/api draws temporary passwords
// from. A new alphabet literal would be a high-entropy run the mirror gate can
// never approve (scripts/mirror-baseline-lint.go, entropyExceptions).
const idChars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// token is a random alphanumeric string. Lengths stay under 32 so no id reads
// as a secret-shaped blob to the mirror gate.
func token(r *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = idChars[r.IntN(len(idChars))]
	}
	return string(b)
}

// uuid4 is a version-4 UUID drawn from r, so the same seed yields the same ids.
func uuid4(r *rand.Rand) string {
	var b [16]byte
	for i := range b {
		b[i] = byte(r.UintN(256))
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func buildPlan(seed uint64, anchor time.Time, days int) *plan {
	pl := &plan{Anchor: anchor, Days: days}
	for i, p := range team {
		r := rand.New(rand.NewPCG(seed, uint64(i+1)))
		pl.Sessions = append(pl.Sessions, planPersona(r, p, anchor, days)...)
	}
	for _, s := range pl.Sessions {
		usePlugins(s)
	}
	return pl
}

func planPersona(r *rand.Rand, p *persona, anchor time.Time, days int) []*session {
	var out []*session
	last := anchor.In(kst)
	first := time.Date(last.Year(), last.Month(), last.Day()-days, 0, 0, 0, 0, kst)
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		weekend := day.Weekday() == time.Saturday || day.Weekday() == time.Sunday
		active := p.ActiveWeekday
		if weekend {
			active = p.ActiveWeekend
		}
		if r.Float64() >= active {
			continue
		}
		var blocks [][2]time.Time
		at := func(min int) time.Time { return day.Add(time.Duration(min) * time.Minute) }
		if weekend {
			s := at(13*60 + r.IntN(180))
			blocks = append(blocks, [2]time.Time{s, s.Add(between(r, 40*time.Minute, 150*time.Minute))})
		} else {
			blocks = append(blocks,
				[2]time.Time{at(p.DayStart + r.IntN(30)), at(12*60 - r.IntN(20))},
				[2]time.Time{at(13*60 + r.IntN(30)), at(p.DayEnd - 15 + r.IntN(60))})
			if r.Float64() < p.Evening {
				s := at(21*60 + r.IntN(60))
				blocks = append(blocks, [2]time.Time{s, s.Add(between(r, 30*time.Minute, 100*time.Minute))})
			}
		}
		for _, b := range blocks {
			t := b[0]
			for t.Before(b[1]) && t.Before(anchor) {
				s := planSession(r, p, t, b[1], anchor)
				if s == nil {
					break
				}
				out = append(out, s)
				t = s.End.Add(between(r, 4*time.Minute, 28*time.Minute))
			}
		}
	}
	return out
}

func newTask(r *rand.Rand, p *persona, proj *project, kind string, korean bool) task {
	c := proj.Components[r.IntN(len(proj.Components))]
	t := task{Kind: kind, Korean: korean, Project: proj, Comp: c,
		Feature: pickStr(r, c.Features), Thing: pickStr(r, c.Things), Case: pickStr(r, c.Cases)}
	slug := strings.Fields(strings.ToLower(strings.NewReplacer("-", " ", "/", " ", "@", "", "'", "").Replace(t.Thing)))
	if len(slug) > 3 {
		slug = slug[len(slug)-3:]
	}
	prefix := map[string]string{"debugging": "fix", "documentation": "docs", "testing": "test"}[kind]
	if prefix == "" {
		prefix = "feat"
	}
	t.Branch = prefix + "/" + strings.Join(slug, "-")
	return t
}

func planSession(r *rand.Rand, p *persona, start, blockEnd, anchor time.Time) *session {
	s := &session{P: p, Start: start, ID: uuid4(r)}
	// The first project is the main one; the others take a third of the time.
	key := p.Projects[0]
	if len(p.Projects) > 1 && r.Float64() < 0.4 {
		key = p.Projects[1+r.IntN(len(p.Projects)-1)]
	}
	s.Proj = projects[key]
	if r.Float64() < p.ClaudeShare {
		s.Agent, s.Model = "claude", pick(r, p.ClaudeModels)
		s.Headless = r.Float64() < p.Headless
	} else {
		s.Agent, s.Model = "codex", pick(r, p.CodexModels)
	}
	korean := r.Float64() < p.Korean
	main := newTask(r, p, s.Proj, pick(r, taskWeights), korean)
	s.Title = main.title()

	softEnd := start.Add(between(r, 12*time.Minute, 95*time.Minute))
	if softEnd.After(blockEnd.Add(15 * time.Minute)) {
		softEnd = blockEnd.Add(15 * time.Minute)
	}
	ctx := 12000 + r.IntN(9000) // system prompt, tools and project rules
	t := start
	if s.Agent == "claude" && !s.Headless {
		s.Background = append(s.Background, backgroundCall(r, t.Add(2*time.Second)))
	}
	tasks := []task{main}
	for i := 0; i < len(tasks) && t.Before(softEnd) && t.Before(anchor); i++ {
		tk := tasks[i]
		tr := &turn{At: t, PromptID: uuid4(r), Task: tk, Prompt: tk.prompt()}
		switch tk.Kind {
		case "ack":
			tr.Prompt, tr.NonTask = tk.followUp("ack"), true
		case "followup-testing", "followup-implementation":
			tr.Prompt = tk.followUp(strings.TrimPrefix(tk.Kind, "followup-"))
		}
		if s.Agent == "claude" && ctx > 160000 {
			tr.Compact, ctx = true, 28000+r.IntN(6000)
		}
		t = t.Add(between(r, 800*time.Millisecond, 3*time.Second))
		for j, step := range script(r, tk) {
			req := &request{Start: t, Model: s.Model, Say: step.say, Final: step.final, Actions: step.actions}
			think := between(r, 3*time.Second, 14*time.Second)
			if step.final {
				think = between(r, 6*time.Second, 28*time.Second)
			}
			req.End = t.Add(think)
			ctx = tokens(r, s, req, ctx, j == 0 && i == 0)
			t = req.End
			for _, a := range step.actions {
				took := toolDuration(r, a.Kind)
				req.ToolTook = append(req.ToolTook, took)
				if s.Agent == "claude" {
					req.ToolIDs = append(req.ToolIDs, "toolu_01"+token(r, 22))
				} else {
					req.ToolIDs = append(req.ToolIDs, "call_"+token(r, 24))
				}
				approval := ""
				if a.Kind == "git-push" || (a.Kind == "test" && r.Float64() < 0.15) {
					approval = "user_temporary"
					took += between(r, 2*time.Second, 25*time.Second)
				}
				req.Approval = append(req.Approval, approval)
				t = t.Add(took)
			}
			req.MsgID, req.ReqID = "msg_01"+token(r, 22), "req_011C"+token(r, 20)
			tr.Requests = append(tr.Requests, req)
			t = t.Add(between(r, 200*time.Millisecond, 1500*time.Millisecond))
			if t.After(anchor) {
				break
			}
		}
		s.Turns = append(s.Turns, tr)
		if s.Headless {
			break
		}
		// What the person does next: a follow-up on the same work, a quick
		// acknowledgement, or a new request about the same project.
		x := r.Float64()
		switch {
		case x < 0.22:
			next := tk
			next.Kind = "followup-testing"
			tasks = append(tasks, next)
		case x < 0.4:
			next := tk
			next.Kind = "followup-implementation"
			tasks = append(tasks, next)
		case x < 0.5:
			next := tk
			next.Kind = "ack"
			tasks = append(tasks, next)
		case x < 0.62:
			next := tk
			next.Kind = "operations"
			tasks = append(tasks, next)
		default:
			tasks = append(tasks, newTask(r, p, s.Proj, pick(r, taskWeights), korean))
		}
		t = t.Add(between(r, 25*time.Second, 6*time.Minute))
		if s.Agent == "claude" && r.Float64() < 0.3 {
			s.Background = append(s.Background, backgroundCall(r, t.Add(-time.Second)))
		}
	}
	if len(s.Turns) == 0 || len(s.Turns[0].Requests) == 0 {
		return nil
	}
	lastTurn := s.Turns[len(s.Turns)-1]
	s.End = lastTurn.Requests[len(lastTurn.Requests)-1].End
	return s
}

type step struct {
	say     string
	final   bool
	actions []action
}

// script lays out the agent's work on one request. Each step is one model call.
func script(r *rand.Rand, t task) []step {
	c := t.Comp
	read := func(f string) action { return action{Kind: "read", File: f} }
	var s []step
	add := func(say string, acts ...action) { s = append(s, step{say: say, actions: acts}) }
	switch t.Kind {
	case "implementation", "followup-implementation":
		add(t.say("look"), read(c.File))
		if r.Float64() < 0.6 {
			add("", read(c.Test), action{Kind: "grep", File: c.File})
		}
		add(t.say("edit"), action{Kind: "edit", File: c.File})
		if r.Float64() < 0.5 {
			add("", action{Kind: "edit", File: c.File})
		}
		add("", action{Kind: "edit", File: c.Test})
		add(t.say("test"), action{Kind: "test", File: c.Test})
	case "debugging":
		add(t.say("fail"), action{Kind: "test-fail", File: c.Test})
		add(t.say("look"), read(c.File), read(c.Test))
		add("", action{Kind: "grep", File: c.File})
		add(t.say("cause"), action{Kind: "edit", File: c.File})
		if r.Float64() < 0.35 {
			add("", action{Kind: "test-fail", File: c.Test})
			add("", action{Kind: "edit", File: c.File})
		}
		add(t.say("test"), action{Kind: "test", File: c.Test})
	case "testing", "followup-testing":
		if t.Kind == "testing" {
			add(t.say("look"), read(c.File), read(c.Test))
			add(t.say("edit"), action{Kind: "edit", File: c.Test})
		}
		add(t.say("test"), action{Kind: "test", File: c.Test})
	case "review":
		add(t.say("look"), action{Kind: "git-diff", File: c.File})
		add("", read(c.File), read(c.Test))
	case "documentation":
		add(t.say("look"), read(t.Project.Docs[0]), read(c.File))
		add(t.say("edit"), action{Kind: "edit", File: t.Project.Docs[0]})
	case "question":
		add(t.say("look"), action{Kind: "grep", File: c.File})
		add("", read(c.File))
	case "planning":
		add(t.say("look"), read(c.File), read(c.Test))
		add(t.say("plan"), action{Kind: "todo", File: c.File})
	case "drafting", "status":
		add(t.say("look"), action{Kind: "git-log", File: c.File})
	case "operations":
		add(t.say("git"), action{Kind: "git-status", File: c.File})
		add("", action{Kind: "git-commit", File: c.File})
		add("", action{Kind: "git-push", File: c.File})
	case "ack":
		add("", action{Kind: "test", File: c.Test})
	}
	s = append(s, step{say: t.answer(), final: true})
	if t.Kind == "ack" || strings.HasPrefix(t.Kind, "followup-") {
		s[len(s)-1].say = t.say("test")
		if t.Korean {
			s[len(s)-1].say = "모두 통과합니다."
		} else {
			s[len(s)-1].say = "All green."
		}
	}
	return s
}

func toolDuration(r *rand.Rand, kind string) time.Duration {
	switch kind {
	case "read", "grep", "todo":
		return between(r, 30*time.Millisecond, 400*time.Millisecond)
	case "edit", "write":
		return between(r, 80*time.Millisecond, 600*time.Millisecond)
	case "test", "test-fail":
		return between(r, 2*time.Second, 38*time.Second)
	case "git-push":
		return between(r, 900*time.Millisecond, 3500*time.Millisecond)
	}
	return between(r, 100*time.Millisecond, 1200*time.Millisecond)
}

// tokens fills one request's usage and cost and returns the context size after it.
func tokens(r *rand.Rand, s *session, req *request, ctx int, first bool) int {
	added := 300 + r.IntN(3800)
	out := 60 + r.IntN(640)
	if req.Final {
		out = 280 + r.IntN(1300)
	}
	if s.Agent == "codex" {
		req.In = ctx + added
		req.Cached = ctx
		if first {
			req.Cached = ctx / 3
		}
		req.Out = out
		pr := codexPrices[s.Model]
		req.Cost = (float64(req.In-req.Cached)*pr.in + float64(req.Out)*pr.out + float64(req.Cached)*pr.cached) / 1e6
		return ctx + added + out
	}
	req.In = 1 + r.IntN(40)
	req.Out = out
	if first {
		req.CacheRead, req.CacheWrite = 11000+r.IntN(2000), ctx-11000
	} else {
		req.CacheRead, req.CacheWrite = ctx, added
	}
	req.Cost = claudeCost(s.Model, req.In, req.Out, req.CacheRead, req.CacheWrite)
	return ctx + added + out
}

func claudeCost(model string, in, out, cacheRead, cacheWrite int) float64 {
	pr := claudePrices[model]
	c := (float64(in)*pr.in + float64(out)*pr.out + float64(cacheRead)*pr.cacheRead + float64(cacheWrite)*pr.cacheWrite) / 1e6
	return math.Round(c*1e6) / 1e6
}

// backgroundCall is one of the small Haiku requests Claude Code makes on its own
// (titles, topic detection). They are billed and reported over OTEL but never
// written to the transcript.
func backgroundCall(r *rand.Rand, at time.Time) *request {
	req := &request{Start: at, End: at.Add(between(r, 400*time.Millisecond, 1800*time.Millisecond)), Model: "claude-haiku-5-5",
		In: 300 + r.IntN(900), Out: 8 + r.IntN(40)}
	req.Cost = claudeCost(req.Model, req.In, req.Out, 0, 0)
	return req
}
