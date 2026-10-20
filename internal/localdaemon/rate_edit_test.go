package localdaemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestActiveEditKeepsRetry(t *testing.T) {
	for _, destination := range []string{"same", "different"} {
		t.Run(destination, func(t *testing.T) {
			root := t.TempDir()
			old := Spec{ID: "one", Name: "one", RuntimeDir: filepath.Join(root, "one"), Revision: "old", Enabled: true, Destination: "same", Run: func(context.Context, func(Progress)) error { return nil }}
			fresh := old
			fresh.Revision = "new"
			fresh.Destination = destination
			s := New(Config{Root: root, Load: func() ([]Spec, error) { return []Spec{fresh}, nil }})
			floor := time.Now().Add(time.Hour)
			j := &job{spec: old, status: JobStatus{ID: "one", Name: "one", State: "retrying", ErrorCode: "rate_limited", NextRetryAt: floor, RouteSignature: routeSignature(old), RouteOwner: true}, next: floor, active: true, token: "old", cancel: func() {}}
			s.jobs["one"] = j
			s.reconcile()
			s.handleEvent(event{id: "one", token: "old", exit: true, err: context.Canceled}, false)
			if destination == "same" {
				if s.canStart(j) || !j.next.Equal(floor) || !j.status.NextRetryAt.Equal(floor) || j.status.State != "retrying" {
					t.Fatalf("same destination lost server floor: canStart=%v next=%v status=%+v", s.canStart(j), j.next, j.status)
				}
			} else if !s.canStart(j) || !j.next.IsZero() || !j.status.NextRetryAt.IsZero() {
				t.Fatalf("new destination inherited old server floor: next=%v status=%+v", j.next, j.status)
			}
		})
	}
}
