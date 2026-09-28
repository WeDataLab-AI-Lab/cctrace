package main

import (
	"context"
	"path/filepath"
	"testing"

	"cctrace/internal/profile"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

// options.exclude_accounts only protects anything if the client every syncer
// shares carries it. The endpoint is unreachable on purpose: a locally excluded
// account must be dropped without asking the server.
func TestNewSyncClientCarriesExcludedAccounts(t *testing.T) {
	p := profile.NewDefault()
	p.Options.ExcludeAccounts = []string{"openai:acct-personal"}
	client := newSyncClient(p, "http://127.0.0.1:1", "test", "")

	state, err := syncer.LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	kept, err := syncer.NewAccountFilter(client, state).Drop(context.Background(), "openai",
		[]*store.SessionRecord{{AccountID: "acct-personal"}})
	if err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if len(kept) != 0 {
		t.Fatalf("kept %d records of an account the profile excludes", len(kept))
	}
}
