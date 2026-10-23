package gitcmd

import "strings"

// CodexCommand is the literal shell command of a Codex shell tool call, read
// as ParseCodex reads it. It is empty for another tool and for exec code that
// runs no command; dynamic reports a command the code builds at run time,
// which is not read.
func CodexCommand(toolName, arguments string) (command string, dynamic bool) {
	name := strings.ToLower(toolName)
	switch name {
	case "exec", "exec_command", "shell", "shell_command":
	default:
		return "", false
	}
	command, _, ok, incomplete := codexCommand(arguments, name)
	if ok {
		return command, false
	}
	return "", incomplete
}

// CommandWords splits a shell line into the words of each simple command, in
// order. Leading variable assignments and an env prefix are left out, as are
// redirection operands and heredoc bodies; nothing is expanded, so a word with
// an expansion stays as written. complete is false when the line holds a
// construct the lexer cannot read literally.
func CommandWords(line string) ([][]string, bool) {
	if len(line) > MaxCommandBytes {
		return nil, false
	}
	tokens, complete := lex(line)
	var commands [][]string
	var words []string
	flush := func() {
		for len(words) > 0 && assignment(words[0]) {
			words = words[1:]
		}
		if len(words) > 0 && words[0] == "env" {
			words = words[1:]
			for len(words) > 0 && assignment(words[0]) {
				words = words[1:]
			}
		}
		if len(words) > 0 {
			commands = append(commands, words)
		}
		words = nil
	}
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		switch {
		case !t.op:
			words = append(words, t.text)
		case t.text == ">" || t.text == ">>" || t.text == "<" || t.text == "<<" || t.text == "<<-" || t.text == "<<<":
			if i+1 < len(tokens) && !tokens[i+1].op {
				i++
			}
		default:
			flush()
		}
	}
	flush()
	return commands, complete
}
