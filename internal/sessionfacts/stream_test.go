package sessionfacts

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func streamed(t *testing.T, in Input, batch int) TimelineCommitOutput {
	t.Helper()
	ctx := context.Background()
	b := NewIndexBuilder(in, nil)
	for start := 0; start < len(in.Rows); start += batch {
		for i := start; i < len(in.Rows) && i < start+batch; i++ {
			if err := b.AddClaude(ctx, int64(i), in.Rows[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	for start := 0; start < len(in.CodexRows); start += batch {
		for i := start; i < len(in.CodexRows) && i < start+batch; i++ {
			if err := b.AddCodex(ctx, int64(i), in.CodexRows[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	a, err := b.Seal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, h := range in.Heads {
		if err := a.AddHead(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range in.OTEL {
		if err := a.AddEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range in.CommitResults {
		if err := a.AddCommitResult(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, ref := range a.ClaudeOrder() {
		if err := a.ApplyClaude(ctx, ref.Ordinal, in.Rows[ref.Ordinal]); err != nil {
			t.Fatal(err)
		}
	}
	for _, ref := range a.CodexOrder() {
		if err := a.ApplyCodex(ctx, ref.Ordinal, in.CodexRows[ref.Ordinal]); err != nil {
			t.Fatal(err)
		}
	}
	out, err := a.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestStreamMatchesExistingFixtures(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(934))
	for _, tc := range allCases(t) {
		in := tc.in.in
		want := ComputeWithTimelineAndCommits(in)
		for _, batch := range []int{1, 2, 7} {
			mixed := in
			if batch != 1 {
				mixed.Rows = shuffled(rng, in.Rows)
				mixed.CodexRows = shuffled(rng, in.CodexRows)
				mixed.Heads = shuffled(rng, in.Heads)
				mixed.OTEL = shuffled(rng, in.OTEL)
			}
			got := streamed(t, mixed, batch)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s batch=%d mismatch: %s", tc.name, batch, firstDifference(reflect.ValueOf(got), reflect.ValueOf(want), "out"))
			}
		}
	}

}

func firstDifference(a, b reflect.Value, path string) string {
	if a.Type() != b.Type() {
		return path + ": type mismatch"
	}
	if a.Type() == reflect.TypeFor[time.Time]() {
		if a.Interface() != b.Interface() {
			return fmt.Sprintf("%s: got %v want %v", path, a.Interface(), b.Interface())
		}
		return ""
	}
	switch a.Kind() {
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if x := firstDifference(a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name); x != "" {
				return x
			}
		}
	case reflect.Pointer:
		if a.IsNil() != b.IsNil() {
			return path + ": pointer nil mismatch"
		}
		if !a.IsNil() {
			return firstDifference(a.Elem(), b.Elem(), path)
		}
	case reflect.Slice:
		if a.IsNil() != b.IsNil() || a.Len() != b.Len() {
			return fmt.Sprintf("%s: slice got %v want %v", path, a.Interface(), b.Interface())
		}
		for i := 0; i < a.Len(); i++ {
			if x := firstDifference(a.Index(i), b.Index(i), fmt.Sprintf("%s[%d]", path, i)); x != "" {
				return x
			}
		}
	default:
		if !reflect.DeepEqual(a.Interface(), b.Interface()) {
			return fmt.Sprintf("%s: got %v want %v", path, a.Interface(), b.Interface())
		}
	}
	return ""
}
