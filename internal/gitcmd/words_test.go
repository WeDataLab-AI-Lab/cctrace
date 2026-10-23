package gitcmd

import (
	"reflect"
	"testing"
)

func TestCodexCommandReadsOnlyLiteralShellCommands(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, tool, args, want string
		dynamic                bool
	}{
		{"exec_command", "exec_command", `{"cmd":"go test ./...","workdir":"/repo"}`, "go test ./...", false},
		{"shell array", "shell", `{"command":["bash","-lc","make build"]}`, "make build", false},
		{"exec JS", "exec", `await tools.exec_command({cmd: "npm run lint"})`, "npm run lint", false},
		{"dynamic template", "exec", "tools.exec_command({cmd: `npm run ${task}`})", "", true},
		{"exec without a command", "exec", `await tools.apply_patch("*** Begin Patch")`, "", false},
		{"other tool", "apply_patch", `{"cmd":"go test"}`, "", false},
	} {
		got, dynamic := CodexCommand(tc.tool, tc.args)
		if got != tc.want || dynamic != tc.dynamic {
			t.Fatalf("%s: got %q %v, want %q %v", tc.name, got, dynamic, tc.want, tc.dynamic)
		}
	}
}

func TestCommandWordsSplitsSimpleCommandsWithoutExpanding(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		line     string
		want     [][]string
		complete bool
	}{
		{`cd "my dir" && FOO="a b" go test ./... > out.txt`, [][]string{{"cd", "my dir"}, {"go", "test", "./..."}}, true},
		{"env A=1 make build; echo done | tee log", [][]string{{"make", "build"}, {"echo", "done"}, {"tee", "log"}}, true},
		{"cat <<EOF\ngit commit\nEOF\nls -la", [][]string{{"cat"}, {"ls", "-la"}}, true},
		{`echo "$(date)"`, [][]string{{"echo", "$(date)"}}, false},
	} {
		got, complete := CommandWords(tc.line)
		if !reflect.DeepEqual(got, tc.want) || complete != tc.complete {
			t.Fatalf("%q: got %q %v, want %q %v", tc.line, got, complete, tc.want, tc.complete)
		}
	}
}
