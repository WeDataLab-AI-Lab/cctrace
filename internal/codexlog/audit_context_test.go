package codexlog

import (
	"strings"
	"testing"
)

func TestAuditContextKeepsInstructionsWithoutUnrelatedMetadata(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		`{"type":"session_meta","timestamp":"2026-10-17T00:00:00Z","payload":{"id":"s","cwd":"/work","originator":"codex-tui","cli_version":"test","base_instructions":{"text":"BASE-INSTRUCTIONS"},"unrelated_secret":"do-not-send"}}`,
		`{"type":"turn_context","timestamp":"2026-10-17T00:00:01Z","payload":{"turn_id":"turn-1","cwd":"/work","model":"test-model","developer_instructions":"DEVELOPER-INSTRUCTIONS","user_instructions":"USER-INSTRUCTIONS","unrelated_secret":"do-not-send"}}`,
		`{"type":"turn_context","timestamp":"2026-10-17T00:00:01Z","payload":{"turn_id":"turn-1","collaboration_mode":{"mode":"plan","settings":{"developer_instructions":"NESTED-INSTRUCTIONS","unrelated_secret":"do-not-send"}},"unrelated_secret":"do-not-send"}}`,
	} {
		state := &scanState{sessionID: "s", offset: 123, turnMetadataScanned: true, format: FormatNew}
		r := parseNewFormatLine([]byte(line), state)
		if r == nil {
			t.Fatalf("instruction context was dropped")
		}
		if r.RecordType != "context" || !strings.Contains(string(r.Raw), "INSTRUCTIONS") || strings.Contains(string(r.Raw), "do-not-send") {
			t.Errorf("wrong context record: %+v", r)
		}
	}
}

func TestAuditContextDoesNotEmitInheritedForkInstructions(t *testing.T) {
	t.Parallel()
	state := &scanState{sessionID: "child", format: FormatNew, isSubagentFork: true, forkHasTriggerTurn: true}
	line := []byte(`{"type":"session_meta","timestamp":"2026-10-17T00:00:00Z","payload":{"id":"parent","base_instructions":{"text":"PARENT-ONLY"}}}`)
	if got := parseNewFormatLine(line, state); got != nil {
		t.Errorf("inherited parent instructions attributed to child: %+v", got)
	}
}
