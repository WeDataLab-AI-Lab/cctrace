package store

import (
	"context"
	"strings"

	"cctrace/internal/sessiontrace"
)

func traceAuditValue(value any) sessiontrace.AuditValue {
	if value == nil {
		return sessiontrace.AuditValue{State: "not_collected"}
	}
	return sessiontrace.AuditValue{State: traceAuditState(value), Value: value}
}

// Walk existing decoded values instead of marshaling a potentially large source
// again just to classify its availability.
func traceAuditState(value any) string {
	state := "observed"
	switch v := value.(type) {
	case string:
		if strings.Contains(v, "[redacted by cctrace client]") {
			return "redacted"
		}
		if strings.Contains(v, "[withheld by cctrace") {
			return "withheld"
		}
	case map[string]any:
		for _, child := range v {
			next := traceAuditState(child)
			if next == "redacted" {
				return next
			}
			if next == "withheld" {
				state = next
			}
		}
	case []any:
		for _, child := range v {
			next := traceAuditState(child)
			if next == "redacted" {
				return next
			}
			if next == "withheld" {
				state = next
			}
		}
	}
	return state
}

func (s *PgStore) TraceNodeDetail(parent context.Context, id string, a SessionAnalysisAccess, nodeID string) (TraceNodeDetail, error) {
	return s.TraceNodeDetailWithOptions(parent, id, a, nodeID, TraceNodeDetailOptions{})
}

func (s *PgStore) TraceNodeDetailWithOptions(parent context.Context, id string, a SessionAnalysisAccess, nodeID string, options TraceNodeDetailOptions) (TraceNodeDetail, error) {
	out := TraceNodeDetail{SessionID: id, Input: traceAuditValue(nil), Output: traceAuditValue(nil), Context: traceAuditValue(nil), Usage: map[string]any{}, RawRecords: []*SessionRecord{}, Notices: []string{}}
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
	if options.RequestParts != "" && options.RequestParts != "page" {
		return out, ErrTraceInvalid
	}
	if options.InputLimit < 0 || options.InputLimit > 100 || (options.InputCursor != "" && options.RequestParts != "page") {
		return out, ErrTraceInvalid
	}
	revision, epoch, err := traceRevision(ctx, tx, id)
	if err != nil {
		return out, err
	}
	cursor := traceInputCursor{Session: id, Scope: traceScope(a), Node: nodeID, Revision: revision, Epoch: epoch}
	position := ""
	if options.InputCursor != "" {
		position, err = s.decodeInputCursor(options.InputCursor, cursor)
		if err != nil {
			return out, err
		}
	}
	r := traceNodeReader{ctx: ctx, tx: tx, session: id, access: a, flow: true, relations: map[string]traceToolRelation{}, agentParents: map[string]*traceNodeKey{}, responses: map[string]traceResponseSummary{}}
	row, key, err := r.selected(nodeID)
	if err != nil {
		return out, err
	}
	if err = r.primeToolRelations([]traceNodeRow{row}); err != nil {
		return out, err
	}
	nodes, err := r.nodes(row)
	if err != nil {
		return out, err
	}
	found := false
	for _, node := range nodes {
		if node.NodeID == encodeNodeKey(key) {
			out.Node = node
			found = true
			break
		}
	}
	if !found {
		// Older direct links classified settings and bookkeeping as instructions
		// or events. Preserve their source identity across the new view taxonomy.
		if key.Index < 0 && (key.Kind == "instructions" || key.Kind == "event" || key.Kind == "context") {
			for _, node := range nodes {
				if node.Kind == "context" || node.Kind == "metadata" || node.Kind == "instructions" {
					out.Node = node
					out.Node.NodeID = nodeID
					key.Kind = node.Kind
					found = true
					break
				}
			}
		}
	}
	if !found && key.Kind == "source" {
		out.Node = sessiontrace.Node{NodeID: nodeID, Kind: "metadata", Title: "원자료", SourceRef: key.Ref, Availability: traceAuditValue(row.raw).State, Evidence: []string{"authorized_source_locator"}}
		out.Context = traceAuditValue(row.raw)
		out.Input = sessiontrace.AuditValue{State: "not_applicable"}
		out.Output = sessiontrace.AuditValue{State: "not_applicable"}
		found = true
	}
	if !found {
		return out, ErrSessionAnalysisNotFound
	}
	out.RawRecords = append(out.RawRecords, row.record)
	if (key.Kind == "turn" || key.Kind == "event" || key.Kind == "instructions") && key.Index < 0 {
		if out.Interpretation, err = r.interpretation(row); err != nil {
			return out, err
		}
	}
	m := nodeMessage(row)
	if key.Kind == "instructions" || key.Kind == "context" || key.Kind == "metadata" || key.Kind == "usage" || key.Kind == "turn" {
		out.Input = sessiontrace.AuditValue{State: "not_applicable"}
		out.Output = sessiontrace.AuditValue{State: "not_applicable"}
	}
	out.Usage["agent"] = row.record.Agent
	if usage, ok := m["usage"]; ok {
		out.Usage["source"] = "session_log"
		out.Usage["values"] = usage
		out.Usage["scope"] = "response"
	}
	if row.record.RecordType == "usage" {
		out.Usage["source"] = "session_log"
		out.Usage["scope"] = "usage_record"
		out.Usage["values"] = m["usage"]
		for _, k := range []string{"response_id", "turn_id", "root_turn_id", "thread_id", "session_id"} {
			if v, ok := m[k]; ok {
				out.Usage[k] = v
			}
		}
		if m["usage"] == nil {
			out.Usage["values"] = m["info"]
		}
	}
	for _, k := range []string{"diagnostics", "stop_reason", "stop_sequence", "input_transformations", "output_tokens_details"} {
		if v, ok := m[k]; ok {
			out.Usage[k] = v
		}
	}
	switch key.Kind {
	case "tool", "agent":
		call := toolCall(row, key)
		if key.Index >= 0 && key.Index < len(nodeBlocks(row)) {
			block, _ := nodeBlocks(row)[key.Index].(map[string]any)
			out.Input = traceAuditValue(block["input"])
		} else {
			out.Input = traceAuditValue(m["arguments"])
			if out.Input.State == "not_collected" {
				out.Input = traceAuditValue(m["input"])
			}
		}
		results, e := r.scoped(row, `(r.tool_call_id=$5 AND r.record_type='tool_output' OR EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(r.raw->'message'->'content')='array' THEN r.raw->'message'->'content' ELSE '[]'::jsonb END) b WHERE b->>'type'='tool_result' AND b->>'tool_use_id'=$5))`, []any{call}, 101, true)
		if e != nil {
			return out, e
		}
		if len(results) > 100 {
			results = results[:100]
			out.Notices = append(out.Notices, "tool_result_candidates_truncated")
		}
		if len(results) == 1 {
			result := results[0]
			out.RawRecords = append(out.RawRecords, result.record)
			out.SourceLocations = append(out.SourceLocations, traceSourceLocation(result, "event", ""))
			if result.record.RecordType == "tool_output" {
				out.Output = traceAuditValue(nodeMessage(result)["output"])
			} else {
				for _, b := range nodeBlocks(result) {
					block, _ := b.(map[string]any)
					if nodeString(block, "tool_use_id") == call {
						out.Output = traceAuditValue(block["content"])
					}
				}
			}
		}
		if len(results) > 1 {
			candidates := []any{}
			for _, result := range results {
				out.RawRecords = append(out.RawRecords, result.record)
				out.SourceLocations = append(out.SourceLocations, traceSourceLocation(result, "event", ""))
				value := nodeMessage(result)["output"]
				if result.record.RecordType != "tool_output" {
					var matches []any
					for _, item := range nodeBlocks(result) {
						block, _ := item.(map[string]any)
						if nodeString(block, "tool_use_id") == call {
							matches = append(matches, block["content"])
						}
					}
					value = matches
				}
				candidates = append(candidates, map[string]any{"source_ref": result.key("event", -1).Ref, "node_id": encodeNodeKey(result.key("event", -1)), "value": value})
			}
			out.Output = sessiontrace.AuditValue{State: "ambiguous", Value: candidates, Reason: "multiple_exact_tool_results"}
			out.Notices = append(out.Notices, "ambiguous_tool_result")
		}
		if err = r.attachNodeTelemetry(&out, row, "tool_use_id", call); err != nil {
			return out, err
		}
		if key.Kind == "agent" {
			parameters, _ := out.Input.Value.(map[string]any)
			if text, ok := out.Input.Value.(string); ok {
				parameters, _ = sessiontrace.DecodeObject([]byte(text))
			}
			dispatch := map[string]any{}
			for _, name := range []string{"prompt", "message", "description", "subagent_type", "agent_type", "model", "name", "fork_context", "fork_turns"} {
				if value, ok := parameters[name]; ok {
					dispatch[name] = value
				}
			}
			parent := sessiontrace.AuditValue{State: "observed", Value: map[string]any{"node_id": out.Node.ParentID, "evidence": "exact_dispatch_tool"}}
			out.Context = traceAuditValue(map[string]any{"dispatch": dispatch, "parent": parent, "history": sessiontrace.AuditValue{State: "not_collected", Reason: "child_context_not_linked"}})
		}
	case "hook":
		out.Context = traceAuditValue(row.raw)
	case "instructions", "context":
		if out.Node.Semantic == "runtime_notice" || out.Node.Semantic == "message" {
			out.Input = traceAuditValue(m["content"])
			out.Context = sessiontrace.AuditValue{State: "not_applicable"}
		} else {
			out.Context = traceAuditValue(traceStructuredContext(row))
		}
	case "metadata":
		out.Context = traceAuditValue(row.raw)
	case "turn":
		out.Input = traceAuditValue(m["content"])
		out.Context = traceAuditValue(map[string]any{"grouping_basis": out.Node.GroupingBasis, "meaning": "input_group_not_api_request"})
	case "response":
		out.Output = traceAuditValue(m["content"])
		if mid := nodeString(m, "id"); mid != "" {
			fragments, e := r.scoped(row, `r.record_type='assistant' AND COALESCE(r.message_id,r.raw->'message'->>'id')=$5`, []any{mid}, 101, true)
			if e != nil {
				return out, e
			}
			if len(fragments) > 100 {
				fragments = fragments[:100]
				out.Notices = append(out.Notices, "response_fragments_truncated")
			}
			var blocks []any
			var usageSnapshots []any
			for _, f := range fragments {
				if f.id != row.id {
					out.RawRecords = append(out.RawRecords, f.record)
					out.SourceLocations = append(out.SourceLocations, traceSourceLocation(f, "response", "message.content"))
				}
				if b, ok := nodeMessage(f)["content"].([]any); ok {
					blocks = append(blocks, b...)
				}
				if u := nodeMessage(f)["usage"]; u != nil {
					usageSnapshots = append(usageSnapshots, u)
				}
			}
			if len(usageSnapshots) > 0 {
				out.Usage["values"] = sessiontrace.MergeResponseUsage(usageSnapshots)
				out.Usage["fold"] = "maximum_per_field"
			}
			if len(blocks) > 0 {
				out.Output = traceAuditValue(blocks)
			}
		}
	case "event":
		if row.record.RecordType == "reasoning" || nodeString(m, "type") == "reasoning" {
			out.Input = sessiontrace.AuditValue{State: "not_applicable"}
			value := m["summary"]
			if value == nil {
				value = m["content"]
			}
			var text []any
			if blocks, ok := value.([]any); ok {
				for _, v := range blocks {
					if b, ok := v.(map[string]any); ok {
						switch nodeString(b, "type") {
						case "summary_text", "text", "input_text", "output_text":
							if body, ok := b["text"].(string); ok {
								text = append(text, map[string]any{"type": b["type"], "text": body})
							}
						}
					}
				}
			}
			if len(text) > 0 {
				out.Output = traceAuditValue(text)
			} else if body, ok := value.(string); ok {
				out.Output = traceAuditValue(body)
			}
			if _, exists := m["encrypted_content"]; exists {
				out.Notices = append(out.Notices, "encrypted_reasoning_content_not_decrypted")
				out.Context = traceAuditValue(map[string]any{"encrypted_content_present": true})
			}
		} else if row.record.RecordType == "tool_output" {
			out.Output = traceAuditValue(m["output"])
			out.Node.Status = "completed"
		} else if key.Index >= 0 && key.Index < len(nodeBlocks(row)) {
			block, _ := nodeBlocks(row)[key.Index].(map[string]any)
			out.Output = traceAuditValue(block["content"])
		} else {
			out.Input = traceAuditValue(m["content"])
		}
	}
	var recipe *traceNodeRow
	if key.Kind == "request" {
		recipe = &row
	} else if ref := nodeString(row.raw, "requestRef"); ref != "" && key.Kind != "source" {
		found, e := r.nativeRows(row, `r.record_type='api-request' AND r.raw->>'id'=$5`, []any{ref}, 2)
		if e != nil {
			return out, e
		}
		if len(found) == 1 {
			recipe = &found[0]
		} else {
			for _, candidate := range found {
				out.RawRecords = append(out.RawRecords, candidate.record)
				out.SourceLocations = append(out.SourceLocations, traceSourceLocation(candidate, "request", ""))
			}
			out.Notices = append(out.Notices, "request_reference_unresolved")
		}
	}
	if recipe != nil {
		lookup := &nativeNodeLookup{reader: r, scope: *recipe, cache: map[string]nativeNodeReference{}}
		if err = lookup.primeRequests(); err != nil {
			return out, err
		}
		var request sessiontrace.NativeRequest
		var e error
		if options.RequestParts == "page" {
			request, e = sessiontrace.ResolveNativeRequestPage(recipe.raw, lookup.read, sessiontrace.NativeInputOptions{Limit: options.InputLimit, Cursor: position, PayloadLimit: (1 << 20) - (32 << 10)})
		} else if options.CompareTo != "" {
			request, e = sessiontrace.ResolveNativeRequestBudget(recipe.raw, lookup.read, 32<<20)
		} else {
			request, e = sessiontrace.ResolveNativeRequest(recipe.raw, lookup.read)
		}
		if e != nil {
			return out, e
		}
		out.Request = &request
		lookup.enrich(out.Request, *recipe)
		if request.InputPage != nil && request.InputPage.NextCursor != nil {
			cursor.Position = *request.InputPage.NextCursor
			token, e := s.encodeInputCursor(cursor)
			if e != nil {
				return out, e
			}
			request.InputPage.NextCursor = &token
		}
		out.SourceLocations = append(out.SourceLocations, lookup.locations...)
		out.Notices = append(out.Notices, lookup.notices...)
		if options.SourceRef != "" {
			if source, ok := lookup.sourceRows[options.SourceRef]; ok {
				out.RawRecords = append(out.RawRecords, source.record)
			}
		}
		if options.CompareTo != "" {
			previous, previousKey, e := r.selected(options.CompareTo)
			if e != nil {
				return out, e
			}
			if previousKey.Kind != "request" || previous.record.RecordType != "api-request" {
				return out, ErrTraceInvalid
			}
			// Both explicit requests share one 64MiB reconstruction allowance.
			compareLookup := &nativeNodeLookup{reader: r, scope: previous, cache: map[string]nativeNodeReference{}}
			if e = compareLookup.primeRequests(); e != nil {
				return out, e
			}
			before, e := sessiontrace.ResolveNativeRequestBudget(previous.raw, compareLookup.read, 32<<20)
			if e != nil {
				return out, e
			}
			current := request
			if options.RequestParts == "page" {
				current, e = sessiontrace.ResolveNativeRequestBudget(recipe.raw, lookup.read, 32<<20)
				if e != nil {
					return out, e
				}
			}
			comparison := sessiontrace.CompareNativeRequests(current, before, options.CompareTo)
			out.Request.Comparison = &comparison
		}
		if key.Kind == "request" || key.Kind == "response" {
			out.Input = sessiontrace.AuditValue{State: request.State, Value: request.Body, Reason: "harness_recorded_api_request"}
			if request.InputPage != nil {
				out.Input = sessiontrace.AuditValue{State: request.State, Reason: "harness_recorded_api_request_page"}
			}
		}
		if recipe.id != row.id {
			out.RawRecords = append(out.RawRecords, recipe.record)
		}
		if system, ok := request.Body["system"]; ok && key.Kind == "request" {
			out.Context = traceAuditValue(map[string]any{"system": system, "tools": request.Body["tools"]})
		}
	}
	if requestID := nodeString(row.raw, "requestId"); requestID != "" {
		if err = r.attachNodeTelemetry(&out, row, "request_id", requestID); err != nil {
			return out, err
		}
	}
	if key.Kind == "response" && row.record.Agent == "claude" {
		if err = r.previousResponse(&out, row); err != nil {
			return out, err
		}
	}
	if out.Node.Availability != "observed" {
		out.Notices = append(out.Notices, "source_"+out.Node.Availability)
	}
	for _, record := range out.RawRecords {
		raw, _ := sessiontrace.DecodeObject(record.Raw)
		source := traceNodeRow{record: record, raw: raw}
		if record == row.record {
			source.id = row.id
		} else {
			continue
		}
		out.SourceLocations = append(out.SourceLocations, traceSourceLocation(source, key.Kind, ""))
	}
	if options.SourceRef != "" {
		var matches []sessiontrace.SourceLocation
		for _, location := range out.SourceLocations {
			if location.SourceRef == options.SourceRef {
				matches = append(matches, location)
			}
		}
		if len(matches) == 0 {
			return out, ErrSessionAnalysisNotFound
		}
		out.SourceLocations = matches
	}
	out.Navigation, err = r.navigation(row, out.Node)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

type nativeNodeReference struct {
	raw map[string]any
	ref string
}
type nativeNodeLookup struct {
	reader     traceNodeReader
	scope      traceNodeRow
	cache      map[string]nativeNodeReference
	locations  []sessiontrace.SourceLocation
	spans      map[string][]nativeSourceSpan
	sourceRows map[string]traceNodeRow
	aliases    map[string][]sessiontrace.SourceLocation
	notices    []string
}

func (l *nativeNodeLookup) read(kind, key string) (map[string]any, string, error) {
	cacheKey := kind + "/" + key
	if cached, ok := l.cache[cacheKey]; ok {
		return cached.raw, cached.ref, nil
	}
	var predicate string
	switch kind {
	case "request":
		predicate = `r.record_type='api-request' AND r.raw->>'id'=$5`
	case "shape":
		predicate = `r.record_type='api-request-shape' AND r.raw->>'shapeHash'=$5`
	case "message":
		predicate = `(r.record_type='api-request-blob' AND r.raw->>'hash'=$5 OR r.raw->'cctraceReferences' @> jsonb_build_object('version',1,'messages',jsonb_build_array(jsonb_build_object('hash',$5::text))))`
	default:
		return nil, "", nil
	}
	rows, err := l.reader.nativeRows(l.scope, predicate, []any{key}, 3)
	if err != nil {
		return nil, "", err
	}
	if len(rows) == 3 {
		l.notices = append(l.notices, "native_source_candidate_limit_reached")
	}
	var answer nativeNodeReference
	var candidates []nativeSourceSpan
	var candidateLocations []sessiontrace.SourceLocation
	ambiguous := false
	for _, row := range rows {
		rawPath := ""
		switch kind {
		case "message":
			rawPath = "message"
		case "shape":
			rawPath = "shape"
		}
		l.addLocation(traceSourceLocation(row, kind, rawPath))
		l.remember(row)
		object := row.raw
		if kind == "message" && row.record.RecordType != "api-request-blob" {
			message, e := l.transcriptMessage(row, key)
			if e != nil {
				return nil, "", e
			}
			if message == nil {
				continue
			}
			object = map[string]any{"message": message}
		} else if kind == "message" {
			object = map[string]any{"message": row.raw["message"]}
			if len(key) == 64 {
				hash, e := sessiontrace.MessageHash(row.raw["message"])
				if e != nil && row.raw["message"] != nil {
					continue
				}
				if row.raw["message"] != nil && hash != key && traceAuditValue(row.raw["message"]).State != "redacted" {
					continue
				}
			}
		}
		if kind == "shape" {
			object = map[string]any{"shape": row.raw["shape"]}
		}
		if kind == "message" {
			if normalized, e := sessiontrace.NormalizeMessage(object["message"]); e == nil {
				object["message"] = normalized
			}
		}
		field := ""
		switch kind {
		case "message":
			field = "message"
		case "shape":
			field = "shape"
		}
		if field != "" && object[field] == nil {
			if fields, ok := row.raw["cctraceWithheld"].([]any); ok && len(fields) > 0 {
				object["cctraceWithheld"] = fields
			}
		}
		candidateLocations = append(candidateLocations, traceSourceLocation(row, kind, rawPath))
		if answer.raw != nil {
			a, _ := sessiontrace.CanonicalJSON(answer.raw)
			b, _ := sessiontrace.CanonicalJSON(object)
			if string(a) != string(b) {
				ambiguous = true
			}
		}
		answer = nativeNodeReference{raw: object, ref: row.key(kind, -1).Ref}
		if kind == "message" {
			ref := row.key(kind, -1).Ref
			if spans := l.spans[ref]; len(spans) > 0 {
				candidates = append(candidates, spans...)
			} else {
				message, _ := object["message"].(map[string]any)
				blocks, _ := message["content"].([]any)
				location := traceSourceLocation(row, kind, "message.content")
				start, end := 0, len(blocks)
				location.BlockStart = &start
				location.BlockEnd = &end
				l.addLocation(location)
				_, text := nodeMessage(row)["content"].(string)
				candidates = append(candidates, nativeSourceSpan{location: location, start: 0, end: end, text: text})
			}
		}
	}
	if len(candidateLocations) > 0 {
		if l.aliases == nil {
			l.aliases = map[string][]sessiontrace.SourceLocation{}
		}
		l.aliases[answer.ref] = candidateLocations
	}
	if len(candidates) > 0 {
		if l.spans == nil {
			l.spans = map[string][]nativeSourceSpan{}
		}
		l.spans[answer.ref] = candidates
	}
	if ambiguous {
		answer = nativeNodeReference{ref: answer.ref}
	}
	if answer.raw == nil && !ambiguous && kind == "message" {
		// Legacy transcripts have no reference index. A selected request may inspect
		// at most 100 preceding rows; missing older prefixes stay explicit.
		rows, err = l.reader.nativeRows(l.scope, `r.record_type IN ('user','assistant') AND r.ts<=$5`, []any{l.scope.record.Ts}, 100)
		if err != nil {
			return nil, "", err
		}
		var messages []map[string]any
		var sources []traceNodeRow
		for _, row := range rows {
			if m := nodeMessage(row); m != nil {
				messages = append(messages, m)
				sources = append(sources, row)
			}
		}
		prefixes, e := sessiontrace.TranscriptPrefixes(messages)
		if e == nil {
			var spans []nativeSourceSpan
			for i, prefix := range prefixes {
				normal, e := sessiontrace.NormalizeMessage(messages[i])
				if e != nil {
					continue
				}
				blocks, _ := normal["content"].([]any)
				all, _ := prefix["content"].([]any)
				if len(blocks) == len(all) {
					spans = nil
				}
				source := sources[i]
				location := traceSourceLocation(source, "message", "message.content")
				start, end := 0, len(blocks)
				location.BlockStart = &start
				location.BlockEnd = &end
				l.addLocation(location)
				l.remember(source)
				_, text := messages[i]["content"].(string)
				spans = append(spans, nativeSourceSpan{location: location, start: len(all) - len(blocks), end: len(all), text: text})
				hash, _ := sessiontrace.MessageHash(prefix)
				ref := source.key("message", -1).Ref
				reference := nativeNodeReference{raw: map[string]any{"message": prefix}, ref: ref}
				if l.spans == nil {
					l.spans = map[string][]nativeSourceSpan{}
				}
				l.spans[ref] = append([]nativeSourceSpan{}, spans...)
				if hash == key {
					answer = reference
				}
				l.cache["message/"+hash] = reference
			}
		}
	}
	l.cache[cacheKey] = answer
	return answer.raw, answer.ref, nil
}

func (r traceNodeReader) attachNodeTelemetry(out *TraceNodeDetail, row traceNodeRow, key, value string) error {
	if value == "" {
		return nil
	}
	rows, err := r.tx.Query(r.ctx, `SELECT to_jsonb(e)||jsonb_build_object('source_envelope',o.source_envelope) FROM visible_events e
 JOIN otel_events o ON e.tiebreak='o:'||o.id::text AND e.ts=o.ts AND e.session_id=o.session_id
 WHERE e.session_id=$1 AND e.attrs->>$2=$3
 AND ($4::text='' OR e.user_id=$4) AND ($5::text='' OR e.profile_email=$5) AND ($6::text='' OR e.login_email=$6)
 AND e.user_id=$7 AND e.profile_email=$8 AND COALESCE(e.agent,'claude')=$9 ORDER BY e.ts,e.tiebreak LIMIT 101`, r.session, key, value, r.access.UserID, r.access.ProfileEmail, r.access.LoginEmail, row.record.UserID, row.record.ProfileEmail, row.record.Agent)
	if err != nil {
		return err
	}
	defer rows.Close()
	var events []any
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		object, e := sessiontrace.DecodeObject(raw)
		if e != nil {
			return e
		}
		visible := map[string]any{}
		for _, key := range []string{"ts", "event_name", "duration_ms", "input_tokens", "output_tokens", "cache_read_tokens", "cache_create_tokens", "cost_usd", "tool_success", "attrs", "source_envelope"} {
			if v, ok := object[key]; ok {
				visible[key] = v
			}
		}
		events = append(events, visible)
	}
	if len(events) > 100 {
		events = events[:100]
		out.Notices = append(out.Notices, "telemetry_truncated")
	}
	if len(events) > 0 {
		out.Usage["otel"] = events
		out.Usage["otel_evidence"] = "exact_" + key
	}
	return rows.Err()
}
