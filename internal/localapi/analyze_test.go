package localapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"cctrace/internal/jsonlscan"
	"cctrace/internal/sessionfacts"
)

// goldenReadAt pins the clock the analysis reads: a Codex turn without an end
// line is open or undeterminable depending on it.
var goldenReadAt = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

var goldenClaudeLines = []string{
	`{"type":"ai-title","aiTitle":"sample title","sessionId":"native-claude"}`,
	`{"type":"user","timestamp":"2026-01-01T00:00:00Z","uuid":"u1","sessionId":"native-claude","cwd":"/work/alpha","promptId":"p1","promptSource":"typed","message":{"role":"user","content":"please run the tests"}}`,
	`{"type":"assistant","timestamp":"2026-01-01T00:00:02Z","uuid":"a1","parentUuid":"u1","sessionId":"native-claude","message":{"id":"m1","role":"assistant","model":"test-model","content":[{"type":"text","text":"Running them."},{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"go test ./..."}}],"usage":{"input_tokens":10,"output_tokens":2}}}`,
	`{"type":"user","timestamp":"2026-01-01T00:00:03Z","uuid":"u2","parentUuid":"a1","sessionId":"native-claude","promptId":"p1","permissionDecision":{"decision":"accept","source":"user_temporary"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"ok"}]},"toolUseResult":{"stdout":"ok"}}`,
	`{"type":"assistant","timestamp":"2026-01-01T00:00:04Z","uuid":"a2","parentUuid":"u2","sessionId":"native-claude","message":{"id":"m2","role":"assistant","model":"test-model","content":[{"type":"tool_use","id":"tu2","name":"AskUserQuestion","input":{"questions":[{"question":"Which one?","header":"Pick","multiSelect":false,"options":[{"label":"A (Recommended)","description":"first"},{"label":"B","description":"second"}]}]}}],"usage":{"input_tokens":12,"output_tokens":3}}}`,
	`{"type":"user","timestamp":"2026-01-01T00:00:05Z","uuid":"u3","parentUuid":"a2","sessionId":"native-claude","promptId":"p1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu2","content":"answered"}]},"toolUseResult":{"questions":[{"question":"Which one?","header":"Pick","multiSelect":false,"options":[{"label":"A (Recommended)","description":"first"},{"label":"B","description":"second"}]}],"answers":{"Which one?":"B"}}}`,
	`{"type":"assistant","timestamp":"2026-01-01T00:00:06Z","uuid":"a3","parentUuid":"u3","sessionId":"native-claude","message":{"id":"m3","role":"assistant","model":"test-model","content":[{"type":"text","text":"Shall I continue?"}],"usage":{"input_tokens":14,"output_tokens":4}}}`,
	`{"type":"user","timestamp":"2026-01-01T00:01:00Z","uuid":"u4","parentUuid":"a3","sessionId":"native-claude","promptId":"p2","promptSource":"typed","message":{"role":"user","content":"yes"}}`,
	`{"type":"assistant","timestamp":"2026-01-01T00:01:02Z","uuid":"a4","parentUuid":"u4","sessionId":"native-claude","message":{"id":"m4","role":"assistant","model":"test-model","content":[{"type":"tool_use","id":"tu3","name":"Bash","input":{"command":"rm -rf build"}}],"usage":{"input_tokens":16,"output_tokens":5}}}`,
	`{"type":"user","timestamp":"2026-01-01T00:01:03Z","uuid":"u5","parentUuid":"a4","sessionId":"native-claude","promptId":"p2","toolDenialKind":"user-rejected","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu3","is_error":true,"content":"rejected"}]}}`,
	`{"type":"user","timestamp":"2026-01-01T00:01:04Z","uuid":"u6","parentUuid":"u5","sessionId":"native-claude","promptId":"p2","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`,
	`{"type":"system","timestamp":"2026-01-01T00:02:00Z","uuid":"s1","parentUuid":"u6","sessionId":"native-claude","subtype":"compact_boundary","content":"Conversation compacted"}`,
	`{"type":"user","timestamp":"2026-01-01T00:02:01Z","uuid":"u7","logicalParentUuid":"u6","sessionId":"native-claude","isCompactSummary":true,"message":{"role":"user","content":"This session is being continued."}}`,
	`{"type":"user","timestamp":"2026-01-01T00:03:00Z","uuid":"u8","parentUuid":"u7","sessionId":"native-claude","promptId":"p3","promptSource":"typed","message":{"role":"user","content":"now write the summary"}}`,
	`{"type":"assistant","timestamp":"2026-01-01T00:03:02Z","uuid":"a5","parentUuid":"u8","sessionId":"native-claude","message":{"id":"m5","role":"assistant","model":"test-model","content":[{"type":"text","text":"Summary written."}],"usage":{"input_tokens":18,"output_tokens":6}}}`,
}

var goldenCodexLines = []string{
	`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"01234567-89ab-cdef-0123-456789abcdef","cwd":"/work/beta","originator":"codex-tui"}}`,
	`{"timestamp":"2026-01-01T00:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-one"}}`,
	`{"timestamp":"2026-01-01T00:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"run the build"}]}}`,
	`{"timestamp":"2026-01-01T00:00:03Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"call-1","input":"{\"cmd\":\"make build\"}"}}`,
	`{"timestamp":"2026-01-01T00:00:04Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call-1","output":"built"}}`,
	`{"timestamp":"2026-01-01T00:00:05Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Built."}]}}`,
	`{"timestamp":"2026-01-01T00:00:06Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":12,"cached_input_tokens":3,"output_tokens":4}}}}`,
	`{"timestamp":"2026-01-01T00:00:07Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-one"}}`,
	`{"timestamp":"2026-01-01T00:01:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-two"}}`,
	`{"timestamp":"2026-01-01T00:01:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"stop that"}]}}`,
	`{"timestamp":"2026-01-01T00:01:02Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"turn-two","reason":"interrupted"}}`,
	`{"timestamp":"2026-01-01T00:02:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-three"}}`,
	`{"timestamp":"2026-01-01T00:02:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"keep going"}]}}`,
	`{"timestamp":"2026-01-01T00:02:02Z","type":"unrecognised_line","payload":{"note":"kept for the line count"}}`,
}

const goldenCodexFile = "rollout-2026-01-01T00-00-00-01234567-89ab-cdef-0123-456789abcdef.jsonl"

// writeGoldenHome writes one agent home holding one session file.
func writeGoldenHome(t *testing.T, agent string, lines []string, tail string) (home, file string) {
	t.Helper()
	home = t.TempDir()
	dir := filepath.Join(home, "projects", "-work-alpha")
	file = filepath.Join(dir, "session.jsonl")
	if agent == "codex" {
		dir = filepath.Join(home, "sessions")
		file = filepath.Join(dir, goldenCodexFile)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(lines, "\n") + "\n" + tail
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return home, file
}

func goldenServer(t *testing.T, agent, home string) (*Server, string) {
	t.Helper()
	token := base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	opts := Options{Address: "127.0.0.1:12345", Token: token, AllowedOrigins: []string{"http://127.0.0.1:60895"}}
	if agent == "codex" {
		opts.CodexDirs = []string{home}
	} else {
		opts.ClaudeDirs = []string{home}
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return goldenReadAt }
	return s, token
}

// normalizeGolden replaces what depends on the temporary directory: the local
// id, the revision, and the Codex row keys derived from the local id.
func normalizeGolden(s *Server, body []byte) []byte {
	entries, _ := s.files()
	text := string(body)
	for _, e := range entries {
		for offset := int64(0); offset <= e.info.Size(); offset++ {
			key := hash(e.id, strconv.FormatInt(offset, 10))
			if strings.Contains(text, key) {
				text = strings.ReplaceAll(text, key, "codex-row-"+strconv.FormatInt(offset, 10))
			}
		}
		text = strings.ReplaceAll(text, e.id, "local-id")
		text = strings.ReplaceAll(text, e.revision, "revision")
	}
	return []byte(text)
}

// TestAnalysisResponseMatchesGolden pins the analysis endpoint's bytes with a
// fixed read time, so a restructuring of the read path cannot change them.
func TestAnalysisResponseMatchesGolden(t *testing.T) {
	// The oversized cases lower the global line limit, so no case runs in parallel.
	old := jsonlscan.MaxLineBytes
	defer func() { jsonlscan.MaxLineBytes = old }()
	oversized := `{"type":"ignored","padding":"` + strings.Repeat("x", 2048) + "\"}\n"
	for _, tc := range []struct {
		name, agent, tail string
		lines             []string
		limit             int
	}{
		{"claude", "claude", "", goldenClaudeLines, old},
		{"codex", "codex", "", goldenCodexLines, old},
		{"claude_invalid", "claude", "{broken}\n", goldenClaudeLines, old},
		{"claude_oversized", "claude", oversized, goldenClaudeLines, 1024},
		{"codex_oversized_middle", "codex", oversized + goldenCodexLines[len(goldenCodexLines)-1] + "\n", goldenCodexLines, 1024},
		{"codex_oversized_tail", "codex", oversized, goldenCodexLines, 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jsonlscan.MaxLineBytes = tc.limit
			home, _ := writeGoldenHome(t, tc.agent, tc.lines, tc.tail)
			s, token := goldenServer(t, tc.agent, home)
			code, body := request(t, s, token, sessionRoute(t, s, token)+"/analysis")
			if code != 200 {
				t.Fatalf("analysis status %d: %s", code, body)
			}
			got := normalizeGolden(s, body)
			path := filepath.Join("testdata", "golden", "analysis_"+tc.name+".json")
			if os.Getenv("UPDATE_LOCALAPI_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("analysis bytes changed\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

// TestAnalysisOfAChangedFileIsAConflict keeps the 409 mapping for a file that
// no longer matches the entry it was listed as.
func TestAnalysisOfAChangedFileIsAConflict(t *testing.T) {
	t.Parallel()
	home, file := writeGoldenHome(t, "claude", goldenClaudeLines, "")
	s, _ := goldenServer(t, "claude", home)
	entries, err := s.files()
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries %d: %v", len(entries), err)
	}
	if err := os.WriteFile(file, []byte(goldenClaudeLines[1]+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.readDetail(w, httptest.NewRequest("GET", "http://127.0.0.1:12345/v1/sessions/x/analysis", nil), entries[0], "analysis")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "revision_changed") {
		t.Fatalf("changed file status %d: %s", w.Code, w.Body.String())
	}
}

func findOne(t *testing.T, agent, home string) Entry {
	t.Helper()
	claude, codex := []string{home}, []string(nil)
	if agent == "codex" {
		claude, codex = nil, []string{home}
	}
	entries, err := Find(claude, codex)
	if err != nil || len(entries) != 1 {
		t.Fatalf("find %d: %v", len(entries), err)
	}
	return entries[0]
}

// TestAnalyzeVisitsEveryPhysicalLineOnce pins the Visit contract: one call per
// physical line in file order, skipped oversized lines counted as nil, lines
// without a record included, and an unfinished last line left out.
func TestAnalyzeVisitsEveryPhysicalLineOnce(t *testing.T) {
	// The line-size hook is global, so these cases must not run in parallel.
	old := jsonlscan.MaxLineBytes
	defer func() { jsonlscan.MaxLineBytes = old }()
	jsonlscan.MaxLineBytes = 1024
	oversized := `{"type":"ignored","padding":"` + strings.Repeat("x", 2048) + `"}`
	for _, tc := range []struct {
		agent string
		lines []string
	}{{"claude", goldenClaudeLines}, {"codex", goldenCodexLines}} {
		physical := append([]string{}, tc.lines[:3]...)
		physical = append(physical, oversized, oversized, "", "{broken}")
		physical = append(physical, tc.lines[3:]...)
		physical = append(physical, oversized)
		// An oversized line ending the file without a newline is still a line;
		// a short unfinished one is not.
		for _, tail := range []string{oversized, `{"type":"user"`} {
			home, _ := writeGoldenHome(t, tc.agent, physical, tail)
			var seen []RowRef
			var raws [][]byte
			a, err := Analyze(t.Context(), findOne(t, tc.agent, home), AnalyzeOptions{ReadAt: goldenReadAt, Visit: func(row RowRef, raw []byte) {
				seen = append(seen, row)
				raws = append(raws, raw)
			}})
			if err != nil {
				t.Fatal(err)
			}
			want := append(append([]string{}, physical...), tail)
			if tail != oversized {
				want = want[:len(want)-1]
			}
			if len(seen) != len(want) {
				t.Fatalf("%s: visited %d lines, want %d", tc.agent, len(seen), len(want))
			}
			ids := map[int64]bool{}
			for _, row := range a.Input.Rows {
				ids[row.ID] = true
			}
			for _, row := range a.Input.CodexRows {
				ids[row.ID] = true
			}
			offset := int64(0)
			for i, line := range want {
				row := seen[i]
				if row.Line != i+1 || row.Offset != offset {
					t.Fatalf("%s: line %d visited as %+v, want offset %d", tc.agent, i+1, row, offset)
				}
				if line == oversized && raws[i] != nil || line != oversized && string(raws[i]) != line {
					t.Fatalf("%s: line %d raw mismatch", tc.agent, i+1)
				}
				if row.ID != 0 && (!ids[row.ID] || row.ID != offset+1) {
					t.Fatalf("%s: line %d names row %d, which the analysis does not hold", tc.agent, i+1, row.ID)
				}
				offset += int64(len(line)) + 1
			}
			if a.Incomplete == "" {
				t.Fatalf("%s: unreadable lines left the analysis complete", tc.agent)
			}
		}
	}
}

// TestAnalyzeFactsMatchAnalysisEndpoint compares the exported path with the
// endpoint: a Visit callback changes nothing, and the endpoint's bytes are the
// response built from Analyze's facts.
func TestAnalyzeFactsMatchAnalysisEndpoint(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{"claude", "codex"} {
		lines := goldenClaudeLines
		if agent == "codex" {
			lines = goldenCodexLines
		}
		home, _ := writeGoldenHome(t, agent, lines, "")
		s, token := goldenServer(t, agent, home)
		entry := findOne(t, agent, home)
		plain, err := Analyze(t.Context(), entry, AnalyzeOptions{ReadAt: goldenReadAt})
		if err != nil {
			t.Fatal(err)
		}
		visited, err := Analyze(t.Context(), entry, AnalyzeOptions{ReadAt: goldenReadAt, Visit: func(RowRef, []byte) {}})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(plain, visited) {
			t.Fatalf("%s: Visit changed the analysis", agent)
		}
		var built bytes.Buffer
		_ = json.NewEncoder(&built).Encode(analysisResponse(entry.file, plain.Input, sessionfacts.TimelineOutput{Output: plain.Facts.Output, Timeline: plain.Facts.Timeline}))
		code, body := request(t, s, token, "/v1/sessions/"+entry.LocalID+"/analysis")
		if code != 200 || built.String() != string(body) {
			t.Fatalf("%s: endpoint %d differs from Analyze\n got: %s\nwant: %s", agent, code, body, built.String())
		}
	}
}

func TestAnalyzeReportsAFileChangedWhileRead(t *testing.T) {
	t.Parallel()
	home, file := writeGoldenHome(t, "claude", goldenClaudeLines, "")
	once := false
	_, err := Analyze(t.Context(), findOne(t, "claude", home), AnalyzeOptions{Visit: func(RowRef, []byte) {
		if once {
			return
		}
		once = true
		f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(goldenClaudeLines[1] + "\n")
		_ = f.Close()
	}})
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
}

func TestFindListsWhatTheServerLists(t *testing.T) {
	t.Parallel()
	claude, _ := writeGoldenHome(t, "claude", goldenClaudeLines, "")
	codex, _ := writeGoldenHome(t, "codex", goldenCodexLines, "")
	missing := filepath.Join(claude, "missing")
	token := base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	s, err := New(Options{Address: "127.0.0.1:12345", Token: token, ClaudeDirs: []string{claude, missing}, CodexDirs: []string{codex}, AllowedOrigins: []string{"http://127.0.0.1:60895"}})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := Find([]string{claude, missing}, []string{codex})
	if err != nil {
		t.Fatal(err)
	}
	code, body := request(t, s, token, "/v1/sessions")
	var list ListResponse
	if code != 200 || json.Unmarshal(body, &list) != nil || len(list.Sessions) != 2 || len(entries) != 2 {
		t.Fatalf("list %d with %d entries: %s", code, len(entries), body)
	}
	for i, e := range entries {
		if list.Sessions[i].LocalID != e.LocalID || list.Sessions[i].Agent != e.Agent || list.Sessions[i].Revision != e.file.revision {
			t.Fatalf("entry %d differs from the listing", i)
		}
	}
}
