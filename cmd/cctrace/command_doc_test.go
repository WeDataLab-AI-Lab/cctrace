package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The installation guide lists the commands beyond init/status/sync/reset, and
// readers use that table instead of `--help`. It has drifted three times now
// (#261, #268, #534): each round added a command and left the table behind, and
// each round was caught by a person reading both. #534 is the fourth of the same
// shape, which is why this is a test rather than another correction.
//
// The table said "four more" while the binary registered seventeen, and
// `cctrace config` was already used three times elsewhere in the same document.
//
// Both directions are compared. A row for a command that no longer exists is the
// worse failure -- a document naming something that is not there stops the reader
// from looking further -- but a missing row is what recurred.
const commandDoc = "../../docs/guides/guide-installation-and-usage.md"

// The four covered in their own sections rather than the table.
var commandsDocumentedElsewhere = map[string]bool{
	"init": true, "status": true, "sync": true, "reset": true,
}

// Cobra adds these; they are not cctrace's surface.
var cobraBuiltinCommands = map[string]bool{
	"help": true, "completion": true,
}

var commandRowRe = regexp.MustCompile("(?m)^\\| `cctrace ([a-z0-9-]+)")

func documentedCommands(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(commandDoc)
	if err != nil {
		t.Fatalf("reading %s: %v", commandDoc, err)
	}
	_, after, found := strings.Cut(string(raw), "### 3-10. The other commands")
	if !found {
		t.Fatalf("%s: the command table section is gone -- if it was renamed, rename it here too", commandDoc)
	}
	table, _, _ := strings.Cut(after, "\n### ")
	names := map[string]bool{}
	for _, match := range commandRowRe.FindAllStringSubmatch(table, -1) {
		names[match[1]] = true
	}
	if len(names) == 0 {
		t.Fatalf("%s: no command rows parsed -- the table shape changed", commandDoc)
	}
	return names
}

func registeredCommands(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, cmd := range newRootCmd().Commands() {
		name := cmd.Name()
		if cobraBuiltinCommands[name] || commandsDocumentedElsewhere[name] {
			continue
		}
		names[name] = true
	}
	return names
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func TestCommandTableMatchesRegisteredCommands(t *testing.T) {
	documented := documentedCommands(t)
	registered := registeredCommands(t)

	var missing, stale []string
	for _, name := range sortedKeys(registered) {
		if !documented[name] {
			missing = append(missing, name)
		}
	}
	for _, name := range sortedKeys(documented) {
		if !registered[name] {
			stale = append(stale, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%s: registered but undocumented: %s", commandDoc, strings.Join(missing, " "))
	}
	if len(stale) > 0 {
		t.Errorf("%s: documented but not registered: %s", commandDoc, strings.Join(stale, " "))
	}
}

// The sentence above the table states the count. A number in prose drifts even
// more quietly than a row, because nothing else in the document contradicts it.
func TestCommandTableCountSentenceMatchesTable(t *testing.T) {
	raw, err := os.ReadFile(commandDoc)
	if err != nil {
		t.Fatalf("reading %s: %v", commandDoc, err)
	}
	_, after, found := strings.Cut(string(raw), "### 3-10. The other commands")
	if !found {
		t.Fatalf("%s: the command table section is gone", commandDoc)
	}
	sentence, _, _ := strings.Cut(after, "\n|")
	stated := regexp.MustCompile(`there are ([a-z]+|\d+) more`).FindStringSubmatch(sentence)
	if stated == nil {
		t.Fatalf("%s: no \"there are N more\" sentence above the table:\n%s", commandDoc, strings.TrimSpace(sentence))
	}
	want := len(documentedCommands(t))
	spelled := map[int]string{
		4: "four", 5: "five", 6: "six", 7: "seven", 8: "eight", 9: "nine",
		10: "ten", 11: "eleven", 12: "twelve", 13: "thirteen", 14: "fourteen",
		15: "fifteen", 16: "sixteen", 17: "seventeen", 18: "eighteen", 19: "nineteen", 20: "twenty",
	}
	if stated[1] != spelled[want] && stated[1] != itoa(want) {
		t.Errorf("%s: sentence says %q but the table has %d rows", commandDoc, stated[1], want)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
