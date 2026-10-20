package processio

import "testing"

func TestPTYCounterCountsRawBytesAndLogicalLines(t *testing.T) {
	c := NewCounter(ModePTY)
	input := []byte("first\r\nsecond\rthird\nlast")
	c.Input(input[:7])
	c.Input(input[7:])
	output := []byte("\x1b[31mred\x1b[0m\r\nplain\rlast")
	for _, part := range [][]byte{output[:2], output[2:8], output[8:13], output[13:]} {
		c.Output(part)
	}

	got := c.Snapshot()
	if got.InputBytes != int64(len(input)) || got.InputLines != 4 {
		t.Errorf("input = %d bytes, %d lines; want %d bytes, 4 lines", got.InputBytes, got.InputLines, len(input))
	}
	if got.OutputBytes != int64(len(output)) || got.OutputLines != 3 {
		t.Errorf("output = %d bytes, %d lines; want %d bytes, 3 lines", got.OutputBytes, got.OutputLines, len(output))
	}
	if got.StdoutBytes != nil || got.StderrBytes != nil || got.StdoutLines != nil || got.StderrLines != nil {
		t.Errorf("PTY output must not claim separate streams: %+v", got)
	}
}

func TestPipeCounterKeepsStdoutAndStderrLineParsersSeparate(t *testing.T) {
	c := NewCounter(ModePipes)
	c.Input([]byte{0xe2})
	c.Input([]byte{0x82, 0xac, '\n'})
	c.Stdout([]byte("out\r"))
	c.Stdout([]byte("\nlast"))
	c.Stderr([]byte("err\n"))

	got := c.Snapshot()
	if got.InputBytes != 4 || got.InputLines != 1 {
		t.Errorf("input = %d bytes, %d lines; want 4 bytes, 1 line", got.InputBytes, got.InputLines)
	}
	if got.OutputBytes != 13 || got.OutputLines != 3 {
		t.Errorf("output = %d bytes, %d lines; want 13 bytes, 3 lines", got.OutputBytes, got.OutputLines)
	}
	if got.StdoutBytes == nil || *got.StdoutBytes != 9 || got.StdoutLines == nil || *got.StdoutLines != 2 {
		t.Errorf("stdout = %+v/%+v; want 9 bytes, 2 lines", got.StdoutBytes, got.StdoutLines)
	}
	if got.StderrBytes == nil || *got.StderrBytes != 4 || got.StderrLines == nil || *got.StderrLines != 1 {
		t.Errorf("stderr = %+v/%+v; want 4 bytes, 1 line", got.StderrBytes, got.StderrLines)
	}
}

func TestCounterCountsEmptyLinesAndDoesNotJoinStreamTails(t *testing.T) {
	c := NewCounter(ModePipes)
	c.Stdout([]byte("tail"))
	c.Stderr([]byte("\n\n"))
	got := c.Snapshot()
	if got.OutputLines != 3 {
		t.Errorf("output lines = %d, want 3 (stdout partial plus two stderr blank lines)", got.OutputLines)
	}
}
