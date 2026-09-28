package ingestblock

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func loaded(s *Sets) Loader {
	return func(context.Context) (*Sets, error) { return s, nil }
}

// #715: ingest refuses what an excluded account sends, by either key an account
// can be known by -- its billing id (Codex rows carry nothing else) or its login
// address (OTEL carries that).
func TestExcludedAccountsAreRefusedByEitherKey(t *testing.T) {
	c := New()
	if err := c.Refresh(context.Background(), loaded(&Sets{
		ExcludedAccounts: map[string]bool{AccountKey("openai", "acct-personal"): true},
		ExcludedEmails:   map[string]bool{"personal@example.test": true},
	})); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if !c.AccountExcluded("openai", "acct-personal") {
		t.Error("excluded billing account was not refused")
	}
	if c.AccountExcluded("anthropic", "acct-personal") {
		t.Error("the same id under another provider was refused -- it is another account")
	}
	if !c.EmailExcluded("Personal@Example.test") {
		t.Error("excluded address was not refused when its case differed; ingest does not lowercase")
	}
	if c.EmailExcluded("colleague@example.test") {
		t.Error("an address nobody excluded was refused")
	}
}

// An empty key is the absence of an identity, not an identity. Refusing it would
// drop every record that carries no account at all -- most of them.
func TestEmptyKeysAreNeverRefused(t *testing.T) {
	c := New()
	if err := c.Refresh(context.Background(), loaded(&Sets{
		ExcludedAccounts: map[string]bool{AccountKey("", ""): true, AccountKey("openai", ""): true},
		ExcludedEmails:   map[string]bool{"": true},
	})); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if c.AccountExcluded("openai", "") || c.AccountExcluded("", "") {
		t.Error("a record with no billing account was refused")
	}
	if c.EmailExcluded("") {
		t.Error("a record with no address was refused")
	}
}

// Un-excluding has to take effect at once. A stale "excluded" answer drops live
// data that nothing sweeps back -- the unsafe direction this cache otherwise
// avoids by only growing between refreshes.
func TestUnexcludingTakesEffectWithoutARefresh(t *testing.T) {
	c := New()
	c.AddExcludedAccount("openai", "acct-personal")
	c.AddExcludedEmail("personal@example.test")
	if !c.AccountExcluded("openai", "acct-personal") || !c.EmailExcluded("personal@example.test") {
		t.Fatal("Add did not take effect")
	}
	c.RemoveExcludedAccount("openai", "acct-personal")
	c.RemoveExcludedEmail("Personal@Example.test")
	if c.AccountExcluded("openai", "acct-personal") {
		t.Error("account still refused after it was un-excluded")
	}
	if c.EmailExcluded("personal@example.test") {
		t.Error("address still refused after it was un-excluded")
	}
}

// A failed load keeps the previous exclusions. An empty set would mean "refuse
// nothing", turning a transient database error into a leak.
func TestFailedLoadKeepsExclusions(t *testing.T) {
	c := New()
	c.AddExcludedAccount("openai", "acct-personal")
	failing := func(context.Context) (*Sets, error) { return nil, errors.New("db down") }
	if err := c.Refresh(context.Background(), failing); err == nil {
		t.Fatal("Refresh swallowed the load error")
	}
	if !c.AccountExcluded("openai", "acct-personal") {
		t.Error("a failed refresh dropped the exclusions it had")
	}
}

// Two refreshes can overlap: the 30-second tick and the reload an exclusion
// change triggers. The one that started later read the later state, so it has to
// be the one left standing. Otherwise a tick that loaded before an un-exclusion
// committed lands after the handler's reload and refuses the account again until
// the next tick -- dropping live telemetry nothing sweeps back.
func TestLaterRefreshWinsOverAnOverlappingEarlierOne(t *testing.T) {
	c := New()
	stale := make(chan struct{})
	entered := make(chan struct{})
	slow := func(context.Context) (*Sets, error) {
		close(entered)
		<-stale
		return &Sets{ExcludedAccounts: map[string]bool{AccountKey("openai", "acct-personal"): true}}, nil
	}
	fresh := func(context.Context) (*Sets, error) { return &Sets{}, nil }

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = c.Refresh(context.Background(), slow) }()
	<-entered
	freshDone := make(chan struct{})
	go func() { defer wg.Done(); _ = c.Refresh(context.Background(), fresh); close(freshDone) }()
	select {
	case <-freshDone:
	case <-time.After(200 * time.Millisecond):
	}
	close(stale)
	wg.Wait()

	if c.AccountExcluded("openai", "acct-personal") {
		t.Error("an earlier, slower refresh overwrote the later one")
	}
}
