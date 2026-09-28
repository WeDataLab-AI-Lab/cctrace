package api

import (
	"testing"

	"cctrace/internal/store"
)

// The map exists to put names on rows the dashboard already draws. Cost and usage
// aggregates name every user's spend and are not scoped per caller, so a role=user
// looking at Overview sees other people's bars. #299 stopped naming them anyway,
// which left another user's handle beside an amount anyone could read.
func TestVisibleUserNames(t *testing.T) {
	users := []*store.DashboardUser{
		{CctraceUserID: "alice", Name: "Alice"},
		{CctraceUserID: "bob", Name: "Bob"},
		{CctraceUserID: "", Name: "No Id"}, // never mappable
		{CctraceUserID: "carol", Name: ""}, // no name to render
	}

	t.Run("a plain user gets every name, not just their own", func(t *testing.T) {
		got := visibleUserNames(users)
		if got["alice"] != "Alice" || got["bob"] != "Bob" {
			t.Errorf("got %v, want both alice and bob", got)
		}
	})

	t.Run("entries without an id or a name are never emitted", func(t *testing.T) {
		got := visibleUserNames(users)
		if _, ok := got[""]; ok {
			t.Error("an empty cctrace_user_id must not become a key")
		}
		if _, ok := got["carol"]; ok {
			t.Error("a user with no name has nothing to render")
		}
	})

	t.Run("the caller does not narrow the map", func(t *testing.T) {
		// Who is asking used to decide the answer. It decides nothing now -- the
		// aggregates this labels are the same for everyone who can open the page --
		// so the roster is the roster.
		if got := visibleUserNames(users); len(got) != 2 {
			t.Errorf("got %v, want the two mappable users", got)
		}
	})
}
