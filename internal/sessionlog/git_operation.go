package sessionlog

import (
	"encoding/json"
	"unicode/utf8"

	"cctrace/internal/gitcmd"
)

// This undocumented field is kept through an explicit nested allowlist. In
// particular, adding it must not carry arbitrary tool output around redaction.
func keepGitOperation(raw json.RawMessage) (json.RawMessage, bool) {
	var input map[string]json.RawMessage
	if json.Unmarshal(raw, &input) != nil || input == nil {
		return nil, true
	}
	kept := make(map[string]json.RawMessage)
	unread := false
	for name, keys := range map[string][]string{
		"commit": {"sha", "kind", "branch"},
		"push":   {"branch"},
		"pr":     {"action", "number", "url"},
		"branch": {"action", "ref"},
	} {
		value, ok := input[name]
		if !ok || !present(value) {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(value, &fields) != nil || fields == nil {
			unread = true
			continue
		}
		out := make(map[string]json.RawMessage)
		for _, key := range keys {
			v, ok := fields[key]
			if !ok || !present(v) {
				continue
			}
			if key == "number" {
				var n int64
				if json.Unmarshal(v, &n) != nil || n <= 0 {
					unread = true
					continue
				}
				out[key] = v
				continue
			}
			var text string
			if json.Unmarshal(v, &text) != nil {
				unread = true
				continue
			}
			if key == "sha" && !gitcmd.ValidHash(text) {
				unread = true
				continue
			}
			if len(text) > 512 {
				end := 512
				for end > 0 && !utf8.RuneStart(text[end]) {
					end--
				}
				text = text[:end]
			}
			out[key], _ = json.Marshal(text)
		}
		if len(out) > 0 {
			kept[name] = marshalMembers(out)
		}
	}
	if len(kept) == 0 && len(input) > 0 {
		unread = true
	}
	return marshalMembers(kept), unread
}
