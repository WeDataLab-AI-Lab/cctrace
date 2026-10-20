package sessionfacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// allCases is every fixture of the rule tables. The properties below must hold
// for any input, so they are checked on all of them.
func allCases(t *testing.T) []tcase {
	t.Helper()
	var all []tcase
	for _, cases := range [][]tcase{
		treeCases, nodeCases, boundaryCases, splitCases, sharedPromptCases, subagentCases, statusCases,
		inputCases, commandCases, redactionCases, titleCases, ownershipCases, activityCases,
		costCases, copyCases, codexCases, remainingCases,
	} {
		all = append(all, cases...)
	}
	// A table that lost its cases would let every property pass on nothing.
	if len(all) < 90 {
		t.Fatalf("only %d fixtures collected", len(all))
	}
	return all
}

func shuffled[T any](rng *rand.Rand, s []T) []T {
	c := append([]T(nil), s...)
	rng.Shuffle(len(c), func(i, j int) { c[i], c[j] = c[j], c[i] })
	return c
}

// The rows of a session arrive in whatever order the reads return them. The
// same rows in any order give the same output, hash included.
func TestOutputDoesNotDependOnInputOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(20261007))
	for _, tc := range allCases(t) {
		in := tc.in.in
		want := Compute(in)
		for round := 0; round < 25; round++ {
			mixed := in
			mixed.Rows, mixed.CodexRows = shuffled(rng, in.Rows), shuffled(rng, in.CodexRows)
			mixed.Heads, mixed.OTEL = shuffled(rng, in.Heads), shuffled(rng, in.OTEL)
			mixed.Snapshots, mixed.CopiedIDs = shuffled(rng, in.Snapshots), shuffled(rng, in.CopiedIDs)
			if got := Compute(mixed); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: shuffled input (round %d) changed the output\n got  %+v\n want %+v", tc.name, round, got, want)
				break
			}
		}
	}
}

// Compute sorts what it reads. It sorts its own lists, not the caller's.
func TestComputeLeavesItsInputAlone(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, tc := range allCases(t) {
		in := tc.in.in
		in.Rows, in.CodexRows = shuffled(rng, in.Rows), shuffled(rng, in.CodexRows)
		in.Heads, in.OTEL = shuffled(rng, in.Heads), shuffled(rng, in.OTEL)
		in.Snapshots, in.CopiedIDs = shuffled(rng, in.Snapshots), shuffled(rng, in.CopiedIDs)
		before := in
		before.Rows, before.CodexRows = append([]ClaudeRow(nil), in.Rows...), append([]CodexRow(nil), in.CodexRows...)
		before.Heads, before.OTEL = append([]Head(nil), in.Heads...), append([]OTELRow(nil), in.OTEL...)
		before.Snapshots, before.CopiedIDs = append([]Snapshot(nil), in.Snapshots...), append([]int64(nil), in.CopiedIDs...)
		Compute(in)
		if !reflect.DeepEqual(in, before) {
			t.Errorf("%s: Compute changed its input", tc.name)
		}
	}
}

// The two invariants of p1b-design §4.4, on every fixture that has a cost: a
// session's cost is its turns' plus what no turn took, whether it is measured
// or estimated.
func TestTurnCostsAddUpToTheSessionCost(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	checked := map[string]int{}
	for _, tc := range allCases(t) {
		out := Compute(tc.in.in)
		s := out.Session
		if s.CostUSD == nil {
			continue
		}
		var kinds []string
		switch s.CostSource {
		case "otel", "otel_partial":
			kinds = []string{"otel", "otel_partial"}
		case "estimate", "estimate_pending":
			kinds = []string{"estimate", "estimate_pending"}
		default:
			t.Errorf("%s: cost %v under source %q", tc.name, *s.CostUSD, s.CostSource)
			continue
		}
		var ofTurns float64
		for _, turn := range out.Turns {
			counts := turn.CostSource == kinds[0] || turn.CostSource == kinds[1]
			if counts != (turn.CostUSD != nil) {
				t.Errorf("%s: turn %s has source %q and cost %s", tc.name, turn.TurnKey, turn.CostSource, usd(turn.CostUSD))
			}
			if turn.CostUSD != nil {
				ofTurns += *turn.CostUSD
			}
		}
		detail := s.Detail.Cost
		if !near(ofTurns, detail.TurnAttachedUSD) {
			t.Errorf("%s: turns cost %v, turn_attached_usd is %v", tc.name, ofTurns, detail.TurnAttachedUSD)
		}
		if !near(ofTurns+detail.UnattachedTurnUSD, *s.CostUSD) {
			t.Errorf("%s: turns %v + unattached %v != session %v", tc.name, ofTurns, detail.UnattachedTurnUSD, *s.CostUSD)
		}
		checked[kinds[0]]++
	}
	// Both kinds of session, and enough of each for the sums to have had
	// something to disagree about.
	if checked["otel"] < 5 || checked["estimate"] < 8 {
		t.Errorf("invariant checked on %d measured and %d estimated sessions", checked["otel"], checked["estimate"])
	}
}

// richOutput is an output with a value in most columns: turns, inputs, extra
// inputs, segments, a title, measured costs.
func richOutput(t *testing.T) Output {
	t.Helper()
	out := Compute(inputCases[1].in.in)
	if len(out.Turns) == 0 || len(out.Segments) == 0 || len(out.Turns[0].Detail.Inputs) == 0 {
		t.Fatalf("fixture has no turn, segment or input to hash: %+v", out)
	}
	return out
}

// eachLeaf changes every value under v in turn -- a string, a number, a flag,
// an instant, whether a nullable is null, how long a list is -- calls visit
// with the output so changed, and puts it back. excluded is whether the value
// sits under a field tagged hash:"-".
func eachLeaf(v reflect.Value, path string, excluded bool, visit func(path string, excluded bool)) {
	switch v.Kind() {
	case reflect.Struct:
		if t, ok := v.Interface().(time.Time); ok {
			v.Set(reflect.ValueOf(t.Add(time.Second)))
			visit(path, excluded)
			v.Set(reflect.ValueOf(t))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			eachLeaf(v.Field(i), path+"."+f.Name, excluded || f.Tag.Get("hash") == "-", visit)
		}
	case reflect.Pointer:
		old := reflect.New(v.Type()).Elem()
		old.Set(v)
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
			visit(path+" (null to zero)", excluded)
		} else {
			v.Set(reflect.Zero(v.Type()))
			visit(path+" (to null)", excluded)
			v.Set(old)
			eachLeaf(v.Elem(), path+"*", excluded, visit)
		}
		v.Set(old)
	case reflect.Slice:
		old := reflect.New(v.Type()).Elem()
		old.Set(v)
		// A longer list, then each field of the element added, then the ones
		// the fixture filled.
		v.Set(reflect.Append(reflect.AppendSlice(reflect.MakeSlice(v.Type(), 0, v.Len()+1), v), reflect.Zero(v.Type().Elem())))
		visit(path+" (longer)", excluded)
		eachLeaf(v.Index(v.Len()-1), fmt.Sprintf("%s[new]", path), excluded, visit)
		v.Set(old)
		if v.Len() > 0 {
			eachLeaf(v.Index(0), path+"[0]", excluded, visit)
		}
	case reflect.String:
		old := v.String()
		v.SetString(old + "x")
		visit(path, excluded)
		v.SetString(old)
	case reflect.Bool:
		v.SetBool(!v.Bool())
		visit(path, excluded)
		v.SetBool(!v.Bool())
	case reflect.Int, reflect.Int64:
		v.SetInt(v.Int() + 1)
		visit(path, excluded)
		v.SetInt(v.Int() - 1)
	case reflect.Float64:
		old := v.Float()
		v.SetFloat(old + 1)
		visit(path, excluded)
		v.SetFloat(old)
	default:
		panic("eachLeaf: unhandled kind " + v.Kind().String() + " at " + path)
	}
}

// The R2 hash is of every legacy computed value but bookkeeping and additive
// P4 copy/lifecycle detail, which use the opt-in worker hash path.
func TestHashCoversEveryComputedValueAndNoBookkeeping(t *testing.T) {
	out := CommitOutput{Output: richOutput(t)}
	if !bytes.Equal(out.Hash, HashOutput(out.Output)) {
		t.Fatal("Compute hash differs from exported hash")
	}
	commitOutput := ComputeWithCommits(commitFixture())
	if !bytes.Equal(commitOutput.Hash, HashOutputWithCommits(commitOutput)) {
		t.Fatal("Compute omitted commit facts from hash")
	}
	out.Commits = commitOutput.Commits
	out.Hash = HashOutputWithCommits(out)
	base := outputHash(out.Session, out.Turns, out.Segments, out.Commits)
	if !bytes.Equal(base, HashOutputWithCommits(out)) {
		t.Fatal("exported hash differs from computed output hash")
	}
	if !bytes.Equal(base, out.Hash) || len(out.Hash) != 32 {
		t.Fatalf("Output.Hash = %x, outputHash = %x", out.Hash, base)
	}
	legacyBase := HashOutput(out.Output)
	var hashed int
	var bookkeeping []string
	visit := func(path string, excluded bool) {
		legacyChanged := !bytes.Equal(HashOutput(out.Output), legacyBase)
		if legacyChanged != (!excluded && !strings.HasPrefix(path, "Commits")) {
			t.Errorf("%s: legacy hash field coverage changed", path)
		}
		changed := !bytes.Equal(outputHash(out.Session, out.Turns, out.Segments, out.Commits), base)
		switch {
		case excluded && changed:
			t.Errorf("%s is bookkeeping but changes the hash", path)
		case excluded:
			bookkeeping = append(bookkeeping, path)
		case !changed:
			t.Errorf("%s is computed but does not change the hash", path)
		default:
			hashed++
		}
	}
	root := reflect.ValueOf(&out).Elem()
	eachLeaf(root.FieldByName("Session"), "Session", false, visit)
	eachLeaf(root.FieldByName("Turns"), "Turns", false, visit)
	eachLeaf(root.FieldByName("Segments"), "Segments", false, visit)
	eachLeaf(root.FieldByName("Commits"), "Commits", false, visit)

	sort.Strings(bookkeeping)
	equal(t, "hash-excluded fields", bookkeeping, []string{
		"Session.ComputeMS", "Session.ComputedAt", "Session.Detail.CodexTurnAttribution (null to zero)",
		"Session.Detail.Copy (null to zero)", "Session.Detail.TranscriptIO (null to zero)", "Session.RecomputeCount", "Session.SourceGeneration",
	})
	// Session has some forty columns and a turn some thirty; far fewer leaves
	// than that means the walk skipped a row.
	if hashed < 120 {
		t.Errorf("only %d computed values were changed", hashed)
	}
	if !bytes.Equal(outputHash(out.Session, out.Turns, out.Segments, out.Commits), base) {
		t.Fatal("the walk did not put the output back")
	}
}

// Captured from b23e6d3f before the optional commit path existed. Re-pinned when
// Codex turns stopped sharing one request_no counter: only the first turn of
// "codex root with earlier legacy input" changed, from 2 to 1.
func TestR2HashesRemainUnchanged(t *testing.T) {
	h := sha256.New()
	j := sha256.New()
	cases := allCases(t)
	if len(cases) != 133 {
		t.Fatalf("baseline fixture count changed: %d", len(cases))
	}
	for _, tc := range cases {
		out := Compute(tc.in.in)
		fmt.Fprintf(h, "%s:%x\n", tc.name, out.Hash)
		wire, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(j, "%s:%s\n", tc.name, wire)
		if !bytes.Equal(out.Hash, HashOutput(out)) {
			t.Fatalf("%s: hash mismatch", tc.name)
		}
	}
	// The digests are written in 16-character parts: a 64-character hex literal is
	// what the mirror export reads as a secret, and these are only pins on fixtures.
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != "e0e9a3c68cb1e35d"+"e92b423b71cf2b79"+"fb9ce362c92a3e89"+"82bab245ab1bfb90" {
		t.Errorf("r2 fixture digest changed: %s", got)
	}
	if got := fmt.Sprintf("%x", j.Sum(nil)); got != "afe5d7fbdc0b891d"+"f99cdbdc394a641e"+"73391447da330bbb"+"6d0d36efe11500f9" {
		t.Errorf("r2 JSON digest changed: %s", got)
	}
	fallback := Output{Session: Session{Agent: "claude", Coverage: "truncated", RulesVersion: 2}}
	wire, err := json.Marshal(fallback)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(wire)); got != "9b89ae7b086aab03"+"729e7a0d61ddaf14"+"18610bdde19de639"+"16b008266cabf1cf" {
		t.Errorf("r2 fallback JSON changed: %s", got)
	}
	if got := fmt.Sprintf("%x", HashOutput(fallback)); got != "daaec1e3693112d6"+"6bd2edd10e0ad695"+"7339d3d91a3e5d5e"+"0dd3bc9b018e2755" {
		t.Errorf("r2 fallback hash changed: %s", got)
	}
}

// Two outputs that would write the same rows hash alike.
func TestHashIsNormalised(t *testing.T) {
	seoul := time.FixedZone("KST", 9*60*60)
	instant := at(60)
	elsewhere := instant.In(seoul)
	negativeZero := math.Copysign(0, -1)
	a := outputHash(
		Session{UserIDs: nil, FirstActivityAt: &instant, Detail: SessionDetail{Cost: CostDetail{TurnAttachedUSD: 0}}},
		nil, nil, CommitFacts{})
	b := outputHash(
		Session{UserIDs: []string{}, FirstActivityAt: &elsewhere, Detail: SessionDetail{Cost: CostDetail{TurnAttachedUSD: negativeZero}}},
		[]Turn{}, []Segment{}, CommitFacts{Items: []Commit{}})
	if !bytes.Equal(a, b) {
		t.Errorf("an empty list, another zone and a negative zero changed the hash: %x != %x", a, b)
	}
	// An instant is hashed to the nanosecond.
	later := instant.Add(time.Nanosecond)
	if bytes.Equal(outputHash(Session{FirstActivityAt: &instant}, nil, nil, CommitFacts{}), outputHash(Session{FirstActivityAt: &later}, nil, nil, CommitFacts{})) {
		t.Error("two instants a nanosecond apart hash alike")
	}
	// A null and a zero are different rows.
	zero := 0
	if bytes.Equal(outputHash(Session{}, nil, nil, CommitFacts{}), outputHash(Session{Turns: &zero}, nil, nil, CommitFacts{})) {
		t.Error("a null count and a zero count hash alike")
	}
	if bytes.Equal(outputHash(Session{UserIDs: []string{"a"}}, nil, nil, CommitFacts{}), outputHash(Session{ProfileEmails: []string{"a"}}, nil, nil, CommitFacts{})) {
		t.Error("a value hashes alike in two lists")
	}
	// Moving a value to the next column is a different row.
	if bytes.Equal(outputHash(Session{Agent: "ab", Coverage: ""}, nil, nil, CommitFacts{}), outputHash(Session{Agent: "a", Coverage: "b"}, nil, nil, CommitFacts{})) {
		t.Error("two columns hash as their concatenation")
	}
}

// A column of a kind the hash cannot write must not drop out of it quietly.
func TestHashRefusesAKindItCannotWrite(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a map was hashed as nothing")
		}
	}()
	hashValue(nil, reflect.ValueOf(struct{ M map[string]int }{}))
}
