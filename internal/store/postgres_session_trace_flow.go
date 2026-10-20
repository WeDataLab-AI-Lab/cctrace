package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cctrace/internal/sessiontrace"
)

// Flow cursors bind every membership/search/time input and both source fences.
// Before selects the preceding keyset page; Locate includes the root itself.
type traceFlowCursor struct {
	Session, Scope, Parent, Group, View, Query string
	From, To                                   *time.Time
	Revision, Epoch                            int64
	RequestNo                                  int
	Key                                        traceNodeKey
	UUID                                       string
	Before, Locate                             bool
}

func encodeFlowCursor(c traceFlowCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func sameTraceTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

func traceNodeMeaning(n *sessiontrace.Node, row traceNodeRow) {
	// Role describes the original envelope; only explicit instruction evidence
	// gives its contents instruction semantics. Runtime developer notices stay messages.
	if n.Role == "developer" {
		n.Semantic = "message"
		text := strings.ToLower(nodePreview(nodeMessage(row)["content"]))
		for _, marker := range []string{"interrupt", "interrupted", "daemon", "notification", "<turn_aborted>", "[request interrupted"} {
			if strings.Contains(text, marker) {
				n.Semantic = "runtime_notice"
				break
			}
		}
	} else if n.Kind == "instructions" {
		n.Semantic = "instructions"
	}
	if n.Role == "developer" && n.Semantic == "runtime_notice" {
		n.Title = nodePreview(nodeMessage(row)["content"])
		if n.Title == "" {
			n.Title = "실행 알림"
		}
	}
}

func (s *PgStore) traceFlowNodes(parent context.Context, id string, a SessionAnalysisAccess, q TraceNodesRequest) (TraceNodesResponse, error) {
	out := TraceNodesResponse{SessionID: id, Nodes: []sessiontrace.Node{}, Coverage: TraceCoverage{State: "partial", RowLimit: 100}}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 100 || q.RequestNo < 0 || len(q.Query) > 256 || q.View == "timing" && q.GroupID == "" || q.From != nil && q.To != nil && !q.From.Before(*q.To) {
		return out, ErrTraceInvalid
	}
	if q.View == "flow" && (q.From != nil || q.To != nil) {
		return out, ErrTraceInvalid
	}
	// Each source can require exact relation lookups. Bound that work separately
	// from the visible-node upper limit so large pages retain the one-second
	// read budget; cursors carry the remaining sources, including folded rows.
	sourceWindow := min(q.Limit+2, 22)
	out.Coverage.RowLimit = sourceWindow
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
	out.Revision, out.Epoch = revision, epoch
	c := traceFlowCursor{Session: id, Scope: traceScope(a), Parent: q.ParentID, Group: q.GroupID, View: q.View, Query: q.Query, From: q.From, To: q.To, Revision: revision, Epoch: epoch, RequestNo: q.RequestNo}
	if q.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		var decoded traceFlowCursor
		if e != nil || json.Unmarshal(b, &decoded) != nil || decoded.Session != id || decoded.Scope != c.Scope || decoded.Parent != c.Parent || decoded.Group != c.Group || decoded.View != c.View || decoded.Query != c.Query || decoded.RequestNo != c.RequestNo || !sameTraceTime(decoded.From, c.From) || !sameTraceTime(decoded.To, c.To) {
			return out, ErrTraceInvalid
		}
		if decoded.Revision != revision || decoded.Epoch != epoch {
			return out, ErrTraceSnapshot
		}
		c = decoded
	}
	r := traceNodeReader{ctx: ctx, tx: tx, session: id, access: a, flow: true, metadata: true, relations: map[string]traceToolRelation{}, agentParents: map[string]*traceNodeKey{}, responses: map[string]traceResponseSummary{}}
	if q.RequestNo > 0 {
		if q.View != "flow" || q.ParentID != "" || q.GroupID != "" || q.Query != "" {
			return out, ErrTraceInvalid
		}
		if q.Cursor == "" {
			key, uuid, e := r.requestNumber(q.RequestNo, revision, epoch)
			if e != nil {
				return out, e
			}
			c.Key = key
			c.UUID = uuid
			c.Locate = true
			q.Cursor = encodeFlowCursor(c)
		}
	}
	predicate := `r.record_type <> 'usage' AND NOT ` + traceMetadataSQL("r")
	values := []any{}
	groupID := q.GroupID
	if groupID == "" && q.ParentID != "" {
		k, e := decodeNodeKey(q.ParentID)
		if e == nil && k.Kind == "turn" {
			groupID = q.ParentID
		}
	}
	canonicalParent := q.ParentID
	if groupID != "" {
		p, k, e := r.selected(groupID)
		if e != nil {
			return out, e
		}
		if k.Kind != "turn" || !traceIsInput(p) {
			return out, ErrTraceInvalid
		}
		anchor, _, e := r.inputAnchor(p)
		if e != nil {
			return out, e
		}
		if anchor == nil || encodeNodeKey(*anchor) != encodeNodeKey(k) {
			return out, ErrTraceInvalid
		}
		r.group = &p
		next, e := r.nextInput(p)
		if e != nil {
			return out, e
		}
		pred, args := traceTurnChildren(p, next)
		predicate += ` AND ` + pred
		values = args
	} else if q.ParentID != "" {
		p, k, e := r.selected(q.ParentID)
		if e != nil {
			return out, e
		}
		canonicalParent = encodeNodeKey(k)
		switch k.Kind {
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
	} else {
		// Only canonical input containers and source-orphan records appear at root.
		predicate += ` AND (` + traceCanonicalInputSQL("r") + ` OR ` + traceUnmatchedTurnSQL() + ` OR NOT EXISTS(SELECT 1 FROM visible_session_records n WHERE ` + traceSameSourceSQL("n", "r") + ` AND ($4::text='' OR n.login_email=$4) AND ` + traceInputSQL("n") + ` AND (n.ts,n.uuid,n.id)<=(r.ts,r.uuid,r.id)))`
	}
	if q.View == "timing" {
		args := []any{id, a.UserID, a.ProfileEmail, a.LoginEmail}
		args = append(args, values...)
		if err = tx.QueryRow(ctx, `SELECT min(r.ts) FILTER(WHERE r.ts>'epoch'::timestamptz),max(r.ts) FILTER(WHERE r.ts>'epoch'::timestamptz) FROM visible_session_records r WHERE r.session_id=$1 AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4) AND (`+predicate+`)`, args...).Scan(&out.Coverage.From, &out.Coverage.To); err != nil {
			return out, err
		}
		if q.From != nil {
			out.Coverage.From = q.From
		}
		if q.To != nil {
			out.Coverage.To = q.To
		}
	}
	if q.Query != "" {
		n := 5 + len(values)
		term, path := traceFlowSearchValues(q.Query)
		values = append(values, term, path)
		search := traceFlowSearchPredicateSQL("r", fmt.Sprintf("$%d", n), fmt.Sprintf("$%d", n+1))

		predicate += ` AND (` + search + `)`
		out.SearchScope = "request_titles_and_tool_names"
		out.SearchCoverage = "source_metadata"
	}
	// A range limits candidate observations, while exact result lookup remains
	// outside this window so a crossing invocation retains its real endpoint.
	if q.View == "timing" && q.To != nil {
		n := 5 + len(values)
		predicate += fmt.Sprintf(` AND r.ts<=$%d`, n)
		values = append(values, *q.To)
	}
	if q.View == "timing" && q.From != nil {
		n := 5 + len(values)
		term := fmt.Sprintf("$%d", n)
		values = append(values, *q.From)
		// Keep earlier calls whose exact result is within/after the range. The
		// bounded relation projection later rejects ambiguous candidate identities.
		predicate += ` AND (r.ts>=` + term + ` OR r.record_type IN ('tool_call','assistant'))`
	}
	if q.Cursor != "" {
		n := 5 + len(values)
		op := ">="
		if c.Before {
			op = "<="
		}
		predicate += fmt.Sprintf(` AND (r.ts,r.uuid,r.id,`+traceNodeSourceOrder+`)`+op+`($%d,$%d,$%d,$%d)`, n, n+1, n+2, n+3)
		values = append(values, c.Key.TS, c.UUID, c.Key.ID, c.Key.Order)
	}
	r.rootPage = q.View == "flow" && q.ParentID == "" && q.GroupID == "" && q.Query == ""
	var rows []traceNodeRow
	if r.rootPage {
		r.descending = c.Before
		rows, err = r.rows(predicate, values, sourceWindow)
	} else if q.View == "flow" && q.ParentID == "" && q.GroupID == "" && q.Query != "" {
		rows, err = r.flowRootSearchRows(q.Query, c, q.Cursor != "", sourceWindow)
	} else {
		rows, err = r.flowRows(predicate, values, sourceWindow, c.Before)
	}
	if err != nil {
		return out, err
	}
	// Parent links and response folding can consume a full source window
	// without filling even one visible-node page.
	moreSources := len(rows) == sourceWindow
	if err = r.primeToolRelations(rows); err != nil {
		return out, err
	}
	type candidate struct {
		node sessiontrace.Node
		key  traceNodeKey
		uuid string
	}
	candidates := []candidate{}
	for _, row := range rows {
		nodes, e := r.nodes(row)
		if e != nil {
			return out, e
		}
		if c.Before {
			for i, j := 0, len(nodes)-1; i < j; i, j = i+1, j-1 {
				nodes[i], nodes[j] = nodes[j], nodes[i]
			}
		}
		for _, node := range nodes {
			key, _ := decodeNodeKey(node.NodeID)
			if q.Cursor != "" && key.ID == c.Key.ID && key.TS.Equal(c.Key.TS) && key.Order == c.Key.Order {
				if c.Key.Kind == "~" {
					continue // This entire source row was consumed by an empty page.
				}
				ordinal, target := nodeKeyOrdinal(key), nodeKeyOrdinal(c.Key)
				if !c.Before && ordinal < target || c.Before && ordinal > target || !c.Locate && ordinal == target {
					continue
				}
			}
			include := false
			if q.View == "timing" {
				include = node.Kind != "turn" && node.GroupID == groupID
				if include && q.From != nil {
					end := node.Timestamp
					if node.EndedAt != nil {
						end = node.EndedAt
					}
					include = end != nil && !end.Before(*q.From)
				}
			} else if groupID != "" && q.ParentID == groupID {
				include = node.Kind != "turn" && node.ParentID == nil && node.GroupID == groupID
			} else if q.ParentID != "" {
				include = node.ParentID != nil && *node.ParentID == canonicalParent
			} else {
				include = node.Kind == "turn" || node.ParentID == nil && node.GroupID == ""
			}
			if include {
				candidates = append(candidates, candidate{node, key, row.record.UUID})
			}
		}
		if len(candidates) > q.Limit {
			break
		}
	}
	hasMore := len(candidates) > q.Limit || moreSources
	if len(candidates) > q.Limit {
		candidates = candidates[:q.Limit]
	}
	out.Coverage.ProcessedRows = int64(len(rows))
	if !hasMore {
		out.Coverage.State = "complete"
	}
	if c.Before {
		for i, j := 0, len(candidates)-1; i < j; i, j = i+1, j-1 {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		}
	}
	for _, item := range candidates {
		out.Nodes = append(out.Nodes, item.node)
		if q.View == "timing" && q.To == nil && item.node.EndedAt != nil && (out.Coverage.To == nil || item.node.EndedAt.After(*out.Coverage.To)) {
			out.Coverage.To = item.node.EndedAt
		}
	}
	if len(candidates) > 0 {
		first, last := candidates[0], candidates[len(candidates)-1]
		if q.Cursor != "" && !c.Before || c.Before && hasMore {
			prev := c
			prev.Before = true
			prev.Locate = false
			prev.Key = first.key
			prev.UUID = first.uuid
			v := encodeFlowCursor(prev)
			out.PrevCursor = &v
		}
		if !c.Before && hasMore || c.Before {
			next := c
			next.Before = false
			next.Locate = false
			next.Key = last.key
			next.UUID = last.uuid
			v := encodeFlowCursor(next)
			out.NextCursor = &v
		}
	} else {
		if moreSources && len(rows) > 0 {
			next := c
			last := rows[len(rows)-1]
			next.Key = last.key("~", 999999)
			next.UUID = last.record.UUID
			next.Locate = false
			v := encodeFlowCursor(next)
			if c.Before {
				out.PrevCursor = &v
			} else {
				out.NextCursor = &v
			}
		}
		if q.Cursor != "" {
			// An empty source page has no first/last node to anchor its return
			// cursor. Include the incoming boundary when reversing direction.
			back := c
			back.Before = !c.Before
			back.Locate = true
			v := encodeFlowCursor(back)
			if c.Before {
				out.NextCursor = &v
			} else {
				out.PrevCursor = &v
			}
		}
	}
	if q.RequestNo > 0 {
		// requestNumber already established this page's one verified request. Running
		// the full fact join again for a single id spends the same budget twice.
		if len(out.Nodes) > 0 && out.Nodes[0].Kind == "turn" {
			no := q.RequestNo
			out.Nodes[0].RequestNo = &sessiontrace.VerifiedNumber{Value: &no, State: "verified", Definition: "work_request_ordinal", Scope: "authorized_session", Revision: out.Revision}
		}
	} else if err = r.enrichFlowNumbers(&out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (r traceNodeReader) flowRows(predicate string, values []any, limit int, before bool) ([]traceNodeRow, error) {
	args := []any{r.session, r.access.UserID, r.access.ProfileEmail, r.access.LoginEmail}
	args = append(args, values...)
	args = append(args, limit)
	order := `r.ts,r.uuid,r.id,source_order`
	if before {
		order = `r.ts DESC,r.uuid DESC,r.id DESC,source_order DESC`
	}
	q := `WITH keys AS MATERIALIZED (SELECT r.ts,r.uuid,r.id,r.record_type,r.profile_email,` + traceNodeSourceOrder + ` AS source_order FROM visible_session_records r WHERE r.session_id=$1 AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4) AND (` + predicate + `) ORDER BY ` + order + ` LIMIT $` + fmt.Sprint(len(args)) + `) SELECT r.id,k.source_order,` + traceEmptyLegacyMetadataSQL("r") + `,` + traceNodeMetadataJSON + ` FROM keys k JOIN visible_session_records r ON r.session_id=$1 AND r.ts=k.ts AND r.id=k.id AND r.uuid=k.uuid AND r.record_type=k.record_type AND r.profile_email=k.profile_email AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4) ORDER BY ` + order
	return r.collectFlowRows(q, args)
}

func (r traceNodeReader) collectFlowRows(q string, args []any) ([]traceNodeRow, error) {
	result, err := r.tx.Query(r.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	out := []traceNodeRow{}
	for result.Next() {
		var row traceNodeRow
		var raw []byte
		if err = result.Scan(&row.id, &row.order, &row.emptyLegacyMetadata, &raw); err != nil {
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
	return out, result.Err()
}
