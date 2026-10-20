package main

import "testing"

func TestSessionAnalysisFlagDefaultsOff(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false}, {"off", false}, {"true", false}, {"ON", false}, {"on", true},
	} {
		t.Setenv("CCTRACE_SESSION_ANALYSIS", tc.value)
		if got := sessionAnalysisEnabled(); got != tc.want {
			t.Errorf("flag %q enabled = %t, want %t", tc.value, got, tc.want)
		}
	}
}
