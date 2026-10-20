package store

import "testing"

func TestRequestsCompleteDistinguishesFullIndexFromCappedPage(t *testing.T) {
	intp := func(n int) *int { return &n }
	for _, tc := range []struct {
		name     string
		total    *int
		returned int
		want     bool
	}{
		{name: "short", total: intp(3), returned: 3, want: true},
		{name: "capped", total: intp(2500), returned: 2001, want: false},
		{name: "exactly capped and whole", total: intp(2001), returned: 2001, want: true},
		{name: "unknown", total: nil, returned: 10, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestsComplete(tc.total, tc.returned, 2001); got != tc.want {
				t.Fatalf("requestsComplete()=%v want %v", got, tc.want)
			}
		})
	}
}
