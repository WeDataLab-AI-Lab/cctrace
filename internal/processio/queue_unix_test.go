//go:build !windows

package processio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueueRefusesADirectoryOthersCanReach(t *testing.T) {
	for _, mode := range []os.FileMode{0750, 0705, 0770, 0777} {
		queue := newTestQueue(t)
		if err := os.Chmod(filepath.Dir(queue.Path), mode); err != nil {
			t.Fatal(err)
		}
		if err := queue.Upsert(queueReport("session-1", time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC))); err == nil || !strings.Contains(err.Error(), "must be private") {
			t.Errorf("mode %04o: Upsert error = %v, want the private-directory refusal", mode, err)
		}
		if _, err := queue.List(); err == nil || !strings.Contains(err.Error(), "must be private") {
			t.Errorf("mode %04o: List error = %v, want the private-directory refusal", mode, err)
		}
	}
}
