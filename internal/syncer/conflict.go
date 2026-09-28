package syncer

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"time"

	"cctrace/internal/store"
)

// ConflictTail is the part of a file's conflict-key assignment that has to
// outlive the scan that produced it.
//
// Ts is the newest timestamp the consumed prefix of the file emitted, and
// Counts says how many records the prefix already placed on each key at that
// timestamp (keyed by session id, record type and profile email — everything
// in the server's dedup key except the timestamp itself). A ledger is a pure
// function of the file prefix the offset has consumed, so it may only move
// when the offset moves, and it must be dropped whenever the offset stops
// describing the same bytes.
//
// Offset and Anchor are what make that last clause checkable rather than
// assumed. Offset is the byte position the counts describe, and Anchor
// fingerprints the bytes just before it, so a ledger can be told apart from one
// that merely happens to be the same length. Both are required because a byte
// offset is not an identity: a rewritten file keeps the offset while the record
// at it changes. A ledger that fails either check is not "probably fine", it is
// unusable — see ConflictSeed.
//
// Only the newest timestamp needs carrying. Session logs are append-only and
// their records are written in time order, so a key can only be revisited by a
// later record if that record's timestamp is also the newest one seen. Measured
// against 335,839 records in 2,199 local Codex rollouts: 3 records arrive out
// of order and none of them revisits a key an earlier record already claimed.
type ConflictTail struct {
	Ts     time.Time      `json:"ts"`
	Counts map[string]int `json:"counts,omitempty"`
	Offset int64          `json:"offset"`
	Anchor string         `json:"anchor,omitempty"`
}

// ConflictNudger keeps records that would land on the same server dedup key
// apart from one another.
//
// internal/store writes session records with
// ON CONFLICT (session_id, ts, record_type, profile_email, uuid) DO NOTHING.
// Claude's records carry a per-record uuid, so that key is complete for them.
// Codex, gjc and omo have no message lineage to fill uuid with, so for them the
// key collapses to four columns and any two records sharing a timestamp
// collide: the second is discarded, with no error and no counter anywhere.
// Nudging the later record forward by whole microseconds restores uniqueness
// without touching the key, which has to stay exactly as it is -- DO NOTHING on
// a stable key is what makes re-sending safe, and re-sending is how a retry, a
// timeout recovery and a full rescan all get their work done.
//
// The nudge a record gets is its ordinal among the same-key records preceding
// it *in the file*, not in the batch. That distinction is the whole point:
// a scan boundary that falls inside a run of same-timestamp records used to
// restart the count at zero, so the run's second half re-claimed the keys its
// first half had already written and the server dropped it. Counting from the
// file also makes the assignment reproducible — one from-zero scan of the file
// produces exactly the keys any sequence of incremental scans produces, which
// is what lets a re-send deduplicate instead of duplicating.
//
// Reproducible only where the count really did start at the file's first
// record. A count that started anywhere else is not the file's ordinal, it is
// some suffix's, and the keys it produces are ones a from-zero scan gives to
// other records — which turns the loss this fixes into a duplicate the first
// time anything re-sends. State.ConflictSeed decides which of the two a given
// scan has, and hands nil to anything it cannot show is the first.
//
// It lives here rather than in each syncer because all three had a private copy
// of the same function and the invariant is one invariant, not three.
type ConflictNudger struct {
	seen  map[string]int
	maxTs time.Time
	tail  map[string]int
}

// NewConflictNudger resumes from the ledger a previous scan of the same file
// left behind. A nil ledger starts the count at zero.
//
// Starting at zero is right for a scan that starts at byte zero and wrong for
// one that does not — the prefix it skips has already claimed keys nobody is
// counting. Wrong, but wrong in the pre-ledger way: it re-claims keys the
// server already holds and the duplicates are dropped there. Callers only get a
// ledger to pass here when ConflictSeed can show it describes the prefix in
// front of them; everywhere else they pass nil and accept the old loss rather
// than inventing a count.
func NewConflictNudger(tail *ConflictTail) *ConflictNudger {
	n := &ConflictNudger{seen: make(map[string]int), tail: make(map[string]int)}
	if tail == nil {
		return n
	}
	n.maxTs = tail.Ts
	stamp := conflictStamp(tail.Ts)
	for rest, count := range tail.Counts {
		n.seen[stamp+"|"+rest] = count
		n.tail[rest] = count
	}
	return n
}

// Apply moves r's timestamp forward by one microsecond for every record
// already placed on r's key, counting the records this nudger was seeded with.
func (n *ConflictNudger) Apply(r *store.SessionRecord) {
	ts := r.Ts
	rest := conflictKeyRest(r)
	count := n.seen[conflictStamp(ts)+"|"+rest]
	if count > 0 {
		r.Ts = ts.Add(time.Duration(count) * time.Microsecond)
	}
	n.seen[conflictStamp(ts)+"|"+rest] = count + 1

	// A record older than the newest one seen cannot be carried forward: the
	// ledger holds one timestamp, and holding every timestamp a file ever used
	// would grow without bound. Such a record is left where the pre-ledger code
	// left it, which is no worse than before and vanishingly rare in practice.
	switch {
	case ts.After(n.maxTs):
		n.maxTs = ts
		n.tail = map[string]int{rest: count + 1}
	case ts.Equal(n.maxTs):
		n.tail[rest] = count + 1
	}
}

// Tail returns the ledger describing every record applied so far, seeded
// records included. Persist it only alongside an offset that has actually
// moved past those records: a pass that is rewound re-reads them next time and
// has to re-derive the same microseconds, not add to them.
//
// A nudger that applied nothing still returns a ledger, with no counts in it.
// An empty ledger and an absent one say different things — "this prefix placed
// no records" versus "nobody counted this prefix" — and only the second one has
// to give up the nudge. Offset and Anchor are left to the caller, which is the
// only place that knows which bytes the counts ended up describing.
func (n *ConflictNudger) Tail() *ConflictTail {
	counts := make(map[string]int, len(n.tail))
	for rest, count := range n.tail {
		counts[rest] = count
	}
	return &ConflictTail{Ts: n.maxTs, Counts: counts}
}

// conflictAnchorWindow is how many bytes before the offset ConflictAnchor
// covers. Enough to span a session-log line, small enough that reading it every
// pass costs nothing next to the scan that follows it.
const conflictAnchorWindow = 512

// ConflictAnchor fingerprints the bytes immediately before offset, so a ledger
// can tell the prefix it counted apart from a different prefix that happens to
// be the same number of bytes long.
//
// Session logs are not purely append-only. Codex migrates rollouts to paginated
// history and reserializes every line doing it — measured on 195 duplicated
// local sessions, 171 had a changed prefix and 132 of those were the same size
// or larger, so the shrink check never sees them. After such a rewrite the
// stored offset lands on a different record, and a ledger carried across it
// hands later records the microseconds a from-zero scan would give to earlier
// ones. Nothing fails at the time: the rows land on free keys and look right
// until something re-sends the file and the same records land again, on the
// keys they should have had all along.
//
// The window is the end of the prefix rather than all of it because all of it
// costs a full read every pass. A rewrite that leaves the last 512 bytes of the
// prefix byte-identical goes undetected; that is a bounded miss, not a hole —
// what it costs is the nudge, and what it falls back to is the behaviour that
// shipped before the ledger existed. An unreadable file returns the empty
// string, which matches no stored anchor and so also falls back.
func ConflictAnchor(path string, offset int64) string {
	if offset <= 0 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	n := int64(conflictAnchorWindow)
	if offset < n {
		n = offset
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, offset-n); err != nil {
		return ""
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:8])
}

// conflictKeyRest is the server's dedup key minus the timestamp and minus uuid,
// which these agents leave empty. Timestamps are held separately because the
// ledger is keyed on one of them.
func conflictKeyRest(r *store.SessionRecord) string {
	recordType := r.RecordType
	// Codex developer messages were stored as 'user' before codexlog split them
	// out, and the rows (and ledgers) already written carry the nudges that shared
	// key produced. Counting them apart would move the timestamps a re-sync
	// assigns, and the upsert key would miss and duplicate those rows.
	if recordType == "developer" {
		recordType = "user"
	}
	return r.SessionID + "|" + recordType + "|" + r.ProfileEmail
}

// conflictStamp normalizes to UTC before formatting so two records at the same
// instant cannot be told apart by the zone their timestamp was parsed in, and
// so a ledger that has been through the state file's JSON compares equal to the
// records it was built from.
func conflictStamp(ts time.Time) string {
	return ts.UTC().Format(time.RFC3339Nano)
}
