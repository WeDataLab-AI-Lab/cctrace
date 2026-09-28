package store

import (
	"os"
	"strings"
	"testing"
)

// The exclusion views are rebuilt on every migration pass, so a change to their
// text reaches every database on the next start. This pins the text byte for
// byte: assembling the predicate from a shared helper must not change it.
func TestVisibleViewDDLIsPinned(t *testing.T) {
	for _, name := range []string{"visible_events", "visible_session_records"} {
		want, err := os.ReadFile("testdata/" + name + ".sql")
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		var got string
		for _, q := range dependentViews {
			if strings.HasPrefix(q, "CREATE OR REPLACE VIEW "+name+" AS") {
				got = q + "\n"
			}
		}
		if got != string(want) {
			t.Errorf("%s DDL changed.\n got: %q\nwant: %q", name, got, want)
		}
	}
}
