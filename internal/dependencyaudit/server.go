package dependencyaudit

import (
	"slices"

	"cctrace/internal/openinsights"
)

// ServerDecisionSources are the decision sources read from the server: the
// person's approvals and rejections, and hooks. Configuration decisions are
// left out, as the local count gives them only as a total.
var ServerDecisionSources = []string{"user_temporary", "user_permanent", "user_reject", "user_abort", "hook"}

// ServerEvidence is what only the server holds, all of it Claude only: tool
// decisions made on other devices, by clients that write no
// permissionDecision, or in files deleted since, and the practices comparison.
// A part not read is null, and a caveat says why.
type ServerEvidence struct {
	Agents []string `json:"agents"`
	// DecisionSource is "supported" when the server announces Open API events
	// carrying the decision source, "unknown" when there was no upload token
	// it accepted to ask with, "unsupported" otherwise.
	DecisionSource string                  `json:"decision_source"`
	Decisions      *ServerDecisions        `json:"decisions"`
	Practices      *openinsights.Practices `json:"practices"`
}

// ServerDecisions are the server's decisions in the window, joined to the
// local ones by tool_use_id.
type ServerDecisions struct {
	// Read are the distinct decisions the server returned.
	Read int `json:"read"`
	// LocalMatched were recorded by a local file too; the local record counts.
	LocalMatched int `json:"local_matched"`
	// Kept are held by no local file.
	Kept         int            `json:"kept"`
	KeptBySource map[string]int `json:"kept_by_source"`
	KeptByTool   []ToolCount    `json:"kept_by_tool"`
	KeptSessions int            `json:"kept_sessions"`
	// OverlapUncertain have no tool_use_id, so whether a local file holds them
	// cannot be told; they are in neither count.
	OverlapUncertain map[string]int `json:"overlap_uncertain"`
	// MergedBySource counts each decision once per source: the local results
	// (a permissionDecision's source, or user_reject and user_abort for a
	// rejection recorded as toolDenialKind) plus Kept.
	MergedBySource map[string]int `json:"merged_by_source"`
	Truncated      bool           `json:"truncated"`
}

// MergeDecisions joins server decisions to the local ones. A decision a local
// file recorded -- a permissionDecision, or a rejection as toolDenialKind --
// is counted from the file; the server
// copy is matched by tool_use_id whatever session it names, because the local
// audit identifies a Claude call by it across a resumed session's copies. A
// decision no local file recorded is kept from the server.
func (r *Result) MergeDecisions(rows []openinsights.DecisionEvent, truncated bool) *ServerDecisions {
	d := &ServerDecisions{KeptBySource: map[string]int{}, OverlapUncertain: map[string]int{}, MergedBySource: map[string]int{}, Truncated: truncated}
	byTool := map[[2]string]int{}
	sessions := map[string]bool{}
	for _, e := range rows {
		d.Read++
		switch {
		case e.ToolUseID == "":
			d.OverlapUncertain[e.DecisionSource]++
		case hasKey(r.localDecisions, e.ToolUseID):
			d.LocalMatched++
		default:
			d.Kept++
			d.KeptBySource[e.DecisionSource]++
			byTool[[2]string{e.DecisionSource, e.ToolName}]++
			sessions[e.SessionID] = true
		}
	}
	d.KeptByTool = toolCounts(byTool)
	d.KeptSessions = len(sessions)
	for _, source := range ServerDecisionSources {
		d.MergedBySource[source] = d.KeptBySource[source]
	}
	for _, source := range r.localDecisions {
		if _, ok := d.MergedBySource[source]; ok {
			d.MergedBySource[source]++
		}
	}
	return d
}

func hasKey[V any](m map[string]V, k string) bool {
	_, ok := m[k]
	return ok
}

// ServerPractices passes the server's comparison through, with each practice
// name and caveat masked like every other printed text and a kind the server
// does not document shown as "other".
func ServerPractices(p *openinsights.Practices) *openinsights.Practices {
	out := *p
	out.Rows = slices.Clone(p.Rows)
	for i := range out.Rows {
		out.Rows[i].Name = Mask(out.Rows[i].Name)
		if !practiceKinds[out.Rows[i].Kind] {
			out.Rows[i].Kind = "other"
		}
	}
	out.Caveats = make([]string, len(p.Caveats))
	for i, c := range p.Caveats {
		out.Caveats[i] = Mask(c)
	}
	return &out
}

var practiceKinds = map[string]bool{"plugin": true, "skill": true, "hook": true}
