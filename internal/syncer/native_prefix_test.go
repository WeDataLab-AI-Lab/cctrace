package syncer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/sessionlog"
	"cctrace/internal/store"
)

func TestNativeReferencesPreserveTranscriptPrefixes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, role, id, secondID string }{
		{"assistant-fragments", "assistant", "message-1", "message-1"},
		{"consecutive-user", "user", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "session.jsonl")
			source := `{"type":"` + tc.role + `","sessionId":"s","timestamp":"2026-10-18T00:00:00Z","uuid":"first","message":{"role":"` + tc.role + `","id":"` + tc.id + `","content":[{"type":"text","text":"PRIVATE-FIRST"}]}}` + "\n" +
				`{"type":"system","sessionId":"s","uuid":"metadata","subtype":"turn_duration","durationMs":3}` + "\n" +
				`{"type":"` + tc.role + `","sessionId":"s","timestamp":"2026-10-18T00:00:01Z","uuid":"second","message":{"role":"` + tc.role + `","id":"` + tc.secondID + `","content":[{"type":"text","text":"PRIVATE-SECOND"}]}}` + "\n"
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			rows, _, err := sessionlog.ScanFile(path, 0)
			if err != nil {
				t.Fatal(err)
			}
			state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			state.ObserveClaudeAccount(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), normalizeHome(dir), "account-1")
			s := New(dir, "", "", state, NewClient("http://example.invalid", "", ""), nil)
			convert := s.newRecordConverter(path, "project")
			var converted []*store.SessionRecord
			for _, row := range rows {
				converted = append(converted, convert(row, ""))
			}
			refs := decodeObject(t, string(converted[2].Raw))["cctraceReferences"].(map[string]any)
			messages := refs["messages"].([]any)
			if len(messages) != 2 {
				t.Fatalf("got %d message hashes, want row and cumulative prefix: %s", len(messages), converted[2].Raw)
			}
			expected := sha256.Sum256([]byte(`{"content":[{"text":"PRIVATE-FIRST","type":"text"},{"text":"PRIVATE-SECOND","type":"text"}],"role":"` + tc.role + `"}`))
			prefix := messages[1].(map[string]any)
			if prefix["hash"] != hex.EncodeToString(expected[:]) {
				t.Errorf("prefix hash=%v want %x", prefix["hash"], expected)
			}
			sources := prefix["records"].([]any)
			if len(sources) != 2 || sources[0].(map[string]any)["uuid"] != "first" || sources[1].(map[string]any)["uuid"] != "second" || sources[0].(map[string]any)["block_start"] != float64(0) || sources[1].(map[string]any)["block_end"] != float64(1) {
				t.Errorf("prefix original-source map=%v", sources)
			}
			before, _ := json.Marshal(refs)
			Redact(converted, RedactPolicy{UserPrompts: true, ToolDetails: true})
			after, _ := json.Marshal(decodeObject(t, string(converted[2].Raw))["cctraceReferences"])
			if string(before) != string(after) {
				t.Errorf("redaction changed prefix sources: %s", after)
			}
			encoded, _ := json.Marshal(converted)
			if strings.Contains(string(encoded), "PRIVATE-") {
				t.Errorf("redacted transcript leaked bodies: %s", encoded)
			}
		})
	}
}

func TestNativeReferenceResumeMarksOnlyTheUnknownPrefixGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	prefix := `{"type":"assistant","sessionId":"s","uuid":"before","message":{"role":"assistant","id":"message-1","content":[{"type":"text","text":"PRECONSENT"}]}}` + "\n"
	source := prefix + `{"type":"assistant","sessionId":"s","uuid":"resumed","message":{"role":"assistant","id":"message-1","content":[{"type":"text","text":"RESUMED"}]}}` + "\n" +
		`{"type":"system","sessionId":"s","uuid":"metadata","subtype":"turn_duration"}` + "\n" +
		`{"type":"assistant","sessionId":"s","uuid":"same-group","message":{"role":"assistant","id":"message-1","content":[{"type":"text","text":"SAME"}]}}` + "\n" +
		`{"type":"assistant","sessionId":"s","uuid":"new-group","message":{"role":"assistant","id":"message-2","content":[{"type":"text","text":"NEW"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	rows, _, err := sessionlog.ScanFile(path, int64(len(prefix)))
	if err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffset(path, int64(len(prefix)))
	s := New(dir, "", "", state, NewClient("http://example.invalid", "", ""), nil)
	convert := s.newRecordConverter(path, "project")
	for _, row := range rows {
		out := convert(row, "")
		if row.Type != "assistant" {
			continue
		}
		refs := decodeObject(t, string(out.Raw))["cctraceReferences"].(map[string]any)
		incomplete, _ := refs["prefix_incomplete"].(bool)
		if incomplete != (row.UUID != "new-group") {
			t.Errorf("row=%s prefix_incomplete=%v", row.UUID, incomplete)
		}
		if strings.Contains(string(out.Raw), "PRECONSENT") {
			t.Errorf("resume reread before consent: %s", out.Raw)
		}
	}
}

func TestNativeReferencePrefixDoesNotCrossAnUnselectedSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	var source strings.Builder
	for _, uuid := range []string{"first", "unselected", "last"} {
		source.WriteString(`{"type":"user","sessionId":"s","uuid":"` + uuid + `","message":{"role":"user","content":"synthetic"}}` + "\n")
	}
	if err := os.WriteFile(path, []byte(source.String()), 0600); err != nil {
		t.Fatal(err)
	}
	rows, _, err := sessionlog.ScanFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(dir, "", "", state, NewClient("http://example.invalid", "", ""), nil)
	convert := s.newRecordConverter(path, "project")
	_ = convert(rows[0], "")
	last := convert(rows[2], "")
	refs := decodeObject(t, string(last.Raw))["cctraceReferences"].(map[string]any)
	if refs["prefix_incomplete"] != true {
		t.Errorf("unselected source gap not marked: %s", last.Raw)
	}
	messages := refs["messages"].([]any)
	if len(messages) != 1 {
		t.Errorf("invented prefix across source exclusion: %s", last.Raw)
	}
	encoded, _ := json.Marshal(refs)
	if strings.Contains(string(encoded), "first") || strings.Contains(string(encoded), "unselected") {
		t.Errorf("prefix names sources outside selected range: %s", encoded)
	}
}
