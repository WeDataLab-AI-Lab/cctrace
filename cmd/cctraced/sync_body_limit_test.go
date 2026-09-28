package main

import (
	"testing"

	"cctrace/internal/api"
)

// The daemon only reads the value; api.Server decides whether a limit is usable,
// so that "what does an unusable limit mean" has one answer for every caller
// (#588). What this layer owns is the parse: base 10, trimmed, and 0 -- meaning
// unset -- whenever there is nothing to hand over.
func TestEnvSyncBodyLimit(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        int64
	}{
		{"unset", "", 0},
		{"blank", "   ", 0},
		{"plain bytes", "67108864", 67108864},
		{"surrounding space", "  67108864  ", 67108864},
		{"not a number", "64MiB", 0},
		{"hex is not accepted", "0x40", 0},
		{"digit separators are not accepted", "1_000", 0},
		{"overflow", "99999999999999999999", 0},
		// Parsed but unusable values travel on: the warning is emitted here and
		// the fallback happens in one place, api.Server.
		{"zero", "0", 0},
		{"negative", "-1", -1},
		{"at the configurable ceiling", "268435456", api.MaxConfigurableSyncBodyBytes},
		{"above the configurable ceiling", "268435457", api.MaxConfigurableSyncBodyBytes + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CCTRACE_MAX_SYNC_BODY_BYTES", tc.value)
			if got := envSyncBodyLimit(); got != tc.want {
				t.Fatalf("value=%q got=%d want=%d", tc.value, got, tc.want)
			}
		})
	}
}
