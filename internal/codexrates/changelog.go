package codexrates

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// changelogURL is overridable for tests, like the two pricing addresses.
var changelogURL = "https://developers.openai.com/api/docs/changelog.md"

// changeWindow bounds how far back a change may be dated. A price we noticed
// today did not take effect four months ago -- if the only matching entry is
// that old it is about something else, and dating the rate from it would
// reprice months of history that were billed correctly.
const changeWindow = 120 * 24 * time.Hour

// ChangeEntry is one dated changelog item: the models its meta line tagged, and
// whether its prose is about money at all.
//
// Note what is absent: any price. The changelog is read for DATES ONLY. That is
// the whole point of the split -- if this parser breaks, an effective_from is
// off by a few days and nothing else; the rates themselves come from the
// published table and are gated by Accept. Start scraping "$4" out of a
// sentence and a prose change becomes a billing error.
type ChangeEntry struct {
	Date   time.Time
	Models []string
	// Priceish is a ranking preference, not a filter. Entries are hand-written
	// prose, so "this one is about pricing" is a guess; it breaks ties between
	// entries tagging the same model rather than deciding which ones count.
	Priceish bool
}

var (
	monthHeadingRe = regexp.MustCompile(`^##\s+([A-Za-z]+),\s*(\d{4})\s*$`)
	dayHeadingRe   = regexp.MustCompile(`^###\s+([A-Za-z]{3})\s+(\d{1,2})\s*$`)
	modelTagRe     = regexp.MustCompile(`Model:\s*([A-Za-z0-9][A-Za-z0-9._-]*)`)
)

// priceWords are what "this entry is about money" looks like in practice. They
// are deliberately loose; see Priceish.
var priceWords = []string{"cost", "pricing", "price", "% less", "% lower", "$"}

// ParseChangelog reads OpenAI's changelog markdown into dated entries.
//
// The structure it relies on:
//
//	## August, 2026        <- the year lives here, so none is ever inferred
//	### Aug 21
//	Update · Model: gpt-5.6-sol · API: v1/responses
//	GPT-5.6 Sol now costs $4 per million input tokens...
//
// The line right under the day heading is the meta line and the only place
// Model: tags appear; API: tags on the same line are not models.
func ParseChangelog(md []byte) ([]ChangeEntry, error) {
	var entries []ChangeEntry
	var year int
	var cur *ChangeEntry
	var body strings.Builder
	metaSeen := false

	flush := func() {
		if cur == nil {
			return
		}
		cur.Priceish = isPriceish(body.String())
		entries = append(entries, *cur)
		cur = nil
		body.Reset()
		metaSeen = false
	}

	for _, line := range strings.Split(string(md), "\n") {
		if m := monthHeadingRe.FindStringSubmatch(line); m != nil {
			flush()
			if t, err := time.Parse("January 2006", m[1]+" "+m[2]); err == nil {
				year = t.Year()
			}
			continue
		}
		if m := dayHeadingRe.FindStringSubmatch(line); m != nil {
			flush()
			if year == 0 {
				continue
			}
			t, err := time.Parse("Jan 2 2006", m[1]+" "+m[2]+" "+strconv.Itoa(year))
			if err != nil {
				continue
			}
			cur = &ChangeEntry{Date: t}
			continue
		}
		if cur == nil {
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
		if !metaSeen && strings.TrimSpace(line) != "" {
			metaSeen = true
			cur.Models = modelsIn(line)
		}
	}
	flush()

	if len(entries) == 0 {
		return nil, errors.New("no dated changelog entries")
	}
	return entries, nil
}

func modelsIn(metaLine string) []string {
	var models []string
	seen := map[string]bool{}
	for _, m := range modelTagRe.FindAllStringSubmatch(metaLine, -1) {
		name := strings.ToLower(m[1])
		if seen[name] {
			continue
		}
		seen[name] = true
		models = append(models, name)
	}
	return models
}

func isPriceish(body string) bool {
	lower := strings.ToLower(body)
	for _, w := range priceWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// EffectiveDate answers "when did this model's price most plausibly change?".
//
// It looks for the most recent entry tagging the model that is neither after
// the observation nor older than changeWindow, preferring an entry whose prose
// mentions money. The second result is false when nothing matched, in which
// case the observation date is returned: a rate we cannot date is dated from
// the day we first saw it, which is the safest wrong answer available -- it
// leaves history priced as it was and only affects rows from today on.
func EffectiveDate(entries []ChangeEntry, model string, observed time.Time) (time.Time, bool) {
	model = strings.ToLower(model)
	earliest := observed.Add(-changeWindow)
	var best time.Time
	bestPriceish := false
	found := false
	for _, e := range entries {
		if e.Date.After(observed) || e.Date.Before(earliest) {
			continue
		}
		if !tagged(e.Models, model) {
			continue
		}
		// A price-ish entry beats a non-price-ish one whatever their dates; among
		// equals, the later one wins.
		better := !found ||
			(e.Priceish && !bestPriceish) ||
			(e.Priceish == bestPriceish && e.Date.After(best))
		if better {
			best, bestPriceish, found = e.Date, e.Priceish, true
		}
	}
	if !found {
		return observed, false
	}
	return best, true
}

func tagged(models []string, model string) bool {
	for _, m := range models {
		if m == model {
			return true
		}
	}
	return false
}

// FetchChangelog reads the published changelog. Callers treat a failure as
// non-fatal: without dates the rate sync still runs, dating changes from today.
func FetchChangelog(ctx context.Context) ([]ChangeEntry, error) {
	body, err := get(ctx, changelogURL)
	if err != nil {
		return nil, err
	}
	return ParseChangelog([]byte(body))
}
