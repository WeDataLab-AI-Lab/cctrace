package store

type traceResponseSummary struct {
	key         traceNodeKey
	hasChildren bool
}

// Fold only relation metadata. A later fragment can contain the first tool;
// neither a page boundary nor the detail fragment cap proves its absence.
func (r traceNodeReader) responseSummary(row traceNodeRow) (traceResponseSummary, error) {
	summary := traceResponseSummary{key: row.key("response", -1)}
	id := nodeString(nodeMessage(row), "id")
	if id == "" {
		return summary, nil
	}
	cacheKey := traceRelationKey(row, id)
	if cached, ok := r.responses[cacheKey]; ok {
		return cached, nil
	}
	reader := r
	reader.metadata, reader.responseMetadata = true, true
	// Canonical identity remains the earliest fragment in either page direction.
	reader.descending, reader.inputOrder = false, false
	rows, err := reader.scoped(row, `r.record_type='assistant' AND COALESCE(r.message_id,r.raw->'message'->>'id')=$5`, []any{id}, 1, true)
	if err != nil {
		return summary, err
	}
	if len(rows) > 0 {
		summary.key = rows[0].key("response", -1)
		summary.hasChildren = rows[0].responseChildren
	}
	if r.responses != nil {
		r.responses[cacheKey] = summary
	}
	return summary, nil
}

// This exact existence check runs alongside the existing canonical-key query,
// within the same caller/visibility/source scope. It copies no fragment bodies.
func traceResponseChildrenSQL() string {
	return `EXISTS(SELECT 1 FROM visible_session_records f WHERE ` + traceSameSourceSQL("f", "r") + `
 AND ($2::text='' OR f.user_id=$2) AND ($3::text='' OR f.profile_email=$3) AND ($4::text='' OR f.login_email=$4)
 AND f.record_type='assistant' AND COALESCE(f.message_id,f.raw->'message'->>'id')=COALESCE(r.message_id,r.raw->'message'->>'id')
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(f.raw->'message'->'content')='array'
 THEN f.raw->'message'->'content' ELSE '[]'::jsonb END) b WHERE b->>'type'='tool_use'))`
}
