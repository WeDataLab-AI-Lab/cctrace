package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReenrichNativeBlobMessageIsTypeRestricted(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"message":{"role":"user","content":"private"},"hash":"h","id":"r","shape":{"system":"rules"},"unrecognized":"omit"}`)
	for _, typ := range []string{"assistant", "api-request", "api-request-shape", "api-request-blob"} {
		got := string(reenrichMergeObjectForType("claude", typ, raw))
		if strings.Contains(got, `"message"`) != (typ == "api-request-blob") {
			t.Fatalf("%s message restriction %s", typ, got)
		}
		if strings.Contains(got, "unrecognized") {
			t.Fatalf("unknown raw added: %s", got)
		}
	}
	if got := string(reenrichMergeObjectForType("codex", "api-request-blob", raw)); got != "{}" {
		t.Fatalf("cross agent raw=%s", got)
	}
}
