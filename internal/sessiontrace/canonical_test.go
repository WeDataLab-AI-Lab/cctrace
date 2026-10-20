package sessiontrace

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalJSONAndMessageHash(t *testing.T) {
	t.Parallel()
	v := map[string]any{"z": "<>&", "a": json.Number("9007199254740993")}
	b, err := CanonicalJSON(v)
	if err != nil || string(b) != `{"a":9007199254740993,"z":"<>&"}` {
		t.Fatalf("canonical=%s err=%v", b, err)
	}
	text, err := MessageHash(map[string]any{"role": "user", "content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := MessageHash(map[string]any{"content": []any{map[string]any{"type": "text", "text": "hello", "cache_control": map[string]any{"type": "ephemeral"}}}, "role": "user"})
	if err != nil || text != blocks {
		t.Fatalf("different message hashes: %s %s %v", text, blocks, err)
	}
	if !strings.Contains(string(b), "9007199254740993") {
		t.Fatal("number rounded")
	}
}

func TestTranscriptPrefixesAreImmutable(t *testing.T) {
	t.Parallel()
	first := map[string]any{"role": "assistant", "id": "m", "content": []any{map[string]any{"type": "text", "text": "a"}}}
	second := map[string]any{"role": "assistant", "id": "m", "content": []any{map[string]any{"type": "text", "text": "b"}}}
	prefixes, err := TranscriptPrefixes([]map[string]any{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixes) != 2 || len(prefixes[0]["content"].([]any)) != 1 || len(prefixes[1]["content"].([]any)) != 2 {
		t.Fatalf("mutable prefixes: %#v", prefixes)
	}
	if len(first["content"].([]any)) != 1 {
		t.Fatal("mutated caller")
	}
}

func TestCanonicalMatchesNativeJSONNumbersAndLineSeparators(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON(map[string]any{"a": json.Number("1.0"), "b": json.Number("1e0"), "c": "\u2028\u2029\\u2028"})
	want := "{\"a\":1,\"b\":1,\"c\":\"\u2028\u2029\\\\u2028\"}"
	if err != nil || string(got) != want {
		t.Fatalf("canonical %q want %q err=%v", got, want, err)
	}
}

func TestNativeSystemMessageRetainsProtocolFields(t *testing.T) {
	t.Parallel()
	message := map[string]any{"role": "system", "content": "rules", "output_config": map[string]any{"effort": "high"}}
	got, err := NormalizeMessage(message)
	if err != nil || got["output_config"] == nil {
		t.Fatalf("native system fields lost: %v %v", got, err)
	}
}

func TestCanonicalUTF16KeyOrder(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON(map[string]any{"\ue000": 1, "\U00010000": 2})
	if err != nil || string(got) != "{\"\U00010000\":2,\"\ue000\":1}" {
		t.Fatalf("native key order=%q err=%v", got, err)
	}
}
