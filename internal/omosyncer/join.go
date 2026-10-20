package omosyncer

import (
	"context"
	"fmt"
	"os"
	"time"

	"cctrace/internal/omolog"
)

// JoinSource takes over the omo home dir that arrives without the previous holder's
// cursor. The files the state has not seen that were last written at or before t0
// are skipped to the ends they had when the join looked at them, as on a first
// run: they hold history this state knows nothing about. A file written after t0
// is left alone and is read from its start, so a session that began while the
// hand-over was under way is not cut short; one that was still being written is
// read whole again, which the server de-duplicates.
//
// It is safe to repeat with the same t0: a file it has skipped is known to the
// state and not touched again. It returns how many files it skipped. The caller
// saves the state.
func (s *OmoSyncer) JoinSource(ctx context.Context, dir string, t0 time.Time) (int, error) {
	files, err := omolog.FindSessionFiles(dir)
	if err != nil {
		return 0, fmt.Errorf("find omo jsonl files: %w", err)
	}
	skipped := 0
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return skipped, err
		}
		if s.state.HasFile(file) {
			continue
		}
		// One look decides and bounds the skip: a line written after it is the
		// passes' to read. A file written after t0, or that cannot be examined,
		// is left to them whole.
		fi, err := joinStat(file)
		if err != nil || fi.ModTime().After(t0) {
			continue
		}
		if err := s.skipExisting(ctx, file, fi.Size()); err != nil {
			return skipped, err
		}
		skipped++
	}
	return skipped, nil
}

// joinStat is how a join looks at a file. A test swaps it to write to the file
// right after the look.
var joinStat = os.Stat
