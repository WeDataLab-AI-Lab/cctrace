package sessionview

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"cctrace/internal/store"
)

// Normalize maps one stored record to its View. It never fails: a raw body it
// cannot read becomes a hidden record, because one bad line must not break the
// rest of the session.
func Normalize(rec *store.SessionRecord) View {
	v := view(rec)
	v.AnchorID = anchorID(rec)
	v.DedupeKey = dedupeKey(rec)
	return v
}

// Summary is what a record is, without its content: for callers such as the Open API.
type Summary struct {
	Kind Kind
	// ToolCallCount counts the calls this record shows. Summing it over a session counts
	// each call once for every agent: Claude/omo/gjc calls sit inside message records,
	// and gjc's sibling tool_call rows are hidden with a count of 0.
	ToolCallCount int
}

// Summarize is Normalize without the anchor and dedupe hashing.
func Summarize(rec *store.SessionRecord) Summary {
	v := view(rec)
	return Summary{Kind: v.Kind, ToolCallCount: len(v.ToolCalls)}
}

func view(rec *store.SessionRecord) View {
	switch rec.Agent {
	case "", "claude":
		return claudeView(rec)
	case "codex":
		return codexView(rec)
	case "omo", "gjc":
		// Both harnesses write the same pi-style message lines.
		return piView(rec)
	}
	var probe struct {
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(rec.Raw, &probe) == nil && isObject(probe.Message) {
		return claudeView(rec)
	}
	return codexView(rec)
}

var hidden = View{Kind: KindHidden}

// --- claude ---

// Local/slash-command scaffolding that Claude Code stores as user records.
// Older records lost the promptSource/isMeta fields that mark it, so the
// content tags are the only tell: command-name becomes a chip, the rest is noise.
var (
	cmdNameRE = regexp.MustCompile(`<command-name>([^<]*)</command-name>`)
	cmdArgsRE = regexp.MustCompile(`<command-args>([^<]*)</command-args>`)
	cmdHideRE = regexp.MustCompile(`^\s*<(local-command-stdout|local-command-stderr|local-command-caveat|command-message|command-args)>`)
)

// Agent-SDK harnesses (omo and others) mark every user record prompt_source
// "sdk", which names the caller, not a human. Such a turn replays the whole
// transcript, appends the real request, and re-serialises tool results as
// prose ("Tool result (Bash, id=toolu_…):"). The id in that prose is the only
// link back to the assistant's tool_use, so results are parsed out for pairing.
//
// The markers are found with plain substring scans rather than regexes: the
// replay grows with every turn, and a regex walking it lazily from the start
// cost ~42ms per 1MB record, quadratic over a session.
const (
	sdkReplayStart = "The above is the conversation history so far"
	sdkReplayEnd   = "continue the transcript."
	sdkResultOpen  = "Tool result ("
	sdkResultID    = ", id=toolu_"
)

var skillTagRE = regexp.MustCompile(`^\s*<skill\s+name="([^"]+)"`)

func claudeView(rec *store.SessionRecord) View {
	var line struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(rec.Raw, &line) != nil {
		return hidden
	}
	text, blocks := contentOf(line.Message.Content, "")
	switch rec.RecordType {
	case "assistant":
		calls := toolCalls(blocks)
		if text == "" && len(calls) == 0 {
			return hidden
		}
		return View{Kind: KindMessage, Role: "assistant", Text: text, ToolCalls: calls}
	case "user":
		return claudeUserView(rec, text, toolResults(blocks))
	}
	return hidden
}

func claudeUserView(rec *store.SessionRecord, text string, results []ToolResult) View {
	if rec.IsCompactSummary {
		return View{Kind: KindMessage, Role: "user", Text: text, CompactSummary: true}
	}
	if rec.PromptSource == "sdk" {
		results = append(results, sdkToolResults(text)...)
		body := sdkBody(text)
		if m := skillTagRE.FindStringSubmatch(body); m != nil {
			return View{Kind: KindMessage, Role: "user", Command: m[1], ToolResults: results}
		}
		return userView(body, results)
	}
	// Genuine turns carry prompt_source; only legacy records need the tag heuristics.
	if rec.PromptSource == "" {
		if rec.IsMeta {
			return hidden
		}
		if m := cmdNameRE.FindStringSubmatch(text); m != nil {
			v := View{Kind: KindMessage, Role: "user", Command: strings.TrimSpace(m[1])}
			if a := cmdArgsRE.FindStringSubmatch(text); a != nil {
				v.CommandArgs = strings.TrimSpace(a[1])
			}
			return v
		}
		if cmdHideRE.MatchString(text) {
			return hidden
		}
	}
	return userView(text, results)
}

func userView(text string, results []ToolResult) View {
	switch {
	case text != "":
		return View{Kind: KindMessage, Role: "user", Text: text, ToolResults: results}
	case len(results) > 0:
		return View{Kind: KindToolResult, ToolResults: results}
	}
	return hidden
}

// sdkBody drops the replayed transcript before the request and the prose tool
// results after it.
func sdkBody(text string) string {
	if i := strings.Index(text, sdkReplayStart); i >= 0 {
		i += len(sdkReplayStart)
		if j := strings.Index(text[i:], sdkReplayEnd); j >= 0 {
			text = text[skipLineEnd(text, i+j+len(sdkReplayEnd)):]
		}
	}
	if h, ok := nextSdkResult(text, 0); ok {
		// The results start at the line break (and indent) that precedes the first head.
		start := h.start
		for start > 0 && (text[start-1] == ' ' || text[start-1] == '\t') {
			start--
		}
		if start > 0 && text[start-1] == '\n' {
			start--
		}
		text = text[:start]
	}
	return strings.TrimSpace(text)
}

// sdkToolResults parses every prose result, each running to the next head.
func sdkToolResults(text string) []ToolResult {
	var out []ToolResult
	h, ok := nextSdkResult(text, 0)
	for ok {
		next, more := nextSdkResult(text, h.end)
		end := len(text)
		if more {
			end = next.start
		}
		out = append(out, ToolResult{ID: h.id, Output: strings.TrimSpace(text[h.end:end]), Inferred: true})
		h, ok = next, more
	}
	return out
}

// sdkResultHead is one "Tool result (Name, id=toolu_…):" header; end is past
// the trailing blanks and at most one line break.
type sdkResultHead struct {
	start, end int
	id         string
}

func nextSdkResult(text string, from int) (sdkResultHead, bool) {
	for {
		k := strings.Index(text[from:], sdkResultOpen)
		if k < 0 {
			return sdkResultHead{}, false
		}
		start := from + k
		name := start + len(sdkResultOpen)
		// The name runs to the first ',' or ')' and must not be empty.
		if n := strings.IndexAny(text[name:], ",)"); n > 0 && strings.HasPrefix(text[name+n:], sdkResultID) {
			idStart := name + n + len(", id=")
			idEnd := idStart + len("toolu_")
			for idEnd < len(text) && isAlnum(text[idEnd]) {
				idEnd++
			}
			if idEnd > idStart+len("toolu_") && strings.HasPrefix(text[idEnd:], "):") {
				return sdkResultHead{start: start, end: skipLineEnd(text, idEnd+2), id: text[idStart:idEnd]}, true
			}
		}
		from = start + 1
	}
}

func skipLineEnd(text string, i int) int {
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if i < len(text) && text[i] == '\n' {
		i++
	}
	return i
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// --- codex ---

func codexView(rec *store.SessionRecord) View {
	var line struct {
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(rec.Raw, &line) != nil {
		return hidden
	}
	// The old flat format puts the item fields at the top level.
	p := line.Payload
	if !isObject(p) {
		p = rec.Raw
	}
	var item struct {
		Name      string          `json:"name"`
		CallID    string          `json:"call_id"`
		Content   json.RawMessage `json:"content"`
		Summary   json.RawMessage `json:"summary"`
		Arguments json.RawMessage `json:"arguments"`
		Input     json.RawMessage `json:"input"`
		Output    json.RawMessage `json:"output"`
	}
	if json.Unmarshal(p, &item) != nil {
		return hidden
	}
	switch rec.RecordType {
	case "user", "assistant", "agent_task":
		text, _ := contentOf(item.Content, "\n")
		if text == "" {
			return hidden
		}
		if rec.RecordType == "agent_task" {
			return View{Kind: KindMessage, Role: "user", Text: text, AgentTask: true}
		}
		return View{Kind: KindMessage, Role: rec.RecordType, Text: text}
	case "tool_call":
		input := item.Arguments
		if len(input) == 0 {
			input = item.Input
		}
		// The raw line wins over the columns: reenrich can pair same-millisecond
		// parallel calls with the wrong id, while the line itself is never wrong.
		return View{Kind: KindToolCall, Role: "assistant", ToolCalls: []ToolCall{{
			ID:    firstNonEmpty(item.CallID, rec.ToolCallID),
			Name:  firstNonEmpty(item.Name, rec.ToolName),
			Input: jsonText(input),
		}}}
	case "tool_output":
		return View{Kind: KindToolResult, ToolResults: []ToolResult{{
			ID:     firstNonEmpty(item.CallID, rec.ToolCallID),
			Output: contentText(item.Output, "\n"),
		}}}
	case "reasoning":
		// Usually only encrypted_content is present; an empty text still marks where the model thought.
		summary, _ := contentOf(item.Summary, "\n")
		content, _ := contentOf(item.Content, "\n")
		return View{Kind: KindReasoning, Role: "assistant", Text: joinNonEmpty([]string{summary, content}, "\n")}
	}
	return hidden
}

// --- omo / gjc ---

func piView(rec *store.SessionRecord) View {
	var line struct {
		Message struct {
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"toolCallId"`
			IsError    bool            `json:"isError"`
		} `json:"message"`
	}
	if json.Unmarshal(rec.Raw, &line) != nil {
		return hidden
	}
	m := line.Message
	switch rec.RecordType {
	case "user":
		if text, _ := contentOf(m.Content, "\n"); text != "" {
			return View{Kind: KindMessage, Role: "user", Text: text}
		}
	case "assistant":
		text, blocks := contentOf(m.Content, "\n")
		calls := toolCalls(blocks)
		if text != "" || len(calls) > 0 {
			return View{Kind: KindMessage, Role: "assistant", Text: text, ToolCalls: calls}
		}
	case "tool_result":
		return View{Kind: KindToolResult, ToolResults: []ToolResult{{
			ID:      firstNonEmpty(m.ToolCallID, rec.ToolCallID),
			Output:  contentText(m.Content, "\n"),
			IsError: outcome(m.IsError),
		}}}
	}
	// gjc also stores each toolCall block as a sibling "tool_call" record sharing
	// the assistant's raw line; the assistant record already shows those calls.
	return hidden
}

// --- shared helpers ---

// block is the union of the content-block fields every provider uses.
type block struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`     // claude tool_use
	Arguments json.RawMessage `json:"arguments"` // omo/gjc toolCall
	Content   json.RawMessage `json:"content"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

var textBlockTypes = map[string]bool{
	"text": true, "input_text": true, "output_text": true, "summary_text": true, "reasoning_text": true,
}

// contentOf reads a content field that is a plain string or an array of blocks.
// Blocks are decoded one by one so a single odd block does not drop its siblings.
func contentOf(raw json.RawMessage, sep string) (string, []block) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return "", nil
	}
	var blocks []block
	var texts []string
	for _, it := range items {
		var b block
		if json.Unmarshal(it, &b) != nil || b.Type == "" {
			continue
		}
		blocks = append(blocks, b)
		if textBlockTypes[b.Type] {
			texts = append(texts, b.Text)
		}
	}
	return joinNonEmpty(texts, sep), blocks
}

func toolCalls(blocks []block) []ToolCall {
	var out []ToolCall
	for _, b := range blocks {
		switch b.Type {
		case "tool_use":
			out = append(out, ToolCall{ID: b.ID, Name: b.Name, Input: jsonText(b.Input)})
		case "toolCall":
			out = append(out, ToolCall{ID: b.ID, Name: b.Name, Input: jsonText(b.Arguments)})
		}
	}
	return out
}

func toolResults(blocks []block) []ToolResult {
	var out []ToolResult
	for _, b := range blocks {
		// Without an id a result cannot be paired with its call, so the viewer never showed it.
		if b.Type == "tool_result" && b.ToolUseID != "" {
			out = append(out, ToolResult{ID: b.ToolUseID, Output: contentText(b.Content, ""), IsError: outcome(b.IsError)})
		}
	}
	return out
}

// outcome records a provider's tool outcome flag. Claude and pi omit the flag
// on success, so a missing flag there still means not failed.
func outcome(failed bool) *bool { return &failed }

// contentText renders tool output in full: text for strings and block arrays,
// indented JSON for anything else. Output is collapsed in the UI, never truncated.
func contentText(raw json.RawMessage, sep string) string {
	if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '[' {
		text, _ := contentOf(raw, sep)
		return text
	}
	return jsonText(raw)
}

// jsonText returns a JSON string's value, or other JSON re-indented in its
// original key order so the input reads as the model wrote it.
func jsonText(raw json.RawMessage) string {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(t, &s) == nil {
		return s
	}
	var buf bytes.Buffer
	if json.Indent(&buf, t, "", "  ") != nil {
		return string(t)
	}
	return buf.String()
}

// anchorID is stable across reads: jsonb returns the same raw bytes each time,
// and ts is formatted in UTC so the session's display zone cannot change it.
// source_file and tool_call_id are left out because reenrich can fill them in
// later, which would move the anchor under a reader.
func anchorID(rec *store.SessionRecord) string {
	if rec.UUID != "" {
		return rec.UUID
	}
	return "h:" + shortHash(rec.Raw, rec.SessionID, rec.Ts.UTC().Format(time.RFC3339Nano), rec.RecordType)
}

// dedupeKey reads only the source line's own body. Enrichment adds top-level
// fields and columns (uuid, promptSource, isMeta, ...) but never rewrites
// message or payload, so a legacy copy and its enriched twin hash the same.
func dedupeKey(rec *store.SessionRecord) string {
	body := []byte(rec.Raw)
	var probe struct {
		Message json.RawMessage `json:"message"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(rec.Raw, &probe) == nil {
		if present(probe.Message) {
			body = probe.Message
		} else if present(probe.Payload) {
			body = probe.Payload
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, body) == nil {
		body = buf.Bytes()
	}
	return "d:" + shortHash(body, rec.Ts.UTC().Format(time.RFC3339Nano), rec.RecordType)
}

// shortHash joins the parts with NUL so no two field splits collide.
func shortHash(body []byte, parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func present(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && string(t) != "null"
}

func isObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func joinNonEmpty(parts []string, sep string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}
