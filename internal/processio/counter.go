package processio

import "sync"

type Mode string

const (
	ModePTY   Mode = "pty"
	ModePipes Mode = "pipes"
)

// Snapshot contains counters only. No stream bytes are retained.
type Snapshot struct {
	InputBytes  int64  `json:"input_bytes"`
	InputLines  int64  `json:"input_lines"`
	OutputBytes int64  `json:"output_bytes"`
	OutputLines int64  `json:"output_lines"`
	StdoutBytes *int64 `json:"stdout_bytes"`
	StdoutLines *int64 `json:"stdout_lines"`
	StderrBytes *int64 `json:"stderr_bytes"`
	StderrLines *int64 `json:"stderr_lines"`
}

// Counter counts raw bytes while maintaining independent, incremental line
// parsers for each stream. ANSI control sequences do not make a line nonempty.
type Counter struct {
	mu     sync.Mutex
	mode   Mode
	input  lineCounter
	output lineCounter
	stdout lineCounter
	stderr lineCounter
}

func NewCounter(mode Mode) *Counter {
	return &Counter{mode: mode}
}

func (c *Counter) Input(p []byte) {
	c.mu.Lock()
	c.input.add(p)
	c.mu.Unlock()
}

// Output counts the merged output of a PTY.
func (c *Counter) Output(p []byte) {
	c.mu.Lock()
	c.output.add(p)
	c.mu.Unlock()
}

func (c *Counter) Stdout(p []byte) {
	c.mu.Lock()
	c.stdout.add(p)
	c.mu.Unlock()
}

func (c *Counter) Stderr(p []byte) {
	c.mu.Lock()
	c.stderr.add(p)
	c.mu.Unlock()
}

func (c *Counter) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	inputBytes, inputLines := c.input.bytes, c.input.lines()
	if c.mode == ModePTY {
		return Snapshot{
			InputBytes: inputBytes, InputLines: inputLines,
			OutputBytes: c.output.bytes, OutputLines: c.output.lines(),
		}
	}
	stdoutBytes, stdoutLines := c.stdout.bytes, c.stdout.lines()
	stderrBytes, stderrLines := c.stderr.bytes, c.stderr.lines()
	return Snapshot{
		InputBytes: inputBytes, InputLines: inputLines,
		OutputBytes: saturatingAdd(stdoutBytes, stderrBytes),
		OutputLines: saturatingAdd(stdoutLines, stderrLines),
		StdoutBytes: int64Pointer(stdoutBytes), StdoutLines: int64Pointer(stdoutLines),
		StderrBytes: int64Pointer(stderrBytes), StderrLines: int64Pointer(stderrLines),
	}
}

func int64Pointer(value int64) *int64 { return &value }

func saturatingAdd(a, b int64) int64 {
	if b > 0 && a > int64(^uint64(0)>>1)-b {
		return int64(^uint64(0) >> 1)
	}
	return a + b
}

type escapeState uint8

const (
	escapeNone escapeState = iota
	escapeStart
	escapeCSI
	escapeOSC
	escapeOSCEscape
	escapeString
	escapeStringEscape
)

type lineCounter struct {
	bytes       int64
	terminated  int64
	lineContent bool
	pendingCR   bool
	escape      escapeState
}

func (c *lineCounter) add(p []byte) {
	c.bytes = saturatingAdd(c.bytes, int64(len(p)))
	for _, b := range p {
		if c.consumeEscape(b) {
			continue
		}
		if c.pendingCR {
			c.pendingCR = false
			if b == '\n' {
				continue
			}
		}
		switch b {
		case '\r':
			c.terminateLine()
			c.pendingCR = true
		case '\n':
			c.terminateLine()
		default:
			c.lineContent = true
		}
	}
}

func (c *lineCounter) terminateLine() {
	c.terminated = saturatingAdd(c.terminated, 1)
	c.lineContent = false
}

// consumeEscape returns true for bytes belonging to an ANSI escape sequence.
// The state crosses read boundaries, so split CSI/OSC sequences stay filtered.
func (c *lineCounter) consumeEscape(b byte) bool {
	switch c.escape {
	case escapeNone:
		if b == 0x1b {
			c.escape = escapeStart
			return true
		}
		return false
	case escapeStart:
		switch b {
		case '[':
			c.escape = escapeCSI
		case ']':
			c.escape = escapeOSC
		case 'P', '^', '_':
			c.escape = escapeString
		default:
			c.escape = escapeNone
		}
		return true
	case escapeCSI:
		if b >= 0x40 && b <= 0x7e {
			c.escape = escapeNone
		}
		return true
	case escapeOSC:
		switch b {
		case 0x07:
			c.escape = escapeNone
		case 0x1b:
			c.escape = escapeOSCEscape
		}
		return true
	case escapeOSCEscape:
		if b == '\\' {
			c.escape = escapeNone
		} else if b != 0x1b {
			c.escape = escapeOSC
		}
		return true
	case escapeString:
		if b == 0x1b {
			c.escape = escapeStringEscape
		}
		return true
	case escapeStringEscape:
		if b == '\\' {
			c.escape = escapeNone
		} else if b != 0x1b {
			c.escape = escapeString
		}
		return true
	default:
		c.escape = escapeNone
		return false
	}
}

func (c *lineCounter) lines() int64 {
	if c.lineContent {
		return saturatingAdd(c.terminated, 1)
	}
	return c.terminated
}
