package gitcmd

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestLiteralCommitMessages(t *testing.T) {
	for _, tc := range []struct {
		command, message string
		known            bool
	}{
		{`git commit -m 'first' -m 'body'`, "first", true},
		{"git commit --message='first\n\nbody'", "first", true},
		{"git commit -m 'first  \nsecond\t\n \t\nbody'", "first second", true},
		{`git commit -am "change"`, "change", true},
		{`git commit -amchange`, "change", true},
		{`git commit -mchange`, "change", true},
		{`git commit -m "$MESSAGE"`, "", false},
		{`git commit -F file`, "", false},
		{`git commit --amend --no-edit`, "", false},
	} {
		got := ParseWithMessages(tc.command, "/work")
		if len(got.Git) != 1 || got.Git[0].Message != tc.message || got.Git[0].MessageKnown != tc.known {
			t.Fatalf("%q: %+v", tc.command, got)
		}
	}
}

func TestDynamicCommitMessagesDoNotHideExecution(t *testing.T) {
	for _, option := range []string{`-m "$MESSAGE"`, `--message "$MESSAGE"`, `--message="$MESSAGE"`, `-m"$MESSAGE"`, `-am "$MESSAGE"`, `-am"$MESSAGE"`} {
		command := "git commit " + option
		want := Parse(`git commit -m literal`, "/work")
		got := ParseWithMessages(command, "/work")
		if got.Incomplete || len(got.Git) != 1 || got.Git[0].MessageKnown || got.Git[0].Message != "" {
			t.Fatalf("message changed execution evidence: %q %+v", command, got)
		}
		client := Parse(command, "/work")
		if !reflect.DeepEqual(client, want) {
			t.Fatalf("client state differs: %q %+v", command, client)
		}
		wire, err := json.Marshal(client)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(wire), "MESSAGE") || strings.Contains(fmt.Sprintf("%+v", client), "MESSAGE") {
			t.Fatal("dynamic message leaked")
		}
		var restored Commands
		if err := json.Unmarshal(wire, &restored); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(restored, client) {
			t.Fatal("client state did not round trip")
		}
	}
	for _, option := range []string{"-m \"$(echo title)\"", "--message=\"$(echo title)\"", "-m \"`echo title`\"", "--message=\"`echo title`\""} {
		for _, parse := range []func(string, string) Commands{Parse, ParseWithMessages} {
			if got := parse("git commit "+option, "/work"); !got.Incomplete {
				t.Fatalf("command substitution became complete: %q %+v", option, got)
			}
		}
	}
}

func TestCommitMessagesStayOutOfCallState(t *testing.T) {
	const secret = "private-commit-message-fixture"
	command := "git commit -m '" + secret + "'"
	client := Parse(command, "/work")
	if len(client.Git) != 1 || client.Git[0].Message != "" || client.Git[0].MessageKnown || strings.Contains(fmt.Sprintf("%+v", client), secret) {
		t.Fatal("client parse retained message for queue/logging")
	}
	transient := ParseWithMessages(command, "/work")
	if len(transient.Git) != 1 || transient.Git[0].Message != secret {
		t.Fatal("transient evidence missing")
	}
	for _, ops := range [][]Operation{client.Git, transient.Git} {
		state := struct {
			Ops []Operation `json:"ops"`
		}{ops}
		wire, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(wire), secret) || strings.Contains(string(wire), "Message") || strings.Contains(string(wire), "message") {
			t.Fatal("message serialized through call state")
		}
		var restored struct {
			Ops []Operation `json:"ops"`
		}
		if err := json.Unmarshal(wire, &restored); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(restored.Ops, client.Git) {
			t.Fatal("call state no longer round trips")
		}
	}
}

func TestParseCommandEvidence(t *testing.T) {
	cases := []struct {
		name, command string
		kinds, dirs   []string
		incomplete    bool
	}{
		{"commit", "git commit -m 'git commit --amend'", []string{"commit"}, []string{"/work"}, false},
		{"quote", `echo "git commit"; printf '%s' 'git merge x'`, nil, nil, false},
		{"comment", "# git commit\ngit status # git merge x", nil, nil, false},
		{"heredoc", "cat <<'EOF'\ngit commit\nEOF\ngit commit -m ok", []string{"commit"}, []string{"/work"}, false},
		{"message heredoc", "git commit -F - <<'EOF'\ngit merge not-a-command\nEOF", []string{"commit"}, []string{"/work"}, false},
		{"literal cd", `cd 'other dir' && git commit -m ok`, []string{"commit"}, []string{"/work/other dir"}, false},
		{"cd chain join", `cd /other && false; git commit`, []string{"commit"}, []string{""}, false},
		{"cd status join", `cd /other && git status; git commit`, []string{"commit"}, []string{""}, false},
		{"cd long chain", `cd /other && true && git commit`, []string{"commit"}, []string{"/other"}, false},
		{"quoted substitution", `echo "$(git commit -m x)"`, nil, nil, true},
		{"quoted backtick", "echo \"`git commit -m x`\"", nil, nil, true},
		{"dynamic absolute C", `git -C"/tmp/$TREE" commit -m x`, []string{"commit"}, []string{""}, false},
		{"unchecked cd", `cd elsewhere; git commit`, []string{"commit"}, []string{""}, false},
		{"dynamic cd", `cd "$PROJECT" && git commit`, []string{"commit"}, []string{""}, false},
		{"glob cd", `cd work* && git commit`, []string{"commit"}, []string{""}, false},
		{"repeated C", `git -C /other -C child commit`, []string{"commit"}, []string{"/other/child"}, false},
		{"dynamic C", `git -C"$PROJECT" commit`, []string{"commit"}, []string{""}, false},
		{"subshell", `(cd /other && git commit); git commit`, []string{"commit", "commit"}, []string{"/other", "/work"}, false},
		{"substitution", `echo $(git commit); git commit`, []string{"commit"}, []string{"/work"}, true},
		{"backticks", "echo `printf x; git commit`; git commit", []string{"commit"}, []string{"/work"}, true},
		{"redirect", `git commit >git-commit-output; echo done`, []string{"commit"}, []string{"/work"}, false},
		{"pull rebase", `git pull --rebase; git rebase main`, nil, nil, false},
		{"merge chain", `git merge branch && git cherry-pick abcdefg; git revert abcdefg; git pull`, []string{"merge", "cherry_pick", "revert", "pull"}, []string{"/work", "/work", "/work", "/work"}, false},
		{"dry run", `git commit --dry-run; git merge --abort`, nil, nil, false},
		{"message flag", `git commit -m '--dry-run'`, []string{"commit"}, []string{"/work"}, false},
		{"env tree", `GIT_WORK_TREE=/other git commit`, []string{"commit"}, []string{""}, true},
		{"env wrapper", `env GIT_DIR=/other/.git git commit`, []string{"commit"}, []string{""}, true},
		{"conditional cwd", `if ok; then cd /other; fi; git commit`, []string{"commit"}, []string{""}, true},
		{"shell string", `sh -c 'git commit'`, nil, nil, true},
		{"function", `f() { git commit; }; f`, nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(tc.command, "/work")
			var kinds, dirs []string
			for _, op := range got.Git {
				kinds = append(kinds, op.Kind)
				dir := ""
				if op.DirKnown {
					dir = op.Dir
				}
				dirs = append(dirs, dir)
			}
			if !reflect.DeepEqual(kinds, tc.kinds) || !reflect.DeepEqual(dirs, tc.dirs) || got.Incomplete != tc.incomplete {
				t.Fatalf("got %+v; want kinds=%v dirs=%v incomplete=%v", got, tc.kinds, tc.dirs, tc.incomplete)
			}
		})
	}
}

func TestParseAmendBackgroundGitHubAndBounds(t *testing.T) {
	got := Parse(`git commit --amend -m ok & gh -R owner/repo pr merge 12 --auto`, "/work")
	if len(got.Git) != 1 || !got.Git[0].Amend || !got.Git[0].Background {
		t.Fatalf("git: %+v", got)
	}
	if len(got.GitHub) != 1 || got.GitHub[0] != (GitHubCall{Noun: "pr", Verb: "merge", Target: "12", Repository: "owner/repo", Auto: true}) {
		t.Fatalf("gh: %+v", got)
	}
	for _, command := range []string{`gh pr merge 12 --squash --auto`, `gh pr merge --squash --auto 12`} {
		parsed := Parse(command, "/work")
		if len(parsed.GitHub) != 1 || !parsed.GitHub[0].Auto || parsed.GitHub[0].Target != "12" {
			t.Fatalf("merge flags: %+v", parsed)
		}
	}
	if !Parse(strings.Repeat("x", MaxCommandBytes+1), "/work").Incomplete {
		t.Fatal("oversize treated as complete")
	}
	if Parse("git commit", "").Git[0].DirKnown {
		t.Fatal("missing cwd treated as known")
	}
}

func TestOutputRequiresCommitReceipt(t *testing.T) {
	hash := strings.Repeat("a", 64)
	got := ReadOutput("unrelated deadbeef\n[main abcdef1] private subject\n[main abcdef1] duplicate\n[main (root-commit) " + hash + "] subject\nnothing to commit, working tree clean\n")
	if !reflect.DeepEqual(got.Hashes, []string{"abcdef1", hash}) || !got.NothingToCommit {
		t.Fatalf("output: %+v", got)
	}
	if len(ReadOutput("deadbeef\ncommit abcdef12345\nremote: [main abcdef1] other").Hashes) != 0 {
		t.Fatal("non-receipt hash retained")
	}
	if !ReadOutput(strings.Repeat("x", MaxOutputBytes+1)).Incomplete {
		t.Fatal("oversize output treated as complete")
	}
}
