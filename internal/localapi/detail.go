package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cctrace/internal/codexlog"
	"cctrace/internal/jsonlscan"
	"cctrace/internal/sessionfacts"
	"cctrace/internal/sessionlog"
	"cctrace/internal/sessionview"
	"cctrace/internal/store"
)

type recordCursor struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Offset   int64  `json:"offset"`
}

func openEntry(e fileEntry) (*os.File, error) {
	rel, err := filepath.Rel(e.root, e.path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenInRoot(e.root, rel)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fileIdentity(e.info, fi) {
		_ = f.Close()
		return nil, errors.New("session changed")
	}
	return f, nil
}

func readClaudeLines(ctx context.Context, f *os.File, size, from int64, visit func([]byte, int64, int64) error) error {
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(io.LimitReader(f, size-from), 64*1024)
	offset := from
	for {
		line, n, complete, err := jsonlscan.ReadLine(ctx, reader)
		if errors.Is(err, jsonlscan.ErrLineTooLong) {
			if visitErr := visit(nil, offset, offset+n); visitErr != nil {
				if visitErr == io.EOF {
					return nil
				}
				return visitErr
			}
			offset += n
			continue
		}
		if err != nil && err != io.EOF {
			return err
		}
		if complete {
			if visitErr := visit(bytes.TrimRight(line, "\r\n"), offset, offset+n); visitErr != nil {
				if visitErr == io.EOF {
					return nil
				}
				return visitErr
			}
			offset += n
		}
		if err == io.EOF {
			return nil
		}
	}
}

func (s *Server) readRecords(w http.ResponseWriter, r *http.Request, e fileEntry) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			fail(w, 400, "invalid", "invalid limit")
			return
		}
		limit = n
	}
	from := int64(0)
	if c := r.URL.Query().Get("cursor"); c != "" {
		var v recordCursor
		b, err := base64.RawURLEncoding.DecodeString(c)
		if err != nil || json.Unmarshal(b, &v) != nil || v.ID != e.id || v.Offset < 0 {
			fail(w, 400, "invalid", "invalid cursor")
			return
		}
		if v.Revision != e.revision {
			fail(w, 409, "revision_changed", "session changed")
			return
		}
		if v.Offset > e.info.Size() {
			fail(w, 400, "invalid", "invalid cursor")
			return
		}
		from = v.Offset
	}
	f, err := openEntry(e)
	if err != nil {
		fail(w, 409, "revision_changed", "session changed")
		return
	}
	defer f.Close()
	if from > 0 {
		var previous [1]byte
		if _, err := f.ReadAt(previous[:], from-1); err != nil || previous[0] != '\n' {
			fail(w, 400, "invalid", "invalid cursor boundary")
			return
		}
	}
	result := RecordsResponse{Records: []Record{}, Revision: e.revision}
	nextOffset := int64(0)
	add := func(st *store.SessionRecord, offset int64) error {
		if offset < from {
			return nil
		}
		if len(result.Records) == limit {
			nextOffset = offset
			return io.EOF
		}
		result.Records = append(result.Records, viewRecord(st))
		return nil
	}
	if e.agent == "claude" {
		err = readClaudeLines(r.Context(), f, e.info.Size(), from, func(line []byte, offset, _ int64) error {
			if !json.Valid(line) {
				return nil
			}
			var rec sessionlog.Record
			if json.Unmarshal(line, &rec) != nil || rec.Type == "" {
				return nil
			}
			st := claudeStoreRecord(e, rec, line)
			return add(&st, offset)
		})
	} else {
		_, err = codexlog.ScanReaderContext(r.Context(), f, e.info.Size(), nativeID(e), func(rec *codexlog.Record, line []byte, offset, _ int64, _ codexlog.Metadata) error {
			if rec == nil {
				return nil
			}
			st := codexStoreRecord(e, rec, offset)
			return add(&st, offset)
		})
	}
	if err != nil {
		fail(w, 500, "read_error", "session read failed")
		return
	}
	if err = stable(e); err != nil {
		fail(w, 409, "revision_changed", "session changed")
		return
	}
	if nextOffset > 0 {
		b, _ := json.Marshal(recordCursor{e.id, e.revision, nextOffset})
		next := base64.RawURLEncoding.EncodeToString(b)
		result.NextCursor = &next
	}
	send(w, result)
}

func (s *Server) readDetail(w http.ResponseWriter, r *http.Request, e fileEntry, kind string) {
	if kind == "records" {
		s.readRecords(w, r, e)
		return
	}
	a, err := analyze(r.Context(), e, AnalyzeOptions{ReadAt: s.now()})
	if errors.Is(err, ErrChanged) {
		fail(w, 409, "revision_changed", "session changed")
		return
	}
	if err != nil {
		fail(w, 500, "read_error", "session read failed")
		return
	}
	out := sessionfacts.TimelineOutput{Output: a.Facts.Output, Timeline: a.Facts.Timeline}
	response := analysisResponse(e, a.Input, out)
	if incomplete := a.Incomplete; incomplete != "" {
		response.Summary.TurnBoundaries = "undeterminable"
		response.Summary.TurnBoundariesReason = incomplete
		response.Summary.TurnCount, response.Summary.RequestCount, response.Summary.InputCount = nil, nil, nil
		response.Summary.InputTokens, response.Summary.OutputTokens = nil, nil
		response.Summary.CacheReadTokens, response.Summary.CacheCreateTokens = nil, nil
		response.Summary.CostUSD, response.Summary.CostSource = nil, "none"
		response.Turns = []AnalysisTurn{}
		response.Timeline.State, response.Timeline.Reason = "undeterminable", incomplete
		for i := range response.Timeline.Rows {
			response.Timeline.Rows[i].TurnKey = ""
			response.Timeline.Rows[i].Roles = nil
			response.Timeline.Rows[i].Unmapped = incomplete
		}
	}
	send(w, response)
}

func analysisResponse(e fileEntry, input sessionfacts.Input, out sessionfacts.TimelineOutput) AnalysisResponse {
	s := out.Session
	summary := AnalysisSummary{Agent: e.agent, Model: nullable(s.Model), Title: s.Title, TitleState: s.TitleState, StartTime: s.FirstActivityAt, EndTime: s.LastActivityAt, ActiveSeconds: s.ActiveSeconds, CostUSD: s.CostUSD, CostSource: s.CostSource, TurnBoundaries: s.TurnBoundaries, TurnBoundariesReason: s.TurnBoundariesReason, TurnCount: s.Turns, RequestCount: s.Requests, InputCount: s.Inputs, ToolCalls: s.Detail.ToolCalls}
	if len(input.Heads) > 0 {
		summary.InputTokens, summary.OutputTokens = &s.InputTokens, &s.OutputTokens
		summary.CacheReadTokens, summary.CacheCreateTokens = &s.CacheReadTokens, &s.CacheCreateTokens
	}
	response := AnalysisResponse{LocalID: e.id, Revision: e.revision, RulesVersion: sessionfacts.RulesVersion, Summary: summary, Turns: []AnalysisTurn{}, Timeline: AnalysisTimeline{State: out.Timeline.State, Reason: out.Timeline.Reason, Rows: []TimelineRow{}}}
	for _, t := range out.Turns {
		turn := AnalysisTurn{TurnKey: t.TurnKey, Ordinal: t.Ordinal, RequestNo: t.RequestNo, Status: t.Status, StatusReason: t.StatusReason, Branch: t.Branch, InputKind: t.InputKind, Preview: t.Preview, PreviewState: t.PreviewState, StartedAt: t.StartedAt, CostUSD: t.CostUSD, CostSource: t.CostSource, ToolCalls: t.ToolCalls, Subagents: t.Subagents}
		if !t.EndedAt.IsZero() {
			turn.EndedAt = &t.EndedAt
		}
		if t.APIRequests > 0 {
			turn.InputTokens, turn.OutputTokens = &t.InputTokens, &t.OutputTokens
			turn.CacheReadTokens, turn.CacheCreateTokens = &t.CacheReadTokens, &t.CacheCreateTokens
		}
		response.Turns = append(response.Turns, turn)
	}
	for _, row := range out.Timeline.Rows {
		response.Timeline.Rows = append(response.Timeline.Rows, TimelineRow{Ts: row.TS, UUID: row.UUID, RecordType: row.RecordType, TurnKey: row.TurnKey, Roles: row.Roles, Unmapped: row.Unmapped})
	}
	return response
}

func nativeID(e fileEntry) string {
	if id := observedNativeID(e); id != "" {
		return id
	}
	if e.agent == "codex" {
		if id := codexlog.SessionIDFromPath(e.path); id != "" {
			return id
		}
	}
	return strings.TrimSuffix(filepath.Base(e.path), ".jsonl")
}

func claudeStoreRecord(e fileEntry, rec sessionlog.Record, line []byte) store.SessionRecord {
	st := store.SessionRecord{Ts: rec.Timestamp, SessionID: nativeID(e), ProjectHash: sessionlog.ProjectHash(e.root, e.path), RecordType: rec.Type, Raw: json.RawMessage(line), Agent: "claude", UUID: rec.UUID, ParentUUID: rec.ParentUUID, IsSidechain: rec.IsSidechain, AgentID: rec.AgentID, SourceFile: filepath.Base(e.path), IsMeta: rec.IsMeta, IsCompactSummary: rec.IsCompactSummary, PromptSource: rec.PromptSource, Entrypoint: rec.Entrypoint}
	if rec.SessionID != "" {
		st.SessionID = rec.SessionID
	}
	if rec.Type == "assistant" {
		if msg := rec.ParseAssistantMessage(); msg != nil {
			st.Model = msg.Model
			st.InputTokens, st.OutputTokens = &msg.Usage.InputTokens, &msg.Usage.OutputTokens
			st.CacheReadTokens, st.CacheCreateTokens = &msg.Usage.CacheReadInputTokens, &msg.Usage.CacheCreationInputTokens
		}
	}
	st.CommandName = rec.ParseCommandName()
	return st
}

func codexStoreRecord(e fileEntry, rec *codexlog.Record, offset int64) store.SessionRecord {
	return store.SessionRecord{Ts: rec.Timestamp, SessionID: rec.SessionID, RecordType: rec.RecordType, Agent: "codex", Model: rec.Model, Raw: rec.Raw, UUID: hash(e.id, strconv.FormatInt(offset, 10)), SourceFile: filepath.Base(e.path), ToolName: rec.ToolName, ToolCallID: rec.CallID, InputTokens: rec.InputTokens, OutputTokens: rec.OutputTokens, CacheReadTokens: rec.CacheReadTokens}
}

func viewRecord(st *store.SessionRecord) Record {
	return Record{Ts: st.Ts, SessionID: st.SessionID, ProjectHash: st.ProjectHash, RecordType: st.RecordType, ProfileEmail: "", Model: st.Model, Agent: st.Agent, SourceFile: st.SourceFile, UUID: st.UUID, ParentUUID: st.ParentUUID, Raw: st.Raw, View: sessionview.Normalize(st)}
}

func nullable(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func rawString(raw json.RawMessage, key string) string {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	var v string
	_ = json.Unmarshal(m[key], &v)
	return v
}

func rawPayload(raw json.RawMessage, key string) string {
	var m struct {
		Payload json.RawMessage `json:"payload"`
	}
	_ = json.Unmarshal(raw, &m)
	return rawString(m.Payload, key)
}

// codexMessageTurnID is the turn a Codex message line records for itself
// (metadata.retained_source.id.turn_id). The message is written before its
// turn's first event, so this, not its timestamp, says which turn it opened.
func codexMessageTurnID(raw json.RawMessage) string {
	var line struct {
		Metadata struct {
			RetainedSource struct {
				ID struct {
					TurnID string `json:"turn_id"`
				} `json:"id"`
			} `json:"retained_source"`
		} `json:"metadata"`
	}
	_ = json.Unmarshal(raw, &line)
	return line.Metadata.RetainedSource.ID.TurnID
}

func textHead(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	text = text[:limit]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}

func addClaudeHead(input *sessionfacts.Input, heads map[string]int, rec *sessionlog.Record, st *store.SessionRecord, id int64) {
	if rec.Type != "assistant" || st.Model == "" || st.Model == "<synthetic>" {
		return
	}
	var msg struct {
		ID    string `json:"id"`
		Usage struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
			Read   int64 `json:"cache_read_input_tokens"`
			Create int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(rec.Message, &msg) != nil {
		return
	}
	key := msg.ID
	if key == "" {
		key = rec.UUID
	}
	if key == "" {
		key = strconv.FormatInt(id, 10)
	}
	key = st.SourceFile + "\x00" + key
	if index, ok := heads[key]; ok {
		h := &input.Heads[index]
		h.InputTokens, h.OutputTokens = max(h.InputTokens, msg.Usage.Input), max(h.OutputTokens, msg.Usage.Output)
		h.CacheReadTokens, h.CacheCreateTokens = max(h.CacheReadTokens, msg.Usage.Read), max(h.CacheCreateTokens, msg.Usage.Create)
		return
	}
	heads[key] = len(input.Heads)
	input.Heads = append(input.Heads, sessionfacts.Head{ID: id, UUID: rec.UUID, TS: rec.Timestamp, SourceFile: st.SourceFile, AgentID: rec.AgentID, IsSidechain: rec.IsSidechain, Model: st.Model, InputTokens: msg.Usage.Input, OutputTokens: msg.Usage.Output, CacheReadTokens: msg.Usage.Read, CacheCreateTokens: msg.Usage.Create})
}

func projectClaude(rec *sessionlog.Record, st *store.SessionRecord, id int64) sessionfacts.ClaudeRow {
	row := sessionfacts.ClaudeRow{ID: id, TS: rec.Timestamp, RecordType: rec.Type, UUID: rec.UUID, ParentUUID: rec.ParentUUID, IsSidechain: rec.IsSidechain, AgentID: rec.AgentID, SourceFile: st.SourceFile, IsCompactSummary: rec.IsCompactSummary, IsMeta: rec.IsMeta, PromptSource: rec.PromptSource, Entrypoint: rec.Entrypoint, CommandName: st.CommandName, Model: st.Model, MessageID: rawString(rec.Message, "id"), CWD: rec.CWD, ProjectHash: st.ProjectHash, Branch: rec.GitBranch}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(rec.RawLine, &raw)
	row.PromptID = rawString(rec.RawLine, "promptId")
	if rec.ForkedFrom != nil {
		row.ForkedFromSession = rec.ForkedFrom.SessionID
	}
	row.CctraceCollect, row.CctraceDropped, row.CctraceWithheld = raw["cctraceCollect"], raw["cctraceDropped"], raw["cctraceWithheld"]
	row.TurnPosition, row.ImagePasteCount = raw["turnPosition"], raw["imagePasteCount"]
	row.PendingBackgroundAgentCount, row.PendingWorkflowCount = raw["pendingBackgroundAgentCount"], raw["pendingWorkflowCount"]
	row.LogicalParentUUID, row.ToolDenialKind = rawString(rec.RawLine, "logicalParentUuid"), rawString(rec.RawLine, "toolDenialKind")
	row.RawRedacted, row.OriginKind = rawString(rec.RawLine, "redacted"), rawString(raw["origin"], "kind")
	row.TurnOrigin, row.Subtype = rawString(rec.RawLine, "turnOrigin"), rawString(rec.RawLine, "subtype")
	row.InterruptedMessageID, row.PermissionMode = rawString(rec.RawLine, "interruptedMessageId"), rawString(rec.RawLine, "permissionMode")
	row.ToolUseResult, row.Attachment, row.IsAPIErrorMessage = raw["toolUseResult"], raw["attachment"], raw["isApiErrorMessage"]
	row.HasError = len(raw["error"]) > 0
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(rec.Message, &msg)
	content := bytes.TrimSpace(msg.Content)
	if len(content) > 0 {
		switch content[0] {
		case '"':
			row.ContentKind = "string"
			var text string
			_ = json.Unmarshal(content, &text)
			if rec.Type == "user" {
				row.TextHead = textHead(text, 512)
			}
		case '[':
			row.ContentKind = "array"
			var blocks []map[string]json.RawMessage
			_ = json.Unmarshal(content, &blocks)
			compact := make([]map[string]any, 0, len(blocks))
			for _, b := range blocks {
				entry := map[string]any{}
				typ := rawStringFromMember(b["type"])
				entry["t"] = typ
				for _, pair := range [][2]string{{"id", "id"}, {"name", "n"}, {"tool_use_id", "tu"}, {"is_error", "err"}} {
					if v, ok := b[pair[0]]; ok {
						entry[pair[1]] = json.RawMessage(v)
					}
				}
				if input := slimToolInput(b["input"]); input != nil {
					entry["in"] = input
				}
				if rec.Type == "user" && row.TextHead == "" && (typ == "text" || typ == "input_text") {
					row.TextHead = textHead(rawStringFromMember(b["text"]), 512)
				}
				compact = append(compact, entry)
			}
			row.Blocks, _ = json.Marshal(compact)
		}
	}
	return row
}

func rawStringFromMember(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func slimToolInput(raw json.RawMessage) map[string]json.RawMessage {
	var input map[string]json.RawMessage
	if json.Unmarshal(raw, &input) != nil {
		return nil
	}
	out := map[string]json.RawMessage{}
	for _, pair := range [][2]string{{"file_path", "fp"}, {"plan_file_path", "pf"}, {"run_in_background", "bg"}, {"gc", "gc"}, {"id", "id"}, {"status", "st"}, {"statuses", "sts"}} {
		if v, ok := input[pair[0]]; ok {
			out[pair[1]] = v
		}
	}
	return out
}

func projectTitle(snapshot *sessionfacts.Snapshot, typ string, raw []byte) {
	key := ""
	var dst **string
	switch typ {
	case "custom-title":
		key, dst = "customTitle", &snapshot.CustomTitle
	case "ai-title":
		key, dst = "aiTitle", &snapshot.AITitle
	case "agent-name":
		key, dst = "agentName", &snapshot.AgentName
	default:
		return
	}
	value := rawString(raw, key)
	*dst = &value
}

func codexProjection(rec *codexlog.Record, offset int64) sessionfacts.CodexRow {
	row := sessionfacts.CodexRow{ID: offset + 1, TS: rec.Timestamp, RecordType: rec.RecordType, UUID: hash(string(rec.Raw), strconv.FormatInt(offset, 10)), Model: rec.Model, RawType: rawString(rec.Raw, "type"), PayloadType: rawPayload(rec.Raw, "type"), Role: rawPayload(rec.Raw, "role"), TurnID: rawPayload(rec.Raw, "turn_id"), RootTurnID: rawPayload(rec.Raw, "root_turn_id")}
	if row.TurnID == "" {
		row.TurnID = codexMessageTurnID(rec.Raw)
	}
	if rec.RecordType == "user" {
		row.TextHead = textHead(rec.Content, 512)
	}
	if rec.InputTokens != nil {
		row.InputTokens = int64(*rec.InputTokens)
	}
	if rec.OutputTokens != nil {
		row.OutputTokens = int64(*rec.OutputTokens)
	}
	if rec.CacheReadTokens != nil {
		row.CacheReadTokens = int64(*rec.CacheReadTokens)
	}
	return row
}

func projectCodexLifecycle(snapshot *sessionfacts.Snapshot, index map[string]int, active *string, raw []byte) {
	var line struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		Payload   struct {
			Type       string `json:"type"`
			TurnID     string `json:"turn_id"`
			RootTurnID string `json:"root_turn_id"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &line) != nil || line.Type != "event_msg" || line.Payload.TurnID == "" {
		return
	}
	id := line.Payload.TurnID
	switch line.Payload.Type {
	case "task_started":
		*active = id
		index[id] = len(snapshot.CodexTurns)
		snapshot.CodexTurns = append(snapshot.CodexTurns, sessionfacts.SnapshotCodexTurn{TurnID: id, RootTurnID: line.Payload.RootTurnID, StartedAt: line.Timestamp})
	case "task_complete", "task_completed", "turn_complete", "task_failed", "turn_aborted":
		if i, ok := index[id]; ok {
			snapshot.CodexTurns[i].EndedAt = line.Timestamp
			snapshot.CodexTurns[i].End = "complete"
			if line.Payload.Type == "task_failed" {
				snapshot.CodexTurns[i].End = "error"
			}
			if line.Payload.Type == "turn_aborted" {
				snapshot.CodexTurns[i].End = "aborted"
			}
		}
		if *active == id {
			*active = ""
		}
	}
}
