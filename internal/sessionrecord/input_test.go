package sessionrecord

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildInputUsesOpaqueTurnNamesAndKeepsMappingServerSide(t *testing.T) {
	secretKey := "f9748218-4f2f-43d8-a012-123456789abc"
	built, err := BuildInput(Input{Turns: []Turn{{Key: secretKey, Ordinal: 14, Request: true, Status: "ended"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(built.JSON), secretKey) {
		t.Fatalf("turn key escaped into model input: %s", built.JSON)
	}
	if got := built.TurnKeys["t1"]; got != secretKey {
		t.Fatalf("TurnKeys[t1] = %q, want original key", got)
	}
	if !strings.Contains(string(built.JSON), `"t":"t1"`) {
		t.Fatalf("opaque turn name missing from input: %s", built.JSON)
	}
}

func TestBuildInputNullsNonRequestAndRedactedInputAndCountsGap(t *testing.T) {
	built, err := BuildInput(Input{Turns: []Turn{
		{Key: "a", Request: true, Status: "ended", Inputs: []Text{{Kind: "typed", Text: "[redacted by cctrace client]"}}},
		{Key: "b", Request: false, Status: "ended", Inputs: []Text{{Kind: "notification", Text: "private notification"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(built.JSON, &got); err != nil {
		t.Fatal(err)
	}
	turns := got["turns"].([]any)
	first := turns[0].(map[string]any)["inputs"].([]any)[0].(map[string]any)
	if first["text"] != nil {
		t.Fatalf("redacted request text must be null, got %#v", first["text"])
	}
	second := turns[1].(map[string]any)["inputs"].([]any)[0].(map[string]any)
	if second["text"] != nil {
		t.Fatalf("non-request context text must be null, got %#v", second["text"])
	}
	if strings.Contains(string(built.JSON), "private notification") || strings.Contains(string(built.JSON), "redacted by cctrace") {
		t.Fatalf("private or redaction-marker text escaped: %s", built.JSON)
	}
	if built.UncoveredRequests != 1 {
		t.Fatalf("uncovered requests = %d, want 1", built.UncoveredRequests)
	}
}

func TestBuildInputCountsPartiallyRedactedRequestAsUncovered(t *testing.T) {
	built, err := BuildInput(Input{Turns: []Turn{{
		Key: "k", Request: true, Status: "ended", Inputs: []Text{
			{Kind: "typed", Text: "visible text"},
			{Kind: "queued", Text: "[redacted by cctrace client]"},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if built.UncoveredRequests != 1 {
		t.Fatalf("uncovered requests = %d, want 1 for a partially unreadable request", built.UncoveredRequests)
	}
}

func TestBuildInputBoundsTurnAndExcerptsFinalText(t *testing.T) {
	long := strings.Repeat("한", 15000)
	built, err := BuildInput(Input{Turns: []Turn{{
		Key: "turn-key", Request: true, Status: "ended", StartedAt: time.Date(2026, 10, 9, 1, 2, 59, 0, time.UTC),
		Final: long,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(built.JSON, &got); err != nil {
		t.Fatal(err)
	}
	turn := got["turns"].([]any)[0].(map[string]any)
	final := turn["final"].(string)
	if !strings.Contains(final, "중략") || len([]byte(final)) > MaxTurnBytes {
		t.Fatalf("final excerpt does not obey byte budget: bytes=%d excerpt=%q", len([]byte(final)), final)
	}
	if turn["at"] != "2026-10-09T01:02Z" {
		t.Fatalf("at = %v, want minute precision UTC", turn["at"])
	}
}

func TestDetectLanguageFromRequestInputsOnly(t *testing.T) {
	got := DetectLanguage([]Turn{
		{Request: false, Inputs: []Text{{Text: strings.Repeat("日本語", 100)}}},
		{Request: true, Inputs: []Text{{Text: strings.Repeat("한글", 40)}}},
	})
	if got != "ko" {
		t.Fatalf("DetectLanguage() = %q, want ko", got)
	}
}
