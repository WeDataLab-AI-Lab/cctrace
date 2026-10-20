package sessiontrace

import (
	"fmt"
	"strings"
	"testing"
)

func TestNativeRequestRecipeRestoresAndChecksDigest(t *testing.T) {
	t.Parallel()
	refs := map[string]map[string]any{
		"request/base": {"id": "base", "keep": 0, "tail": []any{"one"}},
		"message/one":  {"message": map[string]any{"role": "user", "content": "a"}},
		"message/two":  {"message": map[string]any{"role": "assistant", "content": "b"}},
		"shape/s":      {"shape": map[string]any{"system": []any{map[string]any{"type": "text", "text": "rules"}}, "tools": []any{}}},
	}
	lookup := func(kind, key string) (map[string]any, string, error) {
		r, ok := refs[kind+"/"+key]
		if !ok {
			return nil, "", nil
		}
		return r, kind + "/" + key, nil
	}
	recipe := map[string]any{"base": "base", "keep": 1, "tail": []any{"two"}, "shapeHash": "s", "params": map[string]any{"model": "m", "max_tokens": 10}, "cacheControl": []any{map[string]any{"path": []any{"messages", 0, "content", 0}, "value": map[string]any{"type": "ephemeral"}}}}
	want := map[string]any{"model": "m", "max_tokens": 10, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "a", "cache_control": map[string]any{"type": "ephemeral"}}}}, map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "b"}}}}, "system": []any{map[string]any{"type": "text", "text": "rules"}}, "tools": []any{}}
	recipe["requestDigest"], _ = CanonicalHash(want)
	got, err := ResolveNativeRequest(recipe, lookup)
	if err != nil || got.State != "observed" || got.Digest.State != "matched" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	actual, _ := CanonicalJSON(got.Body)
	expected, _ := CanonicalJSON(want)
	if string(actual) != string(expected) {
		t.Fatalf("body=%s want=%s", actual, expected)
	}
	if _, ok := refs["message/one"]["message"].(map[string]any)["content"].(string); !ok {
		t.Fatal("mutated source message")
	}
}

func TestNativeRequestBodyBudgetStopsReferenceAmplification(t *testing.T) {
	t.Parallel()
	message := map[string]any{"role": "user", "content": strings.Repeat("x", 100)}
	for _, tc := range []struct {
		name   string
		recipe map[string]any
	}{
		{"repeated_blob", map[string]any{"tail": []any{"blob", "blob", "blob", "blob"}}},
		{"params", map[string]any{"tail": []any{"blob"}, "params": map[string]any{"model": strings.Repeat("x", 300)}}},
		{"combined_params_and_message", map[string]any{"tail": []any{"blob"}, "params": map[string]any{"model": strings.Repeat("x", 100)}}},
		{"shape", map[string]any{"tail": []any{"blob"}, "shapeHash": "shape"}},
		{"cache_control", map[string]any{"tail": []any{"blob"}, "cacheControl": []any{map[string]any{"path": []any{"messages", 0}, "value": strings.Repeat("x", 300)}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			lookup := func(kind, key string) (map[string]any, string, error) {
				calls++
				if kind == "shape" {
					return map[string]any{"shape": map[string]any{"system": strings.Repeat("x", 300)}}, "shape", nil
				}
				return map[string]any{"message": message}, "blob", nil
			}
			tc.recipe["requestDigest"] = "expected"
			got, err := resolveNativeRequest(tc.recipe, lookup, 256)
			if err != nil || got.State != "truncated" || got.Digest.State != "unavailable" || got.Digest.Actual != "" || len(got.MissingRefs) == 0 {
				t.Fatalf("limited reconstruction=%+v err=%v", got, err)
			}
			body, _ := CanonicalJSON(got.Body)
			if len(body) > 256 {
				t.Fatalf("body budget exceeded: %d", len(body))
			}
			if tc.name == "repeated_blob" && calls >= 4 {
				t.Fatalf("continued expansion after limit: %d", calls)
			}
			if message["content"] != strings.Repeat("x", 100) {
				t.Fatal("source changed")
			}
		})
	}
}

func TestNativeRequestMissingCycleAndCachePathRemainExplicit(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		recipe map[string]any
		lookup ReferenceLookup
	}{
		{"missing", map[string]any{"tail": []any{"absent"}}, func(string, string) (map[string]any, string, error) { return nil, "", nil }},
		{"cycle", map[string]any{"id": "x", "base": "x", "keep": 1}, func(string, string) (map[string]any, string, error) {
			return map[string]any{"id": "x", "base": "x", "keep": 1}, "ref", nil
		}},
		{"path", map[string]any{"tail": []any{}, "cacheControl": []any{map[string]any{"path": []any{"messages", 99}, "value": map[string]any{"type": "ephemeral"}}}}, func(string, string) (map[string]any, string, error) { return nil, "", nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveNativeRequest(test.recipe, test.lookup)
			if err != nil || got.State != "partial" || len(got.MissingRefs) == 0 {
				t.Fatalf("%s %+v %v", test.name, got, err)
			}
		})
	}
	_, err := ResolveNativeRequest(map[string]any{"tail": []any{"x"}}, func(string, string) (map[string]any, string, error) { return nil, "", fmt.Errorf("storage failure") })
	if err == nil {
		t.Fatal("storage error hidden as absence")
	}
}
