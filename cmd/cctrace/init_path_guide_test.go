package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The PATH guide numbered its options with fixed labels while the options
// themselves were conditional: "a) go install" lived inside `if hasGo`, "b) copy
// the binary" did not. Without Go the guide printed "choose one:" followed by a
// single option labelled b) -- and that is exactly the state of someone who
// installed from a released binary rather than from source.
func TestPathInstallGuideLines(t *testing.T) {
	label := regexp.MustCompile(`^\s*([a-z])\) `)

	labelsOf := func(lines []string) []string {
		var out []string
		for _, l := range lines {
			if m := label.FindStringSubmatch(l); m != nil {
				out = append(out, m[1])
			}
		}
		return out
	}

	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos+"/without go: single option carries no letter", func(t *testing.T) {
			lines := pathInstallGuideLines(goos, false, "/tmp/cctrace")
			if got := labelsOf(lines); len(got) != 0 {
				t.Errorf("one option must not be lettered; got labels %v in:\n%s", got, strings.Join(lines, "\n"))
			}
			if joined := strings.Join(lines, "\n"); strings.Contains(joined, "choose one") {
				t.Errorf("%q with a single option; got:\n%s", "choose one", joined)
			}
		})

		t.Run(goos+"/with go: options are lettered from a", func(t *testing.T) {
			lines := pathInstallGuideLines(goos, true, "/tmp/cctrace")
			got := labelsOf(lines)
			want := []string{"a", "b"}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("labels = %v, want %v in:\n%s", got, want, strings.Join(lines, "\n"))
			}
			if !strings.Contains(strings.Join(lines, "\n"), "choose one") {
				t.Errorf("two options should say %q", "choose one")
			}
		})
	}

	t.Run("the detected binary path is used when known", func(t *testing.T) {
		// Built at run time rather than written as a literal: an absolute path in
		// exported source is an identifier the mirror gate has to have approved,
		// and this one carries no meaning worth approving.
		exe := filepath.Join(t.TempDir(), "cctrace")
		lines := pathInstallGuideLines("linux", false, exe)
		if !strings.Contains(strings.Join(lines, "\n"), exe) {
			t.Error("want the resolved executable path in the copy command")
		}
	})

	// The block is English; a Korean fragment in the middle of it was the only
	// non-English line the CLI printed (#261).
	t.Run("output is english", func(t *testing.T) {
		hangul := regexp.MustCompile(`\p{Hangul}`)
		for _, goos := range []string{"darwin", "linux", "windows", "freebsd"} {
			for _, hasGo := range []bool{false, true} {
				for _, l := range pathInstallGuideLines(goos, hasGo, "") {
					if hangul.MatchString(l) {
						t.Errorf("%s hasGo=%v: non-english line %q", goos, hasGo, l)
					}
				}
			}
		}
	})
}
