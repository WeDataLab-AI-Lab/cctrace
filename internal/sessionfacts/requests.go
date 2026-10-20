package sessionfacts

import "sort"

// costSum adds up the requests and api_request events of a turn, or of the
// session.
type costSum struct {
	requests int
	tokens   [4]int64 // input, output, cache read, cache create

	// Imputed cost (p1b-design §4.3): the requests that have a row, and the
	// ones that do not, split by whether a rate exists for their model.
	priced      int
	waiting     int // a rate and no row yet: the 30-minute rewrite has not come by
	unpriceable int // no rate: there will never be a row
	imputedUSD  float64

	// Telemetry (p1b-design §4.2): the api_request events that carry the turn's
	// prompt id.
	events  int
	otelUSD float64
}

func (s *costSum) add(h *Head) {
	s.requests++
	s.tokens[0] += h.InputTokens
	s.tokens[1] += h.OutputTokens
	s.tokens[2] += h.CacheReadTokens
	s.tokens[3] += h.CacheCreateTokens
	switch {
	case h.Imputed:
		s.priced++
		s.imputedUSD += h.CostUSD
	case h.HasRate:
		s.waiting++
	default:
		s.unpriceable++
	}
}

// requests is the session's requests after the copy predicate, in (ts, id)
// order: sums are taken in an order the rows fix, since float addition is not
// associative and the output must not depend on how the input was sorted.
func (c *computation) requests() []*Head {
	var heads []*Head
	for i := range c.in.Heads {
		c.pulse()
		if h := &c.in.Heads[i]; !c.copied(h.ID) {
			heads = append(heads, h)
		}
	}
	sort.Slice(heads, func(i, j int) bool {
		c.pulse()
		if !heads[i].TS.Equal(heads[j].TS) {
			return heads[i].TS.Before(heads[j].TS)
		}
		return heads[i].ID < heads[j].ID
	})
	return heads
}

// attachRequests adds every request to the session and to its turn (p1b-design
// §3.10). A main-thread request finds its turn through the tree node of its
// head line, so it does not depend on a prompt id; a subagent's goes where its
// thread was attributed. One with no turn -- no node, a line outside every
// turn, a thread nothing placed -- stays in the session's sums alone.
func (c *computation) attachRequests(heads []*Head) {
	placed := c.attributeThreads()
	unattributed := &c.session.Detail.SubagentUnattributed
	for _, h := range heads {
		c.pulse()
		c.total.add(h)
		var t *turn
		if c.in.Agent == "codex" {
			t = c.codexByHead[h.ID]
		} else if h.IsSidechain {
			if t = placed[sidechainKey(h.SourceFile, h.AgentID)]; t == nil {
				unattributed.Requests++
				unattributed.Tokens += h.InputTokens + h.OutputTokens + h.CacheReadTokens + h.CacheCreateTokens
			}
		} else if n := c.byUUID[h.UUID]; n != nil {
			t = n.turn
		}
		if t == nil {
			c.loose.add(h)
			continue
		}
		t.cost.add(h)
	}
}

// sidechainKey identifies a sidechain by its source file, falling back to the
// agent id for older rows without a source file. An empty key cannot identify
// a thread, and file and agent keys have separate namespaces.
func sidechainKey(sourceFile, agentID string) string {
	switch {
	case sourceFile != "":
		return "file:" + sourceFile
	case agentID != "":
		return "agent:" + agentID
	default:
		return ""
	}
}

// attributeThreads finds the turn of each subagent thread, the sidechain lines
// of one source file (or legacy agent id), by the first method that names one
// (p1b-design §3.7):
//
//  1. the promptId of the thread's first user line, which is its parent turn's
//     (5,485 of 5,551 local subagent files) -- unless several turns share that
//     id, when the rows cannot say which of them spawned it (N1);
//  2. the tool_use id sync read from the thread's meta.json, through the
//     main-thread assistant line that holds that tool_use. It is what places
//     most pre-P0 threads (86.5% in production);
//  3. the agent id, through the main-thread user line whose toolUseResult
//     names it. It alone placed 9 local files.
//
// A thread none of them places is absent from the result.
func (c *computation) attributeThreads() map[string]*turn {
	type thread struct {
		firstUser *line
		toolUseID string
		agentID   string
	}
	threads := make(map[string]*thread)
	for _, l := range c.lines {
		c.pulse()
		if !l.IsSidechain {
			continue
		}
		key := sidechainKey(l.SourceFile, l.AgentID)
		if key == "" {
			continue
		}
		th := threads[key]
		if th == nil {
			th = &thread{}
			threads[key] = th
		}
		if th.agentID == "" {
			th.agentID = l.AgentID
		}
		if th.firstUser == nil && l.RecordType == "user" {
			th.firstUser = l
		}
		if th.toolUseID == "" {
			th.toolUseID = l.ToolUseID
		}
	}
	// The first main-thread line whose toolUseResult names each agent.
	results := make(map[string]*line)
	for _, n := range c.nodes {
		c.pulse()
		for _, id := range n.resultAgents() {
			c.pulse()
			if id != "" && results[id] == nil {
				results[id] = n
			}
		}
	}

	turns := make(map[string]*turn)
	for key, th := range threads {
		c.pulse()
		var t *turn
		if th.firstUser != nil {
			if sole := c.byPrompt[th.firstUser.PromptID]; len(sole) == 1 {
				t = sole[0]
			}
		}
		if t == nil {
			if use, ok := c.toolUses[th.toolUseID]; ok {
				t = use.line.turn
			}
		}
		if t == nil {
			if n := results[th.agentID]; n != nil {
				t = n.turn
			}
		}
		if t != nil {
			turns[key] = t
		}
	}
	return turns
}

// attachEvents gives each api_request event to the turn that alone has its
// prompt id (p1b-design §4.2) and returns the session's sum and what no turn
// took. An event without a prompt id, with one no turn has (title generation,
// compaction, a pre-P0 turn's calls) or with one several turns share is
// counted once, in the unattached sum: a plain join would give a shared id's
// cost to both turns, and a one-value map all of it to one.
//
// A subagent's calls carry its parent turn's prompt id, and a turn on an
// abandoned branch takes its events like any other.
func (c *computation) attachEvents() (total, attached, unattached float64) {
	events := make([]*OTELRow, len(c.in.OTEL))
	for i := range c.in.OTEL {
		c.pulse()
		events[i] = &c.in.OTEL[i]
	}
	sort.Slice(events, func(i, j int) bool { c.pulse(); return eventBefore(events[i], events[j]) })
	for _, e := range events {
		c.pulse()
		total += e.CostUSD
		if sole := c.byPrompt[e.PromptID]; len(sole) == 1 {
			sole[0].cost.events++
			sole[0].cost.otelUSD += e.CostUSD
			attached += e.CostUSD
		} else {
			unattached += e.CostUSD
		}
	}
	return total, attached, unattached
}

// eventBefore orders events by ts, prompt id and cost, so that two the order
// leaves equal are equal in all a sum reads.
func eventBefore(a, b *OTELRow) bool {
	switch {
	case !a.TS.Equal(b.TS):
		return a.TS.Before(b.TS)
	case a.PromptID != b.PromptID:
		return a.PromptID < b.PromptID
	}
	return a.CostUSD < b.CostUSD
}
