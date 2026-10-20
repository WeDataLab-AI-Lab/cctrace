package store

import (
	"fmt"
	"strings"
	"time"

	"cctrace/internal/sessiontrace"
)

func traceInputSQL(alias string) string {
	return `(` + strings.NewReplacer("record_type", alias+".record_type", "is_sidechain", alias+".is_sidechain", "is_meta", alias+".is_meta", "prompt_source", alias+".prompt_source", "raw", alias+".raw", "agent", alias+".agent", "command_name", alias+".command_name").Replace(latestSessionRequestSQL) + ` AND NOT ` + traceContextSQL(alias) + `)`
}

func traceSameSourceSQL(a, b string) string {
	return fmt.Sprintf(`%[1]s.session_id=%[2]s.session_id AND %[1]s.profile_email=%[2]s.profile_email AND %[1]s.user_id=%[2]s.user_id
 AND COALESCE(%[1]s.account_id,'')=COALESCE(%[2]s.account_id,'') AND COALESCE(%[1]s.agent,'claude')=COALESCE(%[2]s.agent,'claude') AND COALESCE(%[1]s.source_file,'')=COALESCE(%[2]s.source_file,'')`, a, b)
}

func traceContextSQL(a string) string {
	return strings.NewReplacer("record_type", a+".record_type", "raw", a+".raw").Replace(`COALESCE((record_type IN ('context','developer') OR (record_type='attachment' AND raw->'attachment'->>'type'='instructions')
 OR (record_type IN ('user','assistant','message') AND (raw->'payload'->>'role'='developer' OR raw->'message'->>'role'='developer' OR raw->>'role'='developer'))
 OR (record_type='user' AND (COALESCE(raw->'message'->>'content','') LIKE '%# AGENTS.md instructions%' OR COALESCE(raw->'payload'->>'content','') LIKE '%# AGENTS.md instructions%' OR COALESCE(raw->>'content','') LIKE '%# AGENTS.md instructions%'))),false)`)
}

func traceMetadataSQL(a string) string {
	return `(` + a + `.record_type IN ('last-prompt','history-suppression','cost-state','permission-mode','atis-latch','agent-name','ai-title','custom-title','pr-link','continued-in','mode','bridge-session','queue-operation','api-request-blob','api-request-shape') OR ` + traceEmptyLegacyMetadataSQL(a) + ` OR (` + a + `.ts<='epoch'::timestamptz AND NOT ` + traceRuntimeSQL(a) + ` AND NOT ` + traceContextSQL(a) + `))`
}

func traceEmptyLegacyMetadataSQL(a string) string {
	return `COALESCE((COALESCE(NULLIF(` + a + `.agent,''),'claude')='claude' AND (
 (` + a + `.record_type='system' AND ` + a + `.raw->>'type'='system' AND ` + a + `.raw->>'subtype'='informational' AND jsonb_typeof(` + a + `.raw->'message')='null' AND NOT (` + a + `.raw ? 'content' OR ` + a + `.raw ? 'payload' OR ` + a + `.raw ? 'attachment'))
 OR (` + a + `.record_type='attachment' AND ` + a + `.raw->>'type'='attachment' AND jsonb_typeof(` + a + `.raw->'message')='null' AND NOT (` + a + `.raw ? 'content' OR ` + a + `.raw ? 'payload' OR ` + a + `.raw ? 'attachment'))
)),false)`
}

func traceRuntimeSQL(a string) string {
	return `COALESCE((` + a + `.record_type IN ('user','assistant','tool_call','tool_output','reasoning','usage','api-request','agent_task') OR left(` + a + `.raw->'attachment'->>'type',5)='hook_' OR ` + a + `.raw->'attachment'->>'type'='async_hook_response' OR ` + a + `.raw->>'subtype'='stop_hook_summary'),false)`
}

func traceExecutionSQL(a string) string {
	return `(NOT ` + traceMetadataSQL(a) + ` AND NOT ` + traceContextSQL(a) + `)`
}

func traceNodeView(row traceNodeRow) string {
	attachment, _ := row.raw["attachment"].(map[string]any)
	if row.record.RecordType == "context" || row.record.RecordType == "developer" || nodeString(nodeMessage(row), "role") == "developer" || nodeString(attachment, "type") == "instructions" || len(traceInstructionEntries(row)) > 0 {
		return "context"
	}
	for _, kind := range []string{"last-prompt", "history-suppression", "cost-state", "permission-mode", "atis-latch", "agent-name", "ai-title", "custom-title", "pr-link", "continued-in", "mode", "bridge-session", "queue-operation", "api-request-blob", "api-request-shape"} {
		if row.record.RecordType == kind {
			return "metadata"
		}
	}
	if row.emptyLegacyMetadata || traceEmptyLegacyMetadata(row) {
		return "metadata"
	}
	runtime := false
	for _, kind := range []string{"user", "assistant", "tool_call", "tool_output", "reasoning", "usage", "api-request", "agent_task"} {
		runtime = runtime || row.record.RecordType == kind
	}
	runtime = runtime || strings.HasPrefix(nodeString(attachment, "type"), "hook_") || nodeString(attachment, "type") == "async_hook_response" || nodeString(row.raw, "subtype") == "stop_hook_summary"
	if !row.record.Ts.After(time.Unix(0, 0)) && !runtime {
		return "metadata"
	}
	return "execution"
}

func traceEmptyLegacyMetadata(row traceNodeRow) bool {
	if row.record.Agent != "" && row.record.Agent != "claude" {
		return false
	}
	message, hasMessage := row.raw["message"]
	if !hasMessage || message != nil {
		return false
	}
	for _, key := range []string{"content", "payload", "attachment"} {
		if _, exists := row.raw[key]; exists {
			return false
		}
	}
	switch row.record.RecordType {
	case "system":
		return nodeString(row.raw, "type") == "system" && nodeString(row.raw, "subtype") == "informational"
	case "attachment":
		return nodeString(row.raw, "type") == "attachment"
	default:
		return false
	}
}

func traceIsInput(row traceNodeRow) bool {
	if row.record.RecordType != "user" || row.record.IsSidechain || row.record.IsMeta || traceNodeView(row) != "execution" {
		return false
	}
	switch row.record.PromptSource {
	case "typed", "paste", "queued", "suggestion_accepted":
		return true
	case "":
	default:
		return false
	}
	payload, _ := row.raw["payload"].(map[string]any)
	if role, exists := payload["role"]; exists && role != nil && role != "user" {
		return false
	}
	message, _ := row.raw["message"].(map[string]any)
	blocks, _ := message["content"].([]any)
	for _, block := range blocks {
		if m, ok := block.(map[string]any); ok && nodeString(m, "type") == "tool_result" {
			return false
		}
	}
	return true
}

func traceContainsAgents(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(v, "# AGENTS.md instructions")
	case []any:
		for _, item := range v {
			if block, ok := item.(map[string]any); ok && traceContainsAgents(block["text"]) {
				return true
			}
		}
	}
	return false
}

func traceTurnID(row traceNodeRow) string {
	if row.record.Agent != "codex" {
		return ""
	}
	if row.record.CodexRootTurnID != "" {
		return row.record.CodexRootTurnID
	}
	if row.record.CodexTurnID != "" {
		return row.record.CodexTurnID
	}
	m := nodeMessage(row)
	if id := nodeString(m, "root_turn_id"); id != "" {
		return id
	}
	if id := nodeString(m, "turn_id"); id != "" {
		return id
	}
	nested, _ := m["internal_chat_message_metadata_passthrough"].(map[string]any)
	return nodeString(nested, "turn_id")
}

const traceTurnIDSQL = `COALESCE(NULLIF(r.codex_root_turn_id,''),NULLIF(r.codex_turn_id,''),CASE WHEN jsonb_typeof(r.raw->'payload'->'root_turn_id')='string' THEN NULLIF(r.raw->'payload'->>'root_turn_id','') END,CASE WHEN jsonb_typeof(r.raw->'payload'->'turn_id')='string' THEN NULLIF(r.raw->'payload'->>'turn_id','') END,CASE WHEN jsonb_typeof(r.raw->'payload'->'internal_chat_message_metadata_passthrough'->'turn_id')='string' THEN r.raw->'payload'->'internal_chat_message_metadata_passthrough'->>'turn_id' END,CASE WHEN jsonb_typeof(r.raw #> '{metadata,retained_source,id,turn_id}')='string' THEN NULLIF(r.raw #>> '{metadata,retained_source,id,turn_id}','') END,'')`

func traceCanonicalInputSQL(a string) string {
	id := strings.ReplaceAll(traceTurnIDSQL, "r.", a+".")
	prior := strings.ReplaceAll(traceTurnIDSQL, "r.", "opener.")
	return `(` + traceInputSQL(a) + ` AND (COALESCE(` + a + `.agent,'claude')<>'codex' OR ` + id + `='' OR NOT EXISTS(SELECT 1 FROM visible_session_records opener WHERE ` + traceSameSourceSQL("opener", a) + ` AND ($4::text='' OR opener.login_email=$4) AND ` + traceInputSQL("opener") + ` AND ` + prior + `=` + id + ` AND (opener.ts,opener.uuid,opener.id)<(` + a + `.ts,` + a + `.uuid,` + a + `.id))))`
}

func (r traceNodeReader) inputAnchor(row traceNodeRow) (*traceNodeKey, string, error) {
	id := traceTurnID(row)
	if r.group != nil && traceSameSource(row, *r.group) {
		basis := "source_sequence"
		if id != "" {
			if id != traceTurnID(*r.group) {
				return nil, "", nil
			}
			basis = "exact_turn_id"
		}
		key := r.group.key("turn", -1)
		return &key, basis, nil
	}
	if r.rootPage && traceIsInput(row) {
		k := row.key("turn", -1)
		basis := "source_sequence"
		if id != "" {
			basis = "exact_turn_id"
		}
		return &k, basis, nil
	}
	if r.rootPage {
		return nil, "", nil
	} // SQL already selected canonical inputs or proven input-orphans.
	if traceIsInput(row) && id == "" {
		k := row.key("turn", -1)
		basis := "source_sequence"
		return &k, basis, nil
	}
	metadata := r
	metadata.metadata = true
	metadata.inputOrder = true
	if id != "" {
		matches, err := metadata.scoped(row, traceInputSQL("r")+` AND `+traceTurnIDSQL+`=$5`, []any{id}, 1, true)
		if err != nil {
			return nil, "", err
		}
		if len(matches) == 1 {
			k := matches[0].key("turn", -1)
			return &k, "exact_turn_id", nil
		}
		return nil, "", nil
	}
	metadata.descending = true
	matches, err := metadata.scoped(row, traceCanonicalInputSQL("r")+` AND (r.ts,r.uuid,r.id)<=($5,$6,$7)`, []any{row.record.Ts, row.record.UUID, row.id}, 1, true)
	if err != nil || len(matches) == 0 {
		return nil, "", err
	}
	if other := traceTurnID(matches[0]); id != "" && other != "" && id != other {
		return nil, "", nil
	}
	k := matches[0].key("turn", -1)
	return &k, "source_sequence", nil
}

// Root membership repeatedly consults the same small input-key set. Materialize
// its source identity once; no user body is retained in this SQL projection.
func traceInputSetCTE() string {
	return `trace_inputs AS MATERIALIZED (SELECT r.session_id,r.ts,r.id,r.uuid,r.record_type,r.profile_email,r.user_id,r.login_email,COALESCE(r.account_id,'') AS account_id,COALESCE(r.agent,'claude') AS agent,COALESCE(r.source_file,'') AS source_file,
 CASE WHEN r.agent='codex' THEN ` + traceTurnIDSQL + ` ELSE '' END AS native_turn_id
 FROM visible_session_records r WHERE r.session_id=$1 AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4) AND ` + traceInputSQL("r") + `),
 trace_first_inputs AS MATERIALIZED (SELECT DISTINCT ON (profile_email,user_id,account_id,agent,source_file) * FROM trace_inputs
 ORDER BY profile_email,user_id,account_id,agent,source_file,ts,uuid,id), `
}

func traceUseInputSet(predicate string) string {
	for _, a := range []string{"n", "opener"} {
		predicate = strings.ReplaceAll(predicate, traceInputSQL(a), "TRUE")
		predicate = strings.ReplaceAll(predicate, strings.ReplaceAll(traceTurnIDSQL, "r.", a+"."), a+".native_turn_id")
		predicate = strings.ReplaceAll(predicate, "FROM visible_session_records "+a+" WHERE", "FROM trace_inputs "+a+" WHERE")
	}
	prior := `FROM trace_inputs n WHERE ` + traceSameSourceSQL("n", "r") + ` AND ($4::text='' OR n.login_email=$4) AND TRUE AND (n.ts,n.uuid,n.id)<=(r.ts,r.uuid,r.id)`
	predicate = strings.ReplaceAll(predicate, prior, strings.Replace(prior, "FROM trace_inputs n", "FROM trace_first_inputs n", 1))
	return predicate
}

func traceRootCandidatesCTE(limit int, cursor bool) string {
	canonical := traceUseInputSet(traceCanonicalInputSQL("r"))
	canonical = strings.ReplaceAll(canonical, traceInputSQL("r"), "TRUE")
	canonical = strings.ReplaceAll(canonical, traceTurnIDSQL, "r.native_turn_id")
	unmatched := traceUseInputSet(traceUnmatchedTurnSQL())
	orphans := `SELECT r.ts,r.id,r.uuid,r.record_type,r.profile_email,` + traceNodeSourceOrder + ` AS source_order
 FROM session_records r LEFT JOIN trace_first_inputs first_input ON ` + traceSameSourceSQL("r", "first_input") + `
 WHERE r.session_id=$1
 AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4)
 AND (first_input.id IS NULL
 OR ((r.ts,r.uuid,r.id)<(first_input.ts,first_input.uuid,first_input.id) AND (COALESCE(r.agent,'claude')<>'codex' OR ` + traceTurnIDSQL + `='' OR ` + unmatched + `))
 OR ((r.ts,r.uuid,r.id)>=(first_input.ts,first_input.uuid,first_input.id) AND ` + unmatched + `))
 AND ` + traceExecutionSQL("r")
	if cursor {
		orphans += ` AND (r.ts,r.uuid,r.id,` + traceNodeSourceOrder + `)>=($5,$6,$7,$8)`
	}
	// TraceNodes authorizes the complete session before enabling rootPage in
	// the same repeatable-read transaction: admin rejects any excluded/blocked
	// source; personal ownership requires bool_and(allowed) over every source.
	// Raw candidates contain keys only and retain caller/source bounds. The final
	// selected-key projection still rechecks the visibility view and caller scope.
	// Only the global orphan page can enter the final root page. Limiting each
	// source first instead multiplies work by every source in a large session.
	return `trace_root_candidates AS MATERIALIZED (
 SELECT r.ts,r.id,r.uuid,r.record_type,r.profile_email,md5(jsonb_build_array(r.uuid,r.record_type,r.profile_email)::text) AS source_order
 FROM trace_inputs r WHERE ` + canonical + `
 UNION ALL (` + orphans + ` ORDER BY r.ts,r.uuid,r.id,source_order LIMIT ` + fmt.Sprint(limit) + `)), `
}

func traceSameSource(a, b traceNodeRow) bool {
	x, y := a.record, b.record
	return x.SessionID == y.SessionID && x.UserID == y.UserID && x.ProfileEmail == y.ProfileEmail && x.AccountID == y.AccountID && x.Agent == y.Agent && x.SourceFile == y.SourceFile
}

type traceToolRelation struct {
	parent      *traceNodeKey
	observation traceToolObservation
}

func traceRelationKey(row traceNodeRow, call string) string {
	key, _ := sessiontrace.CanonicalHash([]any{row.record.SessionID, row.record.UserID, row.record.ProfileEmail, row.record.AccountID, row.record.Agent, row.record.SourceFile, call})
	return key
}

// A bounded metadata page shares its exact call/result lookups. No arguments,
// output contents or hook bodies participate in this relation cache.
func (r traceNodeReader) primeToolRelations(page []traceNodeRow) error {
	if r.relations == nil {
		return nil
	}
	type scopeCalls struct {
		row  traceNodeRow
		ids  []string
		seen map[string]bool
	}
	groups := map[string]*scopeCalls{}
	for _, row := range page {
		scope := traceRelationKey(row, "")
		g := groups[scope]
		if g == nil {
			g = &scopeCalls{row: row, seen: map[string]bool{}}
			groups[scope] = g
		}
		add := func(id string) {
			if id != "" && !g.seen[id] {
				g.seen[id] = true
				g.ids = append(g.ids, id)
			}
		}
		add(row.record.ToolCallID)
		attachment, _ := row.raw["attachment"].(map[string]any)
		add(nodeString(attachment, "toolUseID"))
		add(nodeString(row.raw, "toolUseID"))
		for _, value := range nodeBlocks(row) {
			if b, ok := value.(map[string]any); ok {
				if nodeString(b, "type") == "tool_use" {
					add(nodeString(b, "id"))
				}
				if nodeString(b, "type") == "tool_result" {
					add(nodeString(b, "tool_use_id"))
				}
			}
		}
	}
	for _, g := range groups {
		if len(g.ids) == 0 {
			continue
		}
		reader := r
		reader.metadata = true
		rows, err := reader.scoped(g.row, `(r.tool_call_id=ANY($5::text[]) AND r.record_type IN ('tool_call','tool_output') OR r.raw->'attachment'->>'toolUseID'=ANY($5::text[]) OR (r.record_type='system' AND r.raw->>'toolUseID'=ANY($5::text[])) OR EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(r.raw->'message'->'content')='array' THEN r.raw->'message'->'content' ELSE '[]'::jsonb END) b WHERE b->>'type'='tool_use' AND b->>'id'=ANY($5::text[]) OR b->>'type'='tool_result' AND b->>'tool_use_id'=ANY($5::text[])))`, []any{g.ids}, 501, true)
		if err != nil {
			return err
		}
		if len(rows) > 500 {
			continue
		} // Never treat an incomplete candidate set as unique.
		parents := map[string][]traceNodeKey{}
		observations := map[string]traceToolObservation{}
		for _, row := range rows {
			child := func(id string, result bool, failed bool) {
				if !g.seen[id] {
					return
				}
				o := observations[id]
				o.HasChildren = true
				if result {
					o.ResultCount++
					o.Failed = o.Failed || failed
					ts := row.record.Ts
					if o.ResultTS == nil || ts.Before(*o.ResultTS) {
						o.ResultTS = &ts
					}
				}
				observations[id] = o
			}
			if row.record.RecordType == "tool_call" && g.seen[row.record.ToolCallID] {
				parents[row.record.ToolCallID] = append(parents[row.record.ToolCallID], row.key("tool", -1))
			}
			if row.record.RecordType == "tool_output" {
				child(row.record.ToolCallID, true, false)
			}
			attachment, _ := row.raw["attachment"].(map[string]any)
			child(nodeString(attachment, "toolUseID"), false, false)
			if row.record.RecordType == "system" {
				child(nodeString(row.raw, "toolUseID"), false, false)
			}
			for i, value := range nodeBlocks(row) {
				if b, ok := value.(map[string]any); ok {
					if nodeString(b, "type") == "tool_use" && g.seen[nodeString(b, "id")] {
						id := nodeString(b, "id")
						parents[id] = append(parents[id], row.key("tool", i))
					}
					if nodeString(b, "type") == "tool_result" {
						failed, _ := b["is_error"].(bool)
						child(nodeString(b, "tool_use_id"), true, failed)
					}
				}
			}
		}
		for _, id := range g.ids {
			relation := traceToolRelation{observation: observations[id]}
			if p := parents[id]; len(p) == 1 {
				key := p[0]
				relation.parent = &key
			}
			r.relations[traceRelationKey(g.row, id)] = relation
		}
	}
	return r.primeAgentParents(page)
}

func traceToolParentBranches(ids string) (string, string) {
	return `r.tool_call_id=ANY(` + ids + `::text[]) AND r.record_type='tool_call'`, `jsonb_path_query_array(CASE WHEN jsonb_typeof(r.raw->'message'->'content')='array' THEN r.raw->'message'->'content' ELSE '[]'::jsonb END, '$[*] ? (@.type == "tool_use").id') ?| ` + ids + `::text[]`
}

func traceToolParentsSQL(ids string) string {
	typed, native := traceToolParentBranches(ids)
	return `(` + typed + ` OR ` + native + `)`
}

// Match indexed native IDs before source/visibility planning can replace the GIN
// probe with a broad scope scan. This stage contains keys only. Every original
// caller/source predicate and visibility rule is checked before the key limit;
// no candidate body is projected until the final visible-source join.
func traceParentKeyQuery(selectSQL, order, limit string) string {
	typed, native := traceToolParentBranches("$5")
	rawTyped := strings.Replace(selectSQL, "FROM visible_session_records r", "FROM session_records r", 1)
	rawTyped = strings.Replace(rawTyped, traceToolParentsSQL("$5"), `(`+typed+`)`, 1)
	rawTyped = strings.Replace(rawTyped, "SELECT ", "SELECT r.session_id,", 1)
	columns := strings.SplitN(selectSQL, " FROM visible_session_records r", 2)[0]
	rawNative := strings.Replace(columns, "SELECT ", "SELECT r.session_id,", 1) + ` FROM session_records r WHERE ` + native
	verified := strings.Replace(selectSQL, "FROM visible_session_records r", `FROM visible_session_records r JOIN trace_parent_candidates c ON r.session_id=c.session_id AND r.ts=c.ts AND r.id=c.id AND r.uuid=c.uuid AND r.record_type=c.record_type AND r.profile_email=c.profile_email`, 1)
	verified = strings.Replace(verified, traceToolParentsSQL("$5"), "TRUE", 1)
	return `WITH trace_parent_native_keys AS MATERIALIZED (` + rawNative + `), trace_parent_candidates AS MATERIALIZED (` + rawTyped + ` UNION SELECT * FROM trace_parent_native_keys) ` + verified + ` ORDER BY ` + order + ` LIMIT ` + limit
}

// Sidechain rows refer to dispatch calls across source files. Batch only exact
// parent keys within the same visible session/profile/user/account/agent scope.
func traceAgentRelationKey(row traceNodeRow, call string) string {
	copy := *row.record
	row.record = &copy
	row.record.SourceFile = ""
	return traceRelationKey(row, call)
}

func (r traceNodeReader) primeAgentParents(page []traceNodeRow) error {
	if r.agentParents == nil {
		return nil
	}
	type group struct {
		row  traceNodeRow
		ids  []string
		seen map[string]bool
	}
	groups := map[string]*group{}
	for _, row := range page {
		call := row.record.ToolUseID
		if call == "" {
			continue
		}
		scope := traceAgentRelationKey(row, "")
		g := groups[scope]
		if g == nil {
			g = &group{row: row, seen: map[string]bool{}}
			groups[scope] = g
		}
		if !g.seen[call] {
			g.seen[call] = true
			g.ids = append(g.ids, call)
		}
	}
	for _, g := range groups {
		reader := r
		reader.metadata = true
		reader.parentKeys = true
		rows, err := reader.scoped(g.row, traceToolParentsSQL("$5"), []any{g.ids}, 501, false)
		if err != nil {
			return err
		}
		if len(rows) > 500 {
			continue
		}
		parents := map[string][]traceNodeKey{}
		for _, row := range rows {
			found := map[string]int{}
			if row.record.RecordType == "tool_call" && g.seen[row.record.ToolCallID] {
				found[row.record.ToolCallID] = -1
			}
			for i, value := range nodeBlocks(row) {
				if b, ok := value.(map[string]any); ok && nodeString(b, "type") == "tool_use" {
					call := nodeString(b, "id")
					if index, exists := found[call]; g.seen[call] && (!exists || index < 0) {
						found[call] = i
					}
				}
			}
			for call, index := range found {
				parents[call] = append(parents[call], row.key("tool", index))
			}
		}
		for _, call := range g.ids {
			var parent *traceNodeKey
			if found := parents[call]; len(found) == 1 {
				copy := found[0]
				parent = &copy
			}
			r.agentParents[traceAgentRelationKey(g.row, call)] = parent
		}
	}
	return nil
}

func traceUnmatchedTurnSQL() string {
	native := strings.ReplaceAll(traceTurnIDSQL, "r.", "n.")
	return `(COALESCE(r.agent,'claude')='codex' AND ` + traceTurnIDSQL + `<>'' AND NOT EXISTS(SELECT 1 FROM visible_session_records n WHERE ` + traceSameSourceSQL("n", "r") + ` AND ($4::text='' OR n.login_email=$4) AND ` + traceInputSQL("n") + ` AND ` + native + `=` + traceTurnIDSQL + `))`
}

func traceAttachInput(node *sessiontrace.Node, key *traceNodeKey, basis string) {
	if node.ParentID != nil || key == nil || node.Kind == "turn" {
		return
	}
	id := encodeNodeKey(*key)
	node.ParentID = &id
	node.GroupingBasis = basis
	node.ParentRelation = "source_sequence"
	if basis == "exact_turn_id" {
		node.ParentRelation = "turn_membership"
	}
	node.Evidence = append(node.Evidence, basis)
}

func (r traceNodeReader) nextInput(row traceNodeRow) (*traceNodeRow, error) {
	reader := r
	reader.metadata = true
	reader.inputOrder = true
	rows, err := reader.scoped(row, traceCanonicalInputSQL("r")+` AND (r.ts,r.uuid,r.id)>($5,$6,$7)`, []any{row.record.Ts, row.record.UUID, row.id}, 1, true)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func traceTurnChildren(row traceNodeRow, next *traceNodeRow) (string, []any) {
	scope := `r.profile_email=$5 AND r.user_id=$6 AND COALESCE(r.account_id,'')=$7 AND COALESCE(r.agent,'claude')=$8 AND COALESCE(r.source_file,'')=$9`
	sequence := `(r.ts,r.uuid,r.id)>=($10,$11,$12)`
	args := []any{row.record.ProfileEmail, row.record.UserID, row.record.AccountID, row.record.Agent, row.record.SourceFile, row.record.Ts, row.record.UUID, row.id}
	if next != nil {
		sequence += ` AND (r.ts,r.uuid,r.id)<($13,$14,$15)`
		args = append(args, next.record.Ts, next.record.UUID, next.id)
	}
	if id := traceTurnID(row); id != "" {
		sequence = `(` + traceTurnIDSQL + `=$` + fmt.Sprint(5+len(args)) + ` OR (` + traceTurnIDSQL + `='' AND ` + sequence + `))`
		args = append(args, id)
	}
	return scope + ` AND (` + sequence + `)`, args
}

func traceInstructionEntries(row traceNodeRow) []any {
	var entries []any
	add := func(kind, path string, value any, file any) {
		if value == nil || value == "" {
			return
		}
		if values, ok := value.([]any); ok && len(values) == 0 {
			return
		}
		entry := map[string]any{"kind": kind, "source_path": path, "body": traceAuditValue(value)}
		if file != nil {
			entry["path"] = file
		}
		entries = append(entries, entry)
	}
	m := nodeMessage(row)
	prefix := "payload"
	if _, ok := row.raw["payload"].(map[string]any); !ok && row.record.Agent == "codex" {
		prefix = ""
	}
	if row.record.Agent != "codex" {
		if _, ok := row.raw["message"].(map[string]any); ok {
			prefix = "message"
		}
	}
	if m == nil {
		m = row.raw
		prefix = ""
	}
	field := func(key string) string {
		if prefix == "" {
			return key
		}
		return prefix + "." + key
	}
	for _, key := range []string{"base_instructions", "developer_instructions", "user_instructions"} {
		add(key, field(key), m[key], nil)
	}
	if c, ok := m["collaboration_mode"].(map[string]any); ok {
		if settings, ok := c["settings"].(map[string]any); ok {
			add("developer_instructions", field("collaboration_mode.settings.developer_instructions"), settings["developer_instructions"], nil)
		}
	}
	if nodeString(m, "role") == "developer" || row.record.RecordType == "developer" {
		add("developer_instructions", field("content"), m["content"], nil)
	}
	if row.record.RecordType == "user" && (traceContainsAgents(m["content"]) || row.raw["cctraceHasAgentsInstructions"] == true) {
		add("agents_md", field("content"), m["content"], nil)
	}
	attachment, _ := row.raw["attachment"].(map[string]any)
	if nodeString(attachment, "type") == "instructions" {
		files, _ := attachment["files"].([]any)
		for _, value := range files {
			if f, ok := value.(map[string]any); ok {
				add("instruction_file", "attachment.files[].content", f["content"], f["path"])
			}
		}
		add("instructions", "attachment.content", attachment["content"], attachment["path"])
	}
	if row.record.RecordType == "context" {
		add("instructions", "content", row.raw["content"], nil)
	}
	if kinds, ok := row.raw["cctraceInstructionKinds"].([]any); ok && len(entries) == 0 {
		for _, kind := range kinds {
			add(fmt.Sprint(kind), "", true, nil)
		}
	}
	return entries
}

func traceStructuredContext(row traceNodeRow) map[string]any {
	m := nodeMessage(row)
	settings, provenance := map[string]any{}, map[string]any{}
	for _, key := range []string{"model", "effort", "approval_policy", "approvals_reviewer", "permission_profile", "sandbox_policy", "runtime_workspace_roots", "context_window", "model_context_window"} {
		if v, ok := m[key]; ok {
			settings[key] = v
		}
	}
	for _, key := range []string{"parent_thread_id", "forked_from_id", "thread_source", "source", "history_base", "history_mode", "agent_path", "agent_nickname", "turn_id", "root_turn_id"} {
		if v, ok := m[key]; ok {
			provenance[key] = v
		}
	}
	var sv, pv any
	if len(settings) > 0 {
		sv = settings
	}
	if len(provenance) > 0 {
		pv = provenance
	}
	entries := traceInstructionEntries(row)
	if entries == nil {
		entries = []any{}
	}
	return map[string]any{"instructions": entries, "settings": traceAuditValue(sv), "provenance": traceAuditValue(pv)}
}
