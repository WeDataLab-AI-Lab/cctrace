package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseWindow reads a time window the way people ask for one.
//
// time.ParseDuration stops at hours: "7d" is an error there, and a week is "168h".
// Every window this CLI is asked for is really measured in days, so days are
// accepted and expanded before anything else sees the value. Everything else falls
// through to the standard parser, so "24h" and "1h30m" keep working.
//
// A day here is 24 hours. That is wrong twice a year in zones that observe DST, and
// it is still the right answer: someone asking for "7d" of usage wants seven days
// of work, not a calendar computation whose result shifts by an hour depending on
// when they ran it.
func parseWindow(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty time window")
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("invalid time window %q: want a number of days like 7d", s)
		}
		if days <= 0 {
			return 0, fmt.Errorf("invalid time window %q: must be positive", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid time window %q: use 7d, 24h, or 90m", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid time window %q: must be positive", s)
	}
	return d, nil
}
