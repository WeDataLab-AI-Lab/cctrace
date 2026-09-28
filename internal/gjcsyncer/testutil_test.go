package gjcsyncer

import (
	"io"
	"log"
	"time"
)

// redirectLog captures package log output into w for the duration of a test
// and returns a func to restore the previous output.
func redirectLog(w io.Writer) func() {
	prev := log.Writer()
	log.SetOutput(w)
	return func() { log.SetOutput(prev) }
}

func parseRFC3339(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}
