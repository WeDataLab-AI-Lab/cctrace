package sessionfacts

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func TestTimelineDeepSingleTurnUsesLinearAncestryWork(t *testing.T) {
	t.Parallel()
	const n = 1024
	ctx := context.Background()
	b := NewIndexBuilder(Input{Agent: "claude"}, nil)
	for i := 0; i < n; i++ {
		row := ClaudeRow{ID: int64(i + 1), TS: time.Unix(1_000_000+int64(i), 0), UUID: fmt.Sprint(i), RecordType: "assistant", ContentKind: "array"}
		if i == 0 {
			row.RecordType, row.PromptSource, row.TextHead = "user", "typed", "one long turn"
			row.ContentKind = "string"
		} else {
			row.ParentUUID = fmt.Sprint(i - 1)
		}
		if err := b.AddClaude(ctx, int64(i), row); err != nil {
			t.Fatal(err)
		}
	}
	a, err := b.Seal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// Completed rule operations are deterministic; this catches quadratic parent
	// walking without a wall-clock threshold or an intentionally slow fixture.
	if a.work > 100*n {
		t.Fatalf("single-turn ancestry work=%d for %d rows, want <=%d", a.work, n, 100*n)
	}
	if len(a.c.turns) != 1 || len(a.timeline.Rows) != n {
		t.Fatalf("turns=%d timeline=%d", len(a.c.turns), len(a.timeline.Rows))
	}
	for _, row := range a.timeline.Rows {
		if row.TurnKey != a.c.turns[0].out.TurnKey || row.Unmapped != "" {
			t.Fatalf("deep row lost exact lineage: %+v", row)
		}
	}
}

func TestNearestLineageAncestorMatchesParentWalk(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(934))
	var nodes []*line
	counts := map[string]int{}
	for i := 0; i < 256; i++ {
		n := &line{ClaudeRow: &ClaudeRow{UUID: fmt.Sprint(i)}, direct: rng.Intn(8) == 0}
		if i > 0 && rng.Intn(5) != 0 {
			n.parent = nodes[rng.Intn(i)]
		}
		counts[n.UUID] = 1 + rng.Intn(2)
		nodes = append(nodes, n)
	}
	cache := map[*line]*line{}
	// Reverse order exercises ancestors not yet visited in the source order.
	for i := len(nodes) - 1; i >= 0; i-- {
		want := nodes[i]
		for want != nil && counts[want.UUID] <= 1 && !want.direct {
			want = want.parent
		}
		if got := nearestLineageAncestor(nodes[i], counts, cache, func() {}); got != want {
			t.Fatalf("node %d: nearest barrier=%p want=%p", i, got, want)
		}
	}
}
