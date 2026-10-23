package dependencyaudit

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/openinsights"
)

// A server decision a local file recorded is counted from the file, under any
// session that holds the call: a permissionDecision, or a rejection the file
// records as toolDenialKind. One no local file holds is kept; one without a
// tool_use_id cannot be matched and is counted apart.
func TestMergeDecisionsPrefersLocalAndKeepsTheRest(t *testing.T) {
	t.Parallel()
	fx := buildFixture(t)
	ab := newClaude("sess-abort", time.Date(2026, 1, 18, 9, 0, 0, 0, time.UTC))
	ab.typed("run the migration", "typed")
	ab.tool("ti1", "Bash", map[string]any{"command": "make migrate"})
	ab.result("ti1", true, map[string]any{"toolDenialKind": "interrupted"})
	writeLines(t, filepath.Join(fx.homeA, "projects", "-work-alpha", "abort.jsonl"), ab.lines, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	res, _ := runFixture(t, Options{Agent: "claude", ClaudeDirs: []string{fx.homeA, fx.homeB}})
	rows := []openinsights.DecisionEvent{
		{EventName: "tool_decision", SessionID: "sess-orig", ToolUseID: "tb1", DecisionSource: "user_temporary", ToolName: "Bash"},
		{EventName: "tool_decision", SessionID: "sess-copy", ToolUseID: "tb1", DecisionSource: "user_temporary", ToolName: "Bash"},
		{EventName: "tool_decision", SessionID: "sess-orig", ToolUseID: "tb2", DecisionSource: "user_reject", ToolName: "Bash"},
		{EventName: "tool_decision", SessionID: "sess-abort", ToolUseID: "ti1", DecisionSource: "user_abort", ToolName: "Bash"},
		{EventName: "tool_decision", SessionID: "sess-elsewhere", ToolUseID: "toolu_remote", DecisionSource: "user_permanent", ToolName: "Edit"},
		{EventName: "tool_decision", SessionID: "sess-orig", DecisionSource: "hook", ToolName: "Read"},
	}
	d := res.MergeDecisions(rows, true)
	if d.Read != 6 || d.LocalMatched != 4 || d.Kept != 1 || d.KeptSessions != 1 || !d.Truncated {
		raw, _ := json.Marshal(d)
		t.Fatalf("decisions = %s", raw)
	}
	if want := map[string]int{"user_permanent": 1}; !maps.Equal(d.KeptBySource, want) {
		t.Errorf("kept_by_source = %v, want %v", d.KeptBySource, want)
	}
	if want := map[string]int{"hook": 1}; !maps.Equal(d.OverlapUncertain, want) {
		t.Errorf("overlap_uncertain = %v, want %v", d.OverlapUncertain, want)
	}
	// Local per call: user_reject from tb4's permissionDecision and tb2's
	// toolDenialKind, user_abort from ti1's; the kept one adds to user_permanent.
	want := map[string]int{"user_temporary": 2, "user_permanent": 6, "user_reject": 2, "user_abort": 1, "hook": 1}
	if !maps.Equal(d.MergedBySource, want) {
		t.Errorf("merged_by_source = %v, want %v", d.MergedBySource, want)
	}
	if len(d.KeptByTool) != 1 {
		t.Errorf("kept_by_tool = %+v", d.KeptByTool)
	}
}

func TestServerPracticesMasksPracticeNames(t *testing.T) {
	t.Parallel()
	secret := secretShapes()[3]
	in := &openinsights.Practices{Rows: []openinsights.PracticeRow{{Kind: "skill", Name: "deploy " + secret, UsersWith: 3}}, Caveats: []string{"correlation only"}}
	out := ServerPractices(in)
	if strings.Contains(out.Rows[0].Name, secret) || out.Rows[0].UsersWith != 3 || len(out.Caveats) != 1 {
		t.Fatalf("practices = %+v", out)
	}
}
