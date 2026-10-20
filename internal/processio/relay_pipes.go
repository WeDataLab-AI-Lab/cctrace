package processio

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"time"
)

var interruptGracePeriod = 2 * time.Second

func runPipes(ctx context.Context, options RelayOptions) (RelayResult, error) {
	if err := ctx.Err(); err != nil {
		return RelayResult{}, err
	}
	if err := configurePipesCommand(options.Command); err != nil {
		return RelayResult{}, err
	}
	relayCtx, cancelRelay := context.WithCancel(ctx)
	defer cancelRelay()
	counter := NewCounter(ModePipes)
	tracker := &reportTracker{options: options, mode: ModePipes, counter: counter}
	options.Command.Stdout = relayWriter{dst: options.Stdout, count: counter.Stdout, failed: func(err error) {
		tracker.markFailure(FailureOutputRelay, err)
		cancelRelay()
	}}
	options.Command.Stderr = relayWriter{dst: options.Stderr, count: counter.Stderr, failed: func(err error) {
		tracker.markFailure(FailureOutputRelay, err)
		cancelRelay()
	}}
	childStdin, err := options.Command.StdinPipe()
	if err != nil {
		return RelayResult{}, err
	}
	if err := options.Command.Start(); err != nil {
		_ = childStdin.Close()
		return RelayResult{}, err
	}
	startedAt := time.Now().UTC()
	pid := options.Command.Process.Pid
	tracker.pid = pid
	if options.OnStarted != nil {
		options.OnStarted(pid)
	}
	stopReports := tracker.start()

	inputDone := make(chan error, 1)
	stopInput := make(chan struct{})
	go func() {
		err := relayInterruptibleInput(options.Stdin, childStdin, counter, stopInput)
		if err != nil {
			tracker.markFailure(FailureInputRelay, err)
			cancelRelay()
		}
		inputDone <- err
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- options.Command.Wait() }()
	waitErr := waitForChild(relayCtx, options.Command, waitDone)
	close(stopInput)
	if closer, ok := options.Stdin.(io.Closer); ok {
		_ = closer.Close()
	}
	inputErr := <-inputDone
	if inputErr != nil && !errors.Is(inputErr, io.EOF) {
		tracker.markFailure(FailureInputRelay, inputErr)
	}
	_ = childStdin.Close()
	report, reportErr := stopReports()
	code := processExitCode(options.Command, waitErr)
	err = errors.Join(nonExitWaitError(waitErr), inputErr, reportErr)
	return processResult(pid, startedAt, report, code, err)
}

type relayWriter struct {
	dst    io.Writer
	count  func([]byte)
	failed func(error)
}

func (w relayWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		w.count(p[:n])
	}
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.failed(err)
	}
	return n, err
}

func relayInput(source io.Reader, destination io.WriteCloser, counter *Counter) error {
	defer destination.Close()
	if source == nil {
		return nil
	}
	buffer := make([]byte, 32<<10)
	for {
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

func relayInterruptibleInput(source io.Reader, destination io.WriteCloser, counter *Counter, stop <-chan struct{}) error {
	if file, ok := source.(*os.File); ok {
		return relayPipesFileInput(file, destination, counter, stop)
	}
	return relayInput(source, destination, counter)
}

func writeInput(destination io.Writer, p []byte, counter *Counter) error {
	for len(p) > 0 {
		n, err := destination.Write(p)
		if n > 0 {
			counter.Input(p[:n])
			p = p[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func waitForChild(ctx context.Context, command *exec.Cmd, waitDone <-chan error) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	select {
	case err := <-waitDone:
		return err
	case <-signals:
		if command.Process != nil {
			_ = forwardInterrupt(command)
		}
		return waitAfterInterrupt(command, waitDone)
	case <-ctx.Done():
		if command.Process != nil {
			_ = forwardInterrupt(command)
		}
		return waitAfterInterrupt(command, waitDone)
	}
}

func waitAfterInterrupt(command *exec.Cmd, waitDone <-chan error) error {
	timer := time.NewTimer(interruptGracePeriod)
	defer timer.Stop()
	select {
	case err := <-waitDone:
		return err
	case <-timer.C:
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		return <-waitDone
	}
}

func nonExitWaitError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	return err
}
