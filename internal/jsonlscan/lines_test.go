package jsonlscan

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// readLine is exercised with a small limit so tests stay fast; production keeps
// MaxLineBytes.

func TestReadLineSkipsOversizedLineAndKeepsNextLineIntact(t *testing.T) {
	input := strings.Repeat("A", 50) + "\n" + `{"n":2}` + "\n"
	reader := bufio.NewReaderSize(strings.NewReader(input), 16)

	line, consumed, complete, err := readLine(context.Background(), reader, 20)
	if !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("err = %v, want ErrLineTooLong", err)
	}
	if line != nil {
		t.Fatalf("line = %q, want nil", line)
	}
	if complete {
		t.Fatal("complete = true, want false for a skipped line")
	}
	if consumed != 51 {
		t.Fatalf("consumed = %d, want 51 (whole oversized line incl newline)", consumed)
	}

	line, consumed, complete, err = readLine(context.Background(), reader, 20)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !complete {
		t.Fatal("complete = false, want true")
	}
	if string(line) != `{"n":2}`+"\n" {
		t.Fatalf("line = %q, want the next line intact", line)
	}
	if consumed != 8 {
		t.Fatalf("consumed = %d, want 8", consumed)
	}
}

func TestReadLineOversizedLineEndingAtEOF(t *testing.T) {
	input := strings.Repeat("A", 50)
	reader := bufio.NewReaderSize(strings.NewReader(input), 16)

	_, consumed, complete, err := readLine(context.Background(), reader, 20)
	if !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("err = %v, want ErrLineTooLong", err)
	}
	if complete {
		t.Fatal("complete = true, want false")
	}
	if consumed != 50 {
		t.Fatalf("consumed = %d, want 50", consumed)
	}

	_, consumed, complete, err = readLine(context.Background(), reader, 20)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
	if complete || consumed != 0 {
		t.Fatalf("complete = %v consumed = %d, want false/0", complete, consumed)
	}
}

func TestReadLineBoundaryAtLimit(t *testing.T) {
	const max = 32

	t.Run("exactly max including newline", func(t *testing.T) {
		input := strings.Repeat("A", max-1) + "\n"
		reader := bufio.NewReaderSize(strings.NewReader(input), 16)
		line, consumed, complete, err := readLine(context.Background(), reader, max)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if !complete || len(line) != max || consumed != max {
			t.Fatalf("complete = %v len(line) = %d consumed = %d", complete, len(line), consumed)
		}
	})

	t.Run("one byte over max", func(t *testing.T) {
		input := strings.Repeat("A", max) + "\n"
		reader := bufio.NewReaderSize(strings.NewReader(input), 16)
		_, consumed, _, err := readLine(context.Background(), reader, max)
		if !errors.Is(err, ErrLineTooLong) {
			t.Fatalf("err = %v, want ErrLineTooLong", err)
		}
		if consumed != max+1 {
			t.Fatalf("consumed = %d, want %d", consumed, max+1)
		}
	})

	t.Run("exactly max without newline at EOF", func(t *testing.T) {
		input := strings.Repeat("A", max)
		reader := bufio.NewReaderSize(strings.NewReader(input), 16)
		line, consumed, _, err := readLine(context.Background(), reader, max)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("err = %v, want io.EOF", err)
		}
		if len(line) != max || consumed != max {
			t.Fatalf("len(line) = %d consumed = %d, want %d", len(line), consumed, max)
		}
	})
}

func TestReadLineHonorsContextWhileDraining(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := &cancelAfterReads{
		r:      strings.NewReader(strings.Repeat("A", 4096) + "\n"),
		cancel: cancel,
		after:  3,
	}
	reader := bufio.NewReaderSize(src, 16)

	_, _, _, err := readLine(ctx, reader, 20)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

type cancelAfterReads struct {
	r      io.Reader
	cancel context.CancelFunc
	after  int
	count  int
}

func (c *cancelAfterReads) Read(p []byte) (int, error) {
	c.count++
	if c.count >= c.after {
		c.cancel()
	}
	return c.r.Read(p)
}

func TestReadLineUsesMaxLineBytesByDefault(t *testing.T) {
	input := strings.Repeat("A", 1024) + "\n"
	reader := bufio.NewReaderSize(strings.NewReader(input), 16)
	line, consumed, complete, err := ReadLine(context.Background(), reader)
	if err != nil || !complete {
		t.Fatalf("err = %v complete = %v, want nil/true", err, complete)
	}
	if len(line) != 1025 || consumed != 1025 {
		t.Fatalf("len(line) = %d consumed = %d, want 1025", len(line), consumed)
	}
}
