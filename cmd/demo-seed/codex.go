package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// codexRolloutPath is where Codex keeps the session, relative to the Codex home.
// The session id is the UUID at the end of the name; cctrace reads it from there.
func codexRolloutPath(s *session) string {
	t := s.Start.In(kst)
	return filepath.Join("sessions", t.Format("2006"), t.Format("01"), t.Format("02"),
		"rollout-"+t.Format("2006-01-02T15-04-05")+"-"+s.ID+".jsonl")
}

// codexTool is the response item for an action and the tool name Codex reports.
func codexTool(t task, cwd string, a action, callID string) (map[string]any, string) {
	switch a.Kind {
	case "edit":
		old, new := editFor(t, a.File)
		var patch strings.Builder
		fmt.Fprintf(&patch, "*** Begin Patch\n*** Update File: %s\n@@\n", a.File)
		for _, l := range strings.Split(old, "\n") {
			patch.WriteString("-" + l + "\n")
		}
		for _, l := range strings.Split(new, "\n") {
			patch.WriteString("+" + l + "\n")
		}
		patch.WriteString("*** End Patch")
		return map[string]any{"type": "custom_tool_call", "status": "completed", "call_id": callID, "name": "apply_patch", "input": patch.String()}, "apply_patch"
	case "todo":
		var steps []map[string]any
		for i, item := range todoItems(t) {
			status := "pending"
			if i == 0 {
				status = "in_progress"
			}
			steps = append(steps, map[string]any{"step": item, "status": status})
		}
		args, _ := json.Marshal(map[string]any{"plan": steps})
		return map[string]any{"type": "function_call", "name": "update_plan", "arguments": string(args), "call_id": callID}, "update_plan"
	}
	var cmd string
	switch a.Kind {
	case "read":
		cmd = "sed -n '1,160p' " + a.File
	case "grep":
		cmd = "rg -n \"" + symbolName(t.Comp) + "\" " + filepath.Dir(a.File)
	default:
		cmd, _ = shellCommand(t, a)
	}
	args, _ := json.Marshal(map[string]any{"command": []string{"bash", "-lc", cmd}, "workdir": cwd, "timeout_ms": 120000})
	return map[string]any{"type": "function_call", "name": "shell", "arguments": string(args), "call_id": callID}, "shell"
}

func codexOutput(t task, cwd string, a action, callID string, took time.Duration) map[string]any {
	out, failed := toolOutput(t, cwd, a)
	exit := 0
	if failed {
		exit = 1
	}
	switch a.Kind {
	case "edit":
		body, _ := json.Marshal(map[string]any{"output": "Success. Updated the following files:\nM " + a.File + "\n",
			"metadata": map[string]any{"exit_code": 0, "duration_seconds": 0.0}})
		return map[string]any{"type": "custom_tool_call_output", "call_id": callID, "output": string(body)}
	case "todo":
		return map[string]any{"type": "function_call_output", "call_id": callID, "output": "Plan updated"}
	}
	text := fmt.Sprintf("Exit code: %d\nWall time: %.1f seconds\nOutput:\n%s", exit, took.Seconds(), out)
	return map[string]any{"type": "function_call_output", "call_id": callID, "output": text}
}

func codexToolName(a action) string {
	switch a.Kind {
	case "edit":
		return "apply_patch"
	case "todo":
		return "update_plan"
	}
	return "shell"
}

// codexRollout is the session as Codex CLI records it: session_meta, then per
// turn task_started, turn_context, the response items, a cumulative token_count
// after each model response, and task_complete.
func codexRollout(homeDir string, s *session) []byte {
	cwd := filepath.Join(homeDir, "work", s.Proj.Key)
	var buf bytes.Buffer
	emit := func(ts time.Time, typ string, payload map[string]any) {
		b, _ := json.Marshal(map[string]any{"timestamp": iso(ts), "type": typ, "payload": payload})
		buf.Write(b)
		buf.WriteByte('\n')
	}
	emit(s.Start.Add(-500*time.Millisecond), "session_meta", map[string]any{
		"id": s.ID, "timestamp": iso(s.Start), "cwd": cwd, "originator": "codex-tui", "cli_version": codexVersion,
		"source": "cli", "model_provider": "openai",
	})
	total := map[string]int{"input_tokens": 0, "cached_input_tokens": 0, "output_tokens": 0}
	for ti, tr := range s.Turns {
		turnID := fmt.Sprintf("%s-t%d", s.ID[:8], ti+1)
		emit(tr.At, "event_msg", map[string]any{"type": "task_started", "turn_id": turnID, "started_at": float64(tr.At.UnixMilli()) / 1000,
			"model_context_window": 272000, "collaboration_mode_kind": "default"})
		emit(tr.At.Add(50*time.Millisecond), "turn_context", map[string]any{"turn_id": turnID, "cwd": cwd, "model": s.Model,
			"effort": "medium", "approval_policy": "on-request", "sandbox_policy": map[string]any{"type": "workspace-write"}, "summary": "auto"})
		emit(tr.At.Add(100*time.Millisecond), "response_item", map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": tr.Prompt}}})
		if tr.Skill != "" {
			emit(tr.At.Add(120*time.Millisecond), "response_item", map[string]any{"type": "message", "role": "user",
				"content": []any{map[string]any{"type": "input_text", "text": skillInjection(homeDir, tr.Skill)}}})
		}
		last := ""
		for _, req := range tr.Requests {
			at := req.End
			if req.Say != "" {
				emit(at, "response_item", map[string]any{"type": "message", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": req.Say}}})
				last = req.Say
			}
			for k, a := range req.Actions {
				item, _ := codexTool(tr.Task, cwd, a, req.ToolIDs[k])
				emit(at.Add(time.Duration(k+1)*10*time.Millisecond), "response_item", item)
			}
			total["input_tokens"] += req.In
			total["cached_input_tokens"] += req.Cached
			total["output_tokens"] += req.Out
			lastUsage := map[string]int{"input_tokens": req.In, "cached_input_tokens": req.Cached, "output_tokens": req.Out}
			emit(at.Add(100*time.Millisecond), "event_msg", map[string]any{"type": "token_count", "info": map[string]any{
				"total_token_usage": copyUsage(total), "last_token_usage": lastUsage, "model_context_window": 272000}, "rate_limits": nil})
			done := at
			for k, a := range req.Actions {
				done = done.Add(req.ToolTook[k])
				emit(done, "response_item", codexOutput(tr.Task, cwd, a, req.ToolIDs[k], req.ToolTook[k]))
			}
		}
		end := tr.Requests[len(tr.Requests)-1].End
		emit(end.Add(200*time.Millisecond), "event_msg", map[string]any{"type": "task_complete", "turn_id": turnID,
			"completed_at": float64(end.UnixMilli()) / 1000, "duration_ms": end.Sub(tr.At).Milliseconds(), "last_agent_message": last})
	}
	return buf.Bytes()
}

func copyUsage(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
