package main

import (
	"fmt"
	"path"
	"strings"
)

// component is one piece of a project that tasks are about. Its language decides
// every snippet, command and test output shown for it, so a .tsx file never reads
// back as Go (the incoherence that sank the first docs-screenshot data, #794).
type component struct {
	Name     string
	File     string
	Test     string
	Lang     string // go | ts | py
	Symbol   string
	Features []string
	Things   []string
	Cases    []string
}

type project struct {
	Key  string
	Org  string
	Docs []string
	// Rules is written as the repository's CLAUDE.md, which the client uploads and
	// the Rules page lists.
	Rules      string
	Components []component
}

var projects = map[string]*project{
	"nebula-ingest": {
		Key: "nebula-ingest", Org: "nebula",
		Docs:  []string{"README.md", "docs/operations.md"},
		Rules: "# nebula-ingest\n\n- Go 1.26, `go test ./...` before every commit\n- Retries go through internal/retry; never sleep in a handler\n- Schema changes need a migration and a fixture\n",
		Components: []component{
			{"retry policy", "internal/retry/retry.go", "internal/retry/retry_test.go", "go", "Policy.Next",
				[]string{"jitter on the backoff", "a cap on total retry time", "a Retry-After override"},
				[]string{"backoff jitter", "the retry budget"},
				[]string{"a zero base delay", "attempt overflow"}},
			{"batch writer", "internal/ingest/batch.go", "internal/ingest/batch_test.go", "go", "BatchWriter.Flush",
				[]string{"size-based flushing", "a flush deadline"},
				[]string{"partial flushes on shutdown", "duplicate rows after a retry"},
				[]string{"an empty batch", "a flush during shutdown"}},
			{"queue consumer", "internal/queue/consumer.go", "internal/queue/consumer_test.go", "go", "Consumer.Poll",
				[]string{"visibility timeout extension", "dead-letter routing"},
				[]string{"message redelivery", "a stalled poll loop"},
				[]string{"a poisoned message", "a closed channel"}},
			{"ingest panel", "web/components/ingest/panel.tsx", "web/components/ingest/panel.test.tsx", "ts", "IngestPanel",
				[]string{"a retry button", "a lag indicator"},
				[]string{"the retry state", "a stale lag badge"},
				[]string{"a failed source", "an empty source list"}},
		},
	},
	"nebula-cli": {
		Key: "nebula-cli", Org: "nebula",
		Docs:  []string{"README.md", "docs/commands.md"},
		Rules: "# nebula-cli\n\n- Every command needs `--json` output and a golden test\n- Keep flags backward compatible; deprecate before removing\n",
		Components: []component{
			{"config loader", "internal/config/load.go", "internal/config/load_test.go", "go", "Load",
				[]string{"env var overrides", "a --profile flag"},
				[]string{"the XDG path lookup", "a missing config file"},
				[]string{"an empty profile name", "a read-only home"}},
			{"table renderer", "internal/output/table.go", "internal/output/table_test.go", "go", "Table.Render",
				[]string{"a --json flag", "column truncation"},
				[]string{"wide unicode columns", "empty results"},
				[]string{"a single row", "a 200-character cell"}},
			{"login command", "cmd/nebula/login.go", "cmd/nebula/login_test.go", "go", "loginCmd.Run",
				[]string{"device code login", "token refresh"},
				[]string{"expired tokens", "the callback timeout"},
				[]string{"a revoked token", "no browser available"}},
		},
	},
	"nebula-docs": {
		Key: "nebula-docs", Org: "nebula",
		Docs:  []string{"content/guides/getting-started.mdx", "content/reference/cli.mdx"},
		Rules: "# nebula-docs\n\n- Next.js site; run `npm run lint && npx vitest run` before pushing\n- Every guide needs a runnable example\n",
		Components: []component{
			{"search box", "components/search/search-box.tsx", "components/search/search-box.test.tsx", "ts", "SearchBox",
				[]string{"keyboard navigation", "recent searches"},
				[]string{"the debounce", "empty query results"},
				[]string{"an empty index", "a query with only spaces"}},
			{"table of contents", "lib/toc.ts", "lib/toc.test.ts", "ts", "buildToc",
				[]string{"nested headings", "a scroll spy"},
				[]string{"duplicate heading ids", "headings inside code blocks"},
				[]string{"a page without headings", "repeated titles"}},
		},
	},
	"orbit-scheduler": {
		Key: "orbit-scheduler", Org: "orbit",
		Docs:  []string{"README.md", "docs/scheduling.md"},
		Rules: "# orbit-scheduler\n\n- Python 3.13, `pytest -q` must pass\n- All times are stored in UTC; convert at the edges\n- New endpoints need an OpenAPI example\n",
		Components: []component{
			{"planner", "orbit/scheduler/planner.py", "tests/test_planner.py", "py", "Planner.next_runs",
				[]string{"timezone-aware schedules", "a max-runs limit"},
				[]string{"DST transitions", "skipped runs after downtime"},
				[]string{"a leap day", "a schedule with no future runs"}},
			{"cron parser", "orbit/scheduler/cron.py", "tests/test_cron.py", "py", "CronExpr.parse",
				[]string{"step values", "@hourly aliases"},
				[]string{"day-of-week ranges", "trailing whitespace"},
				[]string{"a range that wraps Sunday", "an invalid step"}},
			{"jobs API", "orbit/api/routes.py", "tests/test_routes.py", "py", "create_job",
				[]string{"pagination on /jobs", "a dry-run flag"},
				[]string{"validation errors", "duplicate job names"},
				[]string{"a missing schedule", "an unknown timezone"}},
		},
	},
}

// task is one request a person makes, with everything needed to phrase it and to
// script the agent's work on it.
type task struct {
	Kind    string
	Korean  bool
	Project *project
	Comp    component
	Feature string
	Thing   string
	Case    string
	Branch  string
}

func (t task) testName() string {
	if t.Comp.Lang == "py" {
		return "test_" + strings.ToLower(strings.ReplaceAll(t.Comp.Symbol, ".", "_"))
	}
	return "Test" + strings.ReplaceAll(t.Comp.Symbol, ".", "")
}

func (t task) prompt() string {
	c, f, th, cs := t.Comp.Name, t.Feature, t.Thing, t.Case
	file := t.Comp.File
	if t.Korean {
		switch t.Kind {
		case "implementation":
			return fmt.Sprintf("%s의 %s에 %s 추가해줘.", file, c, f)
		case "debugging":
			return fmt.Sprintf("%s가 %s에서 간헐적으로 실패해. 원인 찾아서 고쳐줘.", t.testName(), th)
		case "testing":
			return fmt.Sprintf("%s에 %s 케이스 테스트 추가해줘.", c, cs)
		case "review":
			return fmt.Sprintf("%s 변경사항 PR 올리기 전에 리뷰해줘.", c)
		case "documentation":
			return fmt.Sprintf("%s 문서에 %s 설명 넣어줘.", c, f)
		case "question":
			return fmt.Sprintf("%s가 %s을 어떻게 처리하는지 설명해줘.", c, th)
		case "planning":
			return fmt.Sprintf("%s 구조를 작게 나누는 계획 세워줘.", c)
		case "drafting":
			return fmt.Sprintf("%s 릴리스 노트 초안 작성해줘.", f)
		case "status":
			return fmt.Sprintf("%s 작업 진행 상황 정리해줘.", f)
		case "operations":
			return "커밋하고 브랜치 푸시해줘."
		}
	}
	switch t.Kind {
	case "implementation":
		return fmt.Sprintf("Add %s to the %s in %s.", f, c, file)
	case "debugging":
		return fmt.Sprintf("%s is flaky around %s. Please fix it.", t.testName(), th)
	case "testing":
		return fmt.Sprintf("Write tests for the %s covering %s.", c, cs)
	case "review":
		return fmt.Sprintf("Review my changes to the %s before I open a PR.", c)
	case "documentation":
		return fmt.Sprintf("Update the docs for the %s to cover %s.", c, f)
	case "question":
		return fmt.Sprintf("Explain how the %s handles %s.", c, th)
	case "planning":
		return fmt.Sprintf("Plan how we could split the %s into smaller pieces.", c)
	case "drafting":
		return fmt.Sprintf("Write a draft of the release notes for %s.", f)
	case "status":
		return fmt.Sprintf("What's the status of the %s work?", f)
	case "operations":
		return "Commit this and push the branch."
	}
	return "Continue."
}

func (t task) title() string {
	c := t.Comp.Name
	switch t.Kind {
	case "implementation":
		return capitalize(fmt.Sprintf("add %s to %s", t.Feature, c))
	case "debugging":
		return capitalize(fmt.Sprintf("fix flaky %s in %s", t.Thing, c))
	case "testing":
		return capitalize(fmt.Sprintf("tests for %s: %s", c, t.Case))
	case "review":
		return capitalize(fmt.Sprintf("review %s changes", c))
	case "documentation":
		return capitalize(fmt.Sprintf("document %s in %s", t.Feature, c))
	case "question":
		return capitalize(fmt.Sprintf("how %s handles %s", c, t.Thing))
	case "planning":
		return capitalize(fmt.Sprintf("plan to restructure %s", c))
	case "drafting":
		return capitalize(fmt.Sprintf("release notes for %s", t.Feature))
	case "status":
		return capitalize(fmt.Sprintf("status of %s", t.Feature))
	}
	return capitalize(c)
}

// followUp is a later prompt in the same session. Some are tasks of their own,
// some are acknowledgements the classifier files as non-task.
func (t task) followUp(kind string) string {
	switch kind {
	case "testing":
		if t.Korean {
			return "전체 테스트도 돌려줘."
		}
		return "Also run the full test suite."
	case "implementation":
		if t.Korean {
			return fmt.Sprintf("%s 처리도 추가해줘.", t.Case)
		}
		return fmt.Sprintf("Also add handling for %s.", t.Case)
	case "operations":
		return task{Kind: "operations", Korean: t.Korean}.prompt()
	case "ack":
		if t.Korean {
			return "좋아"
		}
		return "ok"
	}
	return "continue"
}

// say is what the agent writes before a step, in the prompt's language.
func (t task) say(what string) string {
	c, sym := t.Comp.Name, t.Comp.Symbol
	if t.Korean {
		switch what {
		case "look":
			return fmt.Sprintf("먼저 %s 코드를 보겠습니다.", c)
		case "cause":
			return fmt.Sprintf("%s에서 %s를 처리하지 않는 것이 원인입니다. 고치겠습니다.", sym, t.Case)
		case "edit":
			return fmt.Sprintf("%s을 수정하겠습니다.", sym)
		case "test":
			return "테스트를 돌려 보겠습니다."
		case "fail":
			return "실패하는 테스트를 먼저 재현하겠습니다."
		case "plan":
			return "작업을 단계로 나눠 두겠습니다."
		case "git":
			return "변경을 커밋하고 푸시하겠습니다."
		}
		return "확인하겠습니다."
	}
	switch what {
	case "look":
		return fmt.Sprintf("Let me look at the %s first.", c)
	case "cause":
		return fmt.Sprintf("Found it: %s doesn't handle %s. Fixing that.", sym, t.Case)
	case "edit":
		return fmt.Sprintf("I'll update %s.", sym)
	case "test":
		return "Running the tests."
	case "fail":
		return "Reproducing the failure first."
	case "plan":
		return "Breaking this into steps."
	case "git":
		return "Committing and pushing."
	}
	return "Checking."
}

// answer is the closing message of a task.
func (t task) answer() string {
	c, sym, f, th, cs := t.Comp.Name, t.Comp.Symbol, t.Feature, t.Thing, t.Case
	if t.Korean {
		switch t.Kind {
		case "implementation":
			return fmt.Sprintf("%s에 %s를 추가했습니다. %s에 테스트 두 개를 넣었고 모두 통과합니다.", c, f, t.Comp.Test)
		case "debugging":
			return fmt.Sprintf("원인은 %s가 %s에서 %s를 처리하지 않던 것입니다. 수정 후 %s를 20회 반복 실행했고 모두 통과했습니다.", sym, th, cs, t.testName())
		case "testing":
			return fmt.Sprintf("%s에 %s 케이스를 추가했습니다. 기존 테스트와 함께 모두 통과합니다.", t.Comp.Test, cs)
		case "review":
			return fmt.Sprintf("크게 두 가지입니다.\n\n1. %s에서 %s 경우의 에러를 삼키고 있습니다.\n2. 새 분기에 테스트가 없습니다.\n\n나머지는 괜찮아 보입니다.", sym, cs)
		case "documentation":
			return fmt.Sprintf("%s에 %s 절을 추가하고 예제를 넣었습니다.", t.Project.Docs[0], f)
		case "question":
			return fmt.Sprintf("%s는 %s에서 처리합니다. 호출마다 상태를 새로 계산하고, %s일 때는 기본값으로 돌아갑니다.", th, sym, cs)
		case "planning":
			return fmt.Sprintf("세 단계로 나누는 것을 제안합니다.\n\n1. %s의 입력 검증을 별도 함수로 분리\n2. 상태를 다루는 부분을 작은 타입으로 이동\n3. 기존 테스트를 유지한 채 호출부 정리", sym)
		case "drafting":
			return fmt.Sprintf("## %s\n\n- %s 지원\n- %s 관련 수정\n- 문서 업데이트", capitalize(f), f, th)
		case "status":
			return fmt.Sprintf("%s는 구현과 테스트가 끝났고 리뷰 대기 중입니다. 남은 것은 문서 한 절입니다.", f)
		case "operations":
			return fmt.Sprintf("`%s` 브랜치에 커밋하고 푸시했습니다.", t.Branch)
		}
	}
	switch t.Kind {
	case "implementation":
		return fmt.Sprintf("Added %s to the %s, with two new tests in %s. Everything passes.", f, c, t.Comp.Test)
	case "debugging":
		return fmt.Sprintf("The flake came from %s not handling %s around %s. Fixed it and ran %s 20 times; all green.", sym, cs, th, t.testName())
	case "testing":
		return fmt.Sprintf("Added a test for %s to %s. The suite passes.", cs, t.Comp.Test)
	case "review":
		return fmt.Sprintf("Two things:\n\n1. %s swallows the error for %s.\n2. The new branch has no test.\n\nThe rest looks good.", sym, cs)
	case "documentation":
		return fmt.Sprintf("Added a section on %s to %s, with an example.", f, t.Project.Docs[0])
	case "question":
		return fmt.Sprintf("%s handles %s: it recomputes the state on each call and falls back to the default for %s.", sym, th, cs)
	case "planning":
		return fmt.Sprintf("I'd split it in three steps:\n\n1. Move input validation out of %s\n2. Put the state handling in a small type\n3. Tidy the call sites, keeping the existing tests", sym)
	case "drafting":
		return fmt.Sprintf("## %s\n\n- Support for %s\n- Fix for %s\n- Docs updated", capitalize(f), f, th)
	case "status":
		return fmt.Sprintf("%s is implemented and tested, waiting on review. One docs section is left.", capitalize(f))
	case "operations":
		return fmt.Sprintf("Committed and pushed `%s`.", t.Branch)
	}
	return "Done."
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// snippet is a short, plausible view of a file in its own language.
func snippet(c component) string {
	parts := strings.Split(c.Symbol, ".")
	typ, method := parts[0], parts[len(parts)-1]
	switch c.Lang {
	case "go":
		pkg := path.Base(path.Dir(c.File))
		if len(parts) == 1 {
			return fmt.Sprintf("package %s\n\n// %s reads the settings from disk and the environment.\nfunc %s(dir string) (*Settings, error) {\n\tpath := filepath.Join(dir, \"config.toml\")\n\tdata, err := os.ReadFile(path)\n\tif err != nil {\n\t\treturn nil, fmt.Errorf(\"read %%s: %%w\", path, err)\n\t}\n\treturn parse(data)\n}\n", pkg, method, method)
		}
		return fmt.Sprintf("package %s\n\n// %s keeps the state for one run.\ntype %s struct {\n\tmu    sync.Mutex\n\tlimit int\n\tclock func() time.Time\n}\n\nfunc (x *%s) %s(ctx context.Context) error {\n\tx.mu.Lock()\n\tdefer x.mu.Unlock()\n\tif x.limit == 0 {\n\t\treturn errLimit\n\t}\n\treturn x.step(ctx)\n}\n", pkg, typ, typ, typ, method)
	case "ts":
		if strings.HasSuffix(c.File, ".tsx") {
			return fmt.Sprintf("import { useMemo, useState } from 'react';\n\nexport function %s({ items }: { items: Item[] }) {\n  const [query, setQuery] = useState('');\n  const visible = useMemo(\n    () => items.filter((item) => item.name.includes(query)),\n    [items, query],\n  );\n  return <List items={visible} onQuery={setQuery} />;\n}\n", typ)
		}
		return fmt.Sprintf("export interface Heading {\n  id: string;\n  depth: number;\n  text: string;\n}\n\nexport function %s(markdown: string): Heading[] {\n  const lines = markdown.split('\\n');\n  return lines\n    .filter((line) => line.startsWith('#'))\n    .map(toHeading);\n}\n", typ)
	case "py":
		if len(parts) == 1 {
			// A raw string: after a \n escape the decorator line reads as an
			// email address to the mirror gate's inventory.
			return fmt.Sprintf(`from fastapi import APIRouter, HTTPException

router = APIRouter()


@router.post("/jobs")
def %s(job: JobIn) -> JobOut:
    if job.schedule is None:
        raise HTTPException(422, "schedule is required")
    return store.save(job)
`, method)
		}
		return fmt.Sprintf("from datetime import datetime, timedelta\n\n\nclass %s:\n    \"\"\"Turns schedule rules into concrete run times.\"\"\"\n\n    def __init__(self, rules, tz):\n        self.rules = rules\n        self.tz = tz\n\n    def %s(self, now: datetime, limit: int = 5):\n        runs = []\n        for rule in self.rules:\n            runs.extend(rule.after(now, limit))\n        return sorted(runs)[:limit]\n", typ, method)
	}
	return ""
}

// numbered renders text the way Claude Code's Read tool returns it.
func numbered(text string) string {
	var b strings.Builder
	for i, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, line)
	}
	return b.String()
}

// editPair is a small, language-correct change for an Edit or apply_patch.
func editPair(c component) (string, string) {
	switch c.Lang {
	case "go":
		return "\tif x.limit == 0 {\n\t\treturn errLimit\n\t}", "\tif x.limit <= 0 {\n\t\treturn fmt.Errorf(\"%w: %d\", errLimit, x.limit)\n\t}"
	case "ts":
		return "item.name.includes(query)", "item.name.toLowerCase().includes(query.trim().toLowerCase())"
	case "py":
		return "        for rule in self.rules:", "        for rule in self.rules:\n            if rule.paused:\n                continue"
	}
	return "", ""
}

func testCommand(c component) string {
	switch c.Lang {
	case "go":
		return "go test ./" + path.Dir(c.File) + "/..."
	case "ts":
		return "npx vitest run " + c.Test
	case "py":
		return "pytest " + c.Test + " -q"
	}
	return "make test"
}

func testOutput(p *project, c component, pass bool, testName string) string {
	switch c.Lang {
	case "go":
		pkg := "git.example.com/" + p.Org + "/" + p.Key + "/" + path.Dir(c.File)
		if pass {
			return fmt.Sprintf("ok  \t%s\t0.%03ds", pkg, 120+len(testName)*37%800)
		}
		return fmt.Sprintf("--- FAIL: %s (0.00s)\n    %s:41: got 3 attempts, want 4\nFAIL\nFAIL\t%s\t0.388s\nFAIL", testName, path.Base(c.Test), pkg)
	case "ts":
		if pass {
			return fmt.Sprintf(" PASS  %s (6 tests) 41ms\n\n Test Files  1 passed (1)\n      Tests  6 passed (6)\n   Duration  1.12s", c.Test)
		}
		return fmt.Sprintf(" FAIL  %s > %s > keeps the query when items change\nAssertionError: expected 2 to be 3\n\n Test Files  1 failed (1)\n      Tests  1 failed | 5 passed (6)", c.Test, c.Symbol)
	case "py":
		if pass {
			return "..................                                                       [100%]\n18 passed in 0.42s"
		}
		return fmt.Sprintf("FAILED %s::%s - AssertionError: assert 3 == 4\n1 failed, 17 passed in 0.51s", c.Test, testName)
	}
	return "ok"
}

func grepOutput(c component) string {
	parts := strings.Split(c.Symbol, ".")
	name := parts[len(parts)-1]
	return fmt.Sprintf("%s:12:func %s\n%s:40:\t%s(", c.File, name, c.Test, name)
}
