package syncer

import (
	"encoding/json"
	"strings"
	"testing"

	"cctrace/internal/store"
)

// The flag this replaces (log_user_prompts) was read by nothing: it sat in
// `cctrace config` at false while prompts were uploaded, stored, and served back
// by the API. A privacy control that names a protection it does not provide is
// worse than no control -- an operator reads it and stops looking.
//
// So these tests assert the property the name claims, on the actual bytes that
// leave the machine, not on an intermediate field.

func recordWithRaw(t *testing.T, raw string) *store.SessionRecord {
	t.Helper()
	return &store.SessionRecord{RecordType: "user", Raw: json.RawMessage(raw)}
}

func TestRedactRemovesPromptTextFromRaw(t *testing.T) {
	// Claude's shape: the typed text sits under message.content[].text.
	r := recordWithRaw(t, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"deploy the prod key rotation"}]},"usage":{"input_tokens":42}}`)

	Redact([]*store.SessionRecord{r}, RedactPolicy{UserPrompts: true})

	got := string(r.Raw)
	if strings.Contains(got, "deploy the prod key rotation") {
		t.Errorf("prompt text survived redaction:\n%s", got)
	}
	if !strings.Contains(got, redactedMarker) {
		t.Errorf("no marker left behind -- a reader cannot tell withheld from absent:\n%s", got)
	}
	// Accounting must be untouched: redaction is a privacy control, not a billing
	// change. If it moved a number, an operator would face a choice between privacy
	// and correct costs, and would pick costs.
	if !strings.Contains(got, `"input_tokens":42`) {
		t.Errorf("usage did not survive redaction:\n%s", got)
	}
}

func TestRedactLeavesEverythingWhenPolicyEmpty(t *testing.T) {
	const raw = `{"message":{"content":[{"text":"hello"}]}}`
	r := recordWithRaw(t, raw)

	Redact([]*store.SessionRecord{r}, RedactPolicy{})

	if string(r.Raw) != raw {
		t.Errorf("zero policy modified the record:\ngot  %s\nwant %s", r.Raw, raw)
	}
}

// The two settings are separate decisions. An operator may want the conversation
// without the shell commands it produced, or the commands without the conversation.
func TestRedactSeparatesPromptsFromToolDetails(t *testing.T) {
	const raw = `{"message":{"content":[{"text":"run the migration"}]},"tool":{"command":"psql -c 'DROP TABLE x'","input":{"path":"/etc/secrets"}}}`

	promptsOnly := recordWithRaw(t, raw)
	Redact([]*store.SessionRecord{promptsOnly}, RedactPolicy{UserPrompts: true})
	if strings.Contains(string(promptsOnly.Raw), "run the migration") {
		t.Error("prompt survived a prompts-only policy")
	}
	if !strings.Contains(string(promptsOnly.Raw), "DROP TABLE x") {
		t.Error("prompts-only policy removed tool details it was not asked to touch")
	}

	toolsOnly := recordWithRaw(t, raw)
	Redact([]*store.SessionRecord{toolsOnly}, RedactPolicy{ToolDetails: true})
	if strings.Contains(string(toolsOnly.Raw), "DROP TABLE x") {
		t.Error("tool command survived a tools-only policy")
	}
	if strings.Contains(string(toolsOnly.Raw), "/etc/secrets") {
		t.Error("tool input survived a tools-only policy")
	}
	if !strings.Contains(string(toolsOnly.Raw), "run the migration") {
		t.Error("tools-only policy removed the prompt it was not asked to touch")
	}
}

// ToolName and ToolCallID are identifiers the session view pairs calls with,
// not tool content: the arguments and output they point at live only in Raw,
// which is where the tool-details policy has to remove them.
func TestRedactToolDetailsKeepsToolIdentity(t *testing.T) {
	r := &store.SessionRecord{
		RecordType: "tool_call",
		ToolName:   "shell",
		ToolCallID: "call_1",
		Raw:        json.RawMessage(`{"payload":{"type":"function_call","call_id":"call_1","name":"shell","arguments":"cat /etc/secrets"}}`),
	}

	Redact([]*store.SessionRecord{r}, RedactPolicy{ToolDetails: true})

	if strings.Contains(string(r.Raw), "/etc/secrets") {
		t.Errorf("tool arguments survived a tool-details policy:\n%s", r.Raw)
	}
	if r.ToolName != "shell" || r.ToolCallID != "call_1" {
		t.Errorf("tool identity = %q, %q; want shell, call_1", r.ToolName, r.ToolCallID)
	}
}

// Content sits at different depths per agent -- Claude nests it under
// message.content[], gjc puts an attribution beside it, Codex goes deeper. A
// walker that only checked the top level would protect one agent and quietly miss
// the others, which is the shape of every bug this repository found today.
func TestRedactReachesNestedContentForEveryAgentShape(t *testing.T) {
	for name, raw := range map[string]string{
		"claude": `{"message":{"role":"user","content":[{"type":"text","text":"SECRET"}]}}`,
		"gjc":    `{"type":"message","message":{"role":"user","attribution":"user","content":"SECRET"}}`,
		"omo":    `{"role":"user","content":[{"type":"text","text":"SECRET"}],"timestamp":"1"}`,
		"codex":  `{"payload":{"type":"message","content":[{"type":"input_text","text":"SECRET"}]}}`,
	} {
		r := recordWithRaw(t, raw)
		Redact([]*store.SessionRecord{r}, RedactPolicy{UserPrompts: true})
		if strings.Contains(string(r.Raw), "SECRET") {
			t.Errorf("%s: prompt survived at its own nesting depth:\n%s", name, r.Raw)
		}
	}
}

// Raw that cannot be parsed cannot be walked. Uploading it unread would defeat the
// setting entirely, so the whole payload is withheld -- losing a record we could
// not inspect is the safe direction once an operator has asked for redaction.
func TestRedactWithholdsUnparseableRaw(t *testing.T) {
	r := recordWithRaw(t, `{"message": truncated...`)

	Redact([]*store.SessionRecord{r}, RedactPolicy{UserPrompts: true})

	if strings.Contains(string(r.Raw), "truncated") {
		t.Errorf("unparseable raw was shipped as-is:\n%s", r.Raw)
	}
	if !json.Valid(r.Raw) {
		t.Errorf("replacement is not valid JSON: %s", r.Raw)
	}
}
