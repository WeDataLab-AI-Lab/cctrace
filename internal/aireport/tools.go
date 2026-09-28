package aireport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cctrace/internal/airuntime"
	"cctrace/internal/sessionview"
	"cctrace/internal/store"
)

const (
	defaultQueryLimit = 20
	maxQueryLimit     = 50
	// maxMessageBytes caps one message's text; maxCallBytes caps one read_segment
	// result, past which the model gets next_offset instead.
	maxMessageBytes  = 2 << 10
	maxCallBytes     = 32 << 10
	conversationPage = 200
)

// errLookup is what the model sees when a store read fails; the cause is logged.
var errLookup = errors.New("record lookup failed")

func segmentScope(sc Scope, wk Week) store.AISegmentScope {
	return store.AISegmentScope{ProfileEmail: sc.ProfileEmail, UserID: sc.UserID, Since: wk.Since, Until: wk.Until}
}

// BuildTools returns query_segments, read_segment and compare_week bound to one
// caller and one week. Neither comes from model arguments.
func BuildTools(st Store, sc Scope, wk Week) []airuntime.Tool {
	scope := segmentScope(sc, wk)
	return []airuntime.Tool{
		{
			Name:        "query_segments",
			Description: "이번 주 작업 구간 목록을 필터·정렬해 조회합니다. 결과의 segment_id 로 read_segment 를 호출하세요.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{
				"had_compact":{"type":"boolean"},
				"min_tool_fail":{"type":"integer","minimum":0},
				"min_typed_turns":{"type":"integer","minimum":0},
				"agent":{"type":"string"},
				"order_by":{"type":"string","enum":["start_ts","typed_turn_count","tool_fail_count"]},
				"limit":{"type":"integer","minimum":1,"maximum":50}}}`),
			Handler: func(ctx context.Context, raw json.RawMessage) (airuntime.ToolOutput, error) {
				return querySegments(ctx, st, scope, raw)
			},
		},
		{
			Name:        "read_segment",
			Description: "작업 구간 하나의 대화(입력·응답 텍스트, 도구 이름과 실패 여부)를 읽습니다. next_offset 이 있으면 이어서 읽을 수 있습니다.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["segment_id"],"properties":{
				"segment_id":{"type":"string"},
				"offset":{"type":"integer","minimum":0}}}`),
			Handler: func(ctx context.Context, raw json.RawMessage) (airuntime.ToolOutput, error) {
				return readSegment(ctx, st, scope, raw)
			},
		},
		{
			Name:        "compare_week",
			Description: "이전 ISO 주와 이번 주의 구간 집계를 비교합니다. tool_call_count 에는 결과가 기록되지 않는 호출(Codex)이 섞여 있으므로, 도구 실패율은 tool_fail_count / tool_outcome_observed_count 로만 계산하세요.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
			Handler: func(ctx context.Context, raw json.RawMessage) (airuntime.ToolOutput, error) {
				return compareWeek(ctx, st, sc, wk, raw)
			},
		},
	}
}

// argKeys is the scalar argument whitelist per tool, used for the tool-call log.
var argKeys = map[string][]string{
	"query_segments": {"had_compact", "min_tool_fail", "min_typed_turns", "agent", "order_by", "limit"},
	"read_segment":   {"segment_id", "offset"},
	"compare_week":   {},
}

// SummarizeArgs keeps the known scalar arguments of a tool so the log and the
// progress stream never carry whatever else the model put in a call.
func SummarizeArgs(tool string, args map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range argKeys[tool] {
		switch v := args[key].(type) {
		case string:
			out[key] = truncateBytes(v, 64)
		case bool, int, int64, float64:
			out[key] = v
		}
	}
	return out
}

// decodeArgs parses a tool call strictly: unknown fields and trailing data fail.
// Empty or null arguments mean {}.
func decodeArgs(raw json.RawMessage, v any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		trimmed = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("invalid arguments: trailing data")
	}
	return nil
}

func marshalText(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSpace(buf.String())
}

type querySegmentsArgs struct {
	HadCompact    *bool  `json:"had_compact"`
	MinToolFail   int    `json:"min_tool_fail"`
	MinTypedTurns int    `json:"min_typed_turns"`
	Agent         string `json:"agent"`
	OrderBy       string `json:"order_by"`
	Limit         int    `json:"limit"`
}

type segmentView struct {
	SegmentID      string    `json:"segment_id"`
	StartTs        time.Time `json:"start_ts"`
	ProjectName    string    `json:"project_name"`
	Agent          string    `json:"agent"`
	TypedTurnCount int64     `json:"typed_turn_count"`
	ToolCallCount  int64     `json:"tool_call_count"`
	ToolFailCount  int64     `json:"tool_fail_count"`
	CommandCount   int64     `json:"command_count"`
	HadCompact     bool      `json:"had_compact"`
	ToolEvidence   bool      `json:"tool_evidence"`
	// ToolOutcomeEvidence false means tool_fail_count is unobserved, not zero
	// (Codex records calls but no outcome).
	ToolOutcomeEvidence bool `json:"tool_outcome_evidence"`
}

func querySegments(ctx context.Context, st Store, scope store.AISegmentScope, raw json.RawMessage) (airuntime.ToolOutput, error) {
	var a querySegmentsArgs
	if err := decodeArgs(raw, &a); err != nil {
		return airuntime.ToolOutput{}, err
	}
	if a.OrderBy == "" {
		a.OrderBy = store.AISegmentOrderStartTs
	}
	switch a.OrderBy {
	case store.AISegmentOrderStartTs, store.AISegmentOrderTypedTurnCount, store.AISegmentOrderToolFailCount:
	default:
		return airuntime.ToolOutput{}, fmt.Errorf("invalid arguments: order_by must be start_ts, typed_turn_count or tool_fail_count")
	}
	if a.Limit == 0 {
		a.Limit = defaultQueryLimit
	}
	if a.Limit < 1 || a.Limit > maxQueryLimit || a.MinToolFail < 0 || a.MinTypedTurns < 0 {
		return airuntime.ToolOutput{}, fmt.Errorf("invalid arguments: limit must be 1..%d and minimums non-negative", maxQueryLimit)
	}
	summary := map[string]any{"order_by": a.OrderBy, "limit": a.Limit}
	if a.HadCompact != nil {
		summary["had_compact"] = *a.HadCompact
	}
	if a.MinToolFail > 0 {
		summary["min_tool_fail"] = a.MinToolFail
	}
	if a.MinTypedTurns > 0 {
		summary["min_typed_turns"] = a.MinTypedTurns
	}
	if a.Agent != "" {
		summary["agent"] = truncateBytes(a.Agent, 64)
	}

	segs, err := st.AIWeekSegments(ctx, scope, store.AISegmentFilter{
		HadCompact: a.HadCompact, MinToolFail: a.MinToolFail, MinTypedTurns: a.MinTypedTurns,
		Agent: a.Agent, OrderBy: a.OrderBy, Limit: a.Limit,
	})
	if err != nil {
		log.Printf("[aireport] query_segments: %v", err)
		return airuntime.ToolOutput{}, errLookup
	}
	views := make([]segmentView, 0, len(segs))
	for _, s := range segs {
		views = append(views, segmentView{
			SegmentID: strconv.FormatInt(s.ID, 10), StartTs: s.StartTs, ProjectName: s.ProjectName, Agent: s.Agent,
			TypedTurnCount: s.TypedTurnCount, ToolCallCount: s.ToolCallCount, ToolFailCount: s.ToolFailCount,
			CommandCount: s.CommandCount, HadCompact: s.HadCompact, ToolEvidence: s.ToolEvidence,
			ToolOutcomeEvidence: s.ToolOutcomeEvidence,
		})
	}
	return airuntime.ToolOutput{
		Text:        marshalText(map[string]any{"segments": views}),
		Rows:        len(views),
		ArgsSummary: summary,
	}, nil
}

type readSegmentArgs struct {
	SegmentID string `json:"segment_id"`
	Offset    int    `json:"offset"`
}

type messageView struct {
	Role    string `json:"role"`
	Text    string `json:"text,omitempty"`
	Tool    string `json:"tool,omitempty"`
	IsError *bool  `json:"is_error,omitempty"`
}

func readSegment(ctx context.Context, st Store, scope store.AISegmentScope, raw json.RawMessage) (airuntime.ToolOutput, error) {
	var a readSegmentArgs
	if err := decodeArgs(raw, &a); err != nil {
		return airuntime.ToolOutput{}, err
	}
	id, err := strconv.ParseInt(a.SegmentID, 10, 64)
	if err != nil || id <= 0 || a.Offset < 0 {
		return airuntime.ToolOutput{}, errors.New("invalid arguments: segment_id must be a segment id from query_segments and offset non-negative")
	}
	found, err := st.AISegmentsByIDs(ctx, scope, []int64{id})
	if err != nil {
		log.Printf("[aireport] read_segment lookup: %v", err)
		return airuntime.ToolOutput{}, errLookup
	}
	if _, ok := found[id]; !ok {
		return airuntime.ToolOutput{}, errors.New("segment not found in this week's records")
	}
	recs, err := st.AISegmentConversation(ctx, scope, id, a.Offset, conversationPage)
	if err != nil {
		log.Printf("[aireport] read_segment conversation: %v", err)
		return airuntime.ToolOutput{}, errLookup
	}

	messages := []messageView{}
	var next *int
	total := 0
	for i, rec := range recs {
		entries := conversationEntries(rec)
		size := 0
		for _, e := range entries {
			size += len(e.Text) + len(e.Tool) + 48 // rough JSON overhead per entry
		}
		if total+size > maxCallBytes && len(messages) > 0 {
			n := a.Offset + i
			next = &n
			break
		}
		total += size
		messages = append(messages, entries...)
	}
	if next == nil && len(recs) == conversationPage {
		n := a.Offset + len(recs)
		next = &n
	}
	out := map[string]any{"segment_id": a.SegmentID, "messages": messages}
	if next != nil {
		out["next_offset"] = *next
	}
	return airuntime.ToolOutput{
		Text:        marshalText(out),
		Rows:        len(messages),
		ArgsSummary: map[string]any{"segment_id": a.SegmentID, "offset": a.Offset},
	}, nil
}

// conversationEntries reduces one record to what read_segment may send: user
// and assistant text, tool names, and tool_result error flags where the provider
// records them. Tool inputs and
// outputs are dropped. The record is read through sessionview, so every agent
// the dashboard shows is read the same way here.
func conversationEntries(rec *store.SessionRecord) []messageView {
	if rec == nil || rec.IsMeta || rec.IsCompactSummary {
		return nil
	}
	v := sessionview.Normalize(rec)
	if v.AgentTask || v.Kind == sessionview.KindHidden || v.Kind == sessionview.KindReasoning {
		return nil
	}
	out := textEntry(v.Role, v.Text)
	for _, c := range v.ToolCalls {
		out = append(out, messageView{Role: "assistant", Tool: truncateBytes(c.Name, 128)})
	}
	for _, r := range v.ToolResults {
		if r.Inferred {
			continue
		}
		// A nil IsError (Codex) stays out of the entry: not observed is not success.
		out = append(out, messageView{Role: "tool_result", IsError: r.IsError})
	}
	return out
}

// injectedTags wrap user-role text the harness wrote rather than the person:
// command and hook output, slash-command plumbing, reminders, environment and
// instruction preambles. The consent text promises tool input and output are
// not sent, so these go wherever they sit in a message.
var injectedTags = []string{
	"system-reminder", "bash-input", "bash-stdout", "bash-stderr",
	"command-name", "command-message", "command-args",
	"local-command-stdout", "local-command-stderr", "local-command-caveat",
	"environment_context", "user_instructions",
}

// injectedBlock matches one tagged block; an unclosed one runs to the end of
// the text, so a cut-off block is dropped rather than sent.
var injectedBlock = func() *regexp.Regexp {
	alts := make([]string, 0, len(injectedTags))
	for _, tag := range injectedTags {
		alts = append(alts, "<"+tag+`(?:\s[^>]*)?>.*?(?:</`+tag+`>|\z)`)
	}
	return regexp.MustCompile(`(?s)` + strings.Join(alts, "|"))
}()

// stripInjected removes injected blocks from a message's text.
func stripInjected(text string) string {
	return injectedBlock.ReplaceAllString(text, "")
}

func textEntry(role, text string) []messageView {
	text = strings.TrimSpace(stripInjected(text))
	if text == "" {
		return nil
	}
	return []messageView{{Role: role, Text: truncateBytes(text, maxMessageBytes)}}
}

// truncateBytes cuts s to at most n bytes on a rune boundary and marks the cut.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func compareWeek(ctx context.Context, st Store, sc Scope, wk Week, raw json.RawMessage) (airuntime.ToolOutput, error) {
	var a struct{}
	if err := decodeArgs(raw, &a); err != nil {
		return airuntime.ToolOutput{}, err
	}
	summary := map[string]any{}
	prev := wk.Previous()
	prevAgg, err := st.AIWeekAggregate(ctx, segmentScope(sc, prev))
	if err != nil {
		log.Printf("[aireport] compare_week previous: %v", err)
		return airuntime.ToolOutput{}, errLookup
	}
	if prevAgg == nil || prevAgg.SegmentCount == 0 {
		return airuntime.ToolOutput{
			Text:        marshalText(map[string]any{"comparable": false, "reason": "비교 불가"}),
			ArgsSummary: summary,
		}, nil
	}
	cur, err := st.AIWeekAggregate(ctx, segmentScope(sc, wk))
	if err != nil {
		log.Printf("[aireport] compare_week current: %v", err)
		return airuntime.ToolOutput{}, errLookup
	}
	return airuntime.ToolOutput{
		Text: marshalText(map[string]any{
			"comparable": true, "week": wk.ID, "previous_week": prev.ID, "current": cur, "previous": prevAgg,
		}),
		Rows:        1,
		ArgsSummary: summary,
	}, nil
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
