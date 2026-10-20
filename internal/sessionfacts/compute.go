package sessionfacts

import "sort"

// computation is one run of Compute.
type computation struct {
	stream *FactAccumulator
	in     Input
	copies map[int64]bool

	lines       []*line            // every Claude row, in (ts, uuid) order
	nodes       []*line            // the main-thread forest, in the same order
	byUUID      map[string]*line   // its nodes
	toolUses    map[string]toolUse // the main thread's tool_use blocks, by id
	turns       []*turn            // in start order
	byPrompt    map[string][]*turn // the turns of each prompt id
	codexByHead map[int64]*turn
	todoState   map[string]string
	// dropped are the turns dropCopiedTurns removed, and toolUseCount how many
	// main-thread nodes carry each tool_use id. toolUses keeps only the last of
	// a repeated id; only the opt-in timeline reads either.
	dropped                     []*turn
	toolUseCount                map[string]int
	codexTurnAttributionUnknown bool
	strictCodexTurnAttribution  bool

	total   costSum // every request of the session
	loose   costSum // the requests attached to no turn
	session Session
}

// toolUse is a tool_use block: the assistant line that holds it and the tool.
type toolUse struct {
	line *line
	name string
}

// Compute works out a session's facts from its rows.
//
// Claude turns follow the main-thread forest. Codex turns follow the root
// lifecycle entries and usage rows (p1b-design §3.8). Neither path reads the
// clock or performs I/O.
//
// Compute keeps nothing and does not modify in.
func Compute(in Input) Output {
	out, _ := compute(in, false)
	return out
}

// ComputeWithCommits is the commit-only preparation path; the worker uses the
// combined timeline, commit and task entry point below.
func ComputeWithCommits(in Input) CommitOutput {
	out, c := compute(in, false)
	extended := CommitOutput{Output: out, Commits: c.readCommits()}
	c.applyCodexTurnAttributionState()
	extended.Session = c.session
	extended.Session.RulesVersion = RulesVersion
	extended.Hash = HashOutputWithCommits(extended)
	return extended
}

// TimelineCommitOutput is the worker's complete P4 computation. It preserves
// the existing timeline mapping while hashing commits and task actions with it.
type TimelineCommitOutput struct {
	Output
	Timeline     TimelineFacts
	Commits      CommitFacts
	Tasks        TaskFacts
	TimelineHash []byte
}

// ComputeWithTimelineAndCommits is the opt-in worker path for P4. The pure r2,
// timeline-only, and commit-only entry points keep their existing contracts.
func ComputeWithTimelineAndCommits(in Input) TimelineCommitOutput {
	out, c := compute(in, true)
	extended := TimelineCommitOutput{
		Output: out, Timeline: c.readTimeline(),
		Commits: c.readCommits(),
	}
	extended.Tasks = c.readTasks(extended.Commits)
	c.applyCodexTurnAttributionState()
	c.session.Detail.TranscriptIO = in.TranscriptIO
	extended.Session = c.session
	extended.Session.RulesVersion = RulesVersion
	if extended.Tasks.State == "" {
		extended.Tasks.State = "observed"
	}
	extended.Hash = HashOutputWithCommitsAndTasks(extended.Output, extended.Commits, extended.Tasks)
	extended.TimelineHash = HashOutputWithTimelineAndCommits(extended)
	return extended
}

func compute(in Input, strictCodexTurnAttribution bool) (Output, *computation) {
	c := &computation{in: in, copies: make(map[int64]bool), strictCodexTurnAttribution: strictCodexTurnAttribution}
	if in.CopyGate {
		for _, id := range in.CopiedIDs {
			c.copies[id] = true
		}
	}
	s := &c.session
	s.Agent, s.Coverage, s.RulesVersion = in.Agent, "full", baseRulesVersion
	if in.CopyState != "" {
		s.Detail.Copy = &CopyStateDetail{State: in.CopyState, Source: in.CopySource}
	}

	var segments []Segment
	if in.Agent == "codex" {
		c.readCodex()
	} else {
		segments = c.readClaude()
	}

	heads := c.requests()
	if in.Agent == "codex" {
		c.readCodexTurns()
	}
	c.attachRequests(heads)
	s.APIRequests = c.total.requests
	s.InputTokens, s.OutputTokens = c.total.tokens[0], c.total.tokens[1]
	s.CacheReadTokens, s.CacheCreateTokens = c.total.tokens[2], c.total.tokens[3]
	if in.Agent == "codex" {
		c.readCodexContext(heads)
		c.session.Detail.Signals = Signals{Turn: observed(*c.session.Turns), Question: Signal{State: "not_applicable"}, Background: Signal{State: "not_applicable"}, Todo: Signal{State: "not_applicable"}}
	} else {
		c.readContext(heads)
		c.readClaudeFacts()
	}
	c.applyCodexTurnAttributionState()
	// The session's model is the one its last main-thread request ran on: a
	// subagent's model does not name the session.
	for _, h := range heads {
		if !h.IsSidechain {
			s.Model = h.Model
		}
	}
	c.priceSession()
	c.entitle()

	out := Output{Session: c.session, Segments: segments}
	for _, t := range c.turns {
		t.out.APIRequests = t.cost.requests
		t.out.InputTokens, t.out.OutputTokens = t.cost.tokens[0], t.cost.tokens[1]
		t.out.CacheReadTokens, t.out.CacheCreateTokens = t.cost.tokens[2], t.cost.tokens[3]
		out.Turns = append(out.Turns, t.out)
	}
	out.Hash = HashOutput(out)
	return out, c
}

func (c *computation) applyCodexTurnAttributionState() {
	if !c.strictCodexTurnAttribution || !c.codexTurnAttributionUnknown {
		return
	}
	c.session.Requests, c.session.Inputs = nil, nil
	c.session.Detail.CodexTurnAttribution = &CodexTurnAttribution{
		State:  "undeterminable",
		Source: "session_records.timestamp+session_snapshots.codex_turns",
		Reason: "same_second_coarse_lifecycle_boundary",
	}
}

func (c *computation) copied(id int64) bool { return c.copies[id] }

// readClaude reads a Claude session's rows: the tree, the turns and what each
// turn's own lines say, the session's identity, and its activity. It returns
// the activity segments.
func (c *computation) readClaude() []Segment {
	rows := c.in.Rows
	for i := range rows {
		c.pulse()
		c.lines = append(c.lines, newLine(&rows[i], c.copied(rows[i].ID)))
	}
	return c.prepareClaude()
}

func (c *computation) prepareClaude() []Segment {
	var contributors []contributor
	var where latest
	s := &c.session
	s.Transcript = len(c.lines) > 0
	sort.Slice(c.lines, func(i, j int) bool { c.pulse(); return c.lines[i].before(c.lines[j]) })
	for _, l := range c.lines {
		c.pulse()
		contributors = append(contributors, contributor{l.UserID, l.ProfileEmail,
			AccountKey{LoginEmail: l.LoginEmail, BillingProvider: l.BillingProvider, AccountID: l.AccountID}})
		s.LoginEmailInferred = s.LoginEmailInferred || inferredLogin(l.LoginEmailSource)
		where.see(l.ProjectHash, l.RepositoryID, l.Branch)
	}
	c.identify(contributors)
	c.place(where)

	c.nodes, c.byUUID = buildForestChecked(c.lines, c.pulse)
	c.toolUses = make(map[string]toolUse)
	c.toolUseCount = make(map[string]int)
	for _, n := range c.nodes {
		c.pulse()
		for _, b := range n.blocks {
			c.pulse()
			if b.Type == "tool_use" && b.ID != "" {
				c.toolUses[b.ID] = toolUse{line: n, name: b.Name}
				c.toolUseCount[b.ID]++
			}
		}
	}

	boundaries := c.openTurns()
	if boundaries && !c.hasPreLineage() {
		c.readTurns()
	} else {
		// Missing lineage cannot place an old main-thread conversation row in
		// a turn. Redaction takes precedence when it also hides the input shape.
		// Tokens, cost and activity do not need turn boundaries.
		c.turns = nil
		for _, n := range c.nodes {
			c.pulse()
			n.turn = nil
		}
		s.TurnBoundaries, s.TurnBoundariesReason = "undeterminable", "pre_lineage"
		if !boundaries {
			s.TurnBoundariesReason = "redacted"
		}
	}
	return c.claudeActivity()
}

func (c *computation) hasPreLineage() bool {
	for _, l := range c.lines {
		c.pulse()
		if !l.IsSidechain && l.dated() && l.UUID == "" && (l.RecordType == "user" || l.RecordType == "assistant") {
			return true
		}
	}
	return false
}

// readTurns completes the turns openTurns opened and counts them.
func (c *computation) readTurns() {
	c.assignMembers()
	c.dropCopiedTurns()
	// A turn's key is its first line, which is its opening line unless a line
	// that followed an ancestor into it is stamped earlier. Start order is the
	// order of those first lines.
	sort.Slice(c.turns, func(i, j int) bool { return c.turns[i].members[0].before(c.turns[j].members[0]) })

	c.byPrompt = make(map[string][]*turn)
	for _, t := range c.turns {
		c.pulse()
		if t.promptID != "" {
			c.byPrompt[t.promptID] = append(c.byPrompt[t.promptID], t)
		}
	}
	var turns, requests, abandoned int
	s := &c.session
	for i, t := range c.turns {
		c.pulse()
		first, last := t.members[0], t.members[len(t.members)-1]
		o := &t.out
		o.TurnKey, o.Ordinal, o.OpenerUUID = first.UUID, i+1, t.opener.UUID
		o.StartedAt, o.EndedAt = first.TS, last.TS
		o.PromptID, o.PromptIDShared = t.promptID, len(c.byPrompt[t.promptID]) > 1
		o.LoginEmail = t.opener.LoginEmail
		o.Branch = "abandoned"
		if t.opener.active {
			o.Branch = "active"
		}
		t.readLines()
		c.readInputs(t)
		o.Status, o.StatusReason = t.state()
		o.IsRequest = t.isRequest()

		if o.Branch != "active" {
			abandoned++
			continue
		}
		turns++
		if o.IsRequest {
			requests++
			no := requests
			o.RequestNo = &no
		}
		s.LastTurnKey, s.LastTurnStatus = o.TurnKey, o.Status
	}
	inputs := c.numberInputs()
	// A prompt a person queued inside a turn is a request too, though it opened
	// no turn: the session can have more requests than request turns.
	for _, t := range c.turns {
		c.pulse()
		requests += t.out.Detail.ExtraInputs
	}
	s.TurnBoundaries = "observed"
	s.Turns, s.Requests, s.Inputs, s.AbandonedTurns = &turns, &requests, &inputs, &abandoned
}

// readCodex reads a Codex session's rows for its identity and activity range.
func (c *computation) readCodex() {
	rows := make([]*CodexRow, len(c.in.CodexRows))
	for i := range c.in.CodexRows {
		c.pulse()
		rows[i] = &c.in.CodexRows[i]
	}
	sort.Slice(rows, func(i, j int) bool {
		c.pulse()
		if !rows[i].TS.Equal(rows[j].TS) {
			return rows[i].TS.Before(rows[j].TS)
		}
		return rows[i].ID < rows[j].ID
	})
	var contributors []contributor
	var where latest
	s := &c.session
	s.Transcript = len(rows) > 0
	for _, r := range rows {
		c.pulse()
		contributors = append(contributors, contributor{r.UserID, r.ProfileEmail,
			AccountKey{LoginEmail: r.LoginEmail, BillingProvider: r.BillingProvider, AccountID: r.AccountID}})
		s.LoginEmailInferred = s.LoginEmailInferred || inferredLogin(r.LoginEmailSource)
		where.see(r.ProjectHash, r.RepositoryID, r.Branch)
	}
	c.identify(contributors)
	c.place(where)
	c.codexActivity()
}

// place sets where the session ran. The repository key is the stable grouping
// key of project_rules: the repository id, or the project hash without one.
func (c *computation) place(where latest) {
	s := &c.session
	s.ProjectHash, s.Branch, s.RepositoryKey = where.project, where.branch, where.repository
	if s.RepositoryKey == "" {
		s.RepositoryKey = s.ProjectHash
	}
}
