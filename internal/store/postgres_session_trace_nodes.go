package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cctrace/internal/sessiontrace"

	"github.com/jackc/pgx/v5"
)

type traceNodeKey struct {
	ID    int64     `json:"id"`
	TS    time.Time `json:"ts"`
	Index int       `json:"index"`
	Kind  string    `json:"kind"`
	Ref   string    `json:"ref"`
	Order string    `json:"order,omitempty"`
}
type traceNodeCursor struct {
	Session, Scope, Parent, View string
	Revision, Epoch              int64
	Key                          traceNodeKey
	UUID                         string
}
type traceNodeRow struct {
	id                  int64
	order               string
	emptyLegacyMetadata bool
	responseChildren    bool
	record              *SessionRecord
	raw                 map[string]any
}
type traceNodeReader struct {
	ctx              context.Context
	tx               pgx.Tx
	session          string
	access           SessionAnalysisAccess
	flow             bool
	metadata         bool
	descending       bool
	inputOrder       bool
	parentKeys       bool
	rootPage         bool
	responseMetadata bool
	group            *traceNodeRow
	relations        map[string]traceToolRelation
	agentParents     map[string]*traceNodeKey
	responses        map[string]traceResponseSummary
}

func encodeNodeKey(key traceNodeKey) string {
	b, _ := json.Marshal(key)
	return "n:" + base64.RawURLEncoding.EncodeToString(b)
}
func decodeNodeKey(id string) (traceNodeKey, error) {
	var key traceNodeKey
	if !strings.HasPrefix(id, "n:") {
		return key, ErrTraceInvalid
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "n:"))
	if err != nil || json.Unmarshal(b, &key) != nil || key.ID <= 0 || key.Ref == "" || key.Kind == "" {
		return key, ErrTraceInvalid
	}
	return key, nil
}
func (r traceNodeRow) key(kind string, index int) traceNodeKey {
	ref, _ := sessiontrace.CanonicalHash([]any{r.id, r.record.Ts.UTC().Format(time.RFC3339Nano), r.record.SessionID, r.record.UUID, r.record.RecordType, r.record.Agent, r.record.ProfileEmail, r.record.UserID, r.record.AccountID, r.record.SourceFile, r.record.AgentID})
	return traceNodeKey{ID: r.id, TS: r.record.Ts, Index: index, Kind: kind, Ref: ref, Order: r.order}
}

const traceNodeSourceOrder = `md5(jsonb_build_array(r.uuid,r.record_type,r.profile_email)::text)`

// Returned projections use the visibility view and caller filters. Authorized
// root and parent lookups may first narrow raw source keys; list requests never
// load all session bodies.
func (r traceNodeReader) rows(predicate string, values []any, limit int) ([]traceNodeRow, error) {
	args := []any{r.session, r.access.UserID, r.access.ProfileEmail, r.access.LoginEmail}
	args = append(args, values...)
	args = append(args, limit)
	limitArg := "$" + strconv.Itoa(len(args))
	projection := "to_jsonb(r)"
	if r.metadata {
		projection = traceNodeMetadataJSON
	}
	order := "r.ts,r.id,source_order"
	if r.inputOrder {
		order = "r.ts,r.uuid,r.id,source_order"
	}
	if r.descending {
		order = "r.ts DESC,r.uuid DESC,r.id DESC,source_order DESC"
	}
	prefix := "WITH "
	if r.rootPage {
		resolved, err := r.rootResolvedDispatches()
		if err != nil {
			return nil, err
		}
		args = append(args, resolved)
		prefix += traceRootResolvedDispatchCTE("$" + strconv.Itoa(len(args)))
		prefix += traceInputSetCTE()
		rootCTE := traceRootCandidatesCTE(limit, len(values) == 4)
		rootCTE = strings.Replace(rootCTE, " AND "+traceExecutionSQL("r"), " AND "+traceExecutionSQL("r")+" AND "+traceRootDispatchFilterSQL("r"), 1)
		if r.flow {
			rootCTE = strings.ReplaceAll(rootCTE, traceExecutionSQL("r"), `(NOT `+traceMetadataSQL("r")+` AND r.record_type<>'usage')`)
		}
		if r.descending {
			rootCTE = strings.ReplaceAll(rootCTE, `>=($5,$6,$7,$8)`, `<=($5,$6,$7,$8)`)
			rootCTE = strings.ReplaceAll(rootCTE, `ORDER BY r.ts,r.uuid,r.id,source_order`, `ORDER BY r.ts DESC,r.uuid DESC,r.id DESC,source_order DESC`)
			rootCTE = strings.ReplaceAll(rootCTE, `ORDER BY ts,uuid,id,source_order`, `ORDER BY ts DESC,uuid DESC,id DESC,source_order DESC`)
		}
		prefix += rootCTE
	}
	keySelect := `SELECT r.ts,r.id,r.uuid,r.record_type,r.profile_email,` + traceNodeSourceOrder + ` AS source_order FROM visible_session_records r
 WHERE r.session_id=$1 AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4) AND (` + predicate + `)`
	keyQuery := keySelect + ` ORDER BY ` + order + ` LIMIT ` + limitArg
	if r.parentKeys {
		keyQuery = traceParentKeyQuery(keySelect, order, limitArg)
	}
	if r.rootPage {
		keyQuery = `SELECT r.ts,r.id,r.uuid,r.record_type,r.profile_email,r.source_order FROM trace_root_candidates r`
		if len(values) == 4 {
			op := ">="
			if r.descending {
				op = "<="
			}
			keyQuery += ` WHERE (r.ts,r.uuid,r.id,r.source_order)` + op + `($5,$6,$7,$8)`
		}
		if r.descending {
			keyQuery += ` ORDER BY r.ts DESC,r.uuid DESC,r.id DESC,r.source_order DESC LIMIT ` + limitArg
		} else {
			keyQuery += ` ORDER BY r.ts,r.uuid,r.id,r.source_order LIMIT ` + limitArg
		}
	}
	resultOrder := "r.ts,r.id,k.source_order"
	if r.rootPage {
		resultOrder = "r.ts,r.uuid,r.id,k.source_order"
	}
	if r.descending {
		resultOrder = "r.ts DESC,r.uuid DESC,r.id DESC,k.source_order DESC"
	}
	emptyLegacyMetadata := "FALSE"
	if r.metadata {
		emptyLegacyMetadata = traceEmptyLegacyMetadataSQL("r")
	}
	responseChildren := "false"
	if r.responseMetadata {
		responseChildren = traceResponseChildrenSQL()
	}
	q := prefix + `keys AS MATERIALIZED (` + keyQuery + `)
 SELECT r.id,k.source_order,` + emptyLegacyMetadata + `,` + responseChildren + `,` + projection + ` FROM keys k JOIN visible_session_records r ON r.session_id=$1 AND r.ts=k.ts AND r.id=k.id
 AND r.uuid=k.uuid AND r.record_type=k.record_type AND r.profile_email=k.profile_email
 AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4) ORDER BY ` + resultOrder
	rows, err := r.tx.Query(r.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []traceNodeRow
	for rows.Next() {
		var row traceNodeRow
		var raw []byte
		if err = rows.Scan(&row.id, &row.order, &row.emptyLegacyMetadata, &row.responseChildren, &raw); err != nil {
			return nil, err
		}
		row.record = &SessionRecord{}
		if err = json.Unmarshal(raw, row.record); err != nil {
			return nil, err
		}
		row.raw, err = sessiontrace.DecodeObject(row.record.Raw)
		if err != nil {
			row.raw = map[string]any{}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
func (r traceNodeReader) scoped(row traceNodeRow, predicate string, values []any, limit int, thread bool) ([]traceNodeRow, error) {
	// A native recipe or call id can repeat across accounts and source copies.
	values = append(values, row.record.ProfileEmail, row.record.UserID, row.record.AccountID, row.record.Agent, row.record.SourceFile)
	n := 5 + len(values) - 5
	predicate += fmt.Sprintf(` AND r.profile_email=$%d AND r.user_id=$%d AND COALESCE(r.agent,'claude')=$%d`, n, n+1, n+3)
	if !thread && row.record.AccountID == "" && !row.record.Ts.After(time.Unix(0, 0)) && strings.HasPrefix(row.record.RecordType, "api-request") {
		predicate += fmt.Sprintf(` AND $%d::text=''`, n+2)
	} else {
		predicate += fmt.Sprintf(` AND COALESCE(r.account_id,'')=$%d`, n+2)
	}
	if thread {
		predicate += fmt.Sprintf(` AND COALESCE(r.source_file,'')=$%d`, n+4)
	} else {
		predicate += fmt.Sprintf(` AND $%d::text IS NOT NULL`, n+4)
	}
	reader := r
	reader.rootPage = false
	return reader.rows(predicate, values, limit)
}

// Native reference artifacts have no observation timestamp, hence no account-at-
// time claim. Exact references stay within the same authorized session/file/user;
// blank artifact accounts are never rewritten or inferred from the consumer.
func (r traceNodeReader) nativeRows(row traceNodeRow, predicate string, values []any, limit int) ([]traceNodeRow, error) {
	n := 5 + len(values)
	values = append(values, row.record.ProfileEmail, row.record.UserID, row.record.AccountID, row.record.Agent, row.record.SourceFile)
	predicate += fmt.Sprintf(` AND r.profile_email=$%d AND r.user_id=$%d AND COALESCE(r.agent,'claude')=$%d AND COALESCE(r.source_file,'')=$%d`, n, n+1, n+3, n+4)
	if row.record.AccountID == "" && !row.record.Ts.After(time.Unix(0, 0)) && strings.HasPrefix(row.record.RecordType, "api-request") {
		predicate += fmt.Sprintf(` AND $%d::text=''`, n+2)
	} else {
		predicate += fmt.Sprintf(` AND (COALESCE(r.account_id,'')=$%d OR (r.ts='epoch'::timestamptz AND COALESCE(r.account_id,'')='' AND r.record_type IN ('api-request','api-request-blob','api-request-shape')))`, n+2)
	}
	reader := r
	reader.rootPage = false
	return reader.rows(predicate, values, limit)
}
func (r traceNodeReader) selected(id string) (traceNodeRow, traceNodeKey, error) {
	key, err := decodeNodeKey(id)
	if err != nil {
		// Old event_id links retain their exact original source and block ordinal.
		var metadata []byte
		err = r.tx.QueryRow(r.ctx, `SELECT e.metadata FROM session_trace_events e JOIN session_trace_snapshots s USING(snapshot_id)
 WHERE s.session_id=$1 AND s.scope_key=$2 AND e.event_id=$3 ORDER BY s.created_at DESC LIMIT 1`, r.session, traceScope(r.access), id).Scan(&metadata)
		if errors.Is(err, pgx.ErrNoRows) {
			return traceNodeRow{}, key, ErrSessionAnalysisNotFound
		}
		if err != nil {
			return traceNodeRow{}, key, err
		}
		var envelope struct {
			Events []struct {
				Observations []struct {
					Source sessiontrace.Source `json:"source"`
					Index  int                 `json:"index"`
					Event  sessiontrace.Event  `json:"event"`
				} `json:"observations"`
			} `json:"events"`
		}
		if json.Unmarshal(metadata, &envelope) != nil || len(envelope.Events) == 0 || len(envelope.Events[0].Observations) == 0 {
			return traceNodeRow{}, key, ErrSessionAnalysisNotFound
		}
		o := envelope.Events[0].Observations[0]
		key = traceNodeKey{ID: o.Source.ID, TS: o.Source.TS, Index: -1, Kind: o.Event.Kind}
		if key.Kind == "input" {
			key.Kind = "event"
		}
		if key.Kind == "tool" {
			key.Index = o.Index
			if o.Source.Agent == "codex" {
				key.Index = -1
			}
		}
	}
	predicate := `r.id=$5 AND r.ts=$6`
	values := []any{key.ID, key.TS}
	if key.Order != "" {
		predicate += ` AND ` + traceNodeSourceOrder + `=$7`
		values = append(values, key.Order)
	}
	rows, err := r.rows(predicate, values, 2)
	if err != nil {
		return traceNodeRow{}, key, err
	}
	if len(rows) != 1 {
		return traceNodeRow{}, key, ErrSessionAnalysisNotFound
	}
	actual := rows[0].key(key.Kind, key.Index)
	if key.Ref != "" && key.Ref != actual.Ref {
		return traceNodeRow{}, key, ErrSessionAnalysisNotFound
	}
	if key.Kind == "response" {
		canonical, e := r.responseKey(rows[0])
		if e != nil {
			return traceNodeRow{}, key, e
		}
		if canonical.ID != actual.ID || !canonical.TS.Equal(actual.TS) || canonical.Order != actual.Order {
			return r.selected(encodeNodeKey(canonical))
		}
	}
	return rows[0], actual, nil
}

func nodeMessage(row traceNodeRow) map[string]any {
	if row.record.Agent == "codex" {
		m, _ := row.raw["payload"].(map[string]any)
		if m != nil {
			return m
		}
		return row.raw
	}
	m, _ := row.raw["message"].(map[string]any)
	if m == nil {
		m, _ = row.raw["payload"].(map[string]any)
	}
	if m == nil {
		if _, exists := row.raw["content"]; exists {
			return row.raw
		}
	}
	return m
}
func nodeBlocks(row traceNodeRow) []any {
	m := nodeMessage(row)
	b, _ := m["content"].([]any)
	return b
}
func nodeString(m map[string]any, key string) string { s, _ := m[key].(string); return s }
func nodePreview(value any) string {
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t := nodeString(m, "text"); t != "" {
					text = t
					break
				}
			}
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 96 {
		text = string(runes[:96]) + "…"
	}
	return text
}

func nodeToolPreview(row traceNodeRow) string {
	m := nodeMessage(row)
	value := m["arguments"]
	if value == nil {
		value = m["input"]
	}
	return nodeToolInputPreview(value)
}
func nodeToolInputPreview(value any) string {
	if text, ok := value.(string); ok {
		if parsed, e := sessiontrace.DecodeObject([]byte(text)); e == nil {
			value = parsed
		} else {
			return nodePreview(text)
		}
	}
	if object, ok := value.(map[string]any); ok {
		for _, key := range []string{"cmd", "command", "file_path", "path", "description", "prompt"} {
			if text := nodeString(object, key); text != "" {
				return nodePreview(text)
			}
		}
	}
	return ""
}
func nodeAvailability(raw map[string]any) string {
	for _, key := range []string{"redacted", "withheld", "cctraceWithheld", "cctraceTruncated", "cctraceDropped"} {
		v := raw[key]
		if v == nil {
			continue
		}
		if b, ok := v.(bool); ok && !b {
			continue
		}
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		if a, ok := v.([]any); ok && len(a) == 0 {
			continue
		}
		if key == "cctraceTruncated" {
			return "truncated"
		}
		if key == "cctraceDropped" {
			return "partial"
		}
		return "withheld"
	}
	return "observed"
}

func (r traceNodeReader) toolParent(row traceNodeRow, call string, sameThread bool) (*traceNodeKey, error) {
	if call == "" {
		return nil, nil
	}
	if !sameThread {
		if parent, ok := r.agentParents[traceAgentRelationKey(row, call)]; ok {
			if parent == nil {
				return nil, nil
			}
			copy := *parent
			return &copy, nil
		}
	}
	if sameThread {
		if relation, ok := r.relations[traceRelationKey(row, call)]; ok {
			return relation.parent, nil
		}
	}
	reader := r
	reader.parentKeys = true
	rows, err := reader.scoped(row, traceToolParentsSQL("$5"), []any{[]string{call}}, 2, sameThread)
	if err != nil || len(rows) != 1 {
		return nil, err
	}
	p := rows[0]
	index := -1
	for i, b := range nodeBlocks(p) {
		m, _ := b.(map[string]any)
		if nodeString(m, "type") == "tool_use" && nodeString(m, "id") == call {
			index = i
			break
		}
	}
	k := p.key("tool", index)
	return &k, nil
}
func (r traceNodeReader) responseKey(row traceNodeRow) (traceNodeKey, error) {
	summary, err := r.responseSummary(row)
	return summary.key, err
}

func (r traceNodeReader) nodes(row traceNodeRow) ([]sessiontrace.Node, error) {
	typeName := row.record.RecordType
	if typeName == "api-request-blob" || typeName == "api-request-shape" {
		return nil, nil
	}
	kind, title := "event", "기록 · "+typeName
	m := nodeMessage(row)
	payloadType := nodeString(m, "type")
	switch {
	case typeName == "api-request":
		kind, title = "request", "API 요청"
		params, _ := row.raw["params"].(map[string]any)
		if model := nodeString(params, "model"); model != "" {
			title += " · " + model
		}
	case typeName == "assistant":
		kind, title = "response", "모델 응답"
	case typeName == "tool_call":
		kind, title = "tool", row.record.ToolName
		if title == "" {
			title = nodeString(m, "name")
		}
	case typeName == "usage":
		kind, title = "usage", "사용량 관측"
	case typeName == "context" || typeName == "developer" || nodeString(m, "role") == "developer":
		kind, title = "instructions", "기록된 지시문"
	case typeName == "user":
		title = "사용자 입력"
	}
	attachment, _ := row.raw["attachment"].(map[string]any)
	attachmentType := nodeString(attachment, "type")
	if strings.HasPrefix(attachmentType, "hook_") || attachmentType == "async_hook_response" || nodeString(row.raw, "subtype") == "stop_hook_summary" {
		kind, title = "hook", "훅 · "+nodeString(attachment, "hookName")
		if title == "훅 · " {
			title = "Stop 훅 실행 요약"
		}
	}
	if attachmentType == "instructions" {
		kind, title = "instructions", "주입된 지시문"
	}
	if preview := nodePreview(m["content"]); preview != "" && (kind == "response" || kind == "event") {
		title = preview
		if typeName == "user" && strings.Contains(preview, "# AGENTS.md instructions") {
			kind, title = "instructions", "AGENTS.md 지시문"
		}
	}
	if typeName == "system" && kind == "event" {
		if subtype := nodeString(row.raw, "subtype"); subtype != "" {
			title = "시스템 · " + subtype
		}
	}
	if kind == "tool" {
		if preview := nodeToolPreview(row); preview != "" {
			title += " · " + preview
		}
		if title == "" {
			title = "도구 호출"
		}
	}
	if payloadType == "reasoning" || typeName == "reasoning" {
		title = "기록된 추론"
	}
	view := traceNodeView(row)
	if view == "context" {
		kind, title = "context", "실행 설정·문맥"
		if nodeString(row.raw, "type") == "session_meta" {
			title = "세션 계보·설정"
		}
		if model := nodeString(m, "model"); model != "" {
			title += " · " + nodePreview(model)
		}
		if effort := nodeString(m, "effort"); effort != "" {
			title += " · " + nodePreview(effort)
		}
		if entries := traceInstructionEntries(row); len(entries) > 0 {
			kind, title = "instructions", "기록된 지시문"
			entry, _ := entries[0].(map[string]any)
			switch nodeString(entry, "kind") {
			case "agents_md":
				title = "AGENTS.md 지시문"
			case "developer_instructions":
				title = "개발자 지시문"
			case "base_instructions":
				title = "세션 기본 지시문"
			case "user_instructions":
				title = "사용자 지시문"
			}
			if path, ok := entry["path"].(string); ok && path != "" {
				title += " · " + path
			}
		}
		if attachmentType == "instructions" {
			kind, title = "instructions", "주입된 지시문"
			path := nodeString(attachment, "path")
			if files, ok := attachment["files"].([]any); ok && len(files) > 0 {
				if f, ok := files[0].(map[string]any); ok {
					path = nodeString(f, "path")
				}
			}
			if path != "" {
				title += " · " + nodePreview(path)
			}
		}
	}
	if view == "metadata" {
		kind, title = "metadata", "세션 메타데이터 · "+typeName
	}
	key := row.key(kind, -1)
	var parent *traceNodeKey
	hasChildren := false
	var err error
	if kind == "response" {
		var summary traceResponseSummary
		summary, err = r.responseSummary(row)
		if err != nil {
			return nil, err
		}
		key, hasChildren = summary.key, summary.hasChildren
		if ref := nodeString(row.raw, "requestRef"); ref != "" {
			parents, e := r.nativeRows(row, `r.record_type='api-request' AND r.raw->>'id'=$5`, []any{ref}, 2)
			if e != nil {
				return nil, e
			}
			if len(parents) == 1 {
				k := parents[0].key("request", -1)
				parent = &k
			}
		}
	}
	if kind == "hook" {
		call := nodeString(attachment, "toolUseID")
		if call == "" {
			call = nodeString(row.raw, "toolUseID")
		}
		parent, err = r.toolParent(row, call, true)
		if err != nil {
			return nil, err
		}
	}
	if parent == nil && row.record.ToolUseID != "" {
		parent, err = r.toolParent(row, row.record.ToolUseID, false)
		if err != nil {
			return nil, err
		}
		if parent != nil {
			parent.Kind = "agent"
		}
	}
	// Results remain selectable children of their exact invocation as well as
	// appearing in its consolidated detail.
	if typeName == "tool_output" {
		parent, err = r.toolParent(row, row.record.ToolCallID, true)
		if err != nil {
			return nil, err
		}
		if parent != nil {
			title = "도구 결과"
		} else {
			title = "툴 결과 · 호출 연결 미확인"
		}
	}
	blocks := nodeBlocks(row)
	hasText := nodePreview(m["content"]) != ""
	var tools []sessiontrace.Node
	suppressMain := false
	for i, block := range blocks {
		b, _ := block.(map[string]any)
		bt := nodeString(b, "type")
		if bt == "tool_result" {
			p, e := r.toolParent(row, nodeString(b, "tool_use_id"), true)
			if e != nil {
				return nil, e
			}
			if p != nil {
				result := r.makeNode(row, row.key("event", i), p, "도구 결과", false)
				result.Status = "completed"
				if failed, ok := b["is_error"].(bool); ok && failed {
					result.Status = "error"
				}
				tools = append(tools, result)
				suppressMain = !hasText
			}
		}
		if bt != "tool_use" {
			continue
		}
		hasChildren = true
		tk := row.key("tool", i)
		pk := key
		tool := r.makeNode(row, tk, &pk, nodeString(b, "name"), false)
		observation, e := r.toolObservation(row, nodeString(b, "id"))
		if e != nil {
			return nil, e
		}
		applyToolObservation(&tool, observation)
		preview := nodeString(b, "preview")
		if preview == "" {
			preview = nodeToolInputPreview(b["input"])
		}
		if preview != "" {
			tool.Title += " · " + preview
		}
		tools = append(tools, tool)
		if name := nodeString(b, "name"); name == "Agent" || name == "Task" || name == "spawn_agent" {
			tools[len(tools)-1].HasChildren = true
			ak := row.key("agent", i)
			agent := r.makeNode(row, ak, &tk, "에이전트 전달", true)
			agent.Evidence = append(agent.Evidence, "dispatch_only")
			tools = append(tools, agent)
		}
	}
	if kind == "request" {
		children, e := r.nativeRows(row, `r.raw->>'requestRef'=$5`, []any{nodeString(row.raw, "id")}, 1)
		if e != nil {
			return nil, e
		}
		hasChildren = len(children) > 0
	}
	if kind == "tool" && key.Index < 0 && (row.record.ToolName == "spawn_agent" || row.record.ToolName == "Agent" || row.record.ToolName == "Task") {
		hasChildren = true
		agent := r.makeNode(row, row.key("agent", -1), &key, "에이전트 전달", true)
		agent.Evidence = append(agent.Evidence, "dispatch_only")
		tools = append(tools, agent)
	}
	node := r.makeNode(row, key, parent, title, hasChildren)
	if kind == "tool" {
		observation, e := r.toolObservation(row, toolCall(row, key))
		if e != nil {
			return nil, e
		}
		applyToolObservation(&node, observation)
	}
	var wrapper *sessiontrace.Node
	if (view == "execution" && node.ParentID == nil) || r.flow {
		anchor, basis, e := r.inputAnchor(row)
		if e != nil {
			return nil, e
		}
		if !r.flow {
			traceAttachInput(&node, anchor, basis)
		}
		if r.flow && anchor != nil {
			node.GroupID = encodeNodeKey(*anchor)
			node.GroupingBasis = basis
		}
		if traceIsInput(row) && anchor != nil && anchor.ID == row.id && anchor.TS.Equal(row.record.Ts) && anchor.Order == row.order {
			w := r.makeNode(row, row.key("turn", -1), nil, "입력 구간", true)
			w.GroupingBasis = basis
			if r.flow {
				w.GroupID = w.NodeID
				w.Semantic = "input_group"
			}
			if basis == "exact_turn_id" {
				w.Title = "턴"
			}
			if preview := nodePreview(m["content"]); preview != "" {
				w.Title += " · " + preview
			}
			wrapper = &w
		}
	}
	out := []sessiontrace.Node{}
	if wrapper != nil {
		out = append(out, *wrapper)
	}
	if !suppressMain && key.ID == row.id && key.TS.Equal(row.record.Ts) {
		out = append(out, node)
	}
	if r.flow {
		for i := range tools {
			tools[i].GroupID = node.GroupID
			tools[i].GroupingBasis = node.GroupingBasis
		}
	}
	return append(out, tools...), nil
}

func (r traceNodeReader) makeNode(row traceNodeRow, key traceNodeKey, parent *traceNodeKey, title string, children bool) sessiontrace.Node {
	n := sessiontrace.Node{NodeID: encodeNodeKey(key), Kind: key.Kind, Title: title, Status: "unknown", HasChildren: children, SourceRef: key.Ref, Evidence: []string{"source_record"}, Availability: nodeAvailability(row.raw)}
	if row.record.Ts.After(time.Unix(0, 0)) {
		ts := row.record.Ts
		n.Timestamp = &ts
	}
	n.Role = nodeString(nodeMessage(row), "role")
	if n.Role == "" {
		switch row.record.RecordType {
		case "user", "assistant", "developer", "system":
			n.Role = row.record.RecordType
		}
	}
	n.SourceKind = row.record.RecordType
	n.Semantic = key.Kind
	if r.flow {
		traceNodeMeaning(&n, row)
	}
	if n.Timestamp != nil {
		n.TimeBasis = "point_observation"
	}
	if parent != nil {
		id := encodeNodeKey(*parent)
		n.ParentID = &id
		n.Evidence = append(n.Evidence, "exact_parent")
		n.ParentRelation = "source_reference"
		switch parent.Kind {
		case "request":
			n.ParentRelation = "native_request_ref"
		case "response":
			n.ParentRelation = "message_content"
		case "tool":
			n.ParentRelation = "tool_call_id"
		case "agent":
			n.ParentRelation = "agent_dispatch"
		}
	}
	return n
}

func (s *PgStore) TraceNodes(parent context.Context, id string, a SessionAnalysisAccess, q TraceNodesRequest) (TraceNodesResponse, error) {
	if q.View == "flow" || q.View == "timing" {
		return s.traceFlowNodes(parent, id, a, q)
	}
	out := TraceNodesResponse{SessionID: id, Nodes: []sessiontrace.Node{}, Coverage: TraceCoverage{State: "partial", RowLimit: 100}}
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 100 {
		return out, ErrTraceInvalid
	}
	if q.View == "" {
		q.View = "execution"
	}
	if q.View != "execution" && q.View != "context" && q.View != "metadata" {
		return out, ErrTraceInvalid
	}
	// The API limit caps visible nodes. Bound source processing separately so
	// an execution page can yield partial results within the database budget.
	sourceLimit := 100
	if q.View == "execution" {
		sourceLimit = 20
	}
	out.Coverage.RowLimit = sourceLimit
	ctx, cancel := traceBudget(parent)
	defer cancel()
	tx, cleanup, err := s.turnContextTx(ctx, id)
	if err != nil {
		return out, err
	}
	defer cleanup()
	if err = authorizeTrace(ctx, tx, id, a); err != nil {
		return out, err
	}
	revision, epoch, err := traceRevision(ctx, tx, id)
	if err != nil {
		return out, err
	}
	r := traceNodeReader{ctx: ctx, tx: tx, session: id, access: a, metadata: true, rootPage: q.ParentID == "" && q.View == "execution", relations: map[string]traceToolRelation{}, agentParents: map[string]*traceNodeKey{}, responses: map[string]traceResponseSummary{}}
	cursor := traceNodeCursor{Session: id, Scope: traceScope(a), Parent: q.ParentID, View: q.View, Revision: revision, Epoch: epoch}
	if q.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		if e != nil || json.Unmarshal(b, &cursor) != nil || cursor.Session != id || cursor.Scope != traceScope(a) || cursor.Parent != q.ParentID || cursor.View != q.View {
			return out, ErrTraceInvalid
		}
		if cursor.Revision != revision || cursor.Epoch != epoch {
			return out, ErrTraceSnapshot
		}
	}
	predicate := `r.record_type NOT IN ('api-request-blob','api-request-shape')`
	canonicalParent := q.ParentID
	values := []any{}
	if q.ParentID == "" {
		switch q.View {
		case "context":
			predicate += ` AND ` + traceContextSQL("r")
		case "metadata":
			predicate += ` AND ` + traceMetadataSQL("r")
		default:
			predicate += ` AND ` + traceExecutionSQL("r") + ` AND (` + traceCanonicalInputSQL("r") + ` OR ` + traceUnmatchedTurnSQL() + ` OR NOT EXISTS (SELECT 1 FROM visible_session_records n WHERE ` + traceSameSourceSQL("n", "r") + ` AND ($4::text='' OR n.login_email=$4) AND ` + traceInputSQL("n") + ` AND (n.ts,n.uuid,n.id)<=(r.ts,r.uuid,r.id)))`
		}
	}
	if q.ParentID != "" {
		p, k, e := r.selected(q.ParentID)
		if e != nil {
			return out, e
		}
		canonicalParent = encodeNodeKey(k)
		switch k.Kind {
		case "turn":
			r.group = &p
			next, e := r.nextInput(p)
			if e != nil {
				return out, e
			}
			turnPredicate, turnValues := traceTurnChildren(p, next)
			values = turnValues
			predicate += ` AND ` + turnPredicate + ` AND ` + traceExecutionSQL("r")
		case "request":
			predicate += ` AND r.raw->>'requestRef'=$5`
			values = append(values, nodeString(p.raw, "id"))
		case "response":
			if mid := nodeString(nodeMessage(p), "id"); mid != "" {
				predicate += ` AND COALESCE(r.message_id,r.raw->'message'->>'id')=$5`
				values = append(values, mid)
			} else {
				predicate += ` AND r.id=$5 AND r.ts=$6`
				values = append(values, p.id, p.record.Ts)
			}
		case "tool":
			predicate += ` AND (r.id=$5 AND r.ts=$6 OR ` + traceToolChildrenPredicate("$7") + `)`
			values = append(values, p.id, p.record.Ts, toolCall(p, k))
		case "agent":
			predicate += ` AND r.tool_use_id=$5`
			values = append(values, toolCall(p, k))
		default:
			return out, tx.Commit(ctx)
		}
	}
	if q.Cursor != "" {
		n := 5 + len(values)
		predicate += fmt.Sprintf(` AND (r.ts,r.id,`+traceNodeSourceOrder+`)>=($%d,$%d,$%d)`, n, n+1, n+2)
		if r.rootPage {
			values = append(values, cursor.Key.TS, cursor.UUID, cursor.Key.ID, cursor.Key.Order)
		} else {
			values = append(values, cursor.Key.TS, cursor.Key.ID, cursor.Key.Order)
		}
	}
	rows, err := r.rows(predicate, values, sourceLimit+1)
	if err != nil {
		return out, err
	}
	more := len(rows) > sourceLimit
	if more {
		rows = rows[:sourceLimit]
	}
	if err = r.primeToolRelations(rows); err != nil {
		return out, err
	}
	var last traceNodeKey
	lastUUID := ""
	full := false
	for _, row := range rows {
		nodes, e := r.nodes(row)
		if e != nil {
			return out, e
		}
		for _, node := range nodes {
			key, _ := decodeNodeKey(node.NodeID)
			if q.Cursor != "" && key.ID == cursor.Key.ID && key.TS.Equal(cursor.Key.TS) && key.Order == cursor.Key.Order && nodeKeyOrdinal(key) <= nodeKeyOrdinal(cursor.Key) {
				continue
			}
			pid := ""
			if node.ParentID != nil {
				pid = *node.ParentID
			}
			include := pid == canonicalParent || q.ParentID == "" && q.View != "execution"
			if include && len(out.Nodes) == q.Limit {
				more = true
				full = true
				break
			}
			last = key
			lastUUID = row.record.UUID
			if include {
				out.Nodes = append(out.Nodes, node)
			}
		}
		if full {
			break
		}
		last = row.key("~", 999999)
		lastUUID = row.record.UUID
	}
	out.Coverage.ProcessedRows = int64(len(rows))
	if !more {
		out.Coverage.State = "complete"
	} else if last.ID > 0 {
		cursor.Key = last
		cursor.UUID = lastUUID
		b, _ := json.Marshal(cursor)
		next := base64.RawURLEncoding.EncodeToString(b)
		out.NextCursor = &next
	}
	return out, tx.Commit(ctx)
}

func toolCall(row traceNodeRow, key traceNodeKey) string {
	if key.Index >= 0 && key.Index < len(nodeBlocks(row)) {
		b, _ := nodeBlocks(row)[key.Index].(map[string]any)
		return nodeString(b, "id")
	}
	return row.record.ToolCallID
}
func nodeKeyOrdinal(key traceNodeKey) int {
	if key.Kind == "turn" {
		return -1
	}
	if key.Index < 0 {
		return 0
	}
	if key.Kind == "agent" {
		return key.Index*2 + 2
	}
	return key.Index*2 + 1
}

func traceToolChildrenPredicate(parameter string) string {
	return `(r.raw->'attachment'->>'toolUseID'=` + parameter + ` OR (r.record_type='system' AND r.raw->>'toolUseID'=` + parameter + `) OR (r.record_type='tool_output' AND r.tool_call_id=` + parameter + `) OR EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(r.raw->'message'->'content')='array' THEN r.raw->'message'->'content' ELSE '[]'::jsonb END) b WHERE b->>'type'='tool_result' AND b->>'tool_use_id'=` + parameter + `))`
}

type traceToolObservation struct {
	HasChildren bool
	ResultCount int
	ResultTS    *time.Time
	Failed      bool
}

func applyToolObservation(node *sessiontrace.Node, observation traceToolObservation) {
	node.HasChildren = node.HasChildren || observation.HasChildren
	if node.Kind != "tool" || observation.ResultCount != 1 || node.Timestamp == nil || observation.ResultTS == nil || observation.ResultTS.Before(*node.Timestamp) {
		return
	}
	node.StartedAt, node.EndedAt = node.Timestamp, observation.ResultTS
	node.Status = "completed"
	if observation.Failed {
		node.Status = "error"
	}
	node.TimeBasis = "call_result_observation"
	node.Evidence = append(node.Evidence, "result_observed")
}

// The child hint and unique result interval share one metadata-only query.
// Hook bodies, tool arguments and result contents are never returned here.
func (r traceNodeReader) toolObservation(row traceNodeRow, call string) (traceToolObservation, error) {
	var out traceToolObservation
	if call == "" {
		return out, nil
	}
	if relation, ok := r.relations[traceRelationKey(row, call)]; ok {
		return relation.observation, nil
	}
	err := r.tx.QueryRow(r.ctx, `WITH children AS (
 SELECT r.ts,CASE WHEN r.record_type='tool_output' AND r.tool_call_id=$5 THEN 1 ELSE
 (SELECT count(*) FROM jsonb_array_elements(CASE WHEN jsonb_typeof(r.raw->'message'->'content')='array' THEN r.raw->'message'->'content' ELSE '[]'::jsonb END) b WHERE b->>'type'='tool_result' AND b->>'tool_use_id'=$5) END AS result_count,
 EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(r.raw->'message'->'content')='array' THEN r.raw->'message'->'content' ELSE '[]'::jsonb END) b WHERE b->>'type'='tool_result' AND b->>'tool_use_id'=$5 AND b->>'is_error'='true') AS failed
 FROM visible_session_records r WHERE r.session_id=$1
 AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4)
 AND r.profile_email=$6 AND r.user_id=$7 AND COALESCE(r.account_id,'')=$8 AND COALESCE(r.agent,'claude')=$9 AND COALESCE(r.source_file,'')=$10
 AND `+traceToolChildrenPredicate("$5")+`)
 SELECT count(*)>0,COALESCE(sum(result_count),0)::integer,min(ts) FILTER(WHERE result_count>0),COALESCE(bool_or(failed),false) FROM children`,
		r.session, r.access.UserID, r.access.ProfileEmail, r.access.LoginEmail, call, row.record.ProfileEmail, row.record.UserID, row.record.AccountID, row.record.Agent, row.record.SourceFile).Scan(&out.HasChildren, &out.ResultCount, &out.ResultTS, &out.Failed)
	return out, err
}

var _ TraceNodeStore = (*PgStore)(nil)
