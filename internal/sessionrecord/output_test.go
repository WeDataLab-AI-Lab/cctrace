package sessionrecord

import (
	"strings"
	"testing"
)

func TestValidateOutputRejectsUnexpectedSchemaProperties(t *testing.T) {
	raw := validModelOutput(`,"unexpected":"value"`)
	if _, err := ValidateOutput(raw, ValidationOptions{}); err == nil {
		t.Fatal("unknown output property was accepted")
	}
}

func TestNormalizeAdministrativeTagAppliesCanonicalSafetyRules(t *testing.T) {
	got, ok := NormalizeTag("  ＡＰＩ　server  ", true)
	if !ok || got != "API server" {
		t.Fatalf("normalized tag=%q ok=%v", got, ok)
	}
	if _, ok := NormalizeTag("unsafe/path", true); ok {
		t.Fatal("slash-containing administrative tag accepted")
	}
}

func TestValidateOutputChecksEvidenceAndDoneBoundaries(t *testing.T) {
	raw := `{"headline":"정리","bullets":[{"text":"` + "`API` 변경" + `","turns":["t1","t404"]}],"review":[{"kind":"decision","text":"` + "`API` 선택" + `","turn":"t404"}],"kinds":["implementation"],"components":["API"," Ｍｏｄｕｌｅ "],"screens":[],"new_component":"Ｍｏｄｕｌｅ","new_screen":null,"work":[{"turn":"t1","kind":"request","text":"` + "`API` 완료" + `","done":true,"decision":null,"note":null,"children":[{"kind":"check","done":false,"text":"확인"}]},{"turn":"t2","kind":"followup","text":"완료","done":true,"decision":null,"note":null,"children":[]}],"skipped":[],"work_updates":[{"id":"not-in-prior","done":true,"note":null}]}`
	got, err := ValidateOutput(raw, ValidationOptions{
		TurnKeys:     map[string]string{"t1": "private-turn-a", "t2": "private-turn-b"},
		RequestTurns: []string{"t1", "t2"},
		TurnStatuses: map[string]string{"t1": "open", "t2": "ended"},
		LastTurn:     "t1",
		InputJSON:    []byte(`{"source":"API","literal":"확인"}`),
		Vocabulary:   TagVocabulary{Components: []string{"API"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Bullets) != 1 || len(got.Bullets[0].TurnKeys) != 1 || got.Bullets[0].TurnKeys[0] != "private-turn-a" {
		t.Fatalf("bullet evidence mapping = %#v", got.Bullets)
	}
	if len(got.Review) != 0 {
		t.Fatalf("review with no valid evidence was kept: %#v", got.Review)
	}
	if got.Work[0].Done != nil || got.Work[1].Done == nil || !*got.Work[1].Done {
		t.Fatalf("done correction failed: %#v", got.Work)
	}
	if got.Work[0].TurnKey != "private-turn-a" || got.Work[1].TurnKey != "private-turn-b" {
		t.Fatalf("work turn keys were not restored: %#v", got.Work)
	}
	if got.UncoveredRequests != 0 {
		t.Fatalf("uncovered requests = %d, want 0", got.UncoveredRequests)
	}
	if len(got.WorkUpdates) != 0 {
		t.Fatalf("update outside prior work was kept: %#v", got.WorkUpdates)
	}
	if got.Components[1] != "Module" || got.NewTerms != 1 {
		t.Fatalf("normalized tags/new terms = %#v / %d", got.Components, got.NewTerms)
	}
	if strings.Contains(got.Bullets[0].Text, "t404") || strings.Contains(got.Bullets[0].Text, "private-turn-a") {
		t.Fatalf("turn evidence leaked into user text: %q", got.Bullets[0].Text)
	}
}

func TestVocabularyTermsCannotGroundInlineCodeEvidence(t *testing.T) {
	raw := `{"headline":"API 확인","bullets":[{"text":"` + "`API`" + `를 바꿨다","turns":["t1"]}],"review":[],"kinds":[],"components":["API"],"screens":[],"new_component":null,"new_screen":null,"work":[],"skipped":[],"work_updates":[]}`
	evidence, err := EvidenceText([]byte(`{"vocabulary":{"components":[{"term":"API"}]},"turns":[{"input":"unrelated"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ValidateOutput(raw, ValidationOptions{TurnKeys: map[string]string{"t1": "turn"},
		InputJSON:  evidence,
		Vocabulary: TagVocabulary{Components: []string{"API"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Bullets) != 0 {
		t.Fatalf("vocabulary alone grounded inline code: %#v", got.Bullets)
	}
}

func TestValidateOutputCountsMissingRequestsAndRejectsEmptyContent(t *testing.T) {
	raw := strings.Replace(validModelOutput(""), `"work":[]`, `"work":[]`, 1)
	got, err := ValidateOutput(raw, ValidationOptions{
		TurnKeys:     map[string]string{"t1": "k1", "t2": "k2"},
		RequestTurns: []string{"t1", "t2"},
		InputJSON:    []byte(`{"source":"text"}`),
	})
	if err == nil {
		t.Fatal("empty headline/work output was accepted")
	}
	_ = got

	raw = strings.Replace(raw, `"work":[]`, `"work":[{"turn":"t1","kind":"request","text":"text","done":null,"decision":null,"note":null,"children":[]}]`, 1)
	got, err = ValidateOutput(raw, ValidationOptions{
		TurnKeys:     map[string]string{"t1": "k1", "t2": "k2"},
		RequestTurns: []string{"t1", "t2"},
		TurnStatuses: map[string]string{"t1": "ended"},
		InputJSON:    []byte(`{"source":"text"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.UncoveredRequests != 1 {
		t.Fatalf("uncovered requests = %d, want 1", got.UncoveredRequests)
	}
}

func validModelOutput(suffix string) string {
	return `{"headline":"정리","bullets":[],"review":[],"kinds":[],"components":[],"screens":[],"new_component":null,"new_screen":null,"work":[],"skipped":[],"work_updates":[]` + suffix + `}`
}
