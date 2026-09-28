package store

import (
	"context"
	"testing"
	"time"
)

const metricFilterOwner = "mf" + "@" + "filter.test"

// The model filter runs as a LIKE pattern with an explicit ESCAPE clause. The
// buildMetricWhere tests only compare strings, so they would still pass if the
// server rejected the SQL -- this exercises the real round trip.
func TestListMetrics_ModelFilterRoundTrip(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	models := []string{
		"claude-sonnet-4-6",
		"claude-opus-5",
		"claude-opus-5[1m]",
		"gpt-5.4",
		"gpt-5.4-mini",
		"a_b",
		"aXb",
	}
	var metrics []*OtelMetric
	for i, m := range models {
		metrics = append(metrics, &OtelMetric{
			Ts:           now.Add(time.Duration(i) * time.Second),
			MetricName:   "token_count",
			ProfileEmail: metricFilterOwner,
			Model:        m,
		})
	}
	if err := s.InsertMetrics(ctx, metrics); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	cases := []struct {
		name  string
		model string
		want  int
	}{
		// The dashboard dropdown strips the "claude-" prefix, so the value the
		// UI holds has to reach the stored row. Exact match returned nothing here.
		{name: "prefix-stripped dropdown value", model: "sonnet-4-6", want: 1},
		// Context-window variants are stored as separate values; a contains
		// match spans both, which is what the cost charts want.
		{name: "spans the [1m] variant", model: "claude-opus-5", want: 2},
		{name: "targets the [1m] variant", model: "opus-5[1m]", want: 1},
		// Known cost of the contains match: a shorter model name also selects
		// the longer ones built on it. The dropdown lists these separately, so
		// picking "gpt-5.4" still returns "gpt-5.4-mini" rows. Documented in
		// docs/design/design-api-endpoints.md; this locks the behaviour so the
		// next person sees it is known rather than accidental.
		{name: "also selects longer names", model: "gpt-5.4", want: 2},
		{name: "longer name selects only itself", model: "gpt-5.4-mini", want: 1},
		// "_" must match itself. Without the ESCAPE clause it would be a
		// single-character wildcard and "aXb" would come back too.
		{name: "underscore is literal", model: "a_b", want: 1},
		{name: "no match", model: "gemini", want: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.ListMetrics(ctx, MetricFilter{
				ProfileEmail: metricFilterOwner, Model: c.model, Limit: 10,
			})
			if err != nil {
				t.Fatalf("ListMetrics: %v", err)
			}
			if len(got) != c.want {
				names := make([]string, len(got))
				for i, m := range got {
					names[i] = m.Model
				}
				t.Errorf("model=%q returned %d rows %v, want %d", c.model, len(got), names, c.want)
			}
		})
	}
}

// A caller-supplied limit is capped, matching the other paged reads in this
// package. The model filter cannot use an index, so an unbounded limit would
// let a single request scan every chunk.
func TestListMetrics_LimitIsCapped(t *testing.T) {
	if got := clampMetricLimit(10_000_000); got != metricLimitMax {
		t.Errorf("clampMetricLimit(10000000) = %d, want %d", got, metricLimitMax)
	}
	if got := clampMetricLimit(0); got != metricLimitDefault {
		t.Errorf("clampMetricLimit(0) = %d, want %d", got, metricLimitDefault)
	}
	if got := clampMetricLimit(-1); got != metricLimitDefault {
		t.Errorf("clampMetricLimit(-1) = %d, want %d", got, metricLimitDefault)
	}
	// A limit the UI actually asks for is passed through untouched.
	if got := clampMetricLimit(50); got != 50 {
		t.Errorf("clampMetricLimit(50) = %d, want 50", got)
	}
}
