//go:build codex_replay

package codexlog_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"cctrace/internal/codexlog"
)

// TestReplayLocalCodexFiles reads real ~/.codex/sessions/ files and prints
// normalized Record output. Run with:
//
//	go test -tags codex_replay ./internal/codexlog/ -v -run TestReplayLocalCodexFiles
func TestReplayLocalCodexFiles(t *testing.T) {
	dir := codexlog.DefaultCodexDir()
	files, err := codexlog.FindJSONLFiles(dir)
	if err != nil {
		t.Fatalf("find files: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no files found under %s", dir)
	}

	if len(files) > 3 {
		files = files[:3]
	}

	for _, f := range files {
		sid := codexlog.SessionIDFromPath(f)
		recs, _, err := codexlog.ScanFile(f, 0, sid)
		if err != nil {
			t.Logf("scan %s: %v", f, err)
			continue
		}
		fmt.Printf("\n--- %s (session=%s, records=%d) ---\n", f, sid, len(recs))
		for i, r := range recs {
			if i >= 5 {
				fmt.Printf("  ... (%d more)\n", len(recs)-5)
				break
			}
			b, _ := json.Marshal(map[string]interface{}{
				"type":    r.RecordType,
				"cwd":     r.CWD,
				"model":   r.Model,
				"content": r.Content[:min(len(r.Content), 80)],
				"tool":    r.ToolName,
			})
			fmt.Printf("  %s\n", b)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
