package sessiontrace

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeInputPageReadsOnlySelectedMessagesAndKeepsOrdinals(t *testing.T) {
	t.Parallel()
	calls := 0
	lookup := func(kind, key string) (map[string]any, string, error) {
		calls++
		return map[string]any{"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": key}, map[string]any{"type": "text", "text": "again"}}}}, "source/" + key, nil
	}
	recipe := map[string]any{"tail": []any{"a", "b", "c"}, "requestDigest": "expected"}
	page, err := ResolveNativeRequestPage(recipe, lookup, NativeInputOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.MissingRefs == nil || page.SourceRefs == nil {
		t.Fatalf("empty reference lists must be arrays: %+v", page)
	}
	if calls != 1 || len(page.InputPage.Items) != 1 || page.InputPage.Items[0].RequestPath != "messages[0].content[0]" || page.InputPage.NextCursor == nil || page.Digest.State != "not_checked" {
		t.Fatalf("page=%+v calls=%d", page, calls)
	}
	next, err := ResolveNativeRequestPage(recipe, lookup, NativeInputOptions{Limit: 1, Cursor: *page.InputPage.NextCursor})
	if err != nil || next.InputPage.Items[0].RequestPath != "messages[0].content[1]" {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}

func TestNativeInputPageContinuesLargeBlockWithoutClaimingSourceTruncation(t *testing.T) {
	t.Parallel()
	lookup := func(string, string) (map[string]any, string, error) {
		return map[string]any{"message": map[string]any{"role": "user", "content": strings.Repeat("x", 1000)}}, "source", nil
	}
	recipe := map[string]any{"tail": []any{"a"}}
	opts := NativeInputOptions{Limit: 20, PayloadLimit: 400}
	page, err := ResolveNativeRequestPage(recipe, lookup, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.InputPage.Items) != 1 || !page.InputPage.Items[0].Truncated || page.InputPage.Items[0].State != "observed" || page.InputPage.NextCursor == nil {
		t.Fatalf("page=%+v", page)
	}
	raw, _ := json.Marshal(page.InputPage.Items)
	if len(raw) > 400 {
		t.Fatalf("payload %d exceeds cap", len(raw))
	}
	opts.Cursor = *page.InputPage.NextCursor
	next, err := ResolveNativeRequestPage(recipe, lookup, opts)
	if err != nil || next.InputPage.Items[0].OffsetBytes == 0 {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}

func TestNativeRequestProvenanceAndExplicitComparison(t *testing.T) {
	t.Parallel()
	lookup := func(kind, key string) (map[string]any, string, error) {
		return map[string]any{"message": map[string]any{"role": "user", "content": key}}, "source/" + key, nil
	}
	before, _ := ResolveNativeRequest(map[string]any{"tail": []any{"same", "old", "gone"}}, lookup)
	after, _ := ResolveNativeRequest(map[string]any{"tail": []any{"same", "new"}}, lookup)
	if len(after.Provenance) == 0 || after.Provenance[0].RequestPath != "messages[0].content[0]" {
		t.Fatalf("provenance=%+v", after.Provenance)
	}
	comparison := CompareNativeRequests(after, before, "before")
	seen := map[string]bool{}
	for _, item := range comparison.Items {
		seen[item.Change] = true
	}
	if !seen["common"] || !seen["changed"] || !seen["removed"] {
		t.Fatalf("comparison=%+v", comparison)
	}
	before.State = "partial"
	comparison = CompareNativeRequests(after, before, "before")
	if comparison.State != "partial" {
		t.Fatalf("partial comparison=%+v", comparison)
	}
}

func TestNativeInputPageUsesRecordedInputOrderBeforeSettings(t *testing.T) {
	t.Parallel()
	lookup := func(kind, key string) (map[string]any, string, error) {
		if kind == "shape" {
			return map[string]any{"shape": map[string]any{"system": []any{map[string]any{"type": "text", "text": "rules"}}, "tools": []any{map[string]any{"name": "Read"}}}}, "shape", nil
		}
		return map[string]any{"message": map[string]any{"role": "user", "content": "work"}}, "message", nil
	}
	recipe := map[string]any{"tail": []any{"a"}, "shapeHash": "shape", "params": map[string]any{"model": "m"}}
	got, err := ResolveNativeRequestPage(recipe, lookup, NativeInputOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, item := range got.InputPage.Items {
		paths = append(paths, item.RequestPath)
	}
	want := "system[0],messages[0].content[0],tools[0],model"
	if strings.Join(paths, ",") != want {
		t.Fatalf("paths=%v want=%s", paths, want)
	}
}

func TestNativeInputPageWithheldAndCacheControlRemainExplicit(t *testing.T) {
	t.Parallel()
	recipe := map[string]any{"tail": []any{"hidden"}}
	hidden := func(string, string) (map[string]any, string, error) {
		return map[string]any{"message": nil, "cctraceWithheld": []any{"message"}}, "source", nil
	}
	got, err := ResolveNativeRequestPage(recipe, hidden, NativeInputOptions{})
	if err != nil || got.State != "withheld" || got.InputPage.Items[0].State != "withheld" {
		t.Fatalf("withheld=%+v err=%v", got, err)
	}
	recipe["cacheControl"] = []any{map[string]any{"path": []any{"messages", 0, "content", 0}, "value": map[string]any{"type": "ephemeral"}}}
	lookup := func(string, string) (map[string]any, string, error) {
		return map[string]any{"message": map[string]any{"role": "user", "content": "text"}}, "source", nil
	}
	got, err = ResolveNativeRequestPage(recipe, lookup, NativeInputOptions{})
	block := got.InputPage.Items[0].Value.(map[string]any)
	if err != nil || block["cache_control"] == nil {
		t.Fatalf("control=%+v err=%v", got, err)
	}
}

func TestNativeComparisonKeepsUnrelatedMissingShapeLocalAndSeparatesFields(t *testing.T) {
	t.Parallel()
	a := NativeRequest{State: "partial", MissingRefs: []string{"shape:missing"}, Body: map[string]any{"model": "same", "messages": []any{}}}
	b := NativeRequest{State: "observed", Body: map[string]any{"model": "same", "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "gone"}}}}, "other": "same"}}
	got := CompareNativeRequests(a, b, "before")
	changes := map[string]string{}
	for _, item := range got.Items {
		changes[item.RequestPath] = item.Change
	}
	if changes["messages[0].content[0]"] != "removed" || changes["other"] != "removed" {
		t.Fatalf("unrelated missing shape=%+v", got)
	}
}

func TestNativeInputPageKeepsEmptyShapeFieldsAndCacheProvenance(t *testing.T) {
	t.Parallel()
	recipe := map[string]any{"tail": []any{"a"}, "shapeHash": "s", "cacheControl": []any{map[string]any{"path": []any{"messages", 0, "content", 0}, "value": map[string]any{"type": "ephemeral"}}}}
	lookup := func(kind, key string) (map[string]any, string, error) {
		if kind == "shape" {
			return map[string]any{"shape": map[string]any{"system": []any{}, "tools": []any{}}}, "shape", nil
		}
		return map[string]any{"message": map[string]any{"role": "system", "content": "rules", "output_config": map[string]any{"effort": "high"}}}, "message", nil
	}
	got, err := ResolveNativeRequestPage(recipe, lookup, NativeInputOptions{})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, item := range got.InputPage.Items {
		paths[item.RequestPath] = true
	}
	if !paths["system"] || !paths["tools"] || !paths["messages[0].output_config"] {
		t.Fatalf("missing recorded fields=%+v", got.InputPage.Items)
	}
	found := false
	for _, p := range got.Provenance {
		if p.RequestPath == "messages[0].content[0].cache_control" && p.RawPath == "cacheControl[0].value" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cache provenance=%+v", got.Provenance)
	}
}
