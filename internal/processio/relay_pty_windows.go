//go:build windows

package processio

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

var conPTYResizePollInterval = 150 * time.Millisecond

const waitTimeout = 258

func relayPipesFileInput(source *os.File, destination io.WriteCloser, counter *Counter, stop <-chan struct{}) error {
	defer destination.Close()
	return relayFileInput(source, destination, counter, stop)
}

func CheckPTYSupport(stdin, stdout, stderr *os.File) error {
	if !conpty.IsConPtyAvailable() {
		return errors.New("Windows ConPTY is unavailable; this terminal or Windows version cannot run --observe-process-io")
	}
	if stdin == nil || stdout == nil || stderr == nil ||
		!term.IsTerminal(int(stdin.Fd())) || !term.IsTerminal(int(stdout.Fd())) || !term.IsTerminal(int(stderr.Fd())) {
		return errors.New("ConPTY process I/O needs one interactive terminal for stdin/stdout/stderr; use --io-mode pipes for explicit non-interactive streams")
	}
	// Windows exposes console input and output as different handles even when
	// they belong to one interactive console. Requiring os.SameFile here rejects
	// normal ConPTY-capable terminals; redirection is already rejected above.
	width, height, err := term.GetSize(int(stdout.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		return errors.New("terminal dimensions are invalid for ConPTY process I/O")
	}
	return nil
}

func configurePipesCommand(command *exec.Cmd) error {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &windows.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	return nil
}

func forwardInterrupt(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(command.Process.Pid))
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
	return 1
}

func relayFileInput(source *os.File, destination io.WriteCloser, counter *Counter, stop <-chan struct{}) error {
	fileType, err := windows.GetFileType(windows.Handle(source.Fd()))
	if err != nil || fileType != windows.FILE_TYPE_CHAR {
		return relayInput(source, destination, counter)
	}
	buffer := make([]byte, 32<<10)
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		ready, err := windows.WaitForSingleObject(windows.Handle(source.Fd()), 100)
		if err != nil {
			select {
			case <-stop:
				return nil
			default:
			}
			return err
		}
		if ready == waitTimeout {
			continue
		}
		if ready != windows.WAIT_OBJECT_0 {
			return errors.New("console input wait failed")
		}
		n, readErr := source.Read(buffer)
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
			return io.ErrNoProgress
		}
	}
}

func runPTY(ctx context.Context, options RelayOptions) (RelayResult, error) {
	stdin, ok := options.Stdin.(*os.File)
	if !ok {
		return RelayResult{}, errors.New("ConPTY process I/O needs a terminal stdin file")
	}
	stdout, ok := options.Stdout.(*os.File)
	if !ok {
		return RelayResult{}, errors.New("ConPTY process I/O needs a terminal stdout file")
	}
	stderr, ok := options.Stderr.(*os.File)
	if !ok {
		return RelayResult{}, errors.New("ConPTY process I/O needs a terminal stderr file")
	}
	if err := CheckPTYSupport(stdin, stdout, stderr); err != nil {
		return RelayResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return RelayResult{}, err
	}
	width, height, err := term.GetSize(int(stdout.Fd()))
	if err != nil {
		return RelayResult{}, errors.New("terminal size cannot be read for ConPTY process I/O")
	}
	state, err := term.MakeRaw(int(stdin.Fd()))
	if err != nil {
		return RelayResult{}, errors.New("terminal raw mode is unavailable for ConPTY process I/O")
	}
	commandLine := windows.ComposeCommandLine(options.Command.Args)
	pty, err := conpty.Start(commandLine,
		conpty.ConPtyDimensions(width, height),
		conpty.ConPtyWorkDir(options.Command.Dir),
		conpty.ConPtyEnv(options.Command.Env))
	if err != nil {
		_ = term.Restore(int(stdin.Fd()), state)
		return RelayResult{}, errors.New("Windows ConPTY could not start the child process")
	}
	startedAt := time.Now().UTC()
	pid := pty.Pid()
	counter := NewCounter(ModePTY)
	tracker := &reportTracker{options: options, pid: pid, mode: ModePTY, counter: counter}
	stopResize := watchConPTYResize(stdout, pty, width, height, tracker, options.OnResize)
	if options.OnStarted != nil {
		options.OnStarted(pid)
	}
	stopReports := tracker.start()
	relayCtx, cancelRelay := context.WithCancel(ctx)
	defer cancelRelay()

	stopInput := make(chan struct{})
	inputDone := make(chan error, 1)
	go func() {
		err := relayFileInput(stdin, pty, counter, stopInput)
		if err != nil {
			tracker.markFailure(FailureInputRelay, err)
			cancelRelay()
		}
		inputDone <- err
	}()
	outputDone := make(chan error, 1)
	go func() {
		outputDone <- relayOutput(pty, stdout, counter.Output, func(err error) {
			tracker.markFailure(FailureOutputRelay, err)
			cancelRelay()
		}, func(err error) bool {
			return errors.Is(err, io.EOF) || errors.Is(err, windows.ERROR_BROKEN_PIPE)
		})
	}()
	exitCode, waitErr := waitConPTY(relayCtx, pty)
	outputErr := <-outputDone
	stopResize()
	close(stopInput)
	inputErr := <-inputDone
	if err := term.Restore(int(stdin.Fd()), state); err != nil {
		tracker.markFailure("terminal_restore_failed", err)
	}
	_ = pty.Close()
	if outputErr != nil {
		tracker.markFailure(FailureOutputRelay, outputErr)
	}
	if inputErr != nil && !errors.Is(inputErr, io.EOF) {
		tracker.markFailure(FailureInputRelay, inputErr)
	}
	report, reportErr := stopReports()
	return processResult(pid, startedAt, report, exitCode, errors.Join(waitErr, reportErr))
}

func waitConPTY(ctx context.Context, process *conpty.ConPty) (int, error) {
	type waitResult struct {
		code uint32
		err  error
	}
	waitDone := make(chan waitResult, 1)
	go func() {
		code, err := process.Wait(context.Background())
		waitDone <- waitResult{code: code, err: err}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	select {
	case result := <-waitDone:
		return int(result.code), result.err
	case <-signals:
		_, _ = process.Write([]byte{3})
	case <-ctx.Done():
		_, _ = process.Write([]byte{3})
	}
	timer := time.NewTimer(interruptGracePeriod)
	defer timer.Stop()
	select {
	case result := <-waitDone:
		return int(result.code), result.err
	case <-timer.C:
		_ = process.Close()
		result := <-waitDone
		if result.err != nil {
			return int(result.code), result.err
		}
		return int(result.code), errors.New("ConPTY child did not stop after interrupt")
	}
}

func watchConPTYResize(terminal *os.File, process *conpty.ConPty, width, height int, tracker *reportTracker, onResize func()) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(conPTYResizePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				newWidth, newHeight, err := term.GetSize(int(terminal.Fd()))
				if err != nil {
					tracker.markFailure(FailureResize, err)
					continue
				}
				if newWidth == width && newHeight == height {
					continue
				}
				if err := process.Resize(newWidth, newHeight); err != nil {
					tracker.markFailure(FailureResize, err)
					continue
				}
				width, height = newWidth, newHeight
				if onResize != nil {
					onResize()
				}
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}
