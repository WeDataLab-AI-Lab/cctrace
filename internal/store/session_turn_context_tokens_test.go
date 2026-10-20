package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTurnContextCursorAuthenticityAndScope(t *testing.T) {
	t.Parallel()
	s := &PgStore{}
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 5}
	v := turnContextVersion{Revision: 2, Epoch: 3, Mapping: "mapping", State: "observed", Coverage: "complete", Agent: "claude"}
	revision, err := s.turnContextRevision("session", a, v)
	if err != nil {
		t.Fatal(err)
	}
	c := turnContextCursor{Revision: revision, Turn: "first", Key: SessionTimelineKey{Timestamp: time.Now().UTC(), UUID: "source", RecordID: 10, RecordType: "user", ProfileEmail: "profile"}}
	raw, err := s.encodeTurnContextCursor(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.decodeTurnContextCursor(raw, revision, "first"); err != nil {
		t.Fatal(err)
	}
	// Knowing a real source tuple is insufficient to choose an arbitrary page.
	parts := strings.Split(raw, ".")
	c.Key.RecordID++
	body, _ := json.Marshal(c)
	forged := base64.RawURLEncoding.EncodeToString(body) + "." + parts[1]
	if _, err := s.decodeTurnContextCursor(forged, revision, "first"); !errors.Is(err, ErrTurnContextInvalid) {
		t.Fatalf("forged key=%v", err)
	}
	if _, err := s.decodeTurnContextCursor(raw, revision, "second"); !errors.Is(err, ErrTurnContextRevision) {
		t.Fatalf("other turn=%v", err)
	}
	for _, raw := range []string{"bad", strings.Repeat("a", 4097), parts[0] + ".AA"} {
		if _, err := s.decodeTurnContextCursor(raw, revision, "first"); !errors.Is(err, ErrTurnContextInvalid) {
			t.Fatalf("invalid cursor=%v", err)
		}
	}
	for _, mutate := range []func(*turnContextVersion){func(v *turnContextVersion) { v.Revision++ }, func(v *turnContextVersion) { v.Epoch++ }, func(v *turnContextVersion) { v.Mapping += "changed" }} {
		next := v
		mutate(&next)
		r, err := s.turnContextRevision("session", a, next)
		if err != nil || r == revision {
			t.Fatalf("source/policy/mapping fence=%q %v", r, err)
		}
	}
	for _, test := range []struct {
		id     string
		access SessionAnalysisAccess
	}{{"other", a}, {"session", SessionAnalysisAccess{User: true, UserID: "other", DashboardUserID: 5}}, {"session", SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 6}}, {"session", SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 5, LoginEmail: "account"}}} {
		r, err := s.turnContextRevision(test.id, test.access, v)
		if err != nil || r == revision {
			t.Fatalf("session/caller/scope fence=%q %v", r, err)
		}
	}
}

func TestTurnContextTokenKeyConcurrentInitialization(t *testing.T) {
	t.Parallel()
	s := &PgStore{}
	var wg sync.WaitGroup
	results := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.turnContextRevision("session", SessionAnalysisAccess{}, turnContextVersion{})
			if err != nil {
				t.Error(err)
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	var first string
	for r := range results {
		if first == "" {
			first = r
		}
		if first != r {
			t.Fatal("concurrent initialization changed signing key")
		}
	}
}

func TestTurnContextBudgetAndEmptySourceFields(t *testing.T) {
	t.Parallel()
	started := time.Now()
	ctx, cancel := traceBudget(context.Background())
	finished := time.Now()
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Before(started.Add(time.Second)) || deadline.After(finished.Add(time.Second)) {
		t.Fatal("turn context lost its one-second budget")
	}
	s := &PgStore{}
	// The schema allows an empty UUID or profile; neither changes tuple identity.
	c := turnContextCursor{Revision: "revision", Turn: "turn", Key: SessionTimelineKey{Timestamp: time.Now(), RecordID: 1}}
	raw, err := s.encodeTurnContextCursor(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.decodeTurnContextCursor(raw, "revision", "turn"); err != nil {
		t.Fatalf("valid empty source fields=%v", err)
	}
}
