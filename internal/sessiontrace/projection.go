package sessiontrace

import (
	"bytes"
	"encoding/json"
	"strings"
)

// projection reads classification metadata, never copies content into Events.
// Production callers supply Blocks or a body-free projection of the same shape.
func projection(r Record) (Record, []Block) {
	if r.Blocks != nil {
		return r, textFlag(r.Blocks, r.HasText)
	}
	var raw struct {
		Type          string          `json:"type"`
		Role          string          `json:"role"`
		HasText       bool            `json:"has_text"`
		Message       json.RawMessage `json:"message"`
		Payload       json.RawMessage `json:"payload"`
		ToolUseResult struct {
			AgentID    string `json:"agentId"`
			AltAgentID string `json:"agent_id"`
		} `json:"toolUseResult"`
	}
	if len(r.Raw) > 0 {
		if json.Unmarshal(r.Raw, &raw) != nil {
			return r, nil
		}
		if r.RecordType == "" {
			r.RecordType = raw.Type
		}
		if r.Role == "" {
			r.Role = raw.Role
		}
		if r.ChildAgentID == "" {
			r.ChildAgentID = raw.ToolUseResult.AgentID
			if r.ChildAgentID == "" {
				r.ChildAgentID = raw.ToolUseResult.AltAgentID
			}
		}
	}
	if r.Agent == "codex" {
		var payload struct {
			Type       string          `json:"type"`
			Role       string          `json:"role"`
			CallID     string          `json:"call_id"`
			Name       string          `json:"name"`
			Status     string          `json:"status"`
			TurnID     string          `json:"turn_id"`
			RootTurnID string          `json:"root_turn_id"`
			HasText    bool            `json:"has_text"`
			Content    json.RawMessage `json:"content"`
			Message    json.RawMessage `json:"message"`
		}
		if len(raw.Payload) > 0 {
			_ = json.Unmarshal(raw.Payload, &payload)
		}
		if r.PayloadType == "" {
			r.PayloadType = payload.Type
		}
		if r.Role == "" {
			r.Role = payload.Role
		}
		if r.ToolCallID == "" {
			r.ToolCallID = payload.CallID
		}
		if r.ToolName == "" {
			r.ToolName = payload.Name
		}
		if r.ToolStatus == "" {
			r.ToolStatus = payload.Status
		}
		if r.CodexTurnID == "" {
			r.CodexTurnID = payload.TurnID
		}
		if r.CodexRootTurnID == "" {
			r.CodexRootTurnID = payload.RootTurnID
		}
		typeName := r.PayloadType
		if typeName == "" {
			typeName = r.RecordType
		}
		switch typeName {
		case "function_call", "custom_tool_call", "tool_call":
			return r, []Block{{Type: typeName, ID: r.ToolCallID, Name: r.ToolName, Status: r.ToolStatus}}
		case "function_call_output", "custom_tool_call_output", "tool_result":
			return r, []Block{{Type: typeName, ToolUseID: r.ToolCallID, Status: r.ToolStatus}}
		case "user_message":
			r.Role = "user"
			return r, []Block{{Type: "input"}}
		case "agent_message":
			r.Role = "assistant"
			return r, []Block{{Type: "response"}}
		case "message", "user", "assistant":
			content := payload.Content
			if len(payload.Message) > 0 {
				return messageBlocks(r, payload.Message)
			}
			return r, textFlag(contentBlocks(content), r.HasText || payload.HasText)
		}
	}
	if len(raw.Message) > 0 {
		return messageBlocks(r, raw.Message)
	}
	if r.HasText || raw.HasText {
		return r, []Block{{Type: "text", HasText: true}}
	}
	if r.RecordType == "tool_result" {
		return r, []Block{{Type: "tool_result", ToolUseID: r.ToolUseID, Status: r.ToolStatus}}
	}
	return r, nil
}

func messageBlocks(r Record, raw json.RawMessage) (Record, []Block) {
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		HasText bool            `json:"has_text"`
	}
	if json.Unmarshal(raw, &message) != nil {
		return r, nil
	}
	if r.Role == "" {
		r.Role = message.Role
	}
	return r, textFlag(contentBlocks(message.Content), message.HasText || r.HasText)
}

func textFlag(blocks []Block, hasText bool) []Block {
	if !hasText {
		return blocks
	}
	for _, b := range blocks {
		if b.HasText && (b.Type == "text" || b.Type == "input_text" || b.Type == "output_text") {
			return blocks
		}
	}
	// The flag describes string content. Array projections should retain each
	// text block's actual Index instead. Never shift existing block ordinals.
	index := 0
	return append(append([]Block{}, blocks...), Block{Type: "text", HasText: true, Index: &index})
}

func contentBlocks(raw json.RawMessage) []Block {
	var text string
	if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" {
		return []Block{{Type: "text", HasText: true}}
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := make([]Block, 0, len(items))
	for index, item := range items {
		var b Block
		if json.Unmarshal(item, &b) != nil {
			continue
		}
		// Accept the compact existing sessionfacts SQL block vocabulary as well.
		var meta struct {
			Type      string          `json:"t"`
			Name      string          `json:"n"`
			ToolUseID string          `json:"tu"`
			IsError   *bool           `json:"err"`
			Text      json.RawMessage `json:"text"`
		}
		_ = json.Unmarshal(item, &meta)
		if b.Type == "" {
			b.Type = meta.Type
		}
		if b.Name == "" {
			b.Name = meta.Name
		}
		if b.ToolUseID == "" {
			b.ToolUseID = meta.ToolUseID
		}
		if b.IsError == nil {
			b.IsError = meta.IsError
		}
		if b.Index == nil {
			original := index
			b.Index = &original
		}
		if len(meta.Text) > 0 && !bytes.Equal(bytes.TrimSpace(meta.Text), []byte("null")) {
			var text string
			if json.Unmarshal(meta.Text, &text) == nil {
				b.HasText = strings.TrimSpace(text) != ""
			}
		}
		out = append(out, b)
	}
	return out
}
