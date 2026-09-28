package main

import (
	"testing"
	"time"
)

// Go's ParseDuration stops at hours -- "7d" is an error, not seven days. Every
// window a person actually asks for here is measured in days, so the CLI accepts
// them and converts before anything else sees the value.
func TestParseWindowAcceptsDays(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"7d":    7 * 24 * time.Hour,
		"1d":    24 * time.Hour,
		"30d":   30 * 24 * time.Hour,
		"24h":   24 * time.Hour,
		"90m":   90 * time.Minute,
		"1h30m": 90 * time.Minute,
	} {
		got, err := parseWindow(in)
		if err != nil {
			t.Errorf("parseWindow(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseWindow(%q) = %v, want %v", in, got, want)
		}
	}
}

// A window that cannot be understood must be refused, not silently treated as zero
// -- a zero window returns an empty report that looks like "you used nothing".
func TestParseWindowRejectsNonsense(t *testing.T) {
	for _, in := range []string{"", "7", "d", "-3d", "7dd", "abc"} {
		if got, err := parseWindow(in); err == nil {
			t.Errorf("parseWindow(%q) = %v, want an error", in, got)
		}
	}
}
