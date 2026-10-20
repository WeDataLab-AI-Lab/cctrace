package gitcmd

import "testing"

func TestParseCodexShellCommandsAndLiterals(t *testing.T) {
	cases := []struct {
		name, tool, args               string
		wantKind, wantDir, wantMessage string
		incomplete                     bool
	}{
		{"exec_command", "exec_command", `{"cmd":"git commit -m 'ship'","workdir":"/repo"}`, "commit", "/repo", "ship", false},
		{"shell array", "shell", `{"command":["bash","-lc","git commit -m 'array'"]}`, "commit", "/repo", "array", false},
		{"exec JS", "exec", `const r = await tools.exec_command({cmd: ` + "`git commit -m 'template'`" + `, workdir: "/repo"});`, "commit", "/repo", "template", false},
		{"dynamic template", "exec", "tools.exec_command({cmd: `git commit ${message}`})", "", "", "", true},
		{"command text in string", "exec", `console.log("cmd: git commit -m fake")`, "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseCodex(tc.tool, tc.args, "/repo")
			if got.Incomplete != tc.incomplete {
				t.Fatalf("incomplete=%v, want %v (%+v)", got.Incomplete, tc.incomplete, got)
			}
			if tc.wantKind == "" {
				if len(got.Git) != 0 {
					t.Fatalf("unexpected git calls: %+v", got.Git)
				}
				return
			}
			if len(got.Git) != 1 || got.Git[0].Kind != tc.wantKind || got.Git[0].Dir != tc.wantDir || got.Git[0].Message != tc.wantMessage || !got.Git[0].MessageKnown {
				t.Fatalf("parsed=%+v", got)
			}
		})
	}
}
