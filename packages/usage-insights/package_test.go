package usageinsights

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/openinsights"
)

func TestPackagesShareThePrivacySafeWorkflow(t *testing.T) {
	root := "."
	for _, path := range []string{
		"SKILL.md",
		"skills/usage-insights/SKILL.md",
	} {
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, want := range []string{
			"cctrace usage --since 7d --json",
			"cctrace tools --since 7d --json",
			"cctrace plugins --since 7d --json",
			"cctrace skills --since 7d --json",
			"cctrace organization-insights --since 7d --json",
			"observations, interpretations, and recommended actions",
			"never print, request, or expose api tokens",
			"never use `cctrace sessions`",
			"cctrace insights cost --since 7d --json",
			"cctrace insights context --since 7d --json",
			"report every entry in `caveats`",
		} {
			if !strings.Contains(strings.ToLower(string(body)), strings.ToLower(want)) {
				t.Errorf("%s missing %q", path, want)
			}
		}
	}
}

func TestAgentManifestsHaveNoMCPServer(t *testing.T) {
	for _, path := range []string{
		".claude-plugin/plugin.json",
		"gemini-extension.json",
	} {
		body, err := os.ReadFile(filepath.Join(".", path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var manifest map[string]any
		if err := json.Unmarshal(body, &manifest); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if manifest["name"] != "usage-insights" {
			t.Errorf("%s name = %v", path, manifest["name"])
		}
		if _, ok := manifest["mcpServers"]; ok {
			t.Errorf("%s must not configure MCP", path)
		}
	}
}

func TestRepresentativeFixturesExcludeSensitiveContent(t *testing.T) {
	for _, path := range []string{
		"fixtures/representative-personal-report.json",
		"fixtures/representative-insights-cost.json",
		"fixtures/representative-insights-context.json",
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"prompt", "response", "command_body", "repository_name", "repository_id", "project_hash", "api_token", "email"} {
			if strings.Contains(strings.ToLower(string(body)), forbidden) {
				t.Errorf("%s contains forbidden %q", path, forbidden)
			}
		}
	}
}

// The insights fixtures document what the skill reads. Decoding strictly catches
// a field the CLI no longer sends; re-encoding and comparing key sets catches a
// field the CLI sends that the fixture lacks.
func TestInsightsFixturesMatchCLIOutputShape(t *testing.T) {
	for path, newResult := range map[string]func() any{
		"fixtures/representative-insights-cost.json":    func() any { return &openinsights.CostResult{} },
		"fixtures/representative-insights-context.json": func() any { return &openinsights.ContextResult{} },
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if diff := fixtureShapeDiff(body, newResult()); diff != "" {
			t.Errorf("%s does not match the CLI output shape: %s", path, diff)
		}
		// The key comparison cannot see inside an empty list, so every list must
		// hold an element. caveats is exempt: the cost fixture covers its shape.
		if empty := emptyList("", mustDecode(t, body), "caveats"); empty != "" {
			t.Errorf("%s: %s is empty, so its element shape is unchecked", path, empty)
		}
	}
}

func TestEmptyListReportsNestedEmptyList(t *testing.T) {
	doc := mustDecode(t, []byte(`{"caveats":[],"overall":{"hit_rate":1},"by_model":[{"model":"a"}],"bloated_sessions":[]}`))
	if got := emptyList("", doc, "caveats"); got != "bloated_sessions" {
		t.Fatalf("emptyList = %q", got)
	}
}

func mustDecode(t *testing.T, body []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// emptyList returns the path of the first empty list, skipping keys in exempt.
func emptyList(prefix string, v any, exempt ...string) string {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			skip := false
			for _, e := range exempt {
				skip = skip || e == prefix+k
			}
			if skip {
				continue
			}
			if arr, ok := child.([]any); ok && len(arr) == 0 {
				return prefix + k
			}
			if found := emptyList(prefix+k+".", child, exempt...); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range x {
			if found := emptyList(prefix, child, exempt...); found != "" {
				return found
			}
		}
	}
	return ""
}

func TestFixtureShapeDiffCatchesMissingField(t *testing.T) {
	body, err := os.ReadFile("fixtures/representative-insights-context.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc["rebuilds"].(map[string]any), "min_gap_seconds")
	trimmed, _ := json.Marshal(doc)
	if diff := fixtureShapeDiff(trimmed, &openinsights.ContextResult{}); !strings.Contains(diff, "rebuilds.min_gap_seconds") {
		t.Fatalf("missing nested field not reported: %q", diff)
	}
}

// fixtureShapeDiff decodes body strictly into v, re-encodes v, and reports the
// first key present on one side only.
func fixtureShapeDiff(body []byte, v any) string {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err.Error()
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}
	var fixture, output any
	if err := json.Unmarshal(body, &fixture); err != nil {
		return err.Error()
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		return err.Error()
	}
	return keyDiff("", fixture, output)
}

func keyDiff(prefix string, fixture, output any) string {
	switch f := fixture.(type) {
	case map[string]any:
		o, _ := output.(map[string]any)
		for k := range o {
			if _, ok := f[k]; !ok {
				return "fixture lacks " + prefix + k
			}
		}
		for k, fv := range f {
			if d := keyDiff(prefix+k+".", fv, o[k]); d != "" {
				return d
			}
		}
	case []any:
		o, _ := output.([]any)
		for i := range f {
			if i < len(o) {
				if d := keyDiff(prefix, f[i], o[i]); d != "" {
					return d
				}
			}
		}
	}
	return ""
}
