package sessionfacts

import "sort"

// turn is one turn of a Claude session while it is being worked out.
type turn struct {
	checkpoint func()
	codexInput *CodexRow
	opener     *line
	promptID   string
	// command says a command line carrying the turn's prompt id is in it: the
	// next human text with that id then opens a turn of its own (rule 1-b).
	command bool
	// members are the turn's main-thread lines in (ts, uuid) order, abandoned
	// tails included; read are the ones its state and inputs are read on.
	members []*line
	read    []*line
	// input is the turn's input line, a copy or not; inputs are what counts.
	input  *line
	inputs []*input

	// lastAssistant is the last assistant line state read the turn's end on.
	lastAssistant *line

	cost costSum
	out  Turn

	startedAtSource string
	endedAtSource   string
}

// openTurns walks the main-thread user lines in (ts, uuid) order and opens the
// turns (p1b-design §3.5). It reports false when the session's turn boundaries
// cannot be determined.
//
// A line with a promptId opens a turn the first time the id is seen, and joins
// that id's turn whenever it comes back, even after another turn. A line
// without one is a pre-P0 line and opens a turn by what it is (line.isInput).
//
// Rule 1-b splits one id in two. A slash command and what the person types next
// can share a promptId: 39 of 445 local command lines were followed by human
// text under their own id, most of it more than a minute later (A-22). Left
// together, the command's turn swallows the next request -- its preview becomes
// the command's name, and its reply and the request are counted as one. So
// human text after a command line of the same id opens a new turn, and later
// lines of that id join the newest.
func (c *computation) openTurns() bool {
	last := make(map[string]*turn) // the newest turn of each prompt id
	var blind bool
	for _, l := range c.lines {
		c.pulse()
		if !l.IsSidechain && l.dated() && l.RecordType == "user" && l.PromptID == "" && l.columnJudged() {
			blind = true
		}
	}
	for _, u := range c.nodes {
		c.pulse()
		if u.RecordType != "user" {
			continue
		}
		if u.PromptID == "" {
			if u.isInput() {
				c.open(u)
			}
			continue
		}
		t := last[u.PromptID]
		if t == nil || (t.command && u.isHumanText()) {
			t = c.open(u)
			last[u.PromptID] = t
		} else {
			u.turn, u.direct = t, true
		}
		t.command = t.command || u.isCommand()
	}
	// A marker line with no promptId is typed text or a tool result, and only
	// prompt_source can say which. A session where no user line carries one, or
	// a command column, was collected by a client that wrote neither: nothing
	// in it separates the two, so no turn is opened at all. A marker line with a
	// promptId never gets here; rule 1 placed it.
	if !blind {
		return true
	}
	for _, l := range c.lines {
		c.pulse()
		if l.RecordType == "user" && (l.PromptSource != "" || l.isCommand()) {
			return true
		}
	}
	return false
}

func (c *computation) open(opener *line) *turn {
	t := &turn{opener: opener, promptID: opener.PromptID, checkpoint: c.pulse}
	opener.turn, opener.direct = t, true
	c.turns = append(c.turns, t)
	return t
}

// assignMembers gives every main-thread line its turn (p1b-design §3.5 rule 4).
// A line that opened no turn and carries no prompt id follows its nearest
// ancestor that does. Where no ancestor has a turn -- the lines after a
// newer-format compaction of pre-P0 rows, which start a component of their own
// -- it joins the latest turn opened at or before its own ts, and a line with
// no such turn belongs to none: it still counts as activity.
func (c *computation) assignMembers() {
	for _, n := range c.nodes {
		c.pulse()
		var path []*line
		m := n
		for ; m != nil && !m.direct && !m.walked; m = m.parent {
			c.pulse()
			path = append(path, m)
		}
		var t *turn
		if m != nil {
			t = m.turn
		}
		for _, p := range path {
			c.pulse()
			p.turn, p.walked = t, true
		}
	}
	// c.turns is in the order the turns were opened, which is (ts, uuid) order
	// of their opening lines. The fallback runs after every walk above, so no
	// walk mistakes a turn assigned here for an ancestor's.
	for _, n := range c.nodes {
		c.pulse()
		if n.turn == nil {
			i := sort.Search(len(c.turns), func(i int) bool { return c.turns[i].opener.TS.After(n.TS) })
			if i > 0 {
				n.turn, n.fallback = c.turns[i-1], true
			}
		}
		if n.turn != nil {
			n.turn.members = append(n.turn.members, n)
		}
	}
}

// dropCopiedTurns removes the turns that are another session's whole: with the
// copy predicate on, a turn whose every line is a copy has no row, and its
// lines belong to no turn (p1b-design §3.16). A turn copied in part stays,
// marked, and is no request.
func (c *computation) dropCopiedTurns() {
	kept := c.turns[:0]
	for _, t := range c.turns {
		c.pulse()
		copied := 0
		for _, m := range t.members {
			if t.checkpoint != nil {
				t.checkpoint()
			}
			c.pulse()
			if m.copied {
				copied++
			}
		}
		if copied == len(t.members) {
			for _, m := range t.members {
				if t.checkpoint != nil {
					t.checkpoint()
				}
				c.pulse()
				m.turn = nil
			}
			c.dropped = append(c.dropped, t)
			continue
		}
		t.out.OpenerCopied = copied > 0
		kept = append(kept, t)
	}
	c.turns = kept
}

// readLines picks the lines a turn's state and inputs are read on: for an
// active turn its lines on the active path, so an abandoned tail inside it
// counts for tokens and cost but decides nothing (p1b-design §3.5 rule 6). An
// abandoned turn has no active path to be read on, and is read on all of its
// lines.
func (t *turn) readLines() {
	for _, m := range t.members {
		if t.checkpoint != nil {
			t.checkpoint()
		}
		if m.active || !t.opener.active {
			t.read = append(t.read, m)
		}
	}
}
