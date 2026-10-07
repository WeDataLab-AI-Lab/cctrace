package codexrates

import (
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func findEntry(t *testing.T, entries []ChangeEntry, date time.Time, model string) ChangeEntry {
	t.Helper()
	for _, e := range entries {
		if !e.Date.Equal(date) {
			continue
		}
		for _, m := range e.Models {
			if m == model {
				return e
			}
		}
	}
	t.Fatalf("no entry on %s tagging %s", date.Format("2006-01-02"), model)
	return ChangeEntry{}
}

func TestParseChangelog(t *testing.T) {
	entries, err := ParseChangelog([]byte(readFixture(t, "changelog.md")))
	if err != nil {
		t.Fatalf("ParseChangelog: %v", err)
	}
	if len(entries) < 15 {
		t.Fatalf("parsed only %d entries", len(entries))
	}

	// The year comes from the "## August, 2026" heading above the day, so no
	// year has to be guessed from where the entry sits in the file.
	sol := findEntry(t, entries, day(2026, time.August, 21), "gpt-5.6-sol")
	if !sol.Priceish {
		t.Errorf("the Aug 21 sol price cut was not marked price-ish")
	}
	if len(sol.Models) != 1 {
		t.Errorf("Aug 21 sol entry tagged %v, want just gpt-5.6-sol", sol.Models)
	}

	// One entry, several Model: tags, and API: tags that are not models.
	jul := findEntry(t, entries, day(2026, time.July, 30), "gpt-5.6-luna")
	if !jul.Priceish {
		t.Errorf("the Jul 30 price cut was not marked price-ish")
	}
	want := map[string]bool{"gpt-5.6-sol": true, "gpt-5.6-terra": true, "gpt-5.6-luna": true}
	if len(jul.Models) != 3 {
		t.Fatalf("Jul 30 models = %v, want the three gpt-5.6 models", jul.Models)
	}
	for _, m := range jul.Models {
		if !want[m] {
			t.Errorf("Jul 30 models = %v, want only the three gpt-5.6 models", jul.Models)
		}
	}

	// An entry whose meta line carries no Model: tag is still an entry, and a
	// release note that never mentions money is not price-ish.
	for _, e := range entries {
		if e.Date.Equal(day(2026, time.August, 29)) {
			if len(e.Models) != 0 {
				t.Errorf("Aug 29 (mTLS) tagged models %v, want none", e.Models)
			}
			if e.Priceish {
				t.Errorf("Aug 29 (mTLS) was marked price-ish")
			}
		}
	}
}

func TestEffectiveDate_TC21GPT61PublishedRelease(t *testing.T) {
	entries, err := ParseChangelog([]byte("## September, 2026\n\n### Sep 29\n\nFeature · Model: gpt-6.1-sol · API: v1/responses\n\nReleased GPT-6.1 Sol.\n"))
	if err != nil {
		t.Fatal(err)
	}
	date, known := EffectiveDate(entries, "gpt-6.1-sol", day(2026, time.September, 30))
	if !known || !date.Equal(day(2026, time.September, 29)) {
		t.Fatalf("release = %v (%v), want 2026-09-29 true", date, known)
	}
}

func TestEffectiveDate(t *testing.T) {
	entries, err := ParseChangelog([]byte(readFixture(t, "changelog.md")))
	if err != nil {
		t.Fatalf("ParseChangelog: %v", err)
	}
	observed := day(2026, time.September, 4)

	got, ok := EffectiveDate(entries, "gpt-5.6-sol", observed)
	if !ok || !got.Equal(day(2026, time.August, 21)) {
		t.Errorf("sol = %s (%v), want 2026-08-21 true", got.Format("2006-01-02"), ok)
	}

	// A model the changelog never mentions falls back to the observation date,
	// and says so -- the caller logs that rather than inventing a date.
	got, ok = EffectiveDate(entries, "babbage-002", observed)
	if ok || !got.Equal(observed) {
		t.Errorf("babbage-002 = %s (%v), want the observation date and false", got.Format("2006-01-02"), ok)
	}

	// Priceish is a preference, not a filter: it picks between same-model entries,
	// it does not reject them. gpt-5.6-luna is the honest illustration -- its real
	// price cut was Jul 30, but the Aug 5 Fast-mode note also tags luna and ends
	// with "See pricing details", so it wins on recency. Six days of drift on
	// effective_from is the accepted cost of never reading a price out of prose;
	// the two verified cut-over dates are pinned by the migration backfill, not
	// by this function.
	got, ok = EffectiveDate(entries, "gpt-5.6-luna", observed)
	if !ok || !got.Equal(day(2026, time.August, 5)) {
		t.Errorf("luna = %s (%v), want 2026-08-05 true", got.Format("2006-01-02"), ok)
	}
}

func TestEffectiveDate_IgnoresEntriesOutsideTheWindow(t *testing.T) {
	entries, err := ParseChangelog([]byte(readFixture(t, "changelog.md")))
	if err != nil {
		t.Fatalf("ParseChangelog: %v", err)
	}
	// Observed far enough ahead that every fixture entry is more than 120 days
	// old. A price we only noticed today cannot have taken effect last spring.
	observed := day(2027, time.March, 1)
	got, ok := EffectiveDate(entries, "gpt-5.6-sol", observed)
	if ok || !got.Equal(observed) {
		t.Errorf("sol = %s (%v), want the observation date and false", got.Format("2006-01-02"), ok)
	}

	// Nor can a rate observed on Aug 1 have taken effect on Aug 21: entries after
	// the observation are invisible, so the Jul 30 cut is the answer.
	got, ok = EffectiveDate(entries, "gpt-5.6-sol", day(2026, time.August, 1))
	if !ok || !got.Equal(day(2026, time.July, 30)) {
		t.Errorf("sol = %s (%v), want 2026-07-30 true", got.Format("2006-01-02"), ok)
	}
}

// No changelog at all is not an error the rate sync may die on: it just means
// every changed rate takes effect from the day we noticed it.
func TestEffectiveDate_NoEntries(t *testing.T) {
	observed := day(2026, time.September, 4)
	got, ok := EffectiveDate(nil, "gpt-5.6-sol", observed)
	if ok || !got.Equal(observed) {
		t.Errorf("= %s (%v), want the observation date and false", got.Format("2006-01-02"), ok)
	}
}

func TestParseChangelog_NoEntries(t *testing.T) {
	if _, err := ParseChangelog([]byte("# Changelog\n\nNothing here.\n")); err == nil {
		t.Fatal("ParseChangelog accepted a body with no dated entries")
	}
}
