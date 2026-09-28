package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// inputReader provides terminal-aware line input with proper readline support
// (arrow keys, backspace, cursor movement) and password masking.
type inputReader struct {
	fd     int
	isTerm bool
	reader *bufio.Reader // fallback for non-terminal (pipes, tests)
}

func newInputReader() *inputReader {
	fd := int(os.Stdin.Fd())
	isTerm := term.IsTerminal(fd)
	var r *bufio.Reader
	if !isTerm {
		r = bufio.NewReader(os.Stdin)
	}
	return &inputReader{fd: fd, isTerm: isTerm, reader: r}
}

// Prompt displays a prompt and reads a line with readline editing.
// Up arrow fills the default value for editing. Enter with empty input accepts the default.
func (r *inputReader) Prompt(label, defaultVal string) string {
	prompt := label
	if defaultVal != "" {
		prompt += " [" + defaultVal + "]: "
	} else {
		prompt += ": "
	}

	if r.isTerm {
		old, err := term.MakeRaw(r.fd)
		if err == nil {
			line := readLineRaw(prompt, defaultVal)
			term.Restore(r.fd, old)
			line = strings.TrimSpace(line)
			if line == "" {
				if defaultVal != "" {
					fmt.Printf("\033[A%s%s\n", prompt, defaultVal)
				}
				return defaultVal
			}
			return line
		}
	}

	// Non-terminal fallback
	fmt.Print(prompt)
	line, _ := r.reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		if defaultVal != "" {
			fmt.Printf("\033[A%s%s\n", prompt, defaultVal)
		}
		return defaultVal
	}
	return line
}

// readLineRaw implements a minimal readline in raw mode.
// Supports: typing, backspace, left/right arrows, Ctrl-A/E/U,
// up arrow to fill default, down arrow to clear.
func readLineRaw(prompt, defaultVal string) string {
	buf := make([]byte, 0, 256)
	pos := 0
	tmp := make([]byte, 16)

	os.Stdout.WriteString(prompt)

	for {
		n, err := os.Stdin.Read(tmp)
		if err != nil || n == 0 {
			return string(buf)
		}
		for i := 0; i < n; i++ {
			b := tmp[i]
			switch {
			case b == 13 || b == 10: // Enter
				os.Stdout.WriteString("\r\n")
				return string(buf)
			case b == 127 || b == 8: // Backspace
				if pos > 0 {
					buf = append(buf[:pos-1], buf[pos:]...)
					pos--
					redrawLine(prompt, buf, pos)
				}
			case b == 27: // ESC sequence
				if i+2 < n && tmp[i+1] == '[' {
					switch tmp[i+2] {
					case 'A': // Up → fill default
						if defaultVal != "" {
							buf = append(buf[:0], []byte(defaultVal)...)
							pos = len(buf)
							redrawLine(prompt, buf, pos)
						}
					case 'B': // Down → clear
						buf = buf[:0]
						pos = 0
						redrawLine(prompt, buf, pos)
					case 'C': // Right
						if pos < len(buf) {
							pos++
							os.Stdout.WriteString("\033[C")
						}
					case 'D': // Left
						if pos > 0 {
							pos--
							os.Stdout.WriteString("\033[D")
						}
					}
					i += 2
				}
			case b == 3: // Ctrl-C
				os.Stdout.WriteString("^C\r\n")
				return ""
			case b == 1: // Ctrl-A → beginning
				pos = 0
				redrawLine(prompt, buf, pos)
			case b == 5: // Ctrl-E → end
				pos = len(buf)
				redrawLine(prompt, buf, pos)
			case b == 21: // Ctrl-U → clear line
				buf = buf[:0]
				pos = 0
				redrawLine(prompt, buf, pos)
			case b >= 32 && b < 127: // Printable ASCII
				buf = append(buf, 0)
				copy(buf[pos+1:], buf[pos:])
				buf[pos] = b
				pos++
				redrawLine(prompt, buf, pos)
			}
		}
	}
}

func redrawLine(prompt string, buf []byte, pos int) {
	os.Stdout.WriteString("\r\033[K" + prompt + string(buf))
	if back := len(buf) - pos; back > 0 {
		fmt.Fprintf(os.Stdout, "\033[%dD", back)
	}
}

// PromptPassword reads a password with input masked (no echo).
func (r *inputReader) PromptPassword(label string) string {
	if r.isTerm {
		old, err := term.MakeRaw(r.fd)
		if err == nil {
			t := term.NewTerminal(struct {
				io.Reader
				io.Writer
			}{os.Stdin, os.Stdout}, "")
			pass, err := t.ReadPassword(label + ": ")
			term.Restore(r.fd, old)
			if err != nil {
				return ""
			}
			return strings.TrimSpace(pass)
		}
	}

	// Non-terminal fallback (no masking)
	fmt.Print(label + ": ")
	line, _ := r.reader.ReadString('\n')
	return strings.TrimSpace(line)
}

// promptInput displays a prompt and reads a line.
// Enter with empty input returns the default value.
func promptInput(reader *bufio.Reader, label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("%s: ", label)
	}

	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		if defaultVal != "" {
			// Move cursor up, reprint with default value shown
			fmt.Printf("\033[A%s [%s]: %s\n", label, defaultVal, defaultVal)
		}
		return defaultVal
	}
	return line
}
