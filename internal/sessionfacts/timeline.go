package sessionfacts

import (
	"sort"
	"strings"
	"time"
)

// TimelineOutput is the opt-in result; Output remains the unchanged r2 contract.
type TimelineOutput struct {
	Output
	Timeline TimelineFacts
}

// TimelineFacts places every row the computation read in at most one turn. A
// row is placed only through an exact link -- its own turn, the parentUuid
// lineage to the line that opened it, a prompt id or tool call that names
// exactly one turn, a Codex usage row's own root turn id -- and otherwise says
// why not. Time proximity, the r2 choice of one row among duplicates and an id
// alone never place a row.
//
// State is "observed", "undeterminable" (Reason is the turn boundaries' reason
// and every row is unmapped) or "not_collected": a Codex row arrived without its
// uuid, so the rows cannot be named by their storage key.
type TimelineFacts struct {
	State, Reason string
	// Rows are one per input row, in storage key order (ts, uuid, id,
	// record_type, profile_email).
	Rows []TimelineRow
}

// TimelineRow is one source row: its storage key and id, then either the turn
// it belongs to with its roles there, or the reason it belongs to none.
type TimelineRow struct {
	TS                       time.Time
	UUID                     string
	ID                       int64
	RecordType, ProfileEmail string
	TurnKey                  string
	Roles                    []string
	Unmapped                 string
}

// The roles a placed row can play, in the order Roles lists them.
var timelineRoles = []string{
	"opener", "input", "queued", "answer", "assistant_final", "question", "question_result",
	"plan", "plan_result", "plan_attachment", "subagent", "copied",
}

// ComputeWithTimeline opts into the timeline mapping. The r2 worker continues
// to use Compute until its loader and publisher adopt a new version.
func ComputeWithTimeline(in Input) TimelineOutput {
	out, c := compute(in, false)
	extended := TimelineOutput{Output: out, Timeline: c.readTimeline()}
	extended.Hash = HashOutputWithTimeline(extended)
	return extended
}

// placement is where a row went: a turn, or the reason it has none.
type placement struct {
	turn   *turn
	reason string
}

func (p placement) mapped() bool { return p.turn != nil && p.reason == "" }

type timelineKey struct {
	ts                             int64
	recordType, profileEmail, uuid string
}

func (c *computation) readTimeline() TimelineFacts {
	if c.in.Agent == "codex" {
		return c.readCodexTimeline()
	}
	facts := TimelineFacts{State: "observed"}
	if c.session.TurnBoundaries != "observed" {
		facts.State, facts.Reason = "undeterminable", c.session.TurnBoundariesReason
	}

	// Rows a storage key or a copy mark cannot tell apart.
	keys := make(map[timelineKey]int)
	ids := make(map[int64]int)
	for _, l := range c.lines {
		c.pulse()
		keys[timelineKey{l.TS.UnixNano(), l.RecordType, l.ProfileEmail, l.UUID}]++
		ids[l.ID]++
	}
	ambiguousCopy := func(l *line) bool { return c.copied(l.ID) && ids[l.ID] > 1 }

	place := make(map[*line]placement, len(c.lines))
	if facts.State == "observed" {
		c.placeMain(place, ambiguousCopy)
		c.placeThreads(place)
	}
	roles := c.timelineRoles(place)

	for _, l := range c.lines {
		c.pulse()
		p := place[l]
		switch {
		case keys[timelineKey{l.TS.UnixNano(), l.RecordType, l.ProfileEmail, l.UUID}] > 1:
			p = placement{reason: "duplicate_source_tuple"}
		case ambiguousCopy(l):
			p = placement{reason: "ambiguous_copy_id"}
		case facts.State != "observed":
			p = placement{reason: "turn_boundaries_" + facts.Reason}
		}
		row := TimelineRow{TS: l.TS, UUID: l.UUID, ID: l.ID, RecordType: l.RecordType, ProfileEmail: l.ProfileEmail}
		if p.mapped() {
			row.TurnKey = p.turn.out.TurnKey
			for i, role := range timelineRoles {
				c.pulse()
				if roles[l]&(1<<i) != 0 {
					row.Roles = append(row.Roles, role)
				}
			}
		} else {
			row.Unmapped = p.reason
		}
		facts.Rows = append(facts.Rows, row)
	}
	sortTimelineRowsChecked(facts.Rows, c.pulse)
	return facts
}

// sortTimelineRows puts rows in storage key order.
func sortTimelineRowsChecked(rows []TimelineRow, checkpoint func()) {
	sort.Slice(rows, func(i, j int) bool {
		checkpoint()
		a, b := rows[i], rows[j]
		switch {
		case !a.TS.Equal(b.TS):
			return a.TS.Before(b.TS)
		case a.UUID != b.UUID:
			return a.UUID < b.UUID
		case a.ID != b.ID:
			return a.ID < b.ID
		case a.RecordType != b.RecordType:
			return a.RecordType < b.RecordType
		}
		return a.ProfileEmail < b.ProfileEmail
	})
}

// readCodexTimeline places a Codex session's usage rows by their own root turn
// id (payload.root_turn_id, or turn_id raised by the snapshot lifecycle), as
// readCodexTurns does, but only when every snapshot and usage row that names
// the turn id names the same root. User rows are placed by time in r2 and
// other rows carry no turn, so neither is placed here; row ids and codexByHead
// are never used.
func (c *computation) readCodexTimeline() TimelineFacts {
	for i := range c.in.CodexRows {
		c.pulse()
		if c.in.CodexRows[i].UUID == "" {
			return TimelineFacts{State: "not_collected", Reason: "codex_uuid_not_loaded"}
		}
	}
	facts := TimelineFacts{State: "observed"}
	keys := make(map[timelineKey]int)
	ids := make(map[int64]int)
	for i := range c.in.CodexRows {
		c.pulse()
		r := &c.in.CodexRows[i]
		keys[timelineKey{r.TS.UnixNano(), r.RecordType, r.ProfileEmail, r.UUID}]++
		ids[r.ID]++
	}
	// The roots every source gives each turn id: each snapshot lifecycle, where
	// a turn of its own is its root, and each usage row's explicit root, which
	// is also a root of its own. Rows that repeat one root agree.
	// lifecycle is the snapshots' part alone: what readCodexTurns raises a
	// turn id by.
	roots, lifecycle := make(map[string]map[string]bool), make(map[string]map[string]bool)
	claim := func(set map[string]map[string]bool, id, root string) {
		if set[id] == nil {
			set[id] = make(map[string]bool)
		}
		set[id][root] = true
	}
	for _, snap := range c.in.Snapshots {
		c.pulse()
		for _, item := range snap.CodexTurns {
			c.pulse()
			if item.TurnID == "" {
				continue
			}
			root := item.RootTurnID
			if root == "" {
				root = item.TurnID
			}
			claim(roots, item.TurnID, root)
			claim(lifecycle, item.TurnID, root)
		}
	}
	for i := range c.in.CodexRows {
		c.pulse()
		r := &c.in.CodexRows[i]
		collectedRoot, collectedTurn := codexRowRootTurnID(r), codexRowTurnID(r)
		if c.strictCodexTurnAttribution && collectedRoot != "" {
			claim(roots, collectedRoot, collectedRoot)
			if collectedTurn != "" {
				claim(roots, collectedTurn, collectedRoot)
			}
		}
		if r.RecordType == "usage" && r.RootTurnID != "" {
			claim(roots, r.RootTurnID, r.RootTurnID)
			if r.TurnID != "" {
				claim(roots, r.TurnID, r.RootTurnID)
			}
		}
	}
	// A source that knows id gives a root other than want.
	disagrees := func(id, want string) bool {
		for root := range roots[id] {
			c.pulse()
			if root != want {
				return true
			}
		}
		return false
	}
	turns := make(map[string]*turn)
	for _, t := range c.turns {
		c.pulse()
		if !strings.HasPrefix(t.out.TurnKey, "cx:") {
			turns[t.out.TurnKey] = t
		}
	}
	for i := range c.in.CodexRows {
		c.pulse()
		r := &c.in.CodexRows[i]
		row := TimelineRow{TS: r.TS, UUID: r.UUID, ID: r.ID, RecordType: r.RecordType, ProfileEmail: r.ProfileEmail}
		rowRoot, rowTurn := codexRowRootTurnID(r), codexRowTurnID(r)
		if !c.strictCodexTurnAttribution {
			rowRoot, rowTurn = r.RootTurnID, r.TurnID
		}
		root, reason := "", ""
		switch {
		case keys[timelineKey{r.TS.UnixNano(), r.RecordType, r.ProfileEmail, r.UUID}] > 1:
			reason = "duplicate_source_tuple"
		case c.copied(r.ID) && ids[r.ID] > 1:
			reason = "ambiguous_copy_id"
		case c.copied(r.ID):
			reason = "copied_row"
		case c.strictCodexTurnAttribution && r.CodexTurnMetadataKnown:
			switch {
			case rowRoot != "":
				root = rowRoot
			case rowTurn != "" && len(roots[rowTurn]) > 1:
				reason = "ambiguous_root"
			case rowTurn != "":
				root = rowTurn
				for only := range roots[rowTurn] {
					c.pulse()
					root = only
				}
			default:
				reason = "codex_no_turn_id"
			}
			if reason == "" && rowTurn != "" && disagrees(rowTurn, root) {
				reason = "conflicting_evidence"
			}
		case c.strictCodexTurnAttribution && r.RecordType != "usage" && codexHuman(r) && !codexRowHasTurnID(r) && codexTimeBoundaryAmbiguous(c.turns, r.TS):
			c.codexTurnAttributionUnknown = true
			reason = "codex_same_second_lifecycle_boundary"
		case r.RecordType != "usage" && codexHuman(r):
			reason = "codex_time_interval"
		case r.RecordType != "usage":
			reason = "codex_not_usage"
		case rowRoot != "":
			root = rowRoot
			if rowTurn != "" && disagrees(rowTurn, root) {
				reason = "conflicting_evidence"
			}
		case rowTurn != "" && len(roots[rowTurn]) > 1:
			reason = "ambiguous_root"
		case rowTurn != "":
			// r2 raises the turn id by the lifecycle alone; the other rows
			// must name the root it then counts the row in.
			root = rowTurn
			counted := rowTurn
			for only := range roots[rowTurn] {
				c.pulse()
				root = only
			}
			for only := range lifecycle[rowTurn] {
				c.pulse()
				counted = only
			}
			if root != counted {
				reason = "conflicting_evidence"
			}
		default:
			reason = "codex_no_turn_id"
		}
		// The root must not itself belong to another root in any snapshot.
		if reason == "" && disagrees(root, root) {
			reason = "conflicting_evidence"
		}
		if t := turns[root]; reason == "" && t != nil {
			row.TurnKey = t.out.TurnKey
		} else {
			if reason == "" {
				reason = "no_turn"
			}
			row.Unmapped = reason
		}
		facts.Rows = append(facts.Rows, row)
	}
	sortTimelineRowsChecked(facts.Rows, c.pulse)
	return facts
}

// placeMain places the main-thread rows: a node through its own turn or its
// lineage, then the rows lineage could not place through an independent
// prompt id or tool call that names the same turn r2 gave them.
func (c *computation) placeMain(place map[*line]placement, ambiguousCopy func(*line) bool) {
	uuids := make(map[string]int)
	for _, l := range c.lines {
		c.pulse()
		if nodeCandidate(l) {
			uuids[l.UUID]++
		}
	}
	dropped := make(map[*line]string)
	for _, t := range c.dropped {
		c.pulse()
		reason := "copied_turn"
		for _, m := range t.members {
			c.pulse()
			if ambiguousCopy(m) {
				reason = "ambiguous_copy_id"
			}
		}
		for _, m := range t.members {
			c.pulse()
			dropped[m] = reason
		}
	}
	// A line's placement can rest on its parent's, so they are worked out on
	// demand; the parent links are a forest, which bounds the recursion.
	ancestors := make(map[*line]*line)
	ancestor := func(l *line) *line {
		return nearestLineageAncestor(l, uuids, ancestors, c.pulse)
	}
	var placeOf func(*line) placement
	placeOf = func(l *line) placement {
		if p, ok := place[l]; ok {
			return p
		}
		p := c.lineage(l, uuids, dropped, placeOf, ancestor)
		place[l] = p
		return p
	}
	for _, l := range c.lines {
		c.pulse()
		if !l.IsSidechain {
			placeOf(l)
		}
	}
	// Evidence is read from the lineage placements only, so one restored row
	// never vouches for another and the result does not depend on row order.
	exact := make(map[*line]bool)
	for l, p := range place {
		c.pulse()
		exact[l] = p.mapped()
	}
	for _, l := range c.lines {
		c.pulse()
		p := place[l]
		if l.IsSidechain || (p.reason != "ambiguous_lineage" && p.reason != "ts_fallback" && p.reason != "ambiguous_prompt") {
			continue
		}
		switch t, conflict := c.evidence(l, exact); {
		case conflict, t != nil && t != l.turn:
			place[l] = placement{reason: "conflicting_evidence"}
		case t != nil:
			place[l] = placement{turn: t}
		}
	}
}

// nodeCandidate reports whether buildForest would consider the line for a node.
func nodeCandidate(l *line) bool {
	if l.IsSidechain || !l.keyed() || !l.dated() {
		return false
	}
	switch l.RecordType {
	case "user", "assistant", "system", "attachment":
		return true
	}
	return false
}

// lineage places a line through its own turn or the parentUuid path to the line
// that placed it. A line that joined a turn by a prompt id several turns share
// (rule 1-b) is placed only when its parent is placed in that same turn.
func (c *computation) lineage(l *line, uuids map[string]int, dropped map[*line]string, placeOf func(*line) placement, ancestor func(*line) *line) placement {
	switch {
	case !l.dated():
		return placement{reason: "undated"}
	case !l.keyed():
		return placement{reason: "unkeyed"}
	case !nodeCandidate(l):
		return placement{reason: "non_conversation_type"}
	case uuids[l.UUID] > 1 || c.byUUID[l.UUID] != l:
		return placement{reason: "ambiguous_uuid"}
	case l.turn == nil && dropped[l] != "":
		return placement{reason: dropped[l]}
	case l.turn == nil:
		return placement{reason: "no_turn"}
	case l.fallback:
		return placement{reason: "ts_fallback"}
	case l.direct && (l.turn.opener == l || len(c.byPrompt[l.PromptID]) <= 1):
		return placement{turn: l.turn}
	case l.direct:
		if l.parent == nil || uuids[l.parent.UUID] > 1 {
			return placement{reason: "ambiguous_prompt"}
		}
		switch p := placeOf(l.parent); {
		case !p.mapped():
			return placement{reason: "ambiguous_prompt"}
		case p.turn != l.turn:
			return placement{reason: "conflicting_evidence"}
		}
		return placement{turn: l.turn}
	}
	if p := ancestor(l.parent); p != nil && uuids[p.UUID] <= 1 && p.direct {
		if placeOf(p).mapped() && p.turn == l.turn {
			return placement{turn: l.turn}
		}
	}
	return placement{reason: "ambiguous_lineage"}
}

// nearestLineageAncestor stops at exactly the same direct/ambiguous barrier as
// the parent walk. Cache nil results too: a long single turn or an unplaced
// component must not walk its entire prefix again for each source row.
func nearestLineageAncestor(l *line, uuids map[string]int, cache map[*line]*line, checkpoint func()) *line {
	var path []*line
	for l != nil {
		checkpoint()
		if found, ok := cache[l]; ok {
			l = found
			break
		}
		if uuids[l.UUID] > 1 || l.direct {
			cache[l] = l
			break
		}
		path = append(path, l)
		l = l.parent
	}
	for _, node := range path {
		checkpoint()
		cache[node] = l
	}
	return l
}

// evidence is the one turn a line's own prompt id or tool results name, through
// a prompt id only one turn has or a tool_use carried by exactly one node that
// lineage placed. conflict says two of them name different turns.
func (c *computation) evidence(l *line, exact map[*line]bool) (found *turn, conflict bool) {
	see := func(t *turn) {
		if found == nil {
			found = t
		} else if found != t {
			conflict = true
		}
	}
	if sole := c.byPrompt[l.PromptID]; l.PromptID != "" && len(sole) == 1 {
		see(sole[0])
	}
	for _, b := range l.blocks {
		c.pulse()
		if b.Type != "tool_result" || b.ToolUseID == "" || c.toolUseCount[b.ToolUseID] != 1 {
			continue
		}
		if use := c.toolUses[b.ToolUseID].line; exact[use] {
			see(use.turn)
		}
	}
	return found, conflict
}

// placeThreads places each subagent thread by the methods of attributeThreads,
// in its order, but only through a prompt id one turn has, a tool_use one node
// carries or an agent id one node names, each anchored on an exactly placed
// line. The turn must also be the one r2 attributed the thread to.
func (c *computation) placeThreads(place map[*line]placement) {
	type thread struct {
		firstUser          *line
		toolUseID, agentID string
		lines              []*line
	}
	threads := make(map[string]*thread)
	for _, l := range c.lines {
		c.pulse()
		if !l.IsSidechain {
			continue
		}
		key := sidechainKey(l.SourceFile, l.AgentID)
		if key == "" {
			place[l] = placement{reason: "sidechain_unkeyed"}
			continue
		}
		th := threads[key]
		if th == nil {
			th = &thread{}
			threads[key] = th
		}
		th.lines = append(th.lines, l)
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
	naming := make(map[string][]*line)
	for _, n := range c.nodes {
		c.pulse()
		seen := map[string]bool{}
		for _, id := range n.resultAgents() {
			c.pulse()
			if id != "" && !seen[id] {
				seen[id] = true
				naming[id] = append(naming[id], n)
			}
		}
	}
	placed := c.attributeThreads()
	for key, th := range threads {
		c.pulse()
		var t *turn
		reason := "thread_unattributed"
		if th.firstUser != nil {
			if sole := c.byPrompt[th.firstUser.PromptID]; len(sole) == 1 {
				t = sole[0]
			}
		}
		if t == nil && th.toolUseID != "" {
			switch n := c.toolUseCount[th.toolUseID]; {
			case n > 1:
				reason = "ambiguous_tool_use"
			case n == 1 && place[c.toolUses[th.toolUseID].line].mapped():
				t = c.toolUses[th.toolUseID].line.turn
			}
		}
		if t == nil && th.agentID != "" {
			switch nodes := naming[th.agentID]; {
			case len(nodes) > 1:
				if reason == "thread_unattributed" {
					reason = "ambiguous_agent_id"
				}
			case len(nodes) == 1 && place[nodes[0]].mapped():
				t = nodes[0].turn
			}
		}
		p := placement{turn: t}
		switch {
		case t == nil:
			p = placement{reason: reason}
		case t != placed[key]:
			p = placement{reason: "conflicting_evidence"}
		}
		for _, l := range th.lines {
			c.pulse()
			place[l] = p
		}
	}
}

// timelineRoles is a bit per timelineRoles entry for each line. The input,
// queued and final roles are the turn's own reads. An answer and a question or
// plan result also need their call: the one row in their source file that
// carries that tool_use id, of any tool, and the one node carrying it anywhere,
// placed exactly. A plan attachment needs a parent holding one plan call.
func (c *computation) timelineRoles(place map[*line]placement) map[*line]int {
	roles := make(map[*line]int)
	add := func(l *line, role string) {
		for i, r := range timelineRoles {
			c.pulse()
			if r == role && l != nil {
				roles[l] |= 1 << i
			}
		}
	}
	type callKey struct{ file, id string }
	calls := make(map[callKey][]*line)
	for _, l := range c.lines {
		c.pulse()
		for _, b := range l.blocks {
			c.pulse()
			if b.Type == "tool_use" && b.ID != "" {
				calls[callKey{l.SourceFile, b.ID}] = append(calls[callKey{l.SourceFile, b.ID}], l)
			}
		}
	}
	// exactCall is the main-thread call of tool that the id names in file, or
	// nil when anything else could be it.
	exactCall := func(file, id, tool string) *line {
		ls := calls[callKey{file, id}]
		if len(ls) != 1 || c.toolUseCount[id] != 1 {
			return nil
		}
		call := ls[0]
		if call.IsSidechain || call.copied || !place[call].mapped() {
			return nil
		}
		for _, b := range call.blocks {
			c.pulse()
			if b.Type == "tool_use" && b.ID == id && b.Name == tool {
				return call
			}
		}
		return nil
	}

	for _, t := range c.turns {
		c.pulse()
		add(t.opener, "opener")
		add(t.input, "input")
		add(t.lastAssistant, "assistant_final")
		for _, in := range t.inputs {
			c.pulse()
			if in.line.RecordType == "attachment" {
				add(in.line, "queued")
				continue
			}
			for _, b := range in.line.blocks {
				c.pulse()
				if in.kind == "answer" && b.Type == "tool_result" && string(b.Err) != "true" &&
					exactCall(in.line.SourceFile, b.ToolUseID, "AskUserQuestion") != nil {
					add(in.line, "answer")
				}
			}
		}
	}

	// A result answers only a call read before it, as readQuestionsAndPlans
	// reads them; a question is read only on the active path.
	main := func(l *line) bool { return !l.copied && !l.IsSidechain }
	planLine := map[string][]string{} // a plan line's uuid -> its plan ids, one per row holding it
	for _, l := range c.lines {
		c.pulse()
		if !main(l) {
			continue
		}
		for _, b := range l.blocks {
			c.pulse()
			switch {
			case b.Type == "tool_use" && b.Name == "AskUserQuestion" && b.ID != "" && l.active:
				add(l, "question")
			case b.Type == "tool_use" && b.Name == "ExitPlanMode" && b.ID != "":
				add(l, "plan")
				planLine[l.UUID] = append(planLine[l.UUID], b.ID)
			case b.Type == "tool_result" && l.active:
				if q := exactCall(l.SourceFile, b.ToolUseID, "AskUserQuestion"); q != nil && q.active && q.before(l) {
					add(l, "question_result")
				}
				if p := exactCall(l.SourceFile, b.ToolUseID, "ExitPlanMode"); p != nil && p.before(l) {
					add(l, "plan_result")
				}
			}
		}
	}
	attachments := map[string][]*line{}
	for _, l := range c.lines {
		c.pulse()
		if main(l) && l.attachmentKind() == "plan_file_reference" {
			if len(planLine[l.ParentUUID]) == 1 {
				attachments[l.ParentUUID] = append(attachments[l.ParentUUID], l)
			}
		}
	}
	for _, ls := range attachments {
		c.pulse()
		if len(ls) == 1 {
			add(ls[0], "plan_attachment")
		}
	}

	for _, l := range c.lines {
		c.pulse()
		if l.IsSidechain && place[l].mapped() {
			add(l, "subagent")
		}
		if l.copied {
			add(l, "copied")
		}
	}
	return roles
}
