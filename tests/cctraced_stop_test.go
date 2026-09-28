package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// buildCctracedBinary builds the server binary under test. The client binary
// buildBinary returns is a different command, and no test here needs a database.
func buildCctracedBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cctraced")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/cctraced")
	cmd.Dir = rootDir(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build cctraced: %v\n%s", err, out)
	}
	return bin
}

// cctracedCommandEnv points the binary at a throwaway home so a test can never
// read or write the developer's real ~/.cctrace.
func cctracedCommandEnv(fakeHome string) []string {
	blocked := map[string]struct{}{
		"CCTRACE_ALLOW_EMPTY_DASHBOARD": {},
		"DATABASE_URL":                  {},
		"HOME":                          {},
		"JWT_SECRET":                    {},
		"USERPROFILE":                   {},
	}
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, found := blocked[key]; !found {
			env = append(env, entry)
		}
	}
	return append(env, "HOME="+fakeHome, "USERPROFILE="+fakeHome)
}

// Asking a machine that has never run a daemon to stop one -- or just asking
// the binary what it takes -- must succeed and leave no state behind.
func TestCctracedInformationalCommandsCreateNoState(t *testing.T) {
	bin := buildCctracedBinary(t)

	for _, arg := range []string{"--help", "--version", "--stop"} {
		t.Run(arg, func(t *testing.T) {
			fakeHome := t.TempDir()
			cmd := exec.Command(bin, arg)
			cmd.Env = cctracedCommandEnv(fakeHome)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("cctraced %s: %v\n%s", arg, err, output)
			}
			if _, err := os.Stat(filepath.Join(fakeHome, ".cctrace")); !os.IsNotExist(err) {
				t.Fatalf("cctraced %s created ~/.cctrace: %v", arg, err)
			}
		})
	}
}

// The defect issue #525 reports, end to end: a pid file left behind by a failed
// start names a pid the OS has since handed to something else. The lock is free,
// so nothing is signalled -- the process that pid now names keeps running -- and
// the stale file is cleared.
func TestCctracedStopDoesNotSignalAnUnrelatedProcess(t *testing.T) {
	bin := buildCctracedBinary(t)
	fakeHome := t.TempDir()

	helper := exec.Command(os.Args[0], "-test.run=TestHelperProcessOutlivesStop")
	helper.Env = append(os.Environ(), "CCTRACED_STOP_HELPER=1")
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	exited := make(chan struct{})
	go func() {
		_ = helper.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = helper.Process.Kill()
		<-exited
	})

	// A lock file that exists but is held by nobody is what a crashed or
	// log.Fatalf'ed daemon leaves behind, next to its pid file.
	cctraceDir := filepath.Join(fakeHome, ".cctrace")
	if err := os.MkdirAll(cctraceDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cctraceDir, "cctraced.lock"), nil, 0644); err != nil {
		t.Fatalf("write lock file: %v", err)
	}
	pidFile := filepath.Join(cctraceDir, "cctraced.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(helper.Process.Pid)), 0644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	cmd := exec.Command(bin, "--stop")
	cmd.Env = cctracedCommandEnv(fakeHome)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cctraced --stop: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "No running cctraced") {
		t.Fatalf("cctraced --stop said %q, want it to report nothing running", output)
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("stale pid file survived --stop: %v", err)
	}

	select {
	case <-exited:
		t.Fatal("cctraced --stop terminated the unrelated process named by the stale pid file")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestHelperProcessOutlivesStop is not a test; it is the unrelated process
// TestCctracedStopDoesNotSignalAnUnrelatedProcess borrows a pid from.
func TestHelperProcessOutlivesStop(t *testing.T) {
	if os.Getenv("CCTRACED_STOP_HELPER") != "1" {
		t.Skip("helper process body")
	}
	// The parent kills this in its cleanup; the sleep only bounds a leak.
	time.Sleep(60 * time.Second)
}
