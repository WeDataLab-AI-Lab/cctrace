package api

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"cctrace/internal/store"
)

// The spec is the only thing an external caller reads before writing code against
// this API. If it lists a path we never registered they get a 404 they cannot
// explain; if we register one it omits, nobody outside this repository can find it.
// Neither failure shows up in any other test, so pin the two together.
//
// This mirrors config_doc_test.go, which does the same for the CLI config keys.
func TestOpenAPISpecMatchesRegisteredRoutes(t *testing.T) {
	spec := mustRead(t, "../../docs/api/openapi.yaml")

	registered := registeredOpenAPIPaths(t)
	documented := collect(regexp.MustCompile(`(?m)^  (/api/open/v1[^:]*):`), spec)

	if len(registered) == 0 || len(documented) == 0 {
		t.Fatalf("parsed nothing: %d routes, %d spec paths", len(registered), len(documented))
	}
	for _, path := range registered {
		if !contains(documented, path) {
			t.Errorf("route %s is registered but missing from docs/api/openapi.yaml", path)
		}
	}
	for _, path := range documented {
		if !contains(registered, path) {
			t.Errorf("docs/api/openapi.yaml documents %s but no route serves it", path)
		}
	}
}

// registeredOpenAPIPaths reads every file in the package, not just routes.go, and
// ignores commented-out lines.
//
// Both of those were holes an adversarial review walked through: commenting out a
// route left the test green while the endpoint 404'd, and a route registered from
// any other file in the package was invisible to it. Scanning one file and trusting
// that routes stay there is a convention, and a test that only holds while a
// convention holds is not the guard it claims to be.
//
// The method is parsed but not compared -- every open path is GET today, and
// matching only GET would turn a future POST into a false "documented but unrouted".
func registeredOpenAPIPaths(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	handle := regexp.MustCompile(`"[A-Z]+ (/api/open/v1[^"]*)"`)
	comment := regexp.MustCompile(`^\s*//`)

	var paths []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		for _, line := range strings.Split(mustRead(t, name), "\n") {
			if comment.MatchString(line) {
				continue
			}
			for _, m := range handle.FindAllStringSubmatch(line, -1) {
				paths = append(paths, m[1])
			}
		}
	}
	sort.Strings(paths)
	return paths
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func collect(re *regexp.Regexp, text string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// The route test above pins paths but not fields, and the spec drifted under it:
// /usage gained cost_usd and group_by without the Usage schema learning either,
// and additionalProperties: false made the documented contract reject what the
// server actually sends. Pin every response struct's JSON fields to its schema.
func TestOpenAPISchemasMatchResponseFields(t *testing.T) {
	spec := mustRead(t, "../../docs/api/openapi.yaml")
	for schema, v := range map[string]any{
		"SessionOverview":               openAPISessionOverviewDTO{},
		"SessionRecord":                 openAPISessionRecordDTO{},
		"Event":                         openAPIEventDTO{},
		"Metric":                        openAPIMetricDTO{},
		"ToolUsage":                     openAPIToolUsageDTO{},
		"ToolTimeBucket":                openAPIToolTimeBucketDTO{},
		"ToolFailure":                   openAPIToolFailureDTO{},
		"ToolDetail":                    openAPIToolDetailDTO{},
		"ProjectList":                   openAPIProjectListDTO{},
		"Project":                       openAPIProjectDTO{},
		"PluginUsage":                   openAPIPluginUsageDTO{},
		"SkillUsage":                    openAPISkillUsageDTO{},
		"Rule":                          openAPIRuleDTO{},
		"RuleVersion":                   openAPIRuleVersionDTO{},
		"RuleList":                      openAPIRuleListDTO{},
		"RuleDetail":                    openAPIRuleDetailDTO{},
		"UsageModel":                    openAPIUsageModelDTO{},
		"Usage":                         openAPIUsageDTO{},
		"UsageGroup":                    openAPIUsageGroupDTO{},
		"UsageGroupList":                openAPIUsageGroupListDTO{},
		"WeeklyInsights":                store.WeeklyInsights{},
		"WeeklyInsightHour":             store.WeeklyInsightHour{},
		"WeeklyInsightProject":          store.WeeklyInsightProject{},
		"WeeklyInsightTask":             store.WeeklyInsightTask{},
		"WeeklyInsightTool":             store.WeeklyInsightTool{},
		"OrganizationModelInsight":      store.OrganizationModelInsight{},
		"OrganizationToolInsight":       store.OrganizationToolInsight{},
		"OrganizationTaskInsight":       store.OrganizationTaskInsight{},
		"OrganizationHourInsight":       store.OrganizationHourInsight{},
		"OrganizationProjectInsight":    store.OrganizationProjectInsight{},
		"OrganizationBottleneckInsight": store.OrganizationBottleneckInsight{},
		// The handler builds a map from these fields, so the struct's tags are
		// the keys it can send.
		"OrganizationInsights": store.OrganizationInsights{},
		// writeJSON error bodies: {"error": ...} plus a machine-readable code on
		// refusals such as ingestion_token or unbounded_window.
		"Error": struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}{},
	} {
		documented := schemaProperties(spec, schema)
		if documented == nil {
			t.Errorf("docs/api/openapi.yaml has no schema %s", schema)
			continue
		}
		sent := jsonFields(reflect.TypeOf(v))
		for _, f := range sent {
			if !contains(documented, f) {
				t.Errorf("%s: server sends %q but the schema omits it", schema, f)
			}
		}
		for _, f := range documented {
			if !contains(sent, f) {
				t.Errorf("%s: schema documents %q but the server never sends it", schema, f)
			}
		}
	}
}

// schemaProperties returns the property names of components.schemas.<name>, read
// by indentation: schemas sit at four spaces, their keywords at six, and
// properties at eight under a six-space "properties:".
func schemaProperties(spec, name string) []string {
	lines := strings.Split(spec, "\n")
	start := -1
	for i, l := range lines {
		if l == "    "+name+":" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	keyword := regexp.MustCompile(`^      [A-Za-z]`)
	property := regexp.MustCompile(`^        ([A-Za-z0-9_]+):`)
	var props []string
	inProps := false
	for _, l := range lines[start+1:] {
		if l != "" && !strings.HasPrefix(l, "     ") {
			break // next schema, or the end of components
		}
		if keyword.MatchString(l) {
			inProps = strings.TrimSpace(l) == "properties:"
			continue
		}
		if m := property.FindStringSubmatch(l); inProps && m != nil {
			props = append(props, m[1])
		}
	}
	sort.Strings(props)
	return props
}

func jsonFields(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}
