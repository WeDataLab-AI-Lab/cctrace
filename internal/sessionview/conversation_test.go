package sessionview

import (
	"testing"

	"cctrace/internal/store"
)

func TestAlwaysHiddenForConversationProjectionIsNarrowAndConservative(t *testing.T) {
	for _, recordType := range []string{"last-prompt", "history-suppression", "cost-state", "permission-mode", "atis-latch", "agent-name", "ai-title", "custom-title", "pr-link", "continued-in", "mode"} {
		rec := &store.SessionRecord{Agent: "claude", RecordType: recordType, Raw: []byte(`{}`)}
		if Normalize(rec).Kind != KindHidden {
			t.Errorf("Claude bookkeeping record %q normalized as visible: %+v", recordType, Normalize(rec))
		}
		if !AlwaysHiddenForConversation(rec) {
			t.Errorf("Claude bookkeeping record %q should be always hidden", recordType)
		}
	}
	for _, agent := range []string{"", "claude", "codex", "omo", "gjc"} {
		rec := &store.SessionRecord{Agent: agent, RecordType: "system", Raw: []byte(`{}`)}
		if Normalize(rec).Kind != KindHidden || !AlwaysHiddenForConversation(rec) {
			t.Errorf("system record for %q should be always hidden", agent)
		}
	}
	for _, rec := range []store.SessionRecord{
		{Agent: "claude", RecordType: "queue-operation"},
		{Agent: "claude", RecordType: "attachment"},
		{Agent: "claude", RecordType: "user", Raw: []byte(`{"message":{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"x"}}]}}`)},
		{Agent: "codex", RecordType: "reasoning", Raw: []byte(`{"payload":{"type":"reasoning","summary":[{"text":"thinking"}]}}`)},
		{Agent: "gjc", RecordType: "tool_call"},
		{Agent: "future", RecordType: "system"},
		{Agent: "future", RecordType: "last-prompt"},
		{Agent: "codex", RecordType: "custom"},
	} {
		if AlwaysHiddenForConversation(&rec) {
			t.Errorf("record should be retained conservatively: agent=%q type=%q", rec.Agent, rec.RecordType)
		}
	}
	attachmentTurn := &store.SessionRecord{Agent: "claude", RecordType: "user", Raw: []byte(`{"message":{"content":[{"type":"text","text":"look at this"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"x"}}]}}`)}
	if Normalize(attachmentTurn).Kind == KindHidden || AlwaysHiddenForConversation(attachmentTurn) {
		t.Fatal("message with visible text and attachment must remain eligible")
	}
	if AlwaysHiddenForConversation(&store.SessionRecord{Agent: "claude", RecordType: "user"}) {
		t.Fatal("user message row must remain eligible")
	}
}
