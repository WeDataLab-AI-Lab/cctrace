package sessionlog

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAuditAttachmentsKeepActualContent(t *testing.T) {
	t.Parallel()
	attachments := []string{
		`{"type":"instructions","files":[{"path":"/work/CLAUDE.md","type":"project","content":"CLAUDE-CONTENT","privateExtra":"omit"},{"path":"/work/AGENTS.md","type":"import","content":"AGENTS-CONTENT"}],"unrelated":"omit"}`,
		`{"type":"hook_success","hookName":"PreToolUse:Read","hookEvent":"PreToolUse","toolUseID":"tool-1","content":"HOOK-CONTENT","stdout":"HOOK-STDOUT","stderr":"HOOK-STDERR","exitCode":0,"command":"hook-command","durationMs":12,"unrelated":"omit"}`,
		`{"type":"hook_additional_context","hookName":"SessionStart","hookEvent":"SessionStart","content":[{"type":"text","text":"HOOK-CONTEXT"}]}`,
		`{"type":"async_hook_response","processId":"proc-1","hookName":"Stop","hookEvent":"Stop","toolName":"Read","response":{"hookSpecificOutput":{"additionalContext":"HOOK-ASYNC"}},"stdout":"ASYNC-OUT","stderr":"","exitCode":0}`,
		`{"type":"hook_system_message","hookName":"Stop","toolUseID":"tool-2","hookEvent":"Stop","content":"HOOK-SYSTEM"}`,
	}
	for _, attachment := range attachments {
		r := scanLines(t, []string{`{"type":"attachment","uuid":"a-1","timestamp":"2026-10-17T00:00:00Z","sessionId":"s","attachment":` + attachment + `}`})[0]
		want := decodeJSON(t, attachment)
		delete(want, "unrelated")
		if files, ok := want["files"].([]interface{}); ok {
			delete(files[0].(map[string]interface{}), "privateExtra")
		}
		var got map[string]interface{}
		if err := json.Unmarshal(r.Attachment, &got); err != nil {
			t.Fatalf("attachment was lost: %s: %v", attachment, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("attachment\n got %v\nwant %v", got, want)
		}
		if r.CctraceCollect != 4 {
			t.Errorf("audit content must be marked collection version 4, got %d", r.CctraceCollect)
		}
		r.WithholdContent()
		if strings.Contains(string(r.CollectedRaw()), "CONTENT") || strings.Contains(string(r.CollectedRaw()), "HOOK-") || strings.Contains(string(r.CollectedRaw()), "ASYNC-OUT") || strings.Contains(string(r.CollectedRaw()), "hook-command") {
			t.Errorf("default reenrich leaked content: %s", r.CollectedRaw())
		}
		if len(r.CctraceWithheld) == 0 {
			t.Errorf("withheld audit content was not named: %s", r.CollectedRaw())
		}
	}
}

func TestAuditSystemContentAndTruncationAreExplicit(t *testing.T) {
	t.Parallel()
	rows := scanLines(t, []string{
		`{"type":"system","uuid":"sys-1","sessionId":"s","subtype":"local_command","content":"SYSTEM-CONTENT"}`,
		`{"type":"attachment","uuid":"plan-1","sessionId":"s","attachment":{"type":"plan_file_reference","planContent":"` + strings.Repeat("x", maxPlanContentBytes+1) + `"}}`,
	})
	if string(rows[0].Content) != `"SYSTEM-CONTENT"` {
		t.Errorf("system content was lost: %s", rows[0].CollectedRaw())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rows[1].CollectedRaw(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["cctraceTruncated"]) != `["attachment.planContent"]` {
		t.Errorf("truncation marker = %s", raw["cctraceTruncated"])
	}
}
