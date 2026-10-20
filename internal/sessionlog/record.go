package sessionlog

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// CollectVersion is the collection rule set this client applies, written onto
// every line it keeps fields on (cctraceCollect).
//
// The mark is per row because nothing else can say which rows these rules
// reached. Some kept counts are absent when they are zero -- Claude Code omits
// pendingBackgroundAgentCount then (it is on 807 of 5,832 local turn_duration
// lines, all of them >= 1), and imagePasteCount is omitempty here -- so on a row
// collected before this client an absent count means "never looked" and on a row
// collected by it the same absence means 0. Derived code may read absence as 0
// only where the mark is, and only for a field cctraceDropped does not list.
// cctrace_version cannot carry this: re-enrichment adds these fields to old rows
// but deliberately never rewrites that column (see ReenrichSessionRecords), so a
// row can hold the fields under a version that did not collect them.
// Version 2 adds structured git operations and call-proven commit snapshots.
// Version 3 keeps injected instructions, hook output and system content for
// record inspection, and explicitly marks fields cut by collection limits.
// Version 4 keeps native API request references, wire inputs and stop-hook summaries.
const CollectVersion = 4

// CollectedKeys are the top-level keys keepCollected can add to a line's raw:
// the json names of collectedFields, less imagePasteIds, which is read only to
// be counted. Re-enrichment sends these keys alone (CollectedRaw) and the server
// merges these into a stored row and no others (ReenrichSessionRecords).
// Everything else a stored row lacks -- message on a row stored without one, a
// key some later Claude Code adds -- is not this client's to add after the fact.
var CollectedKeys = []string{
	"promptId", "origin", "turnOrigin", "turnPosition", "permissionMode", "toolDenialKind",
	"interruptedMessageId", "imagePasteCount", "toolUseResult",
	"requestId", "isApiErrorMessage", "error",
	"requestRef", "apiBlockIndex", "wireToolInputs", "wireIngestContext", "effort",
	"apiErrorStatus", "apiError", "apiErrorParams", "apiErrorIsTransient",
	"id", "querySource", "shapeHash", "params", "base", "keep", "tail", "cacheControl",
	"requestDigest", "cut", "cutRequestDigest", "withheld", "hash", "shape",
	"hookCount", "hookInfos", "hookErrors", "hookAdditionalContext", "preventedContinuation", "stopReason", "hasOutput", "level", "toolUseID",
	"cctraceReferences",
	"subtype", "logicalParentUuid", "durationMs", "messageCount", "pendingBackgroundAgentCount",
	"pendingWorkflowCount", "content", "compactMetadata",
	"attachment",
	"cctraceCollect", "cctraceDropped", "cctraceWithheld", "cctraceTruncated",
}

// Caps on selected legacy text fields; their full text stays in the local
// JSONL. Instruction files and hook payloads retain their contents within the
// scanner's line limit. An upload body limit holds the tail instead of cutting.
const (
	maxErrorBytes        = 2 << 10
	maxAwaySummaryBytes  = 4 << 10
	maxPlanContentBytes  = 16 << 10
	maxQueuedPromptBytes = 16 << 10
)

// Record is a single line from a Claude Code session JSONL file.
type Record struct {
	Type             string          `json:"type"`
	Timestamp        time.Time       `json:"timestamp"`
	UUID             string          `json:"uuid"`
	ParentUUID       string          `json:"parentUuid,omitempty"`
	SessionID        string          `json:"sessionId"`
	CWD              string          `json:"cwd"`
	Version          string          `json:"version"`
	GitBranch        string          `json:"gitBranch"`
	IsSidechain      bool            `json:"isSidechain,omitempty"`
	AgentID          string          `json:"agentId,omitempty"`
	ForkedFrom       *ForkRef        `json:"forkedFrom,omitempty"`
	IsCompactSummary bool            `json:"isCompactSummary,omitempty"`
	IsMeta           bool            `json:"isMeta,omitempty"`
	PromptSource     string          `json:"promptSource,omitempty"`
	Entrypoint       string          `json:"entrypoint,omitempty"`
	AttributionSkill string          `json:"attributionSkill,omitempty"`
	Message          json.RawMessage `json:"message"`

	collectedFields

	// RawLine is the line this record was parsed from, kept for identity only.
	// The struct has no catch-all, so metadata lines lose their payload on the way
	// in: two different last-prompt lines marshal back to identical bytes. Anything
	// that needs to tell such records apart has to look at what was actually read.
	RawLine []byte `json:"-"`

	// Offset is the byte position in the file where this record's line starts.
	// A scan's newOffset says where the whole scan ended; this says where each
	// record began, so a caller can consume a tail up to any record and resume
	// from that record later.
	// Offset is also what re-enrichment, which reads every file from byte zero,
	// uses to leave alone the prefix sync never collected
	// (FileState.SkippedAtFirstSync, CollectedFrom).
	Offset int64 `json:"-"`
}

// collectedFields are what a line says about its turn beyond the message: the
// signals the session analysis reads (which prompt a line belongs to, how a turn
// ended, what was left running, which plan and model were in force). Before
// CollectVersion the struct had no home for any of them and they were dropped on
// the way in.
//
// Every field read from the line is json.RawMessage on the way in, whatever its
// final shape (what this client derives is written, never read: see derivedInt).
// Record decodes every line type, and these names are not reserved for the types
// that carry the signals: content is a string on system lines and on
// queue-operation lines, toolUseResult is an object, a string or an array
// (27,259 / 1,313 / 57 in a local sample). A typed field would fail the whole
// line on the first shape it did not expect, and the scanner drops a line that
// fails to decode -- one format change in a field we only carry would silently
// stop collecting the conversation around it. Raw bytes cannot fail;
// keepCollected checks the shape afterwards and drops only the field.
//
// Embedded so keepCollected can reset all of them in one statement: a field
// added here is dropped unless a rule below keeps it, rather than kept unless
// someone remembers to clear it.
type collectedFields struct {
	// user
	PromptID             json.RawMessage `json:"promptId,omitempty"`
	Origin               json.RawMessage `json:"origin,omitempty"`
	TurnOrigin           json.RawMessage `json:"turnOrigin,omitempty"`
	TurnPosition         json.RawMessage `json:"turnPosition,omitempty"`
	PermissionMode       json.RawMessage `json:"permissionMode,omitempty"`
	ToolDenialKind       json.RawMessage `json:"toolDenialKind,omitempty"`
	InterruptedMessageID json.RawMessage `json:"interruptedMessageId,omitempty"`
	// ImagePasteIDs is read only to be counted; keepCollected always clears it.
	ImagePasteIDs   json.RawMessage `json:"imagePasteIds,omitempty"`
	ImagePasteCount derivedInt      `json:"imagePasteCount,omitempty"`
	ToolUseResult   json.RawMessage `json:"toolUseResult,omitempty"`

	// assistant
	RequestID         json.RawMessage `json:"requestId,omitempty"`
	IsAPIErrorMessage json.RawMessage `json:"isApiErrorMessage,omitempty"`
	Error             json.RawMessage `json:"error,omitempty"`

	// Native assistant/API request metadata. Payloads stay raw beneath these
	// explicit roots; new top-level fields do not silently enter collection.
	RequestRef          json.RawMessage `json:"requestRef,omitempty"`
	APIBlockIndex       json.RawMessage `json:"apiBlockIndex,omitempty"`
	WireToolInputs      json.RawMessage `json:"wireToolInputs,omitempty"`
	WireIngestContext   json.RawMessage `json:"wireIngestContext,omitempty"`
	Effort              json.RawMessage `json:"effort,omitempty"`
	APIErrorStatus      json.RawMessage `json:"apiErrorStatus,omitempty"`
	APIError            json.RawMessage `json:"apiError,omitempty"`
	APIErrorParams      json.RawMessage `json:"apiErrorParams,omitempty"`
	APIErrorIsTransient json.RawMessage `json:"apiErrorIsTransient,omitempty"`
	ID                  json.RawMessage `json:"id,omitempty"`
	QuerySource         json.RawMessage `json:"querySource,omitempty"`
	ShapeHash           json.RawMessage `json:"shapeHash,omitempty"`
	Params              json.RawMessage `json:"params,omitempty"`
	Base                json.RawMessage `json:"base,omitempty"`
	Keep                json.RawMessage `json:"keep,omitempty"`
	Tail                json.RawMessage `json:"tail,omitempty"`
	CacheControl        json.RawMessage `json:"cacheControl,omitempty"`
	RequestDigest       json.RawMessage `json:"requestDigest,omitempty"`
	Cut                 json.RawMessage `json:"cut,omitempty"`
	CutRequestDigest    json.RawMessage `json:"cutRequestDigest,omitempty"`
	Withheld            json.RawMessage `json:"withheld,omitempty"`
	Hash                json.RawMessage `json:"hash,omitempty"`
	Shape               json.RawMessage `json:"shape,omitempty"`

	// Stop-hook summary signals and inspectable hook payloads.
	HookCount             json.RawMessage `json:"hookCount,omitempty"`
	HookInfos             json.RawMessage `json:"hookInfos,omitempty"`
	HookErrors            json.RawMessage `json:"hookErrors,omitempty"`
	HookAdditionalContext json.RawMessage `json:"hookAdditionalContext,omitempty"`
	PreventedContinuation json.RawMessage `json:"preventedContinuation,omitempty"`
	StopReason            json.RawMessage `json:"stopReason,omitempty"`
	HasOutput             json.RawMessage `json:"hasOutput,omitempty"`
	Level                 json.RawMessage `json:"level,omitempty"`
	ToolUseID             json.RawMessage `json:"toolUseID,omitempty"`
	CctraceReferences     derivedJSON     `json:"cctraceReferences,omitempty"`

	// system
	Subtype                     json.RawMessage `json:"subtype,omitempty"`
	LogicalParentUUID           json.RawMessage `json:"logicalParentUuid,omitempty"`
	DurationMs                  json.RawMessage `json:"durationMs,omitempty"`
	MessageCount                json.RawMessage `json:"messageCount,omitempty"`
	PendingBackgroundAgentCount json.RawMessage `json:"pendingBackgroundAgentCount,omitempty"`
	PendingWorkflowCount        json.RawMessage `json:"pendingWorkflowCount,omitempty"`
	Content                     json.RawMessage `json:"content,omitempty"`
	CompactMetadata             json.RawMessage `json:"compactMetadata,omitempty"`

	// attachment
	Attachment json.RawMessage `json:"attachment,omitempty"`

	CctraceCollect derivedInt `json:"cctraceCollect,omitempty"`
	// CctraceDropped names the fields the line held in a shape their rule does
	// not read, which keepCollected therefore dropped. Their absence from raw
	// says nothing about the line, so it is not 0 either.
	CctraceDropped derivedStrings `json:"cctraceDropped,omitempty"`
	// CctraceWithheld names the content fields the line held that a default
	// re-enrichment kept on the machine (WithholdContent). Merged like every
	// collected key, so it stays as the first run wrote it. Read a field's value
	// first: a listed field that is present is present -- added by a later
	// --include-content run, or already on a row a collecting client stored
	// whole, which a default run still stamps because the list is the one key that
	// row lacks. Only a listed field that is absent was withheld, and only that
	// absence says nothing about the line.
	CctraceWithheld derivedStrings `json:"cctraceWithheld,omitempty"`
	// CctraceTruncated names fields whose content exceeded a collection cap.
	// An absent suffix must never look like the original record ended there.
	CctraceTruncated derivedStrings `json:"cctraceTruncated,omitempty"`
}

// derivedInt and derivedStrings hold what this client derives and writes into
// raw, never what a line says: decoding ignores whatever the line has under the
// same name. Record is decoded with encoding/json, which matches keys without
// regard to case and fails the whole line on a value of the wrong type, and the
// scanner drops a line that fails -- so with a plain int here, a line carrying
// "imagePasteCount":"three", or "CCTRACECOLLECT":1.5, was not collected at all.
type derivedInt int

func (*derivedInt) UnmarshalJSON([]byte) error { return nil }

type derivedStrings []string

func (*derivedStrings) UnmarshalJSON([]byte) error { return nil }

// derivedJSON can only be set by the collector, never by a JSONL claim.
type derivedJSON json.RawMessage

func (*derivedJSON) UnmarshalJSON([]byte) error    { return nil }
func (v derivedJSON) MarshalJSON() ([]byte, error) { return json.RawMessage(v).MarshalJSON() }

// toolUseResultKeys are the toolUseResult members kept, chosen by key rather than
// by tool: the answers of AskUserQuestion, and whether an Agent or Bash call went
// to the background and how it ended. Picking by key means the scan never has to
// remember which tool_use a result answers. Everything else in toolUseResult --
// stdout, file contents, patches -- is the tool output message.content already
// carries.
var toolUseResultKeys = []string{"answers", "annotations", "isAsync", "status", "agentId", "agent_id", "backgroundTaskId", "interrupted"}

// attachmentKeys lists the supported payloads explicitly. Instructions and hook
// output are needed to inspect what the harness actually supplied; unrelated
// attachment types and unrecognized members are not collected wholesale.
var attachmentKeys = map[string][]string{
	"task_status":             {"type", "taskId", "taskType", "status"},
	"task_reminder":           {"type", "content"},
	"queued_command":          {"type", "prompt", "commandMode", "origin"},
	"plan_mode":               {"type", "planFilePath", "planExists"},
	"plan_mode_exit":          {"type"},
	"plan_file_reference":     {"type", "planFilePath", "planContent"},
	"model":                   {"type", "identity"},
	"instructions":            {"type", "files"},
	"hook_success":            {"type", "hookName", "toolUseID", "hookEvent", "content", "stdout", "stderr", "exitCode", "command", "durationMs"},
	"hook_additional_context": {"type", "hookName", "toolUseID", "hookEvent", "content"},
	"async_hook_response":     {"type", "processId", "hookName", "hookEvent", "toolName", "response", "stdout", "stderr", "exitCode"},
	"hook_system_message":     {"type", "hookName", "toolUseID", "hookEvent", "content"},
}

// The kept fields are of two kinds. Signals say what happened: which prompt a
// line belongs to, how a turn ended, what was left running, which mode and model
// were in force. Content is what was said, planned or tracked: the answers a
// person gave, a prompt queued while the agent was busy (and a background task's
// result, queued the same way), a plan file and its path, task subjects, and
// system messages, injected instruction files and hook output. WithholdContent
// removes these content fields before default re-enrichment.
var (
	toolUseResultContentKeys = []string{"answers", "annotations"}
	attachmentContentKeys    = map[string][]string{
		"task_reminder":           {"content"},
		"queued_command":          {"prompt"},
		"plan_mode":               {"planFilePath"},
		"plan_file_reference":     {"planFilePath", "planContent"},
		"instructions":            {"files"},
		"hook_success":            {"content", "stdout", "stderr", "command"},
		"hook_additional_context": {"content"},
		"async_hook_response":     {"response", "stdout", "stderr"},
		"hook_system_message":     {"content"},
	}
)

// keepCollected applies the collection rules to a line just parsed. It runs once,
// where lines are decoded (ScanFileContext), so every record that reaches raw has
// been through it.
//
// User, assistant, system and attachment lines that carry a uuid, and native
// API protocol lines keyed by their original RawLine digest, keep fields. The
// storage key never depends on the collected fields, so re-enrichment can find
// the same row again to merge them in. Other lines
// without a uuid are keyed by a digest of the line itself; metadata lines among
// them use some of these names too (permission-mode lines carry permissionMode,
// queue-operation lines carry content); they keep nothing, are not marked, and
// marshal exactly as they did before.
//
// A field the line holds in a shape its rule does not read is dropped and named
// in cctraceDropped, so the mark never vouches for an absence the rules caused.
// A null counts as absent, not as a shape.
func (r *Record) keepCollected() {
	in := r.collectedFields
	r.collectedFields = collectedFields{}
	if r.UUID == "" && !isRequestProtocol(r.Type) {
		return
	}
	var dropped []string
	// keep returns what a rule kept of a field, naming the field as dropped when
	// the line held it and the rule kept nothing.
	keep := func(name string, v, kept json.RawMessage) json.RawMessage {
		if kept == nil && present(v) {
			dropped = append(dropped, name)
		}
		return kept
	}
	switch r.Type {
	case "user":
		r.PromptID = keep("promptId", in.PromptID, jsonString(in.PromptID))
		// Only the kind: the rest of origin names the sender and, for messages
		// from another session, carries the message body itself.
		r.Origin = keep("origin", in.Origin, pick(in.Origin, "kind"))
		r.TurnOrigin = keep("turnOrigin", in.TurnOrigin, jsonString(in.TurnOrigin))
		r.TurnPosition = keep("turnPosition", in.TurnPosition, pick(in.TurnPosition, "promptIndex", "turnIndex"))
		r.PermissionMode = keep("permissionMode", in.PermissionMode, jsonString(in.PermissionMode))
		r.ToolDenialKind = keep("toolDenialKind", in.ToolDenialKind, jsonString(in.ToolDenialKind))
		r.InterruptedMessageID = keep("interruptedMessageId", in.InterruptedMessageID, jsonString(in.InterruptedMessageID))
		var ids []json.RawMessage
		switch {
		case json.Unmarshal(in.ImagePasteIDs, &ids) == nil:
			r.ImagePasteCount = derivedInt(len(ids))
		case present(in.ImagePasteIDs):
			dropped = append(dropped, "imagePasteCount")
		}
		r.ToolUseResult = pick(in.ToolUseResult, toolUseResultKeys...)
		if raw := members(in.ToolUseResult, "gitOperation")["gitOperation"]; present(raw) {
			operation, unread := keepGitOperation(raw)
			kept := members(r.ToolUseResult, toolUseResultKeys...)
			if kept == nil {
				kept = make(map[string]json.RawMessage)
			}
			setMember(kept, "gitOperation", operation)
			r.ToolUseResult = marshalMembers(kept)
			if unread {
				dropped = append(dropped, "toolUseResult.gitOperation")
			}
		}
		// Most results are objects holding none of the kept keys, and a string
		// (an error) or an array (MCP content) is a result shape that holds none
		// of them: those are read, and say nothing. Any other shape is unread.
		if v := in.ToolUseResult; present(v) && v[0] != '{' && v[0] != '"' && v[0] != '[' {
			dropped = append(dropped, "toolUseResult")
		}
	case "assistant":
		r.keepNativeAssistant(in, keep)
		r.RequestID = keep("requestId", in.RequestID, jsonString(in.RequestID))
		r.IsAPIErrorMessage = keep("isApiErrorMessage", in.IsAPIErrorMessage, jsonBool(in.IsAPIErrorMessage))
		r.Error = keep("error", in.Error, capJSON(in.Error, maxErrorBytes))
		if r.Error != nil && !bytes.Equal(in.Error, r.Error) {
			r.CctraceTruncated = append(r.CctraceTruncated, "error")
		}
	case "system":
		r.keepStopSummary(in, keep)
		r.Subtype = keep("subtype", in.Subtype, jsonString(in.Subtype))
		r.LogicalParentUUID = keep("logicalParentUuid", in.LogicalParentUUID, jsonString(in.LogicalParentUUID))
		r.DurationMs = keep("durationMs", in.DurationMs, jsonNumber(in.DurationMs))
		r.MessageCount = keep("messageCount", in.MessageCount, jsonNumber(in.MessageCount))
		r.PendingBackgroundAgentCount = keep("pendingBackgroundAgentCount", in.PendingBackgroundAgentCount, jsonNumber(in.PendingBackgroundAgentCount))
		r.PendingWorkflowCount = keep("pendingWorkflowCount", in.PendingWorkflowCount, jsonNumber(in.PendingWorkflowCount))
		// System messages are inspectable too. Retain the existing recap cap,
		// with a marker whenever that cap removes part of the source.
		var subtype string
		if json.Unmarshal(r.Subtype, &subtype) == nil && subtype == "away_summary" {
			r.Content = keep("content", in.Content, capString(in.Content, maxAwaySummaryBytes))
			if r.Content != nil && !bytes.Equal(in.Content, r.Content) {
				r.CctraceTruncated = append(r.CctraceTruncated, "content")
			}
		} else if present(in.Content) {
			r.Content = in.Content
		}
		r.CompactMetadata = keep("compactMetadata", in.CompactMetadata, pick(in.CompactMetadata, "trigger", "preTokens", "postTokens"))
	case "api-request", "api-request-shape", "api-request-blob":
		r.keepNativeRequest(in, keep)
	case "attachment":
		var unread, truncated []string
		r.Attachment, unread, truncated = keepAttachment(in.Attachment)
		dropped = append(dropped, unread...)
		r.CctraceTruncated = truncated
	default:
		return
	}
	r.CctraceCollect = CollectVersion
	r.CctraceDropped = dropped
}

// WithholdContent removes the content fields from a record keepCollected has run
// on -- the toolUseResult answers, a queued prompt or background task result,
// plan files and plan paths, task subjects, system messages, instructions and
// hook output -- leaves every signal
// field in place, and names what it removed in cctraceWithheld. A kept object
// left with nothing (toolUseResult holding only answers) goes too.
func (r *Record) WithholdContent() {
	var withheld, removed []string
	r.ToolUseResult, removed = without(r.ToolUseResult, toolUseResultContentKeys...)
	for _, key := range removed {
		withheld = append(withheld, "toolUseResult."+key)
	}
	// Top-level content is kept on system lines only.
	if r.Content != nil {
		withheld = append(withheld, "content")
		r.Content = nil
	}
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(r.Attachment, &head) == nil {
		if keys := attachmentContentKeys[head.Type]; keys != nil {
			r.Attachment, removed = without(r.Attachment, keys...)
			for _, key := range removed {
				withheld = append(withheld, "attachment."+key)
			}
		}
	}
	withheld = append(withheld, r.withholdNativeContent()...)
	r.CctraceWithheld = withheld
}

// CollectedRaw is a line's raw as re-enrichment sends it: the collected fields
// alone, under the keys in CollectedKeys. Native blob messages are the sole
// exception: their role/content payload is explicitly collected and merges only
// on api-request-blob rows. Ordinary conversation messages never leave the
// machine through reenrichment. Top-level content is a system line's message.
func (r *Record) CollectedRaw() json.RawMessage {
	var content any = r.collectedFields
	if r.Type == "api-request-blob" && present(r.Message) {
		content = struct {
			collectedFields
			Message json.RawMessage `json:"message,omitempty"`
		}{r.collectedFields, r.Message}
	}
	out, err := json.Marshal(content)
	if err != nil {
		return json.RawMessage("{}")
	}
	return out
}

// keepAttachment returns the members attachmentKeys lists for the attachment's
// type, or nil for any other type, and the fields it could not read: the whole
// attachment when it is not an object with a string type, or attachment.<member>
// for a listed member in a shape its rule does not read.
func keepAttachment(raw json.RawMessage) (json.RawMessage, []string, []string) {
	if !present(raw) {
		return nil, nil, nil
	}
	// Read the type alone first: the attachments dropped whole are almost all of
	// them, and decoding this way skips their payloads instead of copying them.
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return nil, []string{"attachment"}, nil
	}
	keys, ok := attachmentKeys[head.Type]
	if !ok {
		return nil, nil, nil
	}
	kept := members(raw, keys...)
	var dropped, truncated []string
	// apply replaces a member with what its rule keeps of it, naming the member
	// as dropped when it was there and the rule kept nothing.
	apply := func(key string, v json.RawMessage) {
		if v == nil && present(kept[key]) {
			dropped = append(dropped, "attachment."+key)
		}
		setMember(kept, key, v)
	}
	switch head.Type {
	case "task_reminder":
		apply("content", pickEach(kept["content"], "id", "subject", "status"))
	case "queued_command":
		prompt, images, cut := queuedPrompt(kept["prompt"])
		if cut {
			truncated = append(truncated, "attachment.prompt")
		}
		apply("prompt", prompt)
		if images > 0 {
			kept["imageCount"] = json.RawMessage(strconv.Itoa(images))
		}
		// Same shape as a user line's origin, body included, and no redaction
		// rule names body: keeping more than the kind would ship the text of a
		// message from another session under every policy.
		apply("origin", pick(kept["origin"], "kind"))
	case "plan_file_reference":
		original := kept["planContent"]
		apply("planContent", capString(kept["planContent"], maxPlanContentBytes))
		if kept["planContent"] != nil && !bytes.Equal(original, kept["planContent"]) {
			truncated = append(truncated, "attachment.planContent")
		}
	case "instructions":
		apply("files", pickEach(kept["files"], "path", "type", "content"))
	case "model":
		apply("identity", pick(kept["identity"], "modelId"))
	}
	return marshalMembers(kept), dropped, truncated
}

// queuedPrompt returns the text of a prompt queued while the agent was busy, cut
// like planContent, and how many images it carried. A prompt with an image
// pasted into it is an array of content blocks with the image inside as base64,
// and 131 of 2,700 local queued prompts run past 16 KiB (the largest, 668 KB):
// the text blocks are joined in order, a newline apart, and the images only
// counted, so no image data is ever kept.
func queuedPrompt(v json.RawMessage) (json.RawMessage, int, bool) {
	text, images := queuedPromptText(v)
	if text == nil {
		return nil, images, false
	}
	out, err := json.Marshal(truncateUTF8(string(text), maxQueuedPromptBytes))
	if err != nil {
		return nil, 0, false
	}
	return out, images, len(text) > maxQueuedPromptBytes
}

func queuedPromptText(v json.RawMessage) ([]byte, int) {
	if jsonString(v) != nil {
		var text string
		if json.Unmarshal(v, &text) != nil {
			return nil, 0
		}
		return []byte(text), 0
	}
	if len(v) == 0 || v[0] != '[' {
		return nil, 0
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(v, &blocks) != nil {
		return nil, 0
	}
	var text []string
	images := 0
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "image":
			images++
		}
	}
	return []byte(strings.Join(text, "\n")), images
}

// members returns the named members of a JSON object, or nil when obj is not one.
func members(obj json.RawMessage, keys ...string) map[string]json.RawMessage {
	if len(obj) == 0 {
		return nil
	}
	var all map[string]json.RawMessage
	if json.Unmarshal(obj, &all) != nil || all == nil {
		return nil
	}
	kept := make(map[string]json.RawMessage, len(keys))
	for _, key := range keys {
		if v, ok := all[key]; ok {
			kept[key] = v
		}
	}
	return kept
}

// pick keeps the named members of a JSON object. It returns nil when obj is not an
// object or has none of them, so the field is left out rather than sent as {}.
func pick(obj json.RawMessage, keys ...string) json.RawMessage {
	return marshalMembers(members(obj, keys...))
}

// pickEach applies pick to every element of a JSON array, dropping elements that
// keep nothing. An empty array stays an empty array: an empty task list is a
// statement that there are no tasks, which an absent field is not.
func pickEach(arr json.RawMessage, keys ...string) json.RawMessage {
	var items []json.RawMessage
	if len(arr) == 0 || arr[0] != '[' || json.Unmarshal(arr, &items) != nil {
		return nil
	}
	kept := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		if p := pick(item, keys...); p != nil {
			kept = append(kept, p)
		}
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return nil
	}
	return out
}

func marshalMembers(m map[string]json.RawMessage) json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return out
}

func setMember(m map[string]json.RawMessage, key string, v json.RawMessage) {
	if v == nil {
		delete(m, key)
		return
	}
	m[key] = v
}

// without returns a JSON object less the named members, or nil when nothing is
// left of it, and which of the members it held. Anything that is not an object
// comes back as it was.
func without(obj json.RawMessage, keys ...string) (json.RawMessage, []string) {
	var all map[string]json.RawMessage
	if len(obj) == 0 || json.Unmarshal(obj, &all) != nil || all == nil {
		return obj, nil
	}
	var removed []string
	for _, key := range keys {
		if _, ok := all[key]; ok {
			removed = append(removed, key)
			delete(all, key)
		}
	}
	return marshalMembers(all), removed
}

// present reports whether a line held a value: a missing key and a null are both
// absent.
func present(v json.RawMessage) bool {
	return len(v) > 0 && string(v) != "null"
}

// jsonString, jsonNumber and jsonBool keep a value of the expected JSON type and
// drop anything else, null included. The decoder hands raw values over without
// surrounding whitespace, so the first byte decides.
func jsonString(v json.RawMessage) json.RawMessage {
	if len(v) > 0 && v[0] == '"' {
		return v
	}
	return nil
}

func jsonNumber(v json.RawMessage) json.RawMessage {
	if len(v) > 0 && (v[0] == '-' || ('0' <= v[0] && v[0] <= '9')) {
		return v
	}
	return nil
}

func jsonBool(v json.RawMessage) json.RawMessage {
	if s := string(v); s == "true" || s == "false" {
		return v
	}
	return nil
}

// capString keeps a JSON string, cut to at most max bytes of text on a UTF-8
// boundary, and drops any other value.
func capString(v json.RawMessage, max int) json.RawMessage {
	var s string
	if jsonString(v) == nil || json.Unmarshal(v, &s) != nil {
		return nil
	}
	if len(s) <= max {
		return v
	}
	out, err := json.Marshal(truncateUTF8(s, max))
	if err != nil {
		return nil
	}
	return out
}

// capJSON keeps a JSON value of any type, null excepted, within max bytes. A
// string is cut like capString. Anything else that is too long is kept as the
// head of its text, as a string: the field's presence is the signal (the turn
// ended in an error), and dropping it would lose that along with the excess.
func capJSON(v json.RawMessage, max int) json.RawMessage {
	if len(v) == 0 || string(v) == "null" {
		return nil
	}
	if len(v) <= max {
		return v
	}
	if jsonString(v) != nil {
		return capString(v, max)
	}
	out, err := json.Marshal(truncateUTF8(string(v), max))
	if err != nil {
		return nil
	}
	return out
}

func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// ForkRef links a branched session back to its origin. Present on the first
// records of a session created via /branch: forkedFrom.sessionId is the origin
// session and forkedFrom.messageUuid is the message the branch diverged at.
type ForkRef struct {
	SessionID   string `json:"sessionId"`
	MessageUUID string `json:"messageUuid"`
}

// AssistantMessage is the parsed message for type="assistant".
type AssistantMessage struct {
	Model string     `json:"model"`
	Usage TokenUsage `json:"usage"`
}

// TokenUsage holds token counts from an assistant message.
type TokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// ParseCommandName extracts the slash command name from a user message.
// Returns empty string if the message is not a slash command invocation.
// Handles content like: <command-name>/oh-my-claudecode:ralph</command-name>
func (r *Record) ParseCommandName() string {
	if r.Type != "user" || len(r.Message) == 0 {
		return ""
	}
	// message.content is a string (not array) for command records
	var msg struct {
		Content interface{} `json:"content"`
	}
	if err := json.Unmarshal(r.Message, &msg); err != nil {
		return ""
	}
	content, ok := msg.Content.(string)
	if !ok {
		return ""
	}
	// Extract <command-name>...</command-name>
	start := strings.Index(content, "<command-name>")
	end := strings.Index(content, "</command-name>")
	if start == -1 || end == -1 || end <= start {
		return ""
	}
	name := content[start+len("<command-name>") : end]
	// Strip leading slash
	return strings.TrimPrefix(name, "/")
}

// IsToolResultUser reports whether a Claude user record is a tool result rather
// than a new human prompt. Claude stores tool results as role=user messages.
func (r *Record) IsToolResultUser() bool {
	if r.Type != "user" || len(r.Message) == 0 {
		return false
	}
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(r.Message, &msg); err != nil || len(msg.Content) == 0 {
		return false
	}
	var items []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(msg.Content, &items); err != nil {
		return false
	}
	for _, item := range items {
		if item.Type == "tool_result" {
			return true
		}
	}
	return false
}

// ParseAssistantMessage parses the Message field for type="assistant".
// Returns nil if parsing fails.
func (r *Record) ParseAssistantMessage() *AssistantMessage {
	if r.Type != "assistant" || len(r.Message) == 0 {
		return nil
	}
	var m AssistantMessage
	if err := json.Unmarshal(r.Message, &m); err != nil {
		return nil
	}
	return &m
}
