package sessionlog

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestGitOperationAllowlistAndWithholding(t *testing.T) {
	r := scanLines(t, []string{`{"type":"user","uuid":"u","toolUseResult":{"gitOperation":{"commit":{"sha":"abcdef1234567","kind":"commit","branch":"main","subject":"private subject"},"pr":{"number":42,"action":"create","url":"https://example.test/pr/42","body":"private body"},"unknown":"secret"}},"message":{"content":[]}}`})[0]
	if CollectVersion != 4 {
		t.Fatal("git operation collection must carry the current collection contract")
	}
	r.WithholdContent()
	raw := string(r.CollectedRaw())
	if !strings.Contains(raw, "abcdef1234567") || !strings.Contains(raw, `"number":42`) || strings.Contains(raw, "private") || strings.Contains(raw, "secret") {
		t.Fatalf("unexpected kept fields: %s", raw)
	}
}

func TestGitOperationMalformedAndCaps(t *testing.T) {
	for _, raw := range []string{`[]`, `"string"`, `{"commit":5}`, `{"pr":{"number":1.5}}`, `{"commit":{"sha":"not-a-hash"}}`} {
		r := scanLines(t, []string{`{"type":"user","uuid":"u","toolUseResult":{"gitOperation":` + raw + `},"message":{}}`})[0]
		if !strings.Contains(string(r.CollectedRaw()), "toolUseResult.gitOperation") {
			t.Errorf("no dropped marker for %s", raw)
		}
	}
	input, _ := json.Marshal(map[string]any{"branch": map[string]any{"ref": strings.Repeat("한", 200)}})
	kept, unread := keepGitOperation(input)
	var value struct {
		Branch struct {
			Ref string `json:"ref"`
		} `json:"branch"`
	}
	if json.Unmarshal(kept, &value) != nil || unread || len(value.Branch.Ref) > 512 || !utf8.ValidString(value.Branch.Ref) {
		t.Fatalf("cap: %s unread=%v", kept, unread)
	}
}
