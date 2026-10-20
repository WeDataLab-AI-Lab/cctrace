package processio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

const defaultReportInterval = 10 * time.Second

const (
	FailureInputRelay  = "input_relay_failed"
	FailureOutputRelay = "output_relay_failed"
	FailureUpload      = "upload_failed"
	FailureResize      = "resize_failed"
	FailureMapping     = "session_mapping_unavailable"
)

type Report struct {
	AsOf        time.Time
	Mode        Mode
	Counts      Snapshot
	Partial     bool
	FailureCode string
	Final       bool
}

type RelayOptions struct {
	Mode           Mode
	Command        *exec.Cmd
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	ReportInterval time.Duration
	OnStarted      func(pid int)
	OnResize       func()
	OnSnapshot     func(pid int, report Report) error
}

type RelayResult struct {
	PID         int
	ExitCode    int
	StartedAt   time.Time
	AsOf        time.Time
	Counts      Snapshot
	Partial     bool
	FailureCode string
}

type ExitError struct{ code int }

func (e *ExitError) Error() string { return fmt.Sprintf("child exited with code %d", e.code) }
func (e *ExitError) ExitCode() int { return e.code }

func Run(ctx context.Context, options RelayOptions) (RelayResult, error) {
	if options.Command == nil {
		return RelayResult{}, errors.New("process I/O relay needs a command")
	}
	if options.Stdin == nil {
		options.Stdin = os.Stdin
	}
	if options.Stdout == nil {
		options.Stdout = os.Stdout
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	if options.ReportInterval <= 0 {
		options.ReportInterval = defaultReportInterval
	}
	switch options.Mode {
	case ModePipes:
		return runPipes(ctx, options)
	case ModePTY:
		return runPTY(ctx, options)
	default:
		return RelayResult{}, fmt.Errorf("unsupported process I/O mode %q", options.Mode)
	}
}

type reportTracker struct {
	mu             sync.Mutex
	options        RelayOptions
	pid            int
	mode           Mode
	counter        *Counter
	partial        bool
	failure        string
	operationalErr error
	uploadErr      error
}

func (r *reportTracker) markFailure(code string, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, ErrSessionMappingUnavailable) {
		code = FailureMapping
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = true
	r.failure = code
	r.operationalErr = errors.Join(r.operationalErr, err)
}

func (r *reportTracker) markUploadFailure(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = true
	r.failure = FailureUpload
	if errors.Is(err, ErrSessionMappingUnavailable) {
		r.failure = FailureMapping
	}
	r.uploadErr = errors.Join(r.uploadErr, err)
}

func (r *reportTracker) emit(final bool) Report {
	report := Report{
		AsOf: time.Now().UTC(), Mode: r.mode, Counts: r.counter.Snapshot(), Final: final,
	}
	r.mu.Lock()
	report.Partial, report.FailureCode = r.partial, r.failure
	r.mu.Unlock()
	if r.options.OnSnapshot != nil {
		if err := r.options.OnSnapshot(r.pid, report); err != nil {
			r.markUploadFailure(err)
		} else if final {
			r.mu.Lock()
			r.uploadErr = nil
			r.mu.Unlock()
		}
	}
	r.mu.Lock()
	report.Partial, report.FailureCode = r.partial, r.failure
	r.mu.Unlock()
	return report
}

func (r *reportTracker) start() (stop func() (Report, error)) {
	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(r.options.ReportInterval)
		defer ticker.Stop()
		r.emit(false)
		for {
			select {
			case <-ticker.C:
				r.emit(false)
			case <-stopped:
				return
			}
		}
	}()
	return func() (Report, error) {
		close(stopped)
		<-done
		report := r.emit(true)
		r.mu.Lock()
		err := errors.Join(r.operationalErr, r.uploadErr)
		r.mu.Unlock()
		return report, err
	}
}

func processResult(pid int, startedAt time.Time, report Report, exitCode int, err error) (RelayResult, error) {
	result := RelayResult{
		PID: pid, ExitCode: exitCode, StartedAt: startedAt, AsOf: report.AsOf,
		Counts: report.Counts, Partial: report.Partial, FailureCode: report.FailureCode,
	}
	if exitCode != 0 {
		err = errors.Join(err, &ExitError{code: exitCode})
	}
	return result, err
}
