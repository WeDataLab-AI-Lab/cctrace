package insights

import "testing"

// Plain substring matching read file paths and machine text as requests. Measured
// on real logs: "document" matched 1,742 turns through
// a path with a Documents directory in it, "spec" matched "specific", "inspect" and
// "suspect" 1,800+ times, and "test" matched "latestupdate". Testing at 26.6% and
// Documentation at 8.4% were substantially those (#429).
func TestKeywordsDoNotMatchInsideOtherWords(t *testing.T) {
	for _, prompt := range []string{
		"read the file at Documents/GitHub/proj/main.go",
		"be concise and specific about the failure",
		"use a subagent to inspect files",
		"those fields are suspect",
		`the json field "latestupdate" is null`,
		"this is the greatest improvement so far",
	} {
		if got := ClassifyPrompt(prompt); got != "unknown" {
			t.Errorf("ClassifyPrompt(%.45q) = %q, want unknown -- a keyword fired inside another word", prompt, got)
		}
	}
}

// The boundary must not cost the matches the rules exist for.
func TestKeywordsStillMatchAsWords(t *testing.T) {
	for _, tc := range []struct{ prompt, want string }{
		{"add a test for the parser", "testing"},
		{"update the document", "documentation"},
		{"write a spec for this", "testing"},
		{"fix the regression", "debugging"},
		{"the tests are failing", "testing"},
		{"check the docs", "documentation"},
	} {
		if got := ClassifyPrompt(tc.prompt); got != tc.want {
			t.Errorf("ClassifyPrompt(%q) = %q, want %q", tc.prompt, got, tc.want)
		}
	}
}

// Korean keywords stay substrings on purpose: the language agglutinates, so 설계 is
// a prefix of 설계해줘 and a boundary rule would reject the ordinary form.
func TestKoreanKeywordsMatchInflectedForms(t *testing.T) {
	for _, tc := range []struct{ prompt, want string }{
		{"설계해줘", "planning"},
		{"테스트를 추가해줘", "testing"},
		{"문서를 갱신해", "documentation"},
		{"버그를 찾아줘", "debugging"},
	} {
		if got := ClassifyPrompt(tc.prompt); got != tc.want {
			t.Errorf("ClassifyPrompt(%q) = %q, want %q", tc.prompt, got, tc.want)
		}
	}
}
