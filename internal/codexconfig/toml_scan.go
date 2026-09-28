package codexconfig

import (
	"strconv"
	"strings"
)

func isOtelTableName(name string) bool {
	return name == "otel" || strings.HasPrefix(name, "otel.")
}

func tomlHeaderName(trimmed string) (string, bool) {
	if !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	openLen := 1
	closeToken := "]"
	if strings.HasPrefix(trimmed, "[[") {
		openLen = 2
		closeToken = "]]"
	}
	end := strings.Index(trimmed[openLen:], closeToken)
	if end < 0 {
		return "", false
	}
	name := normalizeTomlKeyPath(trimmed[openLen : openLen+end])
	after := strings.TrimSpace(trimmed[openLen+end+len(closeToken):])
	if after != "" && !strings.HasPrefix(after, "#") {
		return "", false
	}
	return name, name != ""
}

// otelSectionLines returns the code of the lines replaceOrAppendOtelSection would
// replace -- otel table headers with their bodies, and root-level otel keys --
// with comments stripped. None means the config has no otel section.
func otelSectionLines(content string) []string {
	var lines []string
	inRoot, inOtel := true, false
	var stringState tomlStringState
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if stringState.inMultiline() {
			stringState.update(line)
			continue
		}
		if name, ok := tomlHeaderName(trimmed); ok {
			inOtel = isOtelTableName(name)
			inRoot = false
		}
		if inOtel || (inRoot && isRootOtelKeyLine(trimmed)) {
			// Comments are not configuration: a commented-out endpoint or
			// token must not count toward ownership or be adopted.
			if code := strings.TrimSpace(tomlLineBeforeComment(trimmed)); code != "" {
				lines = append(lines, code)
			}
		}
		stringState.update(line)
	}
	return lines
}

// hasMultilineRootOtelKey reports whether a root-level otel key's value continues
// past its own line: an unclosed multi-line string, or an array or inline table
// left open. replaceOrAppendOtelSection removes root otel keys one line at a
// time, so it would leave such a value's continuation lines behind.
func hasMultilineRootOtelKey(content string) bool {
	var stringState tomlStringState
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if stringState.inMultiline() {
			stringState.update(line)
			continue
		}
		if _, ok := tomlHeaderName(trimmed); ok {
			return false // past the root table
		}
		if isRootOtelKeyLine(trimmed) && tomlValueContinues(trimmed) {
			return true
		}
		stringState.update(line)
	}
	return false
}

// tomlValueContinues reports whether a key line's value is left open at the end
// of the line.
func tomlValueContinues(line string) bool {
	code := tomlLineBeforeComment(line)
	if strings.Count(code, `"""`)%2 == 1 || strings.Count(code, `'''`)%2 == 1 {
		return true
	}
	depth := 0
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case inDouble:
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inDouble = false
			}
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case c == '"':
			inDouble = true
		case c == '\'':
			inSingle = true
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		}
	}
	return depth > 0
}

func isRootOtelKeyLine(trimmed string) bool {
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return false
	}
	eq := strings.Index(trimmed, "=")
	if eq < 0 {
		return false
	}
	key := normalizeTomlKeyPath(trimmed[:eq])
	return key == "otel" || strings.HasPrefix(key, "otel.")
}

func normalizeTomlKeyPath(raw string) string {
	parts := splitTomlKeyPath(raw)
	for i, part := range parts {
		part = strings.TrimSpace(part)
		quoted := false
		if len(part) >= 2 {
			if part[0] == '"' && part[len(part)-1] == '"' {
				quoted = true
				if unquoted, err := strconv.Unquote(part); err == nil {
					part = unquoted
				} else {
					part = part[1 : len(part)-1]
				}
			} else if part[0] == '\'' && part[len(part)-1] == '\'' {
				quoted = true
				part = part[1 : len(part)-1]
			}
		}
		if quoted && strings.Contains(part, ".") {
			part = strconv.Quote(part)
		}
		parts[i] = part
	}
	return strings.Join(parts, ".")
}

func splitTomlKeyPath(raw string) []string {
	parts := make([]string, 0, 4)
	start := 0
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inDouble {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inDouble = false
			}
			continue
		}
		if inSingle {
			if c == '\'' {
				inSingle = false
			}
			continue
		}
		switch c {
		case '"':
			inDouble = true
		case '\'':
			inSingle = true
		case '.':
			parts = append(parts, raw[start:i])
			start = i + 1
		}
	}
	return append(parts, raw[start:])
}

type tomlStringState struct {
	delimiter string
}

func (s *tomlStringState) inMultiline() bool {
	return s.delimiter != ""
}

func (s *tomlStringState) update(line string) {
	for {
		if s.delimiter != "" {
			idx := strings.Index(line, s.delimiter)
			if idx < 0 {
				return
			}
			line = line[idx+len(s.delimiter):]
			s.delimiter = ""
			continue
		}
		line = tomlLineBeforeComment(line)
		doubleIdx := strings.Index(line, `"""`)
		singleIdx := strings.Index(line, `'''`)
		switch {
		case doubleIdx < 0 && singleIdx < 0:
			return
		case singleIdx < 0 || (doubleIdx >= 0 && doubleIdx < singleIdx):
			s.delimiter = `"""`
			line = line[doubleIdx+3:]
		default:
			s.delimiter = `'''`
			line = line[singleIdx+3:]
		}
	}
}

func tomlLineBeforeComment(line string) string {
	inSingle := false
	inDouble := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle && (i == 0 || line[i-1] != '\\') {
				inDouble = !inDouble
			}
		}
	}
	return line
}
