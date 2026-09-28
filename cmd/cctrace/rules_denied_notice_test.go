package main

import (
	"strings"
	"testing"
	"time"
)

// A repository the server refuses rule ingest for is otherwise invisible: the
// refusal is durable, the client parks it and says so once in sync.log, and that
// line rotates away. What is left is a repository whose CLAUDE.md and AGENTS.md
// are never collected with nothing saying so -- production ran that at 23k-41k
// refused posts a day for at least ten days (#619).
func TestRulesDeniedNotice(t *testing.T) {
	if got := rulesDeniedNotice(nil); got != "" {
		t.Errorf("nothing denied produced %q, want silence", got)
	}
	if got := rulesDeniedNotice(map[string]time.Time{}); got != "" {
		t.Errorf("empty map produced %q, want silence", got)
	}

	one := rulesDeniedNotice(map[string]time.Time{"github.com/org/repo": time.Now()})
	if !strings.Contains(one, "github.com/org/repo") {
		t.Errorf("the repository is not named, so the reader cannot act:\n%s", one)
	}
	// The server's own word for it is "access denied", which says nothing about
	// rules going uncollected. The notice has to say the consequence.
	if !strings.Contains(one, "not collected") {
		t.Errorf("the notice does not say what the reader loses:\n%s", one)
	}
}

// Named, not counted -- but bounded, so a machine with many refused repositories
// does not print a paragraph.
func TestRulesDeniedNoticeListsAtMostThree(t *testing.T) {
	denied := map[string]time.Time{}
	for _, r := range []string{"a/1", "b/2", "c/3", "d/4", "e/5"} {
		denied[r] = time.Now()
	}
	got := rulesDeniedNotice(denied)

	for _, want := range []string{"a/1", "b/2", "c/3"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s missing from:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"d/4", "e/5"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("%s listed past the cap:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "and 2 more") {
		t.Errorf("the count of the rest is missing, so the reader underestimates the problem:\n%s", got)
	}
}
