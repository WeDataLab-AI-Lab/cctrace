package syncer

import (
	"encoding/json"

	"cctrace/internal/store"
)

// RedactPolicy says which parts of a session record must not leave the machine.
// The zero value collects everything, which is what cctrace is for.
type RedactPolicy struct {
	UserPrompts bool
	ToolDetails bool
}

// Enabled reports whether the policy removes anything at all.
func (p RedactPolicy) Enabled() bool { return p.UserPrompts || p.ToolDetails }

// redactedMarker replaces text rather than deleting the key. A missing key is
// indistinguishable from a record that never had one; a marker says a person
// chose this, which is the difference between reading a dashboard as "nothing
// was said here" and "this was withheld".
const redactedMarker = "[redacted by cctrace client]"

// promptBearingKeys are the fields that carry what a person typed. They are
// listed by name rather than matched by shape because the four agents write four
// different record layouts, and a walker that tried to understand each would have
// to be updated for the fifth.
//
// Nothing here overlaps the keys the server reads for accounting -- usage,
// input_tokens, cost, model, ts all survive, so redacting does not distort a
// single number on the dashboard. That separation is the reason this can be a
// blunt key-name walk instead of a per-agent parser.
var promptBearingKeys = map[string]bool{
	"content":  true,
	"text":     true,
	"thinking": true,
	"summary":  true,
	"title":    true,
}

// toolBearingKeys carry tool arguments and results: the file paths, commands and
// outputs a tool call moved around. Separate from prompts because the two are
// separate decisions -- an operator may want the conversation and not the shell
// commands, or the reverse.
var toolBearingKeys = map[string]bool{
	"arguments":   true,
	"input":       true,
	"output":      true,
	"result":      true,
	"command":     true,
	"description": true,
	"stdout":      true,
	"stderr":      true,
}

// Redact removes the configured content from a batch, in place, before it is
// serialised for upload.
//
// It runs here -- at the one point every agent's records pass through on their
// way out -- and not in each syncer. A privacy control implemented four times is
// a control with four chances to be missed, and this repository has already been
// bitten by exactly that: a config key added to the syncers and the CLI but not
// to `cctrace status`, and another added to three enumerations but not the
// fourth. One chokepoint or none.
func Redact(records []*store.SessionRecord, policy RedactPolicy) {
	if !policy.Enabled() {
		return
	}
	for _, r := range records {
		if r == nil {
			continue
		}
		redactRecord(r, policy)
	}
}

func redactRecord(r *store.SessionRecord, policy RedactPolicy) {
	// Raw is the whole reason this exists. Every agent uploads the original JSONL
	// line unconditionally, and the dashboard reads the conversation out of it --
	// so blanking a typed field while leaving Raw intact would remove the text from
	// exactly one place and publish it from another. Whatever else changes here,
	// Raw has to be walked.
	if len(r.Raw) == 0 {
		return
	}
	var doc interface{}
	if err := json.Unmarshal(r.Raw, &doc); err != nil {
		// An unparseable Raw cannot be walked, and shipping it unread would defeat
		// the setting. Withhold the whole payload: losing a record we could not
		// inspect is the safe direction when the operator asked for redaction.
		r.Raw = json.RawMessage(`{"redacted":"` + redactedMarker + `"}`)
		return
	}
	redactPaths(doc, policy)
	if r.RecordType == "system" {
		if line, ok := doc.(map[string]interface{}); ok {
			redactMembers(line, "content")
		}
	}
	redactValue(doc, policy)
	if cleaned, err := json.Marshal(doc); err == nil {
		r.Raw = cleaned
	} else {
		r.Raw = json.RawMessage(`{"redacted":"` + redactedMarker + `"}`)
	}
}

// redactPaths removes content the key-name walk does not reach, by where it sits
// in a Claude line rather than by what its key is called. It runs before the walk
// and only ever replaces values with the same marker.
//
// The walk is only safe for names that mean content wherever they appear. The
// fields collected from toolUseResult and attachments (sessionlog.CollectVersion)
// sit under names that do not: "prompt" is also the Agent tool's input (618 local
// calls) and "name" is every tool call's name (31,317), so adding either to the
// maps above would blank the tool names the dashboard pairs calls with. Each rule
// here names the line shape it applies to and touches nothing else.
//
// The rule is that every representation of the same content gets the same
// treatment. The AskUserQuestion answer is the clearest case: the person's choice
// is in the tool_result text and again, structured, in toolUseResult.answers, and
// redacting one of them protects nothing. A background task's result is another:
// it arrives queued (attachment queued_command, commandMode task-notification)
// and then as a user line of its own (origin.kind task-notification). The
// question and options the model asked, and an ExitPlanMode plan, need no rule
// here: they sit in message.content, which the prompt walk replaces whole, and
// under "input", which the tool walk replaces whole.
//
// Redact runs this only when the policy is enabled, so the rules marked "either
// policy" below hold whenever it runs at all.
func redactPaths(doc interface{}, policy RedactPolicy) {
	line, ok := doc.(map[string]interface{})
	if !ok {
		return
	}
	// Native request shapes and wire arguments contain instruction text, schema
	// property names and paths under arbitrary keys, so a key-name walk cannot
	// protect them. Both policies cover every representation of these payloads.
	redactMembers(line, "wireToolInputs", "wireIngestContext", "error", "apiError", "apiErrorParams", "params")
	if shape, ok := line["shape"].(map[string]interface{}); ok {
		redactMembers(shape, "system", "tools")
	}
	redactMembers(line, "hookInfos", "hookErrors", "hookAdditionalContext", "stopReason")
	// Either policy: the answers are the person's words and the tool's result.
	if result, ok := line["toolUseResult"].(map[string]interface{}); ok {
		redactMembers(result, "answers", "annotations")
	}
	// Either policy: system messages can contain recaps, file paths and command
	// echoes, so their content gets the same treatment planContent gets.
	// Matched on subtype alone: only a system line carries one at the top, and a
	// re-enrichment raw is the collected fields without the line's type.
	if line["subtype"] != nil || line["type"] == "system" {
		redactMembers(line, "content")
	}
	// Codex context records keep the actual instruction fields rather than a
	// complete metadata payload. They can contain prompts, code and paths, so
	// either configured policy applies to every representation.
	if payload, ok := line["payload"].(map[string]interface{}); ok {
		redactMembers(payload, "base_instructions", "developer_instructions", "user_instructions")
		if mode, ok := payload["collaboration_mode"].(map[string]interface{}); ok {
			if settings, ok := mode["settings"].(map[string]interface{}); ok {
				redactMembers(settings, "developer_instructions")
			}
		}
	}
	message, _ := line["message"].(map[string]interface{})
	// Native system blobs retain output configuration in the same instruction
	// payload. Match the role too, as reenrich sends no top-level type.
	if message != nil && message["role"] == "system" {
		redactMembers(message, "content", "output_config")
	}
	// Either policy, for the recap's reason: the summary that opens a session
	// continued after compaction restates the earlier conversation, files and
	// code included.
	if message != nil && line["type"] == "user" && line["isCompactSummary"] == true {
		redactMembers(message, "content")
	}
	// A tool_result block's output is under "content", which only the prompt walk
	// names, so it used to leave the machine under the tool-details policy alone.
	if policy.ToolDetails && message != nil {
		if blocks, ok := message["content"].([]interface{}); ok {
			for _, b := range blocks {
				if block, ok := b.(map[string]interface{}); ok && block["type"] == "tool_result" {
					redactMembers(block, "content")
				}
			}
		}
	}
	// A background task's result comes back to the conversation as a user line
	// of its own: an Agent's summary, a shell's output path. It is tool output,
	// so the tool policy takes it too; the prompt walk already does.
	if policy.ToolDetails && message != nil && line["type"] == "user" {
		if origin, ok := line["origin"].(map[string]interface{}); ok && origin["kind"] == "task-notification" {
			redactMembers(message, "content")
		}
	}
	attachment, ok := line["attachment"].(map[string]interface{})
	if !ok {
		return
	}
	switch attachment["type"] {
	case "instructions":
		redactMembers(attachment, "files")
	case "hook_success", "hook_additional_context", "async_hook_response", "hook_system_message":
		redactMembers(attachment, "content", "stdout", "stderr", "command", "response")
	case "queued_command":
		// What the person typed while the agent was busy -- or, queued the same
		// way, a background task's result on its way to becoming the user line
		// above, which the tool policy takes as it takes that line.
		if policy.UserPrompts || (policy.ToolDetails && attachment["commandMode"] == "task-notification") {
			redactMembers(attachment, "prompt")
		}
	case "plan_file_reference":
		// Either policy: the plan is the person's request worked over by the
		// model, and the same plan the ExitPlanMode input carries.
		redactMembers(attachment, "planContent")
		if policy.ToolDetails {
			redactMembers(attachment, "planFilePath")
		}
	case "plan_mode":
		if policy.ToolDetails {
			redactMembers(attachment, "planFilePath")
		}
	case "task_reminder":
		// Task subjects come from TaskCreate input. Under the prompt policy the
		// walk already replaces content.
		if policy.ToolDetails {
			redactMembers(attachment, "content")
		}
	}
}

// redactMembers replaces the named members that are present. An absent one stays
// absent: a marker on a field the line never had would say something was withheld
// that was not there.
func redactMembers(node map[string]interface{}, keys ...string) {
	for _, key := range keys {
		if _, ok := node[key]; ok {
			node[key] = redactedMarker
		}
	}
}

// redactValue walks the decoded document, replacing the values of content-bearing
// keys wherever they appear. It recurses into everything because these keys sit at
// different depths per agent: Claude nests them under message.content[], gjc under
// message with an attribution sibling, Codex deeper still.
func redactValue(v interface{}, policy RedactPolicy) {
	switch node := v.(type) {
	case map[string]interface{}:
		for key, child := range node {
			if (policy.UserPrompts && promptBearingKeys[key]) ||
				(policy.ToolDetails && toolBearingKeys[key]) {
				node[key] = redactedMarker
				continue
			}
			redactValue(child, policy)
		}
	case []interface{}:
		for _, child := range node {
			redactValue(child, policy)
		}
	}
}
