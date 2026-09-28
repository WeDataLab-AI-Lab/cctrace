package activitylabel

import "testing"

func TestClassify_PriorityOrder(t *testing.T) {
	cases := []struct {
		name string
		s    Signals
		want string
	}{
		{"no calls at all -> unknown", Signals{}, "unknown"},
		{"fail alone -> diagnose", Signals{FailPresent: true, TotalCalls: 1}, "diagnose"},
		{"pure shell -> ops", Signals{BashCalls: 9, TotalCalls: 10}, "ops"},
		{"any write -> author", Signals{WriteCalls: 1, TotalCalls: 1}, "author"},
		{"read only -> explore", Signals{ReadCalls: 1, TotalCalls: 1}, "explore"},
		{"write and bash mixed, bash under threshold -> author (not ops)", Signals{WriteCalls: 1, BashCalls: 3, TotalCalls: 4}, "author"},

		// Priority: fail beats a shape that would otherwise read as ops.
		{"fail present despite near-pure shell -> diagnose, not ops", Signals{FailPresent: true, BashCalls: 9, TotalCalls: 10}, "diagnose"},
		// Priority: fail beats a shape that would otherwise read as author.
		{"fail present despite writes -> diagnose, not author", Signals{FailPresent: true, WriteCalls: 3, TotalCalls: 3}, "diagnose"},
		// Priority: any write beats read-only (explore never fires once write_calls>0).
		{"write and read together -> author, not explore", Signals{WriteCalls: 1, ReadCalls: 5, TotalCalls: 6}, "author"},
		// Exactly at the ops threshold does not qualify -- the rule is strictly >0.8.
		{"bash ratio exactly 0.8 -> not ops (falls to unknown, no write/read)", Signals{BashCalls: 8, TotalCalls: 10}, "unknown"},
		{"bash ratio just over 0.8 with no write -> ops", Signals{BashCalls: 9, TotalCalls: 11}, "ops"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.s); got != c.want {
				t.Errorf("Classify(%+v) = %q, want %q", c.s, got, c.want)
			}
		})
	}
}

// Pins the actual empirical basis Classify's doc comment cites: each of Stage
// 3's four real clusters must map to the label the plan assigns it. Clusters 0
// through 2 convert their centroid ratios to a TotalCalls=100 fixture directly.
// Cluster 3's centroid write ratio (0.02) is a cluster AVERAGE, not a claim that
// every member has exactly 2 writes per 100 calls -- the "ops" rule requires
// write_calls == 0 exactly, by design (rule 4, "any write means author", sits
// right after it), so a typical member of a cluster whose average write ratio
// is merely small is one with zero writes, not two. The fixture below models
// that typical member, not the literal mean.
func TestClassify_MatchesClusterCentroids(t *testing.T) {
	cases := []struct {
		cluster string
		s       Signals
		want    string
	}{
		{"cluster 0 (43.3%, fail=100%)", Signals{WriteCalls: 16, ReadCalls: 15, BashCalls: 69, TotalCalls: 100, FailPresent: true}, "diagnose"},
		{"cluster 1 (10.7%)", Signals{WriteCalls: 34, ReadCalls: 37, BashCalls: 21, TotalCalls: 100}, "author"},
		{"cluster 2 (19.2%, bash=0.61, below ops threshold)", Signals{WriteCalls: 20, ReadCalls: 19, BashCalls: 61, TotalCalls: 100}, "author"},
		{"cluster 3 (26.8%, bash~0.95, near-pure shell, typical zero-write member)", Signals{WriteCalls: 0, ReadCalls: 5, BashCalls: 95, TotalCalls: 100}, "ops"},
	}
	for _, c := range cases {
		t.Run(c.cluster, func(t *testing.T) {
			if got := Classify(c.s); got != c.want {
				t.Errorf("%s: Classify(%+v) = %q, want %q", c.cluster, c.s, got, c.want)
			}
		})
	}
}

// Regression guard in the spirit of the plan's length-invariance test: the old
// prompt-text classifier's coverage swung 33x across prompt-length buckets
// because its signal (keyword presence in text) degraded with length. This
// classifier's signal is call-count RATIOS, not raw counts or text length, so
// scaling every count by the same factor (more tool calls in the segment, same
// mix) must not change the label -- coverage must stay flat across segment
// size the same way it is required to stay flat across prompt length.
func TestClassify_ScaleInvariantAcrossSegmentSize(t *testing.T) {
	shapes := []struct {
		name              string
		write, read, bash int
		fail              bool
	}{
		{"diagnose shape", 1, 1, 5, true},
		{"ops shape", 0, 0, 9, false},
		{"author shape", 3, 1, 1, false},
		{"explore shape", 0, 4, 0, false},
	}
	scales := []int{1, 5, 20, 100}
	for _, shape := range shapes {
		var want string
		base := scales[0]
		want = Classify(Signals{
			WriteCalls: shape.write * base, ReadCalls: shape.read * base, BashCalls: shape.bash * base,
			TotalCalls: (shape.write + shape.read + shape.bash) * base, FailPresent: shape.fail,
		})
		for _, scale := range scales {
			got := Classify(Signals{
				WriteCalls: shape.write * scale, ReadCalls: shape.read * scale, BashCalls: shape.bash * scale,
				TotalCalls: (shape.write + shape.read + shape.bash) * scale, FailPresent: shape.fail,
			})
			if got != want {
				t.Errorf("%s at scale %d: Classify(...) = %q, want %q (same as scale %d) -- label must not depend on segment size", shape.name, scale, got, want, base)
			}
		}
	}
}

// Coverage must not degenerate into mostly "unknown" -- the failure mode the
// plan's evaluation section calls out for the old classifier (22.8% coverage).
// Sweeping a broad, reasonable set of call-mix shapes, non-diagnose/ops/author/
// explore ("unknown") must stay a minority outcome, not the norm.
func TestClassify_CoverageIsNotDegenerate(t *testing.T) {
	total, unknown := 0, 0
	for write := 0; write <= 5; write++ {
		for read := 0; read <= 5; read++ {
			for bash := 0; bash <= 5; bash++ {
				if write == 0 && read == 0 && bash == 0 {
					continue // an empty segment is legitimately unknown; excluded from the sweep
				}
				total++
				if Classify(Signals{WriteCalls: write, ReadCalls: read, BashCalls: bash, TotalCalls: write + read + bash}) == "unknown" {
					unknown++
				}
			}
		}
	}
	if rate := float64(unknown) / float64(total); rate > 0.05 {
		t.Fatalf("unknown rate = %.1f%% (%d/%d) across the sweep, want <=5%% -- coverage should not degenerate the way the old text classifier's did", rate*100, unknown, total)
	}
}
