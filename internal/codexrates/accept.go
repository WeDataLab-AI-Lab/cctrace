package codexrates

import (
	"fmt"
	"math"
	"sort"
)

// minRows is the smallest table worth believing. The published standard table
// has carried 35-40 models for years; a handful of rows means the parser found
// a fragment of the page, not the table.
const minRows = 8

// anchors are retired models whose published price is fixed, and they are the
// gate's proof that the parser read the right columns of the right table.
//
// The failure mode being defended against is a scalar multiple, not a shuffle:
// the long-context columns are exactly 2x the short-context ones and the batch
// section is exactly 0.5x, so a parser that grabs the wrong block returns a
// perfectly ordered, perfectly plausible table at the wrong price. Only
// absolute expected values catch that.
//
// They are retired models on purpose. Anchoring on current models locks the
// sync closed the moment OpenAI reprices one -- which is exactly what happened
// to the gpt-5.6 family twice in two months. Nothing competes with gpt-3.5-turbo
// any more, so its price does not move and the anchors need no maintenance.
//
// If one of these does change, the sync fails closed: nothing is updated and
// the mismatch is logged until a human edits this map. A stale rate table costs
// less than a corrupted one.
var anchors = map[string]Rate{
	"gpt-3.5-turbo": {Input: 0.50, Output: 1.50, CacheRead: 0},
	"gpt-4-0613":    {Input: 30.0, Output: 60.0, CacheRead: 0},
	"babbage-002":   {Input: 0.40, Output: 0.40, CacheRead: 0},
}

// Accept reports whether a freshly parsed rate table may replace what we have.
// Returning an error keeps the existing rows untouched.
func Accept(fetched map[string]Rate) error {
	if len(fetched) < minRows {
		return fmt.Errorf("only %d rows, want at least %d", len(fetched), minRows)
	}
	names := make([]string, 0, len(anchors))
	for name := range anchors {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		want := anchors[name]
		got, ok := fetched[name]
		if !ok {
			return fmt.Errorf("anchor %s missing", name)
		}
		if !sameRate(got, want) {
			return fmt.Errorf("anchor %s = %v/%v/%v (in/out/cache), want %v/%v/%v",
				name, got.Input, got.Output, got.CacheRead, want.Input, want.Output, want.CacheRead)
		}
	}
	return nil
}

func sameRate(a, b Rate) bool {
	return math.Abs(a.Input-b.Input) < 1e-9 &&
		math.Abs(a.Output-b.Output) < 1e-9 &&
		math.Abs(a.CacheRead-b.CacheRead) < 1e-9
}
