package store

import (
	"context"
	"errors"
	"testing"
)

func TestCreateInitialDashboardUser(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, email := range []string{"first@example.com", "second@example.com"} {
		go func() {
			<-start
			results <- s.CreateInitialDashboardUser(context.Background(), &DashboardUser{Email: email, PasswordHash: "hash", Role: "admin"})
		}()
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrSetupAlreadyCompleted):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	count, err := s.CountDashboardUsers(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if _, err := s.CreateDashboardUser(context.Background(), &DashboardUser{Email: "regular@example.com", PasswordHash: "hash", Role: "user"}); err != nil {
		t.Fatal(err)
	}
}
