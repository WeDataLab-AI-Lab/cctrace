//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris

package processio

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const ptyInputPollMilliseconds = 100

func relayPipesFileInput(source *os.File, destination io.WriteCloser, counter *Counter, stop <-chan struct{}) error {
	defer destination.Close()
	return relayFileInput(source, destination, counter, stop)
}

func CheckPTYSupport(stdin, stdout, stderr *os.File) error {
	if stdin == nil || stdout == nil || stderr == nil ||
		!term.IsTerminal(int(stdin.Fd())) || !term.IsTerminal(int(stdout.Fd())) || !term.IsTerminal(int(stderr.Fd())) {
		return errors.New("PTY process I/O needs one interactive terminal for stdin/stdout/stderr; use --io-mode pipes for explicit non-interactive streams")
	}
	inInfo, inErr := stdin.Stat()
	outInfo, outErr := stdout.Stat()
	errInfo, stderrErr := stderr.Stat()
	if inErr != nil || outErr != nil || stderrErr != nil {
		return errors.New("terminal identity cannot be checked for PTY process I/O")
	}
	if !os.SameFile(inInfo, outInfo) || !os.SameFile(outInfo, errInfo) {
		return errors.New("PTY process I/O needs stdin, stdout, and stderr on the same terminal; use --io-mode pipes for separated streams")
	}
	master, slave, err := pty.Open()
	if err != nil {
		return errors.New("PTY allocation is unavailable; use --io-mode pipes for explicit non-interactive streams")
	}
	_ = master.Close()
	_ = slave.Close()
	if size, err := pty.GetsizeFull(stdout); err != nil {
		return errors.New("terminal size cannot be read for PTY process I/O")
	} else if size.Cols == 0 || size.Rows == 0 {
		return errors.New("terminal dimensions are invalid for PTY process I/O")
	}
	return nil
}

func configurePipesCommand(command *exec.Cmd) error {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
	return nil
}

func forwardInterrupt(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return syscall.Kill(-command.Process.Pid, syscall.SIGINT)
}

func processExitCode(command *exec.Cmd, waitErr error) int {
	if command == nil || command.ProcessState == nil {
		if waitErr != nil {
			return 1
		}
		return 0
	}
	if code := command.ProcessState.ExitCode(); code >= 0 {
		return code
	}
	if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return 1
}

func runPTY(ctx context.Context, options RelayOptions) (RelayResult, error) {
	stdin, ok := options.Stdin.(*os.File)
	if !ok {
		return RelayResult{}, errors.New("PTY process I/O needs a terminal stdin file")
	}
	stdout, ok := options.Stdout.(*os.File)
	if !ok {
		return RelayResult{}, errors.New("PTY process I/O needs a terminal stdout file")
	}
	stderr, ok := options.Stderr.(*os.File)
	if !ok {
		return RelayResult{}, errors.New("PTY process I/O needs a terminal stderr file")
	}
	if err := CheckPTYSupport(stdin, stdout, stderr); err != nil {
		return RelayResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return RelayResult{}, err
	}
	window, err := pty.GetsizeFull(stdout)
	if err != nil {
		return RelayResult{}, errors.New("terminal size cannot be read for PTY process I/O")
	}
	state, err := term.MakeRaw(int(stdin.Fd()))
	if err != nil {
		return RelayResult{}, errors.New("terminal raw mode is unavailable for PTY process I/O")
	}
	ptmx, err := pty.StartWithSize(options.Command, window)
	if err != nil {
		_ = term.Restore(int(stdin.Fd()), state)
		return RelayResult{}, err
	}
	startedAt := time.Now().UTC()
	pid := options.Command.Process.Pid
	counter := NewCounter(ModePTY)
	tracker := &reportTracker{options: options, pid: pid, mode: ModePTY, counter: counter}
	stopResize := watchPTYResize(stdin, ptmx, tracker, options.OnResize)
	if options.OnStarted != nil {
		options.OnStarted(pid)
	}
	stopReports := tracker.start()
	relayCtx, cancelRelay := context.WithCancel(ctx)
	defer cancelRelay()

	stopInput := make(chan struct{})
	inputDone := make(chan error, 1)
	go func() {
		err := relayFileInput(stdin, ptmx, counter, stopInput)
		if err != nil {
			tracker.markFailure(FailureInputRelay, err)
			cancelRelay()
		}
		inputDone <- err
	}()
	outputDone := make(chan error, 1)
	go func() {
		outputDone <- relayOutput(ptmx, stdout, counter.Output, func(err error) {
			tracker.markFailure(FailureOutputRelay, err)
			cancelRelay()
		}, func(err error) bool { return errors.Is(err, syscall.EIO) || errors.Is(err, io.EOF) })
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- options.Command.Wait() }()
	waitErr := waitForChild(relayCtx, options.Command, waitDone)
	outputErr := <-outputDone
	stopResize()
	close(stopInput)
	inputErr := <-inputDone
	if err := term.Restore(int(stdin.Fd()), state); err != nil {
		tracker.markFailure("terminal_restore_failed", err)
	}
	_ = ptmx.Close()
	if outputErr != nil {
		tracker.markFailure(FailureOutputRelay, outputErr)
	}
	if inputErr != nil && !errors.Is(inputErr, io.EOF) {
		tracker.markFailure(FailureInputRelay, inputErr)
	}
	report, reportErr := stopReports()
	code := processExitCode(options.Command, waitErr)
	err = errors.Join(nonExitWaitError(waitErr), reportErr)
	return processResult(pid, startedAt, report, code, err)
}

func relayFileInput(terminal *os.File, destination io.WriteCloser, counter *Counter, stop <-chan struct{}) error {
	buffer := make([]byte, 32<<10)
	fds := []unix.PollFd{{Fd: int32(terminal.Fd()), Events: unix.POLLIN}}
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		ready, err := unix.Poll(fds, ptyInputPollMilliseconds)
		if err != nil {
			select {
			case <-stop:
				return nil
			default:
			}
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return err
		}
		if ready == 0 {
			continue
		}
		select {
		case <-stop:
			return nil
		default:
		}
		n, readErr := unix.Read(int(terminal.Fd()), buffer)
		if n > 0 {
			if err := writeInput(destination, buffer[:n], counter); err != nil {
				return err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
		if n == 0 {
			return nil
		}
	}
}

func watchPTYResize(terminal, ptmx *os.File, tracker *reportTracker, onResize func()) func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-signals:
				if err := pty.InheritSize(terminal, ptmx); err != nil {
					tracker.markFailure(FailureResize, err)
				} else if onResize != nil {
					onResize()
				}
			case <-stop:
				return
			}
		}
	}()
	return func() {
		signal.Stop(signals)
		close(stop)
		<-done
	}
}
