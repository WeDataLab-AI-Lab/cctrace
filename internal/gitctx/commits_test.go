package gitctx

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestCommitLogRealWorktreesAndAmend(t *testing.T) {
	root, worktree := issue382GitFixture(t)
	gitTestCommand(t, worktree, "commit", "--allow-empty", "-m", "private worktree subject")
	before, err := ReadCommitLog(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Items) != 1 || before.Items[0].Subject != "private worktree subject" {
		t.Fatalf("worktree creation entries: %+v", before)
	}
	original := before.Items[0].Hash
	gitTestCommand(t, worktree, "commit", "--amend", "--allow-empty", "-m", "amended subject")
	after, err := ReadCommitLog(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 2 || after.Items[0].Kind != "amend" || after.Items[0].Replaces != original {
		t.Fatalf("amend: %+v", after)
	}
	main, err := ReadCommitLog(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(main.Items) != 1 || main.Items[0].Hash == original {
		t.Fatalf("other worktree leaked: %+v", main)
	}
}

func TestCommitLogCreationKindsAndBounds(t *testing.T) {
	hash := strings.Repeat("a", 40)
	parent := strings.Repeat("b", 40)
	for _, tc := range []struct {
		action  string
		parents int
		kind    string
	}{
		{"commit (initial)", 0, "commit"}, {"commit", 1, "commit"}, {"commit (amend)", 1, "amend"},
		{"cherry-pick", 1, "cherry_pick"}, {"revert", 1, "revert"}, {"merge feature", 2, "merge"}, {"pull origin main", 2, "merge"},
		{"merge feature", 1, ""}, {"rebase (pick)", 1, ""}, {"am", 1, ""}, {"checkout", 1, ""}, {"reset", 1, ""}, {"clone", 0, ""},
	} {
		t.Run(tc.action, func(t *testing.T) {
			row := fmt.Sprintf("%s\x00HEAD@{100}\x00%s: synthetic\x00100\x00%s\x00subject  \x1e\n", hash, tc.action, strings.TrimSpace(strings.Repeat(parent+" ", tc.parents)))
			got := parseCommitLog([]byte(row))
			if got.Truncated {
				t.Fatal("valid row truncated")
			}
			if tc.kind == "" {
				if len(got.Items) != 0 {
					t.Fatal("noncreation accepted")
				}
				return
			}
			if len(got.Items) != 1 || got.Items[0].Kind != tc.kind || got.Items[0].Subject != "subject  " {
				t.Fatalf("parse %+v", got)
			}
		})
	}
	if !parseCommitLog([]byte("broken\x1e")).Truncated {
		t.Fatal("malformed input not marked")
	}
	row := fmt.Sprintf("%s\x00HEAD@{100}\x00commit: x\x0090\x00\x00x\x1e", hash)
	if len(parseCommitLog([]byte(row)).Items) != 0 {
		t.Fatal("old object treated as newly created")
	}
	var output cappedGitOutput
	if _, err := output.Write(make([]byte, maxReflogBytes+1)); err == nil || !output.exceeded {
		t.Fatal("output limit missing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ReadCommitLog(ctx, t.TempDir())
	if !NoAnswer(err) {
		t.Fatalf("cancel treated as answer: %v", err)
	}
}
