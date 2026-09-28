package codexrates

import (
	"strings"
	"testing"
)

// full is a table that passes both gate conditions.
func full() map[string]Rate {
	m := map[string]Rate{}
	for name, r := range anchors {
		m[name] = r
	}
	for _, name := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5", "gpt-5-mini", "gpt-4.1", "o3"} {
		m[name] = Rate{Input: 1, Output: 2, CacheRead: 0.1}
	}
	return m
}

func TestAccept_OK(t *testing.T) {
	if err := Accept(full()); err != nil {
		t.Fatalf("Accept: %v", err)
	}
}

func TestAccept_RejectsTooFewRows(t *testing.T) {
	m := map[string]Rate{}
	for name, r := range anchors {
		m[name] = r
	}
	err := Accept(m)
	if err == nil {
		t.Fatal("Accept accepted a table of only the anchors")
	}
	if !strings.Contains(err.Error(), "rows") {
		t.Errorf("error should name the row count: %v", err)
	}
}

func TestAccept_RejectsMissingAnchor(t *testing.T) {
	m := full()
	delete(m, "gpt-4-0613")
	err := Accept(m)
	if err == nil {
		t.Fatal("Accept accepted a table with a missing anchor")
	}
	if !strings.Contains(err.Error(), "gpt-4-0613") {
		t.Errorf("error should name the missing anchor: %v", err)
	}
}

func TestAccept_RejectsWrongAnchorValue(t *testing.T) {
	m := full()
	// The batch tier prices every model at exactly half -- the scalar-multiple
	// mistake the anchors exist to catch.
	m["gpt-3.5-turbo"] = Rate{Input: 0.25, Output: 0.75}
	err := Accept(m)
	if err == nil {
		t.Fatal("Accept accepted a table whose anchor was priced at the batch tier")
	}
	if !strings.Contains(err.Error(), "gpt-3.5-turbo") || !strings.Contains(err.Error(), "0.75") {
		t.Errorf("error should name the anchor and the value it got: %v", err)
	}
}

func TestAccept_LiveFixturePasses(t *testing.T) {
	got, err := parseMarkdown(readFixture(t, "pricing.md"))
	if err != nil {
		t.Fatalf("parseMarkdown: %v", err)
	}
	if err := Accept(got); err != nil {
		t.Fatalf("the recorded live table was rejected: %v", err)
	}
}
