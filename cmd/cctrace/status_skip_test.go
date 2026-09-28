package main

import (
	"strings"
	"testing"

	"cctrace/internal/syncer"
)

// The first sync skips session files that already exist, and everything written
// before that moment is never collected. PR #218 made that audible in sync.log,
// which is enough for a post-mortem and not enough for a user: the hook runs the
// daemon with --daemon so the line never reaches a terminal, the guide only
// points at sync.log once you already suspect a problem, and the line is written
// exactly once per file so log rotation eventually takes it away.
//
// `cctrace status` is the one place the user is already sent — init ends by
// telling them to run it. These tests pin what it must say there.
func TestFirstRunSkipNotice(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]*syncer.FileState
		want  []string
		empty bool
	}{
		{
			// Nothing skipped is the ordinary case and must stay quiet. A notice
			// that shows up on every healthy install is one people learn to skip
			// past, and then it is not there when it matters.
			name: "no skips stays silent",
			files: map[string]*syncer.FileState{
				"/a.jsonl": {Offset: 100},
			},
			empty: true,
		},
		{
			name: "one file reports the file and the bytes",
			files: map[string]*syncer.FileState{
				"/a.jsonl": {Offset: 620, SkippedAtFirstSync: 620},
			},
			want: []string{"1 file", "620 B", "not collected"},
		},
		{
			// Both numbers matter and they are different questions: how much of my
			// history is missing, and how many sessions does it touch.
			name: "several files sum the bytes",
			files: map[string]*syncer.FileState{
				"/a.jsonl": {SkippedAtFirstSync: 1000},
				"/b.jsonl": {SkippedAtFirstSync: 24},
				"/c.jsonl": {SkippedAtFirstSync: 0},
			},
			want: []string{"2 files", "1.0 KiB", "not collected"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := firstRunSkipNotice(tc.files)
			if tc.empty {
				if got != "" {
					t.Fatalf("notice = %q, want empty", got)
				}
				return
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("notice %q does not contain %q", got, want)
				}
			}
		})
	}
}

// TestFirstRunSkipNoticeIsNotHealthy: the status line this attaches to reads
// "[OK] healthy". A skip means content is missing, so a run that reports only
// "healthy" is not merely terse — it is wrong, and it is wrong in the direction
// that stops the user from looking further.
func TestFirstRunSkipNoticeIsNotHealthy(t *testing.T) {
	got := firstRunSkipNotice(map[string]*syncer.FileState{
		"/a.jsonl": {SkippedAtFirstSync: 620},
	})
	if !strings.Contains(got, "[!]") {
		t.Errorf("notice %q carries no warning marker", got)
	}
	if strings.Contains(got, "[OK]") {
		t.Errorf("notice %q still reads as OK", got)
	}
}

// The notice above can only report what status actually reads. That list started
// as a literal naming two agents and stayed that way when gjc and omo arrived, so
// their skips were recorded in state and never shown — and they are the two that
// need it most, since neither installs a hook and "turning the integration on" is
// therefore always a first run.
//
// The tests above did not catch it because they exercise the message, not the set
// of files it is built from.
func TestSyncStatePathsCoverEveryAgent(t *testing.T) {
	for _, profileName := range []string{"", "devtest"} {
		paths := syncStatePaths(profileName)
		joined := strings.Join(paths, "\n")
		for _, want := range []string{
			"sync-state.json",
			"codex-sync-state.json",
			"gjc-sync-state.json",
			"omo-sync-state.json",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("profile %q: no state path for %s — its first-run skips are recorded but never shown\n%s",
					profileName, want, joined)
			}
		}
		if len(paths) != 4 {
			t.Errorf("profile %q: %d state paths, want one per agent\n%s", profileName, len(paths), joined)
		}
	}
}
