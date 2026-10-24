package main

import (
	"fmt"
	"hash/fnv"
	"path"
	"strings"
)

// fileText is what reading file shows for this task: the component, its test, or
// one of the project's docs, each in its own language.
func fileText(t task, file string) string {
	switch {
	case file == t.Comp.Test:
		return testSnippet(t)
	case strings.HasSuffix(file, ".md") || strings.HasSuffix(file, ".mdx"):
		return docSnippet(t.Project, file)
	}
	return snippet(t.Comp)
}

func testSnippet(t task) string {
	c := t.Comp
	parts := strings.Split(c.Symbol, ".")
	typ, method := parts[0], parts[len(parts)-1]
	switch c.Lang {
	case "go":
		pkg := path.Base(path.Dir(c.File))
		if len(parts) == 1 {
			return fmt.Sprintf("package %s\n\nfunc %s(t *testing.T) {\n\tdir := t.TempDir()\n\tif _, err := %s(dir); err == nil {\n\t\tt.Fatal(\"want an error for a missing file\")\n\t}\n}\n", pkg, t.testName(), method)
		}
		return fmt.Sprintf("package %s\n\nfunc %s(t *testing.T) {\n\tx := &%s{limit: 3, clock: fixedClock}\n\tif err := x.%s(context.Background()); err != nil {\n\t\tt.Fatalf(\"%s: %%v\", err)\n\t}\n}\n", pkg, t.testName(), typ, method, method)
	case "ts":
		return fmt.Sprintf("import { describe, expect, it } from 'vitest';\n\ndescribe('%s', () => {\n  it('keeps the query when items change', () => {\n    const view = setup({ items: fixtures.items });\n    expect(view.visible).toHaveLength(3);\n  });\n});\n", typ)
	case "py":
		return fmt.Sprintf("from datetime import datetime, timezone\n\nNOW = datetime(2026, 3, 29, 1, 30, tzinfo=timezone.utc)\n\n\ndef %s():\n    subject = make_subject()\n    assert subject is not None\n", t.testName())
	}
	return ""
}

func docSnippet(p *project, file string) string {
	title := strings.ReplaceAll(p.Key, "-", " ")
	front := ""
	if strings.HasSuffix(file, ".mdx") {
		front = fmt.Sprintf("---\ntitle: %s\n---\n\n", capitalize(path.Base(strings.TrimSuffix(file, ".mdx"))))
	}
	return fmt.Sprintf("%s# %s\n\n## Getting started\n\nInstall the tooling, then run the local stack:\n\n```sh\nmake dev\n```\n\n## Configuration\n\nSettings are read from `config.toml` and the environment.\n", front, capitalize(title))
}

// editFor is the old/new pair an edit of file makes for this task.
func editFor(t task, file string) (string, string) {
	c := t.Comp
	switch {
	case file == c.Test:
		switch c.Lang {
		case "go":
			old := fmt.Sprintf("func %s(t *testing.T) {", t.testName())
			return old, old + "\n\tt.Parallel()"
		case "ts":
			old := "  it('keeps the query when items change', () => {"
			return old, fmt.Sprintf("  it('handles %s', () => {\n    expect(setup({ items: [] }).visible).toEqual([]);\n  });\n\n%s", t.Case, old)
		case "py":
			old := fmt.Sprintf("def %s():", t.testName())
			return old, fmt.Sprintf("def %s_edge_case():\n    assert make_subject(edge=True).runs == []\n\n\n%s", t.testName(), old)
		}
	case strings.HasSuffix(file, ".md") || strings.HasSuffix(file, ".mdx"):
		return "## Configuration", fmt.Sprintf("## %s\n\nThe %s supports %s. See the example below.\n\n## Configuration", capitalize(t.Feature), t.Comp.Name, t.Feature)
	}
	return editPair(c)
}

func diffText(t task) string {
	old, new := editPair(t.Comp)
	var b strings.Builder
	f := t.Comp.File
	fmt.Fprintf(&b, "diff --git a/%s b/%s\nindex %s..%s 100644\n--- a/%s\n+++ b/%s\n@@ -12,7 +12,9 @@\n", f, f, shortSHA(f+"a"), shortSHA(f+"b"), f, f)
	for _, l := range strings.Split(old, "\n") {
		b.WriteString("-" + l + "\n")
	}
	for _, l := range strings.Split(new, "\n") {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}

func gitLogText(t task) string {
	subjects := []string{
		"Handle " + t.Case + " in " + t.Comp.Name,
		"Add " + t.Feature,
		"Tidy " + t.Comp.Name + " tests",
		"Fix " + t.Thing,
		"Bump dependencies",
		"Document " + t.Comp.Name,
	}
	var b strings.Builder
	for _, s := range subjects {
		fmt.Fprintf(&b, "%s %s\n", shortSHA(t.Project.Key+s), s)
	}
	return b.String()
}

func gitStatusText(t task) string {
	return fmt.Sprintf(" M %s\n M %s\n", t.Comp.File, t.Comp.Test)
}

func commitMessage(t task) string {
	switch t.Kind {
	case "debugging":
		return "Fix " + t.Thing + " in " + t.Comp.Name
	case "documentation":
		return "Document " + t.Feature
	}
	return capitalize(t.Comp.Name) + ": handle " + t.Case
}

func gitCommitText(t task) string {
	return fmt.Sprintf("[%s %s] %s\n 2 files changed, 18 insertions(+), 4 deletions(-)", t.Branch, shortSHA(t.Branch+t.Comp.File), commitMessage(t))
}

func gitPushText(t task) string {
	return fmt.Sprintf("To git.example.com:%s/%s.git\n * [new branch]      %s -> %s\nbranch '%s' set up to track 'origin/%s'.", t.Project.Org, t.Project.Key, t.Branch, t.Branch, t.Branch, t.Branch)
}

func todoItems(t task) []string {
	return []string{
		"Split input validation out of " + t.Comp.Symbol,
		"Move state handling into its own type",
		"Update call sites and keep " + t.testName() + " green",
	}
}

func shortSHA(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%07x", h.Sum32()&0xfffffff)
}
