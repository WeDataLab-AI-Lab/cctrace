// Package gitcmd reads command evidence without executing shell code. A parsed
// command is an attempted operation, never proof that a commit was created.
package gitcmd

import (
	"path/filepath"
	"strings"
	"unicode"
)

const MaxCommandBytes = 64 << 10

// Operation keeps the execution directory separate for each command in a shell
// call. Unknown directories must not fall back to the session's cwd.
type Operation struct {
	Kind       string `json:"kind"`
	Dir        string `json:"dir,omitempty"`
	DirKnown   bool   `json:"dir_known,omitempty"`
	Amend      bool   `json:"amend,omitempty"`
	Background bool   `json:"background,omitempty"`
	// Message is transient literal command evidence, never persisted in queues.
	Message      string `json:"-"`
	MessageKnown bool   `json:"-"`
}

type GitHubCall struct {
	Noun       string
	Verb       string
	Target     string
	Repository string
	Title      string
	Auto       bool
}

type Commands struct {
	Git        []Operation
	GitHub     []GitHubCall
	Incomplete bool
}

type word struct {
	text    string
	literal bool
}

type token struct {
	word
	op bool
}

// Parse understands literal shell words, list separators and subshell scopes.
// Dynamic execution (eval, substitutions, shell functions) is not evaluated:
// Incomplete prevents its absence from being mistaken for an observed zero.
func Parse(command, cwd string) Commands {
	result := ParseWithMessages(command, cwd)
	for i := range result.Git {
		result.Git[i].Message, result.Git[i].MessageKnown = "", false
	}
	return result
}

// ParseWithMessages is for transient pure computation only. Parse keeps these
// messages out of client queues, including their in-memory and logging forms.
func ParseWithMessages(command, cwd string) Commands {
	var result Commands
	if len(command) > MaxCommandBytes {
		result.Incomplete = true
		return result
	}
	tokens, complete := lex(command)
	result.Incomplete = !complete
	dir, known := filepath.Clean(cwd), filepath.IsAbs(cwd)
	type scope struct {
		dir     string
		known   bool
		changed bool
	}
	cwdChanged := false
	var scopes []scope
	var words []word
	conditional := false
	flush := func(background bool) {
		if len(words) == 0 {
			return
		}
		ws := words
		words = nil
		for len(ws) > 0 && assignment(ws[0].text) {
			if strings.HasPrefix(ws[0].text, "GIT_") || strings.HasPrefix(ws[0].text, "CDPATH=") {
				result.Incomplete = true
				dir, known = "", false
			}
			ws = ws[1:]
		}
		for len(ws) > 0 && (ws[0].text == "then" || ws[0].text == "do" || ws[0].text == "else" || ws[0].text == "if" || ws[0].text == "elif" || ws[0].text == "!" || ws[0].text == "command") {
			if ws[0].text != "command" && ws[0].text != "!" {
				conditional = true
				result.Incomplete = true
			}
			ws = ws[1:]
		}
		if len(ws) == 0 {
			return
		}
		if ws[0].text == "env" {
			ws = ws[1:]
			for len(ws) > 0 && assignment(ws[0].text) {
				if strings.HasPrefix(ws[0].text, "GIT_") || strings.HasPrefix(ws[0].text, "CDPATH=") {
					result.Incomplete = true
					dir, known = "", false
				}
				ws = ws[1:]
			}
			if len(ws) == 0 {
				return
			}
		}
		if !ws[0].literal {
			result.Incomplete = true
			return
		}
		switch ws[0].text {
		case "cd":
			cwdChanged = true
			if len(ws) == 2 && ws[1].text != "-" && !conditional {
				dir, known = resolveDir(dir, known, ws[1])
			} else {
				dir, known = "", false
			}
		case "git":
			op, ok, uncertain := parseGit(ws[1:], dir, known)
			result.Incomplete = result.Incomplete || uncertain
			if ok {
				op.Background = background
				result.Git = append(result.Git, op)
			}
		case "gh":
			if gh, ok := parseGitHub(ws[1:]); ok {
				result.GitHub = append(result.GitHub, gh)
			}
		case "eval", "source", ".", "bash", "sh", "zsh", "function", "for", "while", "until", "case", "{", "}":
			result.Incomplete = true
			dir, known = "", false
		}
	}
	for i, t := range tokens {
		if !t.op {
			words = append(words, t.word)
			continue
		}
		switch t.text {
		case "(":
			if len(words) > 0 {
				result.Incomplete = true
			}
			flush(false)
			scopes = append(scopes, scope{dir, known, cwdChanged})
			cwdChanged = false
		case ")":
			flush(false)
			if len(scopes) == 0 {
				result.Incomplete = true
			} else {
				old := scopes[len(scopes)-1]
				scopes = scopes[:len(scopes)-1]
				dir, known, cwdChanged = old.dir, old.known, old.changed
			}
		case ">", ">>", "<", "<<", "<<-", "<<<", "2>", "2>>":
			// Redirection operands are not command words. The lexer has already
			// removed heredoc bodies, which may contain executable-looking text.
			if i+1 < len(tokens) && !tokens[i+1].op {
				tokens[i+1].text = ""
				tokens[i+1].op = true
			}
		case "":
		default:
			flush(t.text == "&")
			// A successful right-hand command proves cd succeeded only inside
			// its && chain. At a list join, a failed cd could have skipped the
			// whole branch and left the shell in the previous directory.
			if cwdChanged && t.text != "&&" {
				dir, known, cwdChanged = "", false, false
			}
			if t.text == "||" || t.text == "|" || t.text == "&" {
				dir, known = "", false
			}
		}
	}
	flush(false)
	if len(scopes) != 0 {
		result.Incomplete = true
	}
	return result
}

func assignment(s string) bool {
	key, _, ok := strings.Cut(s, "=")
	if !ok || key == "" {
		return false
	}
	for i, r := range key {
		if r != '_' && !unicode.IsLetter(r) && (i == 0 || !unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

func resolveDir(base string, known bool, w word) (string, bool) {
	if !w.literal || w.text == "" || strings.HasPrefix(w.text, "~") {
		return "", false
	}
	if filepath.IsAbs(w.text) {
		return filepath.Clean(w.text), true
	}
	if !known {
		return "", false
	}
	return filepath.Clean(filepath.Join(base, w.text)), true
}

func parseGit(ws []word, dir string, known bool) (Operation, bool, bool) {
	op := Operation{Dir: dir, DirKnown: known}
	for len(ws) > 0 && strings.HasPrefix(ws[0].text, "-") {
		flag, literal := ws[0].text, ws[0].literal
		ws = ws[1:]
		switch {
		case flag == "-C":
			if len(ws) == 0 {
				return op, false, true
			}
			op.Dir, op.DirKnown = resolveDir(op.Dir, op.DirKnown, ws[0])
			ws = ws[1:]
		case strings.HasPrefix(flag, "-C") && len(flag) > 2:
			op.Dir, op.DirKnown = resolveDir(op.Dir, op.DirKnown, word{flag[2:], literal})
		case flag == "-c" || flag == "--config-env":
			if len(ws) == 0 {
				return op, false, true
			}
			ws = ws[1:]
		case strings.HasPrefix(flag, "--git-dir") || strings.HasPrefix(flag, "--work-tree"):
			op.Dir, op.DirKnown = "", false
			if !strings.Contains(flag, "=") && len(ws) > 0 {
				ws = ws[1:]
			}
		case flag == "--no-optional-locks", flag == "--no-pager", flag == "--bare", strings.HasPrefix(flag, "--config-env="):
			if flag == "--bare" {
				op.Dir, op.DirKnown = "", false
			}
		default:
			return op, false, true
		}
	}
	if len(ws) == 0 || !ws[0].literal {
		return op, false, true
	}
	switch ws[0].text {
	case "commit", "merge", "cherry-pick", "revert", "pull":
		op.Kind = strings.ReplaceAll(ws[0].text, "-", "_")
	default:
		return op, false, false
	}
	uncertain := false
	messageSeen := false
	for i := 1; i < len(ws); i++ {
		w := ws[i]
		if w.text == "--" {
			break
		}
		if op.Kind == "commit" {
			value, hasMessage := "", false
			literal := w.literal
			switch {
			case w.text == "-m" || w.text == "--message" || w.text == "-am":
				i++
				if i < len(ws) {
					value, literal, hasMessage = ws[i].text, ws[i].literal, true
				}
			case strings.HasPrefix(w.text, "--message="):
				value, hasMessage = strings.TrimPrefix(w.text, "--message="), true
			case strings.HasPrefix(w.text, "-m") && len(w.text) > 2:
				value, hasMessage = w.text[2:], true
			case strings.HasPrefix(w.text, "-am") && len(w.text) > 3:
				value, hasMessage = w.text[3:], true
			}
			if hasMessage {
				if !messageSeen {
					messageSeen = true
					op.MessageKnown = literal
					if literal {
						var lines []string
						for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
							line = strings.TrimRightFunc(line, unicode.IsSpace)
							if line == "" {
								break
							}
							lines = append(lines, line)
						}
						op.Message = strings.Join(lines, " ")
					}
				}
				continue
			}
		}
		// A dynamic message hides only the subject. The lexer separately marks
		// command substitutions incomplete because they execute other commands.
		if !w.literal {
			uncertain = true
		}
		switch w.text {
		case "-m", "--message", "-F", "--file", "--author", "--date", "-C", "-c", "--reuse-message", "--reedit-message":
			i++
			continue
		case "--dry-run", "--help", "-h", "--abort", "--quit":
			return op, false, false
		case "--amend":
			op.Amend = true
		case "--rebase", "--rebase=true", "--rebase=merges", "--rebase=interactive":
			if op.Kind == "pull" {
				return op, false, false
			}
		}
	}
	return op, true, uncertain
}

func parseGitHub(ws []word) (GitHubCall, bool) {
	var g GitHubCall
	var positional []string
	for i := 0; i < len(ws); i++ {
		w := ws[i]
		if w.text == "--auto" {
			g.Auto = true
			continue
		}
		key, value, hasValue := strings.Cut(w.text, "=")
		if key == "-R" || key == "--repo" || key == "--title" || key == "-t" {
			literal := w.literal
			if !hasValue {
				i++
				if i >= len(ws) {
					return g, false
				}
				value, literal = ws[i].text, ws[i].literal
			}
			if literal {
				if key == "-R" || key == "--repo" {
					g.Repository = value
				} else {
					g.Title = value
				}
			}
			continue
		}
		if strings.HasPrefix(w.text, "-") {
			switch key {
			case "--squash", "--merge", "--rebase", "--delete-branch", "--admin", "--draft", "--web", "--yes":
				continue
			case "--body", "-b", "--body-file", "-F", "--label", "--assignee", "--milestone", "--reason", "--base", "--head":
				if !hasValue {
					i++
				}
				continue
			default:
				break
			}
			break
		}
		if w.literal {
			positional = append(positional, w.text)
		} else {
			positional = append(positional, "")
		}
	}
	if len(positional) < 2 || (positional[0] != "issue" && positional[0] != "pr") {
		return g, false
	}
	g.Noun, g.Verb = positional[0], positional[1]
	switch g.Verb {
	case "create", "close", "edit", "comment", "reopen", "merge", "ready", "review":
	default:
		return g, false
	}
	if len(positional) > 2 {
		g.Target = positional[2]
	}
	return g, true
}

// lex deliberately does not expand variables or command substitutions. A quoted
// or heredoc string containing git is data, not a second command to execute.
func lex(s string) ([]token, bool) {
	var out []token
	var heredocs []word
	complete := true
	for i := 0; i < len(s); {
		if len(out) >= 4096 {
			return out, false
		}
		if s[i] == '\n' && len(heredocs) > 0 {
			i++
			for _, delimiter := range heredocs {
				found := false
				for i < len(s) {
					end := strings.IndexByte(s[i:], '\n')
					if end < 0 {
						end = len(s) - i
					}
					line := s[i : i+end]
					i += end
					if i < len(s) {
						i++
					}
					if strings.TrimLeft(line, "\t") == delimiter.text {
						found = true
						break
					}
				}
				if !found {
					complete = false
				}
			}
			heredocs = nil
			out = append(out, token{word: word{text: ";", literal: true}, op: true})
			continue
		}
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\r' {
			i++
			continue
		}
		if s[i] == '#' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if strings.ContainsRune(";&|()<>\n", rune(s[i])) {
			op := s[i : i+1]
			i++
			if i < len(s) && (op == "&" || op == "|" || op == ">" || op == "<") && s[i:i+1] == op {
				op += op
				i++
			}
			if op == "<<" && i < len(s) && (s[i] == '-' || s[i] == '<') {
				op += s[i : i+1]
				i++
			}
			out = append(out, token{word: word{text: op, literal: true}, op: true})
			continue
		}
		var b strings.Builder
		literal := true
		for i < len(s) && !strings.ContainsRune(" \t\r\n;&|()<>", rune(s[i])) {
			c := s[i]
			if c == '\\' {
				i++
				if i == len(s) {
					complete = false
					break
				}
				if s[i] != '\n' {
					b.WriteByte(s[i])
				}
				i++
				continue
			}
			if c == '\'' || c == '"' {
				quote := c
				i++
				closed := false
				for i < len(s) {
					c = s[i]
					i++
					if c == quote {
						closed = true
						break
					}
					if quote == '"' && c == '\\' && i < len(s) {
						if strings.ContainsRune("$`\"\\\n", rune(s[i])) {
							if s[i] != '\n' {
								b.WriteByte(s[i])
							}
							i++
						} else {
							b.WriteByte(c)
						}
						continue
					}
					if quote == '"' && (c == '$' || c == '`') {
						literal = false
						if c == '`' || (i < len(s) && s[i] == '(') {
							complete = false
						}
					}
					b.WriteByte(c)
				}
				if !closed {
					complete = false
				}
				continue
			}
			if c == '$' || c == '`' || c == '*' || c == '?' || c == '[' || c == '{' {
				literal = false
			}
			if c == '`' {
				complete = false
				i++
				for i < len(s) && s[i] != '`' {
					if s[i] == '\\' && i+1 < len(s) {
						i++
					}
					i++
				}
				if i < len(s) {
					i++
				}
				continue
			}
			if c == '$' && i+1 < len(s) && s[i+1] == '(' {
				// Consume the entire expansion, including nested parentheses, so
				// command-shaped data inside it cannot escape into the outer list.
				complete = false
				depth := 1
				i += 2
				for i < len(s) && depth > 0 {
					if s[i] == '(' {
						depth++
					}
					if s[i] == ')' {
						depth--
					}
					i++
				}
				continue
			}
			b.WriteByte(c)
			i++
		}
		w := word{b.String(), literal}
		if len(out) > 0 && (out[len(out)-1].text == "<<" || out[len(out)-1].text == "<<-") {
			heredocs = append(heredocs, w)
		}
		out = append(out, token{word: w})
	}
	return out, complete && len(heredocs) == 0
}
