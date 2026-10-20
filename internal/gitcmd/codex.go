package gitcmd

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
)

// ParseCodex extracts only literal shell commands from Codex tool arguments.
// It never evaluates rollout code, shell expansions, or dynamic workdirs.
func ParseCodex(toolName, arguments, cwd string) Commands {
	name := strings.ToLower(toolName)
	switch name {
	case "exec", "exec_command", "shell", "shell_command":
	default:
		return Commands{}
	}
	command, workdir, ok, incomplete := codexCommand(arguments, name)
	if !ok {
		return Commands{Incomplete: true}
	}
	if workdir != "" {
		if filepath.IsAbs(workdir) {
			cwd = filepath.Clean(workdir)
		} else {
			cwd = ""
			incomplete = true
		}
	}
	parsed := ParseWithMessages(command, cwd)
	parsed.Incomplete = parsed.Incomplete || incomplete
	return parsed
}

func codexCommand(arguments, name string) (command, workdir string, ok, incomplete bool) {
	var value any
	if json.Unmarshal([]byte(arguments), &value) == nil {
		switch v := value.(type) {
		case string:
			if name == "exec" {
				return parseCodexJS(v)
			}
			return v, "", v != "", false
		case map[string]any:
			command, ok = literalCommand(v["cmd"])
			if !ok {
				command, ok = literalCommand(v["command"])
			}
			if !ok && name == "exec" {
				if code, yes := v["code"].(string); yes {
					return parseCodexJS(code)
				}
			}
			if wd, yes := v["workdir"].(string); yes {
				workdir = wd
			} else if _, has := v["workdir"]; has {
				incomplete = true
			}
			return command, workdir, ok, incomplete
		}
	}
	if name == "exec" {
		return parseCodexJS(arguments)
	}
	return "", "", false, true
}

func literalCommand(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, v != ""
	case []any:
		if len(v) == 0 {
			return "", false
		}
		values := make([]string, len(v))
		for i, part := range v {
			text, ok := part.(string)
			if !ok {
				return "", false
			}
			values[i] = text
		}
		if filepath.Base(values[0]) == "bash" || filepath.Base(values[0]) == "sh" || filepath.Base(values[0]) == "zsh" {
			for i := 1; i+1 < len(values); i++ {
				if values[i] == "-lc" || values[i] == "-c" {
					return values[i+1], true
				}
			}
		}
		args := make([]string, len(values))
		for i, value := range values {
			args[i] = shellQuote(value)
		}
		return strings.Join(args, " "), true
	default:
		return "", false
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func parseCodexJS(source string) (command, workdir string, ok, incomplete bool) {
	fields := make(map[string]string)
	for i := 0; i < len(source); {
		switch {
		case source[i] == '\'' || source[i] == '"' || source[i] == '`':
			_, next, valid := jsLiteral(source, i)
			if !valid {
				return "", "", false, true
			}
			i = next
		case i+1 < len(source) && source[i:i+2] == "//":
			if end := strings.IndexByte(source[i+2:], '\n'); end >= 0 {
				i += end + 3
			} else {
				i = len(source)
			}
		case i+1 < len(source) && source[i:i+2] == "/*":
			if end := strings.Index(source[i+2:], "*/"); end >= 0 {
				i += end + 4
			} else {
				return "", "", false, true
			}
		case isJSIdentStart(source[i]):
			start := i
			i++
			for i < len(source) && isJSIdent(source[i]) {
				i++
			}
			key := source[start:i]
			j := skipSpaces(source, i)
			if j >= len(source) || source[j] != ':' || (key != "cmd" && key != "command" && key != "workdir" && key != "cwd") {
				continue
			}
			j = skipSpaces(source, j+1)
			if key == "command" && j < len(source) && source[j] == '[' {
				end := strings.IndexByte(source[j:], ']')
				if end < 0 {
					incomplete = true
					continue
				}
				var values []any
				if json.Unmarshal([]byte(source[j:j+end+1]), &values) != nil {
					incomplete = true
					continue
				}
				if value, valid := literalCommand(values); valid {
					fields[key] = value
					i = j + end + 1
					continue
				}
				incomplete = true
				continue
			}
			if j >= len(source) || (source[j] != '\'' && source[j] != '"' && source[j] != '`') {
				incomplete = true
				continue
			}
			literal, next, valid := jsLiteral(source, j)
			if !valid {
				incomplete = true
				continue
			}
			fields[key] = literal
			i = next
		default:
			i++
		}
	}
	command = fields["cmd"]
	if command == "" {
		command = fields["command"]
	}
	workdir = fields["workdir"]
	if workdir == "" {
		workdir = fields["cwd"]
	}
	return command, workdir, command != "", incomplete
}

func jsLiteral(source string, start int) (value string, next int, ok bool) {
	quote := source[start]
	for i := start + 1; i < len(source); i++ {
		if source[i] == '\\' {
			i++
			if i >= len(source) {
				return "", len(source), false
			}
			continue
		}
		if quote == '`' && source[i] == '$' && i+1 < len(source) && source[i+1] == '{' {
			return "", len(source), false
		}
		if source[i] == quote {
			literal := source[start+1 : i]
			decoded, valid := decodeJSLiteral(literal, quote)
			return decoded, i + 1, valid
		}
	}
	return "", len(source), false
}

func decodeJSLiteral(value string, quote byte) (string, bool) {
	if quote == '"' {
		decoded, err := strconv.Unquote("\"" + value + "\"")
		return decoded, err == nil
	}
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			out.WriteByte(value[i])
			continue
		}
		i++
		if i >= len(value) {
			return "", false
		}
		switch value[i] {
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		case '\\', '\'', '"', '`':
			out.WriteByte(value[i])
		default:
			return "", false
		}
	}
	return out.String(), true
}

func skipSpaces(source string, index int) int {
	for index < len(source) && (source[index] == ' ' || source[index] == '\t' || source[index] == '\r' || source[index] == '\n') {
		index++
	}
	return index
}

func isJSIdentStart(value byte) bool {
	return value == '_' || value == '$' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func isJSIdent(value byte) bool {
	return isJSIdentStart(value) || value >= '0' && value <= '9'
}
