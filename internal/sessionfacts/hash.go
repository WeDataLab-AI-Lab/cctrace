package sessionfacts

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"math"
	"reflect"
	"time"
)

// outputHash is the normalised hash of an output (p1b-design §5.3 step 6): every
// field of the computed rows, in declaration order, except fields tagged
// hash:"-". Legacy R2 hashing excludes additive P4 detail; the worker's extended
// hash entry points append those values explicitly.
//
// It walks the values rather than listing the fields, so a column added to a
// row is hashed without anyone remembering to add it here. Normalised means two
// outputs that would write the same rows hash alike: a nil list is an empty
// one, an instant is the same in any zone, and a nullable column is told from
// its zero value by a presence byte. The lists are already in their row order
// -- Compute sorts everything it emits -- so nothing here depends on the order
// the input arrived in.
// HashOutput applies the same normalized hash to a computed or truncated output.
func HashOutput(out Output) []byte {
	return outputHash(out.Session, out.Turns, out.Segments)
}

// HashOutputWithCommits is the opt-in hash for the commit-only preparation path.
func HashOutputWithCommits(out CommitOutput) []byte {
	return outputHash(out.Session, out.Turns, out.Segments, out.Commits)
}

// HashOutputWithTimeline is the opt-in hash for the timeline-only path.
func HashOutputWithTimeline(out TimelineOutput) []byte {
	return outputHash(out.Session, out.Turns, out.Segments, out.Timeline)
}

// HashOutputWithTimelineAndCommits covers the worker's timeline and P4 facts.
func HashOutputWithTimelineAndCommits(out TimelineCommitOutput) []byte {
	return outputHash(out.Session, out.Turns, out.Segments, out.Timeline, out.Commits, out.Tasks, out.Session.Detail.Copy, out.Session.Detail.CodexTurnAttribution, out.Session.Detail.TranscriptIO)
}

// HashOutputWithCommitsAndTasks is the fact-table output hash persisted with
// the session, turn and activity rows; timeline mapping keeps its own hash.
func HashOutputWithCommitsAndTasks(out Output, commits CommitFacts, tasks TaskFacts) []byte {
	return outputHash(out.Session, out.Turns, out.Segments, commits, tasks, out.Session.Detail.Copy, out.Session.Detail.CodexTurnAttribution, out.Session.Detail.TranscriptIO)
}

func outputHash(session Session, turns []Turn, segments []Segment, extra ...any) []byte {
	return outputHashChecked(func() {}, session, turns, segments, extra...)
}

func outputHashChecked(checkpoint func(), session Session, turns []Turn, segments []Segment, extra ...any) []byte {
	h := sha256.New()
	hashValueChecked(h, reflect.ValueOf(session), checkpoint)
	hashValueChecked(h, reflect.ValueOf(turns), checkpoint)
	hashValueChecked(h, reflect.ValueOf(segments), checkpoint)
	for _, facts := range extra {
		hashValueChecked(h, reflect.ValueOf(facts), checkpoint)
	}
	return h.Sum(nil)
}

var timeType = reflect.TypeOf(time.Time{})

func hashValue(h hash.Hash, v reflect.Value) { hashValueChecked(h, v, func() {}) }

func hashValueChecked(h hash.Hash, v reflect.Value, checkpoint func()) {
	checkpoint()
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			h.Write([]byte{0})
			return
		}
		h.Write([]byte{1})
		hashValueChecked(h, v.Elem(), checkpoint)
	case reflect.Struct:
		if v.Type() == timeType {
			t := v.Interface().(time.Time)
			hashUint(h, uint64(t.Unix()))
			hashUint(h, uint64(t.Nanosecond()))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).Tag.Get("hash") != "-" {
				hashValueChecked(h, v.Field(i), checkpoint)
			}
		}
	case reflect.Slice:
		hashUint(h, uint64(v.Len()))
		for i := 0; i < v.Len(); i++ {
			hashValueChecked(h, v.Index(i), checkpoint)
		}
	case reflect.String:
		hashUint(h, uint64(v.Len()))
		h.Write([]byte(v.String()))
	case reflect.Bool:
		if v.Bool() {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
	case reflect.Int, reflect.Int64:
		hashUint(h, uint64(v.Int()))
	case reflect.Float64:
		f := v.Float()
		if f == 0 {
			f = 0 // -0 and +0 are one stored value
		}
		hashUint(h, math.Float64bits(f))
	default:
		// A field of a kind this cannot write would drop out of the hash, and a
		// change to it would then go unpublished.
		panic("sessionfacts: output field of unhashed kind " + v.Kind().String())
	}
}

func hashUint(h hash.Hash, n uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	h.Write(b[:])
}
