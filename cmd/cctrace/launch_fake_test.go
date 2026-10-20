package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeCodexProgram is the Windows stand-in for the shell scripts the launch tests
// use: `--version` prints the version the test chose, and any other call records
// what the launch handed the agent where the test asked for it.
const fakeCodexProgram = `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(os.Getenv("CCTRACE_TEST_CODEX_VERSION"))
		return
	}
	if path := os.Getenv("CCTRACE_TEST_LAUNCH_CAPTURE"); path != "" {
		out := os.Getenv("CODEX_HOME") + "\n"
		for _, arg := range os.Args[1:] {
			out += arg + "\n"
		}
		_ = os.WriteFile(path, []byte(out), 0o600)
	}
	if path := os.Getenv("CCTRACE_TEST_LAUNCH_MARKER"); path != "" {
		_ = os.WriteFile(path, []byte("launched"), 0o600)
	}
}
`

// writeFakeCodex puts a stand-in for the codex CLI in binDir. Unix runs unixScript
// as it is. Windows cannot execute a script named codex, so there it is a small
// program built into codex.exe, which finds the version in the environment.
func writeFakeCodex(t *testing.T, binDir, version, unixScript string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(unixScript), 0700); err != nil {
			t.Fatal(err)
		}
		return
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "main.go"), []byte(fakeCodexProgram), 0600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(binDir, "codex.exe"), "main.go")
	build.Dir = source
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake codex: %v %s", err, output)
	}
	t.Setenv("CCTRACE_TEST_CODEX_VERSION", version)
}
