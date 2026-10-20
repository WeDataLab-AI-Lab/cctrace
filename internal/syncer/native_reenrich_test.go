package syncer

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/sessionlog"
)

func TestNativeReenrichRequiresCollectedFileState(t *testing.T) {
	t.Parallel()
	for _, includeContent := range []bool{false, true} {
		t.Run(map[bool]string{false: "metadata", true: "include-content"}[includeContent], func(t *testing.T) {
			t.Parallel()
			srv, sent := reenrichScopeServer(t, nil)
			dir := t.TempDir()
			path, _ := writeScopeSession(t, dir, []string{
				`{"type":"api-request-blob","sessionId":"uncollected","hash":"uncollected-message","message":{"role":"user","content":"UNCONSENTED-NATIVE-CONTENT"}}`,
			})
			state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			s := New(dir, "", "", state, NewClient(srv.URL, "", ""), nil)
			n, err := s.ReenrichOnce(context.Background(), includeContent)
			if err != nil {
				t.Fatal(err)
			}
			if n != 0 || len(sent()) != 0 {
				t.Fatalf("without collected history: updated=%d sent=%d, want none", n, len(sent()))
			}
			if state.Files[path] != nil {
				t.Fatal("reenrich created collected history for an uncollected file")
			}
		})
	}
}

func TestReenrichFileWithoutCollectedStateDoesNotReadSource(t *testing.T) {
	t.Parallel()
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(t.TempDir(), "", "", state, nil, nil)
	n, changed, err := s.reenrichFile(context.Background(), filepath.Join(t.TempDir(), "absent.jsonl"), true)
	if err != nil || n != 0 || changed {
		t.Fatalf("unknown source must be ignored before opening: updated=%d changed=%v error=%v", n, changed, err)
	}
}

func TestNativeReenrichHonorsCollectedRangeAndContentPolicy(t *testing.T) {
	t.Parallel()
	for _, include := range []bool{false, true} {
		for _, policy := range []RedactPolicy{{}, {UserPrompts: true}, {ToolDetails: true}} {
			srv, sent := reenrichScopeServer(t, nil)
			dir := t.TempDir()
			path, starts := writeScopeSession(t, dir, []string{
				`{"type":"api-request-blob","sessionId":"native","hash":"before","message":{"role":"user","content":"PRECONSENT-CONTENT"}}`,
				`{"type":"api-request-shape","sessionId":"native","shapeHash":"shape-1","timestamp":"2026-10-18T00:00:00Z","shape":{"system":[{"type":"text","text":"NATIVE-SYSTEM"}],"tools":[{"name":"Read","input_schema":{"properties":{"NATIVE-PROPERTY":{"type":"string"}}}}]}}`,
				`{"type":"api-request","sessionId":"native","id":"request-1","timestamp":"2026-10-18T00:00:01Z","shapeHash":"shape-1","params":{"model":"synthetic","max_tokens":4096},"keep":0,"tail":["message-1"],"requestDigest":"digest-1"}`,
				`{"type":"api-request-blob","sessionId":"native","hash":"message-1","message":{"role":"user","content":[{"type":"text","text":"NATIVE-PROMPT"}]}}`,
				`{"type":"api-request-blob","sessionId":"native","hash":"unsent","message":{"role":"user","content":"UNSENT-CONTENT"}}`,
			})
			state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			state.SetOffset(path, starts[4])
			state.Files[path].SkippedAtFirstSync = starts[1]
			client := NewClient(srv.URL, "", "")
			client.SetRedactPolicy(policy)
			s := New(dir, "", "", state, client, nil)
			if _, err := s.ReenrichOnce(context.Background(), include); err != nil {
				t.Fatal(err)
			}
			rows := sent()
			if len(rows) != 3 {
				t.Fatalf("include=%v policy=%+v: sent %d rows, want only three collected protocol rows", include, policy, len(rows))
			}
			encoded, _ := json.Marshal(rows)
			if strings.Contains(string(encoded), "PRECONSENT") || strings.Contains(string(encoded), "UNSENT") {
				t.Errorf("reenrich escaped collected range: %s", encoded)
			}
			for _, body := range []string{"NATIVE-SYSTEM", "NATIVE-PROPERTY"} {
				if strings.Contains(string(encoded), body) != (include && !policy.Enabled()) {
					t.Errorf("include=%v policy=%+v: wrong content policy for %s: %s", include, policy, body, encoded)
				}
			}
			if strings.Contains(string(encoded), "NATIVE-PROMPT") != (include && !policy.UserPrompts) {
				t.Errorf("include=%v policy=%+v: wrong blob prompt policy: %s", include, policy, encoded)
			}
			originals, _, err := sessionlog.ScanFile(path, 0)
			if err != nil {
				t.Fatal(err)
			}
			for i, row := range rows {
				original := toStoreRecord(originals[i+1], "", "", "project")
				if row.UUID != original.UUID || !row.Ts.Equal(original.Ts) {
					t.Errorf("content policy changed UUID-less native identity: %s != %s", row.UUID, original.UUID)
				}
			}
			if state.GetOffset(path) != starts[4] {
				t.Error("reenrich changed sync offset")
			}
		}
	}
}
