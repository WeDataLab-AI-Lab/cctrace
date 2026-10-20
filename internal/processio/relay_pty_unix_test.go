//go:build darwin || linux

package processio

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestPTYRelayFixtureProcess(t *testing.T) {
	mode := os.Getenv("CCTRACE_PROCESSIO_PTY_FIXTURE")
	if mode == "" {
		return
	}
	if mode == "interrupt" {
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, os.Interrupt)
		defer signal.Stop(interrupts)
		fmt.Fprintln(os.Stdout, "READY")
		<-interrupts
		os.Exit(130)
	}
	fmt.Fprintln(os.Stdout, "READY")
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			os.Exit(91)
		}
		switch strings.TrimSpace(line) {
		case "size":
			width, height, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil {
				os.Exit(92)
			}
			fmt.Fprintf(os.Stdout, "SIZE %d %d\n", width, height)
		case "exit":
			fmt.Fprintln(os.Stdout, "EXITING")
			os.Exit(23)
		}
	}
}

func TestRunPTYForwardsInputResizeExitAndStartsFreshOnRestart(t *testing.T) {
	first, firstOutput, firstResult := startPTYFixture(t, "interactive")
	waitForPTYText(t, firstOutput, "READY")
	if err := pty.Setsize(first.slave, &pty.Winsize{Cols: 113, Rows: 37}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, first.resizeSeen)
	if _, err := first.master.Write([]byte("size\n")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, firstOutput, "SIZE 113 37")
	if _, err := first.master.Write([]byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	waitForPTYText(t, firstOutput, "EXITING")
	firstOutcome := awaitRelayResult(t, firstResult)
	var firstExit *ExitError
	if !errors.As(firstOutcome.err, &firstExit) || firstExit.ExitCode() != 23 || firstOutcome.result.ExitCode != 23 {
		t.Fatalf("first PTY outcome = code %d, err %v; want exit code 23", firstOutcome.result.ExitCode, firstOutcome.err)
	}
	if firstOutcome.result.Counts.InputBytes != int64(len("size\nexit\n")) {
		t.Errorf("first input bytes = %d, want %d", firstOutcome.result.Counts.InputBytes, len("size\nexit\n"))
	}
	if firstOutcome.result.Counts.OutputBytes == 0 || firstOutcome.result.Counts.StdoutBytes != nil || firstOutcome.result.Counts.StderrBytes != nil {
		t.Errorf("PTY output counters = %+v; want merged output only", firstOutcome.result.Counts)
	}

	second, secondOutput, secondResult := startPTYFixture(t, "interactive")
	waitForPTYText(t, secondOutput, "READY")
	if _, err := second.master.Write([]byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	secondOutcome := awaitRelayResult(t, secondResult)
	if !secondOutcome.result.StartedAt.After(firstOutcome.result.StartedAt) {
		t.Errorf("restart start time %s does not follow first start %s", secondOutcome.result.StartedAt, firstOutcome.result.StartedAt)
	}
	if secondOutcome.result.Counts.InputBytes != int64(len("exit\n")) {
		t.Errorf("restart input bytes = %d, want a fresh counter of %d", secondOutcome.result.Counts.InputBytes, len("exit\n"))
	}
}

func TestRunPTYForwardsCtrlCAndPreservesSignalExitCode(t *testing.T) {
	fixture, output, result := startPTYFixture(t, "interrupt")
	waitForPTYText(t, output, "READY")
	if _, err := fixture.master.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	outcome := awaitRelayResult(t, result)
	if outcome.result.ExitCode != 130 {
		t.Fatalf("PTY Ctrl-C exit code = %d, want 130 (err %v)", outcome.result.ExitCode, outcome.err)
	}
	if outcome.result.Counts.InputBytes != 1 {
		t.Errorf("PTY Ctrl-C input bytes = %d, want 1", outcome.result.Counts.InputBytes)
	}
}

type ptyFixture struct {
	master     *os.File
	slave      *os.File
	pid        int
	resizeSeen <-chan struct{}
}

type relayOutcome struct {
	result RelayResult
	err    error
}

func startPTYFixture(t *testing.T, mode string) (ptyFixture, <-chan []byte, <-chan relayOutcome) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("open outer PTY: %v", err)
	}
	t.Cleanup(func() {
		_ = master.Close()
		_ = slave.Close()
	})
	if err := pty.Setsize(master, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
		t.Fatalf("set initial outer PTY size: %v", err)
	}
	output := make(chan []byte, 32)
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, readErr := master.Read(buffer)
			if n > 0 {
				chunk := append([]byte(nil), buffer[:n]...)
				select {
				case output <- chunk:
				default:
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	command := exec.Command(os.Args[0], "-test.run=^TestPTYRelayFixtureProcess$")
	command.Env = append(os.Environ(), "CCTRACE_PROCESSIO_PTY_FIXTURE="+mode)
	resizeSeen := make(chan struct{}, 1)
	started := make(chan int, 1)
	finished := make(chan relayOutcome, 1)
	go func() {
		result, err := Run(context.Background(), RelayOptions{
			Mode: ModePTY, Command: command, Stdin: slave, Stdout: slave, Stderr: slave,
			ReportInterval: time.Hour,
			OnStarted:      func(pid int) { started <- pid },
			OnResize:       func() { resizeSeen <- struct{}{} },
		})
		finished <- relayOutcome{result: result, err: err}
	}()
	var pid int
	select {
	case pid = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("PTY child did not start")
	}
	return ptyFixture{master: master, slave: slave, pid: pid, resizeSeen: resizeSeen}, output, finished
}

func waitForPTYText(t *testing.T, output <-chan []byte, text string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	var seen bytes.Buffer
	for {
		if strings.Contains(seen.String(), text) {
			return
		}
		select {
		case chunk := <-output:
			_, _ = seen.Write(chunk)
		case <-deadline.C:
			t.Fatalf("PTY output never contained %q; received %q", text, seen.String())
		}
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("PTY resize was not applied")
	}
}

func awaitRelayResult(t *testing.T, result <-chan relayOutcome) relayOutcome {
	t.Helper()
	select {
	case outcome := <-result:
		return outcome
	case <-time.After(3 * time.Second):
		t.Fatal("PTY child did not exit")
		return relayOutcome{}
	}
}
