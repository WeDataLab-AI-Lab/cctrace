package store

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
)

// Record content can change a claim's interpretation without changing its key.
// Queue both the writer's sessions and claim peers, even for duplicate inserts
// and no-op enrichment. These invalidations are not a publication witness.
// Unlike markSessionFactsDirtyInOrder, this path must retain insert scan offsets.
func markClaimSourceFactsDirty(ctx context.Context, tx pgx.Tx, sessions []string, scanFrom []*int64, reason dirtyReason, peers []string) error {
	type target struct {
		id   string
		peer bool
		from *int64
	}
	all := make([]target, 0, len(sessions)+len(peers))
	for i, id := range sessions {
		var from *int64
		if scanFrom != nil {
			from = scanFrom[i]
		}
		all = append(all, target{id: id, from: from})
	}
	for _, id := range peers {
		all = append(all, target{id: id, peer: true})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].id != all[j].id {
			return all[i].id < all[j].id
		}
		return !all[i].peer && all[j].peer
	})
	for start := 0; start < len(all); {
		end := start + 1
		for end < len(all) && all[end].peer == all[start].peer {
			end++
		}
		ids := make([]string, 0, end-start)
		from := make([]*int64, 0, end-start)
		for _, item := range all[start:end] {
			ids = append(ids, item.id)
			from = append(from, item.from)
		}
		r := reason
		if all[start].peer {
			r = dirtyDeletionPeer
		}
		if err := markSessionFactsDirty(ctx, tx, ids, from, r); err != nil {
			return err
		}
		start = end
	}
	return nil
}
