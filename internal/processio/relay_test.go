package processio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestPipeRelayFixtureProcess(t *testing.T) {
	if os.Getenv("CCTRACE_PROCESSIO_FIXTURE") != "1" {
		return
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(90)
	}
	_, _ = os.Stdout.Write(input)
	_, _ = os.Stderr.Write([]byte("\x1b[31merr\x1b[0m\r"))
	os.Exit(7)
}

func TestRunPipesForwardsSeparateStreamsAndPreservesExitCode(t *testing.T) {
	input := []byte("user\x00\r\n")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPipeRelayFixtureProcess$")
	cmd.Env = append(os.Environ(), "CCTRACE_PROCESSIO_FIXTURE=1")
	var stdout, stderr bytes.Buffer
	result, err := Run(context.Background(), RelayOptions{
		Mode: ModePipes, Command: cmd,
		Stdin: bytes.NewReader(input), Stdout: &stdout, Stderr: &stderr,
		ReportInterval: time.Hour,
	})
	var childExit *ExitError
	if !errors.As(err, &childExit) || childExit.ExitCode() != 7 {
		t.Fatalf("Run error = %v, want child exit code 7", err)
	}
	if result.ExitCode != 7 {
		t.Errorf("result exit code = %d, want 7", result.ExitCode)
	}
	if !bytes.Equal(stdout.Bytes(), input) {
		t.Errorf("stdout = %q, want exact input bytes %q", stdout.Bytes(), input)
	}
	if got, want := stderr.String(), "\x1b[31merr\x1b[0m\r"; got != want {
		t.Errorf("stderr = %q, want exact bytes %q", got, want)
	}
	counts := result.Counts
	if counts.InputBytes != int64(len(input)) || counts.InputLines != 1 {
		t.Errorf("input counters = %d bytes/%d lines", counts.InputBytes, counts.InputLines)
	}
	if counts.StdoutBytes == nil || *counts.StdoutBytes != int64(len(input)) || counts.StdoutLines == nil || *counts.StdoutLines != 1 {
		t.Errorf("stdout counters = %+v/%+v", counts.StdoutBytes, counts.StdoutLines)
	}
	if counts.StderrBytes == nil || *counts.StderrBytes != int64(len(stderr.String())) || counts.StderrLines == nil || *counts.StderrLines != 1 {
		t.Errorf("stderr counters = %+v/%+v", counts.StderrBytes, counts.StderrLines)
	}
	if counts.OutputBytes != int64(len(input)+len(stderr.String())) || counts.OutputLines != 2 {
		t.Errorf("combined output counters = %d bytes/%d lines", counts.OutputBytes, counts.OutputLines)
	}
}

func TestRunPipesMarksCounterSnapshotPartialWhenUploadFails(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestPipeRelayFixtureProcess$")
	cmd.Env = append(os.Environ(), "CCTRACE_PROCESSIO_FIXTURE=1")
	var final Report
	result, err := Run(context.Background(), RelayOptions{
		Mode: ModePipes, Command: cmd,
		Stdin: bytes.NewReader([]byte("synthetic-input\n")), Stdout: io.Discard, Stderr: io.Discard,
		ReportInterval: time.Hour,
		OnSnapshot: func(_ int, report Report) error {
			final = report
			return errors.New("fixture upload unavailable")
		},
	})
	var childExit *ExitError
	if !errors.As(err, &childExit) || childExit.ExitCode() != 7 {
		t.Fatalf("Run error = %v, want child exit code 7", err)
	}
	if !result.Partial || result.FailureCode != FailureUpload || !final.Final || !final.Partial || final.FailureCode != FailureUpload {
		t.Fatalf("result/report partial=%v/%v, codes=%q/%q; want upload_failed partial snapshots", result.Partial, final.Partial, result.FailureCode, final.FailureCode)
	}
	if final.Counts.InputBytes == 0 || final.Counts.OutputBytes == 0 {
		t.Fatalf("final counters = %+v; upload failure lost the measured totals", final.Counts)
	}
}
