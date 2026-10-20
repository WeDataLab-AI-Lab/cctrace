package sessiontrace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"time"
)

// Normalize never infers a start, end, parent, turn, or outcome. A zero TS row
// cannot satisfy the nonnullable wire timestamp and contributes no event.
func Normalize(records []Record) []Event {
	var chunks []Event
	for _, r := range records {
		if r.TS.IsZero() {
			continue
		}
		r, blocks := projection(r)
		resultCount := 0
		for _, b := range blocks {
			if b.Type == "tool_result" || b.Type == "function_call_output" || b.Type == "custom_tool_call_output" {
				resultCount++
			}
		}
		for ordinal, b := range blocks {
			index := ordinal
			if b.Index != nil {
				index = *b.Index
			}
			o, ok := observe(r, b, index)
			if ok {
				e := o.event
				e.observations = []observation{o}
				chunks = append(chunks, e)
				child := b.ChildAgentID
				// ToolUseID identifies the outer spawning call for a sidechain
				// file, not this row's result. Row-level child metadata can name
				// one unique result only; multiple results require block metadata.
				if child == "" && ((o.phase == "end" && resultCount == 1) || (o.phase == "start" && len(blocks) == 1)) {
					child = r.ChildAgentID
				}
				if e.Kind == "tool" && child != "" && (o.phase == "end" || (o.phase == "start" && isAgentDispatch(b.Name))) {
					launch, _ := observe(r, Block{Type: "agent_launch", ID: o.callID, ChildAgentID: child}, index)
					point := launch.event
					point.observations = []observation{launch}
					chunks = append(chunks, point)
				}
			}
		}
	}
	return Merge(chunks)
}

// Merge is commutative and replay-idempotent over the original source tuples.
// Preserve private metadata with MarshalStorage before persisting chunks.
func Merge(chunks ...[]Event) []Event {
	byID := make(map[string]map[string]observation)
	var opaque []Event
	for _, chunk := range chunks {
		for _, e := range chunk {
			if len(e.observations) == 0 {
				opaque = append(opaque, e)
				continue
			}
			for _, o := range e.observations {
				if byID[o.event.EventID] == nil {
					byID[o.event.EventID] = make(map[string]observation)
				}
				encoded, _ := json.Marshal(storedObservation{Event: o.event, Source: o.source, Index: o.index, CallID: o.callID, Phase: o.phase, Scope: o.scope, ParentID: o.parentID, ParentScope: o.parentScope})
				byID[o.event.EventID][string(encoded)] = o
			}
		}
	}
	groups := make(map[string][]observation)
	var out []Event
	for _, variants := range byID {
		keys := make([]string, 0, len(variants))
		for key := range variants {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		o := variants[keys[0]]
		if len(keys) > 1 {
			e := cloneEvent(o.event)
			e.Status, e.EndedAt = "unknown", nil
			e.Evidence = append(e.Evidence, "source_conflict")
			for _, key := range keys {
				e.observations = append(e.observations, variants[key])
			}
			out = append(out, e)
			continue
		}
		if o.callID != "" && o.phase != "" && o.scope != "" {
			key := correlation(o.scope, o.callID)
			groups[key] = append(groups[key], o)
		} else {
			out = append(out, standalone(o))
		}
	}
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool { return group[i].event.EventID < group[j].event.EventID })
		var starts, ends []observation
		for _, o := range group {
			if o.phase == "start" {
				starts = append(starts, o)
			} else {
				ends = append(ends, o)
			}
		}
		if len(starts) == 1 && len(ends) == 1 {
			out = append(out, paired(starts[0], ends[0]))
		} else {
			for _, o := range group {
				e := standalone(o)
				if len(starts) > 1 || len(ends) > 1 {
					e.Status = "unknown"
					e.Evidence = append(e.Evidence, "ambiguous_call_id")
				}
				out = append(out, e)
			}
		}
	}
	// Only an explicit parent scope + call ID with a unique observed start links.
	parents := make(map[string][]string)
	for _, e := range out {
		if contains(e.Evidence, "source_conflict") {
			continue
		}
		for _, o := range e.observations {
			if o.phase == "start" && o.callID != "" && o.scope != "" {
				key := correlation(o.scope, o.callID)
				parents[key] = append(parents[key], e.EventID)
			}
		}
	}
	for i := range out {
		e := &out[i]
		parentKeys := make(map[string]bool)
		for _, o := range e.observations {
			if o.parentID != "" {
				parentKeys[correlation(o.parentScope, o.parentID)] = true
			}
		}
		if len(parentKeys) > 1 {
			e.Status = "unknown"
			e.Evidence = append(e.Evidence, "parent_conflict")
			continue
		}
		if contains(e.Evidence, "source_conflict") {
			continue
		}
		for key := range parentKeys {
			ids := parents[key]
			if len(ids) == 1 && ids[0] != e.EventID {
				e.ParentEventID = strptr(ids[0])
				e.Evidence = append(e.Evidence, "explicit_parent")
			} else {
				e.Evidence = append(e.Evidence, "parent_unresolved")
			}
		}
	}
	out = append(out, opaque...)
	for i := range out {
		sort.Strings(out[i].Evidence)
		out[i].Evidence = unique(out[i].Evidence)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.Before(out[j].Timestamp)
		}
		if len(out[i].observations) > 0 && len(out[j].observations) > 0 {
			a, b := out[i].observations[0], out[j].observations[0]
			if a.source.ID != b.source.ID {
				return a.source.ID < b.source.ID
			}
			if a.source == b.source && a.index != b.index {
				return a.index < b.index
			}
			if a.source == b.source && a.index == b.index && out[i].Kind != out[j].Kind {
				// The launch point accompanies its dispatch at the same timestamp.
				if (out[i].Kind == "tool") != (out[j].Kind == "tool") {
					return out[i].Kind == "tool"
				}
				return out[i].Kind < out[j].Kind
			}
		}
		if out[i].SourceRef != out[j].SourceRef {
			return out[i].SourceRef < out[j].SourceRef
		}
		return out[i].EventID < out[j].EventID
	})
	if out == nil {
		return []Event{}
	}
	return out
}

func observe(r Record, b Block, index int) (observation, bool) {
	s := source(r)
	e := Event{Timestamp: r.TS.UTC(), TurnKey: strptr(r.TurnKey), AgentID: strptr(r.AgentID), Status: "unknown", Evidence: []string{"source_timestamp"}, SourceRef: sourceRef(s)}
	o := observation{source: s, index: index, scope: recordScope(r)}
	switch b.Type {
	case "text", "input_text", "output_text", "input", "response", "image", "input_image":
		// A top-level image is an observed message even without text. Its body
		// stays outside the projection, and images nested in tool results do not
		// enter this branch or create synthetic user messages.
		if !b.HasText && b.Type != "input" && b.Type != "response" && b.Type != "image" && b.Type != "input_image" {
			return o, false
		}
		role := r.Role
		if role == "" {
			role = r.RecordType
		}
		switch {
		case b.Type == "input" || role == "user":
			e.Kind, e.Label = "input", "Input"
		case b.Type == "response" || role == "assistant":
			e.Kind, e.Label = "response", "Response"
		default:
			return o, false
		}
		if r.IsMeta || r.IsCompactSummary {
			return o, false
		}
		e.Status = "completed"
		e.Evidence = append(e.Evidence, "observed_point")
	case "tool_use", "function_call", "custom_tool_call", "tool_call", "agent_start":
		e.Kind, e.Label = "tool", b.Name
		if b.Type == "agent_start" {
			e.Kind = "agent"
			o.scope = digest("agent_lifecycle", o.scope)
			child := b.ChildAgentID
			if child == "" {
				child = r.ChildAgentID
			}
			if child != "" {
				e.AgentID = strptr(child)
			}
		}
		if e.Label == "" {
			e.Label = "Tool"
			if e.Kind == "agent" {
				e.Label = "Agent"
			}
		}
		e.StartedAt = timeptr(r.TS)
		o.callID, o.phase = b.ID, "start"
		e.Status = observedStatus(b.Status, nil)
	case "tool_result", "function_call_output", "custom_tool_call_output", "agent_end":
		e.Kind, e.Label = "tool", "Tool result"
		if b.Type == "agent_end" {
			e.Kind, e.Label = "agent", "Agent"
			o.scope = digest("agent_lifecycle", o.scope)
		}
		e.EndedAt = timeptr(r.TS)
		o.callID, o.phase = b.ToolUseID, "end"
		if o.callID == "" {
			o.callID = b.ID
		}
		e.Status = observedStatus(b.Status, b.IsError)
	case "agent_launch":
		e.Kind, e.Label = "agent", "Agent launch"
		e.AgentID = strptr(b.ChildAgentID)
		e.Evidence = append(e.Evidence, "observed_point", "dispatch_only", "end_missing")
		o.parentID, o.parentScope = b.ID, recordScope(r)
	default:
		return o, false
	}
	if o.phase != "" && o.callID == "" {
		e.Evidence = append(e.Evidence, "call_id_missing")
	}
	if o.phase != "" && (r.SessionID == "" || r.Agent == "" || r.SourceFile == "") {
		o.scope = ""
		e.Status = "unknown"
		e.Evidence = append(e.Evidence, "source_scope_missing")
	}
	if r.Redacted {
		e.Evidence = append(e.Evidence, "redacted_metadata")
	}
	if b.Type != "agent_launch" && r.ParentToolUseID != "" && r.ParentSourceFile != "" {
		parent := r
		parent.AgentID, parent.SourceFile = r.ParentAgentID, r.ParentSourceFile
		o.parentID, o.parentScope = r.ParentToolUseID, recordScope(parent)
	}
	e.EventID = digest("event", e.SourceRef, strconv.Itoa(index), e.Kind)
	o.event = e
	return o, true
}

func isAgentDispatch(name string) bool {
	return name == "Agent" || name == "Task" || name == "spawn_agent"
}

func standalone(o observation) Event {
	e := cloneEvent(o.event)
	e.observations = []observation{o}
	switch o.phase {
	case "start":
		e.Evidence = append(e.Evidence, "end_missing")
	case "end":
		e.Evidence = append(e.Evidence, "start_missing")
	}
	return e
}

func paired(start, end observation) Event {
	e := cloneEvent(start.event)
	e.observations = []observation{start, end}
	e.Evidence = append(e.Evidence, end.event.Evidence...)
	e.Evidence = append(e.Evidence, "exact_call_id")
	e.Status = end.event.Status
	if start.event.TurnKey != nil && end.event.TurnKey != nil && *start.event.TurnKey != *end.event.TurnKey {
		e.Status = "unknown"
		e.Evidence = append(e.Evidence, "turn_conflict")
		return e
	}
	if end.event.Timestamp.Before(start.event.Timestamp) {
		e.Status = "unknown"
		e.Evidence = append(e.Evidence, "clock_reversal")
		return e
	}
	e.EndedAt = timeptr(end.event.Timestamp)
	e.Evidence = append(e.Evidence, "observed_interval")
	if e.Status == "unknown" {
		e.Evidence = append(e.Evidence, "outcome_unknown")
	}
	return e
}

func observedStatus(status string, failed *bool) string {
	if failed != nil {
		if *failed {
			return "error"
		}
		return "completed"
	}
	switch status {
	case "running", "completed", "error", "interrupted":
		return status
	case "success", "succeeded":
		return "completed"
	case "failed":
		return "error"
	default:
		return "unknown"
	}
}

func source(r Record) Source {
	return Source{ID: r.ID, SessionID: r.SessionID, Agent: r.Agent, UUID: r.UUID, TS: r.TS.UTC(), RecordType: r.RecordType, SourceFile: r.SourceFile, AgentID: r.AgentID, Branch: r.Branch, SourceScope: r.SourceScope}
}

// SourceReference is the opaque context lookup key, including the row ID within
// the entire source tuple rather than interpreting that numeric ID globally.
func SourceReference(r Record) string { return sourceRef(source(r)) }

func sourceRef(s Source) string {
	return digest("source", s.SessionID, s.Agent, s.AgentID, s.SourceFile, s.Branch, s.SourceScope, s.UUID, strconv.FormatInt(s.ID, 10), s.TS.Format(time.RFC3339Nano), s.RecordType)
}

func recordScope(r Record) string {
	return digest("scope", r.SessionID, r.Agent, r.AgentID, r.SourceFile, r.Branch, r.SourceScope, r.CodexTurnID, r.CodexRootTurnID)
}

func correlation(scope, callID string) string { return digest("call", scope, callID) }

// EventCorrelationKey selects the exact start/result bucket in persistent cache.
// Point events and calls with no recorded ID have no correlation key.
func EventCorrelationKey(e Event) string {
	if contains(e.Evidence, "source_conflict") {
		return ""
	}
	for _, o := range e.observations {
		if o.phase != "" && o.callID != "" && o.scope != "" {
			return correlation(o.scope, o.callID)
		}
	}
	return ""
}

// EventRelatedCorrelationKeys includes exact parent dependencies for a cache
// query before Merge. It never uses agent names or timestamp proximity.
func EventRelatedCorrelationKeys(e Event) []string {
	var keys []string
	if key := EventCorrelationKey(e); key != "" {
		keys = append(keys, key)
	}
	for _, o := range e.observations {
		if o.parentID != "" && o.parentScope != "" {
			keys = append(keys, correlation(o.parentScope, o.parentID))
		}
	}
	sort.Strings(keys)
	return unique(keys)
}

// EventCallIDs gives exact candidate IDs for an indexed boundary query. The
// caller still must compare scoped correlation keys before accepting a match.
func EventCallIDs(e Event) []string {
	var ids []string
	for _, o := range e.observations {
		if o.callID != "" {
			ids = append(ids, o.callID)
		}
		if o.parentID != "" {
			ids = append(ids, o.parentID)
		}
	}
	sort.Strings(ids)
	return unique(ids)
}

func digest(parts ...string) string {
	// JSON arrays give unambiguous framing even if source strings contain NULs.
	encoded, _ := json.Marshal(parts)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func strptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func timeptr(t time.Time) *time.Time { t = t.UTC(); return &t }

func cloneEvent(e Event) Event {
	e.Evidence = append([]string{}, e.Evidence...)
	e.observations = nil
	return e
}

func unique(in []string) []string {
	out := in[:0]
	for _, value := range in {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func contains(in []string, value string) bool {
	for _, item := range in {
		if item == value {
			return true
		}
	}
	return false
}
