package store

import (
	"strconv"

	"cctrace/internal/sessionfacts"
	"cctrace/internal/sessiontrace"
)

func traceFlowFactFence() string {
	return `p.tracking='tracked' AND p.timeline_state='observed' AND p.source_revision=$5 AND p.policy_epoch=$6
 AND p.mapping_version=` + strconv.Itoa(sessionTimelineMappingVersion) + ` AND p.rules_version=` + strconv.Itoa(sessionfacts.RulesVersion) + `
 AND octet_length(p.timeline_hash)=32 AND octet_length(p.fact_output_hash)=32 AND p.source_generation>0
 AND f.output_hash=p.fact_output_hash AND f.source_generation=p.source_generation AND f.rules_version=p.rules_version AND f.coverage='full'
 AND NOT EXISTS(SELECT 1 FROM session_fact_dirty dirty WHERE dirty.session_id=f.session_id)`
}

// Every counted item joins its canonical opener, visible source identity, exact
// timeline member and current published fact. Session-level counts.requests is
// deliberately unused: it also counts queued inputs. The number is taken from
// the turn's order at read time (requestOrdinalSQL), not the stored request_no.
func traceFlowNumberSQL() string {
	return `SELECT r.id,r.ts,r.uuid,r.record_type,r.profile_email,` + traceNodeSourceOrder + ` AS source_order,n.request_no,t.tool_calls
 FROM visible_session_records r JOIN session_timeline_members m ON m.session_id=r.session_id AND m.source_ts=r.ts AND m.source_record_id=r.id
 AND m.source_record_type=r.record_type AND m.source_profile_email=r.profile_email AND m.source_uuid=r.uuid AND 'opener'=ANY(m.roles)
 JOIN session_timeline_provenance p ON p.session_id=m.session_id JOIN session_analysis_facts f ON f.session_id=p.session_id
 JOIN session_turn_facts t ON t.session_id=m.session_id AND t.turn_key=m.turn_key AND t.opener_uuid=r.uuid AND t.is_request AND t.request_no IS NOT NULL
 JOIN (SELECT turn_key,` + requestOrdinalSQL + ` AS request_no FROM session_turn_facts WHERE session_id=$1 AND is_request AND request_no IS NOT NULL) n ON n.turn_key=t.turn_key
 WHERE r.session_id=$1 AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4)
 AND ` + traceFlowFactFence() + ` AND ` + traceCanonicalInputSQL("r")
}
func (r traceNodeReader) enrichFlowNumbers(out *TraceNodesResponse) error {
	// The projection only reads keys and scalar counts; it does not load fact
	// detail/body or trigger computation. An absent/stale fact leaves values absent.
	out.RequestTotal = &sessiontrace.VerifiedNumber{State: "unavailable", Definition: "verified_work_request_roots", Scope: "authorized_session", Revision: out.Revision}
	ids := []int64{}
	for _, node := range out.Nodes {
		if node.Kind == "turn" {
			key, e := decodeNodeKey(node.NodeID)
			if e == nil {
				ids = append(ids, key.ID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := r.tx.Query(r.ctx, traceFlowNumberPageSQL(), r.session, r.access.UserID, r.access.ProfileEmail, r.access.LoginEmail, out.Revision, out.Epoch, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var key traceNodeKey
		var uuid string
		var no, tools, total int
		if err = rows.Scan(&id, &key.TS, &uuid, &key.Order, &no, &tools, &total); err != nil {
			return err
		}
		for i := range out.Nodes {
			k, e := decodeNodeKey(out.Nodes[i].NodeID)
			if e == nil && k.ID == id && k.TS.Equal(key.TS) && k.Order == key.Order {
				n, t := no, tools
				out.Nodes[i].RequestNo = &sessiontrace.VerifiedNumber{Value: &n, State: "verified", Definition: "work_request_ordinal", Scope: "authorized_session", Revision: out.Revision}
				out.Nodes[i].ToolCount = &sessiontrace.VerifiedNumber{Value: &t, State: "verified", Definition: "canonical_tool_calls", Scope: "request", Revision: out.Revision}
			}
		}
		value := total
		out.RequestTotal.Value = &value
		out.RequestTotal.State = "verified"
	}
	return rows.Err()
}

func traceFlowNumberPageSQL() string {
	return `WITH trace_flow_numbers AS MATERIALIZED (` + traceFlowNumberSQL() + `), totals AS (SELECT count(DISTINCT request_no)::integer AS total FROM trace_flow_numbers)
SELECT v.id,v.ts,v.uuid,v.source_order,v.request_no,v.tool_calls,totals.total FROM trace_flow_numbers v CROSS JOIN totals WHERE v.id=ANY($7::bigint[]) LIMIT 101`
}

func traceFlowNumberRequestSQL() string {
	return `WITH trace_flow_numbers AS MATERIALIZED (` + traceFlowNumberSQL() + `) SELECT id,ts,uuid,record_type,profile_email,source_order FROM trace_flow_numbers WHERE request_no=$7 LIMIT 2`
}
func (r traceNodeReader) requestNumber(no int, revision, epoch int64) (traceNodeKey, string, error) {
	rows, err := r.tx.Query(r.ctx, traceFlowNumberRequestSQL(), r.session, r.access.UserID, r.access.ProfileEmail, r.access.LoginEmail, revision, epoch, no)
	if err != nil {
		return traceNodeKey{}, "", err
	}
	defer rows.Close()
	var selected traceNodeKey
	var uuid string
	count := 0
	for rows.Next() {
		var key traceNodeKey
		var rt, profile string
		if err = rows.Scan(&key.ID, &key.TS, &uuid, &rt, &profile, &key.Order); err != nil {
			return key, "", err
		}
		key.Kind = "turn"
		key.Index = -1
		// Revalidate the exact source and derive its opaque reference; do not invent
		// an ID from a fact's ordinal or UUID.
		selected = key
		count++
	}
	if err = rows.Err(); err != nil {
		return selected, "", err
	}
	if count != 1 {
		return selected, "", ErrSessionAnalysisNotFound
	}
	rows.Close()
	matches, err := r.rows(`r.id=$5 AND r.ts=$6 AND `+traceNodeSourceOrder+`=$7`, []any{selected.ID, selected.TS, selected.Order}, 2)
	if err != nil {
		return selected, "", err
	}
	if len(matches) != 1 {
		return selected, "", ErrSessionAnalysisNotFound
	}
	source := matches[0]
	return source.key("turn", -1), uuid, nil
}
