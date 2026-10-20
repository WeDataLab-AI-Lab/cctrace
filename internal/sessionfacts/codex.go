package sessionfacts

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// codexTurnOrder breaks equally observed snapshot ties by their normalized
// lifecycle fields, not the order the input rows happened to arrive in.
func codexTurnOrder(a, b SnapshotCodexTurn) int {
	type lifecycleOrder struct {
		TurnID, RootTurnID string
		StartedAt, EndedAt time.Time
		End                string
		ContextWindow      *int
		TimestampsExact    bool
	}
	left, _ := json.Marshal(lifecycleOrder{a.TurnID, a.RootTurnID, a.StartedAt, a.EndedAt, a.End, a.ContextWindow, a.TimestampsExact})
	right, _ := json.Marshal(lifecycleOrder{b.TurnID, b.RootTurnID, b.StartedAt, b.EndedAt, b.End, b.ContextWindow, b.TimestampsExact})
	return strings.Compare(string(left), string(right))
}

// readCodexTurns groups usage by root turn and uses the P0d lifecycle for the
// status of each root. Child usage belongs to its root, never a second turn.
func (c *computation) readCodexTurns() {
	rows := make([]*CodexRow, 0, len(c.in.CodexRows))
	for i := range c.in.CodexRows {
		c.pulse()
		rows = append(rows, &c.in.CodexRows[i])
	}
	sort.Slice(rows, func(i, j int) bool {
		c.pulse()
		if !rows[i].TS.Equal(rows[j].TS) {
			return rows[i].TS.Before(rows[j].TS)
		}
		return rows[i].ID < rows[j].ID
	})
	life := make(map[string]SnapshotCodexTurn)
	observed := make(map[string]time.Time)
	for _, snap := range c.in.Snapshots {
		c.pulse()
		for _, item := range snap.CodexTurns {
			c.pulse()
			if item.TurnID == "" {
				continue
			}
			previous, exists := life[item.TurnID]
			if !exists || snap.ObservedAt.After(observed[item.TurnID]) || (snap.ObservedAt.Equal(observed[item.TurnID]) && codexTurnOrder(item, previous) > 0) {
				life[item.TurnID] = item
				observed[item.TurnID] = snap.ObservedAt
			}
		}
	}
	roots := make(map[string]*turn)
	c.codexByHead = make(map[int64]*turn)
	for _, r := range rows {
		c.pulse()
		if r.RecordType != "usage" || c.copied(r.ID) {
			continue
		}
		root, turnID := r.RootTurnID, r.TurnID
		if c.strictCodexTurnAttribution {
			root, turnID = codexRowRootTurnID(r), codexRowTurnID(r)
		}
		if root == "" {
			root = turnID
			if item, ok := life[root]; ok && item.RootTurnID != "" {
				root = item.RootTurnID
			}
		}
		if root == "" {
			continue
		}
		t := roots[root]
		if t == nil {
			t = &turn{}
			t.out.TurnKey, t.out.Branch = root, "active"
			t.out.StartedAt, t.out.EndedAt = r.TS, r.TS
			t.out.ContextSource = "unknown"
			roots[root] = t
			c.turns = append(c.turns, t)
		}
		if r.TS.Before(t.out.StartedAt) {
			t.out.StartedAt = r.TS
		}
		if r.TS.After(t.out.EndedAt) {
			t.out.EndedAt = r.TS
		}
		c.codexByHead[r.ID] = t
	}
	// A lifecycle may exist before the first usage row is written.
	for id, item := range life {
		c.pulse()
		if item.RootTurnID != "" || id == "" || roots[id] != nil {
			continue
		}
		t := &turn{}
		t.out.TurnKey, t.out.Branch, t.out.StartedAt, t.out.EndedAt = id, "active", item.StartedAt, item.StartedAt
		t.startedAtSource, t.endedAtSource = item.StartedAtSource, item.EndedAtSource
		if item.TimestampsExact {
			t.startedAtSource, t.endedAtSource = "line", "line"
		}
		t.out.ContextSource = "unknown"
		roots[id] = t
		c.turns = append(c.turns, t)
	}
	for id, t := range roots {
		c.pulse()
		if item, ok := life[id]; ok {
			if !item.StartedAt.IsZero() {
				t.out.StartedAt = item.StartedAt
				t.startedAtSource = item.StartedAtSource
				if item.TimestampsExact {
					t.startedAtSource = "line"
				}
			}
			if !item.EndedAt.IsZero() {
				t.out.EndedAt = item.EndedAt
				t.endedAtSource = item.EndedAtSource
				if item.TimestampsExact {
					t.endedAtSource = "line"
				}
			}
			switch item.End {
			case "complete":
				t.out.Status = "ended"
			case "error":
				t.out.Status = "error"
			case "aborted":
				t.out.Status = "interrupted"
			case "":
				if c.in.ReadAt.IsZero() {
					t.out.Status, t.out.StatusReason = "undeterminable", "no_read_time"
				} else if c.in.ReadAt.Sub(t.out.EndedAt) > 24*time.Hour {
					t.out.Status, t.out.StatusReason = "undeterminable", "no_end_line"
				} else {
					t.out.Status = "open"
				}
			default:
				t.out.Status, t.out.StatusReason = "undeterminable", "no_lifecycle"
			}
		} else {
			t.out.Status, t.out.StatusReason = "undeterminable", "no_lifecycle"
		}
	}
	prefixEnd := c.codexPrefixEnd()
	orderedRoots := make([]*turn, 0, len(roots))
	for _, t := range roots {
		c.pulse()
		orderedRoots = append(orderedRoots, t)
	}
	sort.Slice(orderedRoots, func(i, j int) bool {
		if !orderedRoots[i].out.StartedAt.Equal(orderedRoots[j].out.StartedAt) {
			return orderedRoots[i].out.StartedAt.Before(orderedRoots[j].out.StartedAt)
		}
		return orderedRoots[i].out.TurnKey < orderedRoots[j].out.TurnKey
	})
	// Without a lifecycle start, only the last human input before the first
	// usage can be inferred as its opener. Earlier inputs are legacy turns.
	var inferredOpener *CodexRow
	if len(orderedRoots) > 0 && life[orderedRoots[0].out.TurnKey].StartedAt.IsZero() {
		for _, r := range rows {
			c.pulse()
			if r.TS.After(orderedRoots[0].out.StartedAt) {
				break
			}
			hasTurnID := r.TurnID != "" || r.RootTurnID != ""
			if c.strictCodexTurnAttribution {
				hasTurnID = codexRowHasTurnID(r) || r.CodexTurnMetadataKnown
			}
			if codexHuman(r) && r.TS.After(prefixEnd) && !hasTurnID {
				inferredOpener = r
			}
		}
		if inferredOpener != nil {
			orderedRoots[0].out.StartedAt = inferredOpener.TS
		}
	}
	// Rows outside a known root's interval are still human inputs from older
	// rollout formats, even when this session also has root-aware records.
	for _, r := range rows {
		c.pulse()
		hasTurnID := r.TurnID != "" || r.RootTurnID != ""
		if c.strictCodexTurnAttribution {
			hasTurnID = codexRowHasTurnID(r)
		}
		if !codexHuman(r) || hasTurnID || !r.TS.After(prefixEnd) || r == inferredOpener {
			continue
		}
		if c.strictCodexTurnAttribution && r.CodexTurnMetadataKnown {
			// This row was observed outside an active lifecycle root.
		} else if c.strictCodexTurnAttribution && codexTimeBoundaryAmbiguous(c.turns, r.TS) {
			c.codexTurnAttributionUnknown = true
			continue
		}
		var root *turn
		if !c.strictCodexTurnAttribution || (!r.CodexTurnMetadataKnown && !codexTimeBoundaryAmbiguous(c.turns, r.TS)) {
			for i, candidate := range orderedRoots {
				c.pulse()
				if r.TS.Before(candidate.out.StartedAt) {
					break
				}
				if i+1 < len(orderedRoots) && !r.TS.Before(orderedRoots[i+1].out.StartedAt) {
					continue
				}
				if end := life[candidate.out.TurnKey].EndedAt; end.IsZero() || r.TS.Before(end) {
					root = candidate
				}
				break
			}
		}
		if root != nil {
			continue
		}
		t := &turn{}
		t.out.TurnKey, t.out.Branch, t.out.InputKind = "cx:"+r.TS.UTC().Format(time.RFC3339Nano), "active", "typed"
		t.out.StartedAt, t.out.EndedAt = r.TS, r.TS
		t.codexInput = r
		t.out.Preview = &r.TextHead
		t.out.PreviewState, t.out.Status, t.out.StatusReason, t.out.ContextSource = "ok", "undeterminable", "no_lifecycle", "unknown"
		c.turns = append(c.turns, t)
	}
	sort.Slice(c.turns, func(i, j int) bool {
		a, b := c.turns[i].out, c.turns[j].out
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.Before(b.StartedAt)
		}
		return a.TurnKey < b.TurnKey
	})
	var requestNo, inputNo int
	var entrypoint string
	for _, r := range rows {
		c.pulse()
		if r.Entrypoint != "" {
			entrypoint = r.Entrypoint
		}
	}
	for i, t := range c.turns {
		c.pulse()
		o := &t.out
		o.Ordinal = i + 1
		end := time.Time{}
		if i+1 < len(c.turns) {
			end = c.turns[i+1].out.StartedAt
		}
		var first *CodexRow
		startedFromLifecycle := !life[o.TurnKey].StartedAt.IsZero()
		for _, r := range rows {
			c.pulse()
			if !c.strictCodexTurnAttribution {
				if !codexHuman(r) || !r.TS.After(prefixEnd) ||
					((i > 0 || startedFromLifecycle) && r.TS.Before(o.StartedAt)) || (!end.IsZero() && !r.TS.Before(end)) ||
					(len(roots) == 0 && !r.TS.Equal(o.StartedAt)) || (len(roots) != 0 && strings.HasPrefix(o.TurnKey, "cx:") && !r.TS.Equal(o.StartedAt)) {
					continue
				}
				if rootEnd := life[o.TurnKey].EndedAt; !rootEnd.IsZero() && !r.TS.Before(rootEnd) {
					continue
				}
			} else {
				if !codexHuman(r) || !r.TS.After(prefixEnd) {
					continue
				}
				rowRoot := codexRowRootTurnID(r)
				rowTurnID := codexRowTurnID(r)
				if rowRoot == "" && rowTurnID != "" {
					if item, ok := life[rowTurnID]; ok && item.RootTurnID != "" {
						rowRoot = item.RootTurnID
					} else {
						rowRoot = rowTurnID
					}
				}
				if rowRoot != "" {
					if rowRoot != o.TurnKey {
						continue
					}
				} else if strings.HasPrefix(o.TurnKey, "cx:") {
					if !r.TS.Equal(o.StartedAt) {
						continue
					}
				} else {
					ambiguousTime := r != inferredOpener && codexTimeBoundaryAmbiguous(c.turns, r.TS)
					if ambiguousTime {
						c.codexTurnAttributionUnknown = true
					}
					if r.CodexTurnMetadataKnown || ambiguousTime ||
						((i > 0 || startedFromLifecycle) && r.TS.Before(o.StartedAt)) || (!end.IsZero() && !r.TS.Before(end)) ||
						(len(roots) == 0 && !r.TS.Equal(o.StartedAt)) {
						continue
					}
					if rootEnd := life[o.TurnKey].EndedAt; !rootEnd.IsZero() && !r.TS.Before(rootEnd) {
						continue
					}
				}
			}
			if first == nil {
				first = r
			}
			inputNo++
			kind := "typed"
			if codexCommand(r) {
				kind = "command"
			}
			o.Detail.Inputs = append(o.Detail.Inputs, TurnInput{TS: r.TS, Kind: kind, N: inputNo})
		}
		if first != nil {
			t.codexInput = first
			o.LoginEmail = first.LoginEmail
			o.InputKind = "typed"
			p := first.TextHead
			if codexCommand(first) && first.CommandName != "" {
				p = "/" + first.CommandName
			}
			o.Preview, o.PreviewState = &p, "ok"
			if codexCommand(first) {
				o.InputKind = "command"
			}
			if entrypoint == "" || entrypoint == "cli" {
				if o.InputKind != "command" || codexAnswered(rows, o.StartedAt, end, c.copied) {
					o.IsRequest = true
					requestNo++
					// A copy per turn: pointing at the counter gave every turn its final value.
					no := requestNo
					o.RequestNo = &no
				}
			}
		}
		c.session.LastTurnKey, c.session.LastTurnStatus = o.TurnKey, o.Status
	}
	active, abandoned := len(c.turns), 0
	c.session.TurnBoundaries = "observed"
	c.session.Turns, c.session.Requests, c.session.Inputs, c.session.AbandonedTurns = &active, &requestNo, &inputNo, &abandoned
}

func codexTimeBoundaryAmbiguous(turns []*turn, ts time.Time) bool {
	for _, turn := range turns {
		if (!turn.out.StartedAt.IsZero() && turn.startedAtSource != "line" && sameCodexSecond(ts, turn.out.StartedAt)) ||
			(!turn.out.EndedAt.IsZero() && turn.endedAtSource != "line" && sameCodexSecond(ts, turn.out.EndedAt)) {
			return true
		}
	}
	return false
}

func codexHuman(r *CodexRow) bool {
	if r.projection != nil {
		return r.projection.human
	}
	if r.RecordType != "user" || (r.Role != "" && r.Role != "user") {
		return false
	}
	text := strings.TrimSpace(r.TextHead)
	return text != "" && ((!strings.HasPrefix(text, "<") && !strings.HasPrefix(text, "# AGENTS.md")) || codexCommand(r))
}
func codexCommand(r *CodexRow) bool {
	if r.projection != nil {
		return r.projection.command
	}
	return r.CommandName != "" || strings.HasPrefix(strings.TrimSpace(r.TextHead), "<skill")
}
func codexAnswered(rows []*CodexRow, start, end time.Time, copied func(int64) bool) bool {
	for _, r := range rows {
		if r.RecordType == "assistant" && !copied(r.ID) && !r.TS.Before(start) && (end.IsZero() || r.TS.Before(end)) {
			return true
		}
	}
	return false
}

func (c *computation) readCodexContext(heads []*Head) {
	c.session.LastContextSource = "unknown"
	rows := make(map[int64]*CodexRow)
	for i := range c.in.CodexRows {
		c.pulse()
		rows[c.in.CodexRows[i].ID] = &c.in.CodexRows[i]
	}
	for _, h := range heads {
		c.pulse()
		t := c.codexByHead[h.ID]
		if t == nil {
			continue
		}
		n := h.InputTokens + h.CacheReadTokens + h.CacheCreateTokens
		if r := rows[h.ID]; r != nil {
			n = r.InputTokens
		}
		t.out.ContextTokens = &n
		window := (*int)(nil)
		if r := rows[h.ID]; r != nil {
			window = r.ContextWindow
		}
		var observedAt time.Time
		var selected SnapshotCodexTurn
		for _, snap := range c.in.Snapshots {
			c.pulse()
			for _, life := range snap.CodexTurns {
				c.pulse()
				if life.TurnID == t.out.TurnKey && life.ContextWindow != nil && (selected.TurnID == "" || snap.ObservedAt.After(observedAt) || (snap.ObservedAt.Equal(observedAt) && codexTurnOrder(life, selected) > 0)) {
					window = life.ContextWindow
					observedAt = snap.ObservedAt
					selected = life
				}
			}
		}
		t.out.ContextWindow, t.out.ContextSource = nil, "unknown"
		if window != nil && *window >= int(n) && *window > 0 {
			w := *window
			t.out.ContextWindow, t.out.ContextSource = &w, "observed"
		}
	}
	for _, t := range c.turns {
		c.pulse()
		if t.out.TurnKey == c.session.LastTurnKey {
			c.session.LastContextTokens, c.session.LastContextWindow, c.session.LastContextSource = t.out.ContextTokens, t.out.ContextWindow, t.out.ContextSource
		}
	}
}
