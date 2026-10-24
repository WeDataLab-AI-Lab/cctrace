package main

import (
	"bytes"
	"encoding/json"
	"hash/fnv"
	"math/rand/v2"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var nonProjectChar = regexp.MustCompile(`[^A-Za-z0-9-]`)

// claudeProjectDir is the directory name Claude Code gives a working directory
// under ~/.claude/projects, and so the project hash cctrace reads.
func claudeProjectDir(cwd string) string { return nonProjectChar.ReplaceAllString(cwd, "-") }

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func sessionRand(id string, stream uint64) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return rand.New(rand.NewPCG(h.Sum64(), stream))
}

func symbolName(c component) string {
	parts := strings.Split(c.Symbol, ".")
	return parts[len(parts)-1]
}

// claudeTool renders an action as Claude Code's tool_use name and input.
func claudeTool(t task, cwd string, a action) (string, map[string]any) {
	abs := filepath.Join(cwd, a.File)
	switch a.Kind {
	case "read":
		return "Read", map[string]any{"file_path": abs}
	case "grep":
		return "Grep", map[string]any{"pattern": symbolName(t.Comp), "path": cwd, "output_mode": "content", "-n": true}
	case "edit":
		old, new := editFor(t, a.File)
		return "Edit", map[string]any{"file_path": abs, "old_string": old, "new_string": new}
	case "skill":
		return "Skill", map[string]any{"skill": a.File}
	case "todo":
		var todos []map[string]any
		for i, item := range todoItems(t) {
			status := "pending"
			if i == 0 {
				status = "in_progress"
			}
			todos = append(todos, map[string]any{"content": item, "status": status, "activeForm": item})
		}
		return "TodoWrite", map[string]any{"todos": todos}
	}
	cmd, desc := shellCommand(t, a)
	return "Bash", map[string]any{"command": cmd, "description": desc}
}

func shellCommand(t task, a action) (string, string) {
	switch a.Kind {
	case "test", "test-fail":
		return testCommand(t.Comp), "Run the tests"
	case "git-status":
		return "git status --short", "Show changed files"
	case "git-diff":
		return "git diff main...HEAD -- " + a.File, "Show the branch diff"
	case "git-log":
		return "git log --oneline -6", "Show recent commits"
	case "git-commit":
		return "git commit -am \"" + commitMessage(t) + "\"", "Commit the change"
	case "git-push":
		return "git push -u origin " + t.Branch, "Push the branch"
	}
	return "true", ""
}

// toolOutput is what running the action prints, and whether it failed.
func toolOutput(t task, cwd string, a action) (string, bool) {
	switch a.Kind {
	case "read":
		return fileText(t, a.File), false
	case "grep":
		return grepOutput(t.Comp), false
	case "edit", "write":
		return "", false
	case "test":
		return testOutput(t.Project, t.Comp, true, t.testName()), false
	case "test-fail":
		return testOutput(t.Project, t.Comp, false, t.testName()), true
	case "git-status":
		return gitStatusText(t), false
	case "git-diff":
		return diffText(t), false
	case "git-log":
		return gitLogText(t), false
	case "git-commit":
		return gitCommitText(t), false
	case "git-push":
		return gitPushText(t), false
	case "todo":
		return "", false
	}
	return "", false
}

func claudeResult(t task, cwd string, a action) (string, bool, map[string]any) {
	abs := filepath.Join(cwd, a.File)
	out, failed := toolOutput(t, cwd, a)
	switch a.Kind {
	case "read":
		return numbered(out), false, nil
	case "edit":
		return "The file " + abs + " has been updated successfully.", false, nil
	case "skill":
		return "Launching skill: " + a.File, false, nil
	case "todo":
		return "Todos have been modified successfully. Ensure that you continue to use the todo list to track your progress. Please proceed with the current tasks if applicable", false, nil
	case "grep":
		return out, false, nil
	}
	content := out
	if failed {
		content = "Exit code 1\n" + out
	}
	return content, failed, map[string]any{"stdout": out, "stderr": "", "interrupted": false, "isImage": false}
}

func claudeToolName(a action) string {
	switch a.Kind {
	case "read":
		return "Read"
	case "grep":
		return "Grep"
	case "edit":
		return "Edit"
	case "todo":
		return "TodoWrite"
	case "skill":
		return "Skill"
	}
	return "Bash"
}

// claudeTranscript is the session's JSONL file as Claude Code writes it: one line
// per content block, all lines of one API response sharing its message id and
// usage, chained through parentUuid.
func claudeTranscript(homeDir string, s *session) []byte {
	cwd := filepath.Join(homeDir, "work", s.Proj.Key)
	r := sessionRand(s.ID, 1)
	var buf bytes.Buffer
	prev := ""
	emit := func(m map[string]any) {
		b, _ := json.Marshal(m)
		buf.Write(b)
		buf.WriteByte('\n')
		if u, ok := m["uuid"].(string); ok {
			prev = u
		}
	}
	base := func(typ string, ts time.Time) map[string]any {
		var parent any
		if prev != "" {
			parent = prev
		}
		entry := "cli"
		if s.Headless {
			entry = "sdk-cli"
		}
		return map[string]any{
			"parentUuid": parent, "isSidechain": false, "userType": "external", "cwd": cwd,
			"sessionId": s.ID, "version": claudeCodeVersion, "gitBranch": "main", "entrypoint": entry,
			"type": typ, "uuid": uuid4(r), "timestamp": iso(ts),
		}
	}
	// meta is a line Claude Code adds for the model and hides from the person.
	meta := func(tr *turn, ts time.Time, text string) map[string]any {
		m := base("user", ts)
		m["isMeta"], m["promptId"] = true, tr.PromptID
		m["message"] = map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}}
		return m
	}
	titled := false
	for _, tr := range s.Turns {
		if tr.Compact {
			sys := base("system", tr.At.Add(-2*time.Second))
			sys["subtype"], sys["content"], sys["level"] = "compact_boundary", "Conversation compacted", "info"
			sys["compactMetadata"] = map[string]any{"trigger": "auto", "preTokens": 160000 + r.IntN(8000)}
			emit(sys)
			sum := base("user", tr.At.Add(-time.Second))
			sum["isCompactSummary"] = true
			sum["message"] = map[string]any{"role": "user", "content": "This session is being continued from a previous conversation that ran out of context. The conversation is summarized below:\n" + tr.Task.title() + " is in progress."}
			emit(sum)
		}
		u := base("user", tr.At)
		u["promptId"] = tr.PromptID
		u["permissionMode"] = "default"
		if s.Headless {
			u["promptSource"] = "sdk"
		} else {
			u["promptSource"] = "typed"
			u["origin"] = map[string]any{"kind": "human"}
			u["turnOrigin"] = "human"
		}
		content := tr.Prompt
		if tr.Command != "" {
			content = commandRecord(tr.Command, tr.Task.Comp.File)
		}
		u["message"] = map[string]any{"role": "user", "content": content}
		emit(u)
		if tr.Command != "" {
			emit(meta(tr, tr.At, commandExpansion(tr.Command, tr.Task.Comp.File)))
		}
		// Claude Code marks every response that runs under a skill it applied.
		attributed := ""
		for _, req := range tr.Requests {
			usage := map[string]any{"input_tokens": req.In, "cache_creation_input_tokens": req.CacheWrite,
				"cache_read_input_tokens": req.CacheRead, "output_tokens": req.Out, "service_tier": "standard"}
			var blocks []map[string]any
			if req.Say != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": req.Say})
			}
			for k, a := range req.Actions {
				name, input := claudeTool(tr.Task, cwd, a)
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": req.ToolIDs[k], "name": name, "input": input})
			}
			stop := "end_turn"
			if len(req.Actions) > 0 {
				stop = "tool_use"
			}
			for bi, blk := range blocks {
				a := base("assistant", req.End.Add(time.Duration(bi)*20*time.Millisecond))
				a["requestId"] = req.ReqID
				if attributed != "" {
					a["attributionSkill"] = attributed
				}
				a["message"] = map[string]any{"id": req.MsgID, "type": "message", "role": "assistant", "model": req.Model,
					"content": []any{blk}, "stop_reason": stop, "stop_sequence": nil, "usage": usage}
				emit(a)
			}
			if !titled {
				emit(map[string]any{"type": "ai-title", "sessionId": s.ID, "aiTitle": s.Title})
				titled = true
			}
			at := req.End
			for k, a := range req.Actions {
				at = at.Add(req.ToolTook[k])
				content, isErr, result := claudeResult(tr.Task, cwd, a)
				res := base("user", at)
				res["promptId"] = tr.PromptID
				res["message"] = map[string]any{"role": "user", "content": []any{map[string]any{
					"type": "tool_result", "tool_use_id": req.ToolIDs[k], "content": content, "is_error": isErr}}}
				if result != nil {
					res["toolUseResult"] = result
				}
				emit(res)
				if a.Kind == "skill" {
					emit(meta(tr, at.Add(5*time.Millisecond), skillLoaded(homeDir, a.File)))
					attributed = a.File
				}
			}
		}
	}
	return buf.Bytes()
}
