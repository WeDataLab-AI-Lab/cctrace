package store

import (
	"fmt"
	"strings"
)

// Search each source once, then seek its input through the existing source/time
// index. The previous root × descendant × boundary scan repeated JSON search
// for every input and exceeded the one-second budget on 200,000 source rows.
func (r traceNodeReader) flowRootSearchRows(query string, c traceFlowCursor, hasCursor bool, limit int) ([]traceNodeRow, error) {
	term, path := traceFlowSearchValues(query)
	args := []any{r.session, r.access.UserID, r.access.ProfileEmail, r.access.LoginEmail, term, path}
	order := `k.ts,k.uuid,k.id,k.source_order`
	op := ">="
	if c.Before {
		order = `k.ts DESC,k.uuid DESC,k.id DESC,k.source_order DESC`
		op = "<="
	}
	cursorPredicate := "TRUE"
	if hasCursor {
		cursorPredicate = `(k.ts,k.uuid,k.id,k.source_order)` + op + `($7,$8,$9,$10)`
		args = append(args, c.Key.TS, c.UUID, c.Key.ID, c.Key.Order)
	}
	args = append(args, limit)
	nativeSource := strings.Replace(traceSameSourceSQL("r", "m"), `COALESCE(r.account_id,'')=COALESCE(m.account_id,'')`, `(COALESCE(r.account_id,'')=COALESCE(m.account_id,'') OR (r.ts='epoch'::timestamptz AND COALESCE(r.account_id,'')='' AND r.record_type='api-request'))`, 1)
	keys := `WITH ` + traceInputSetCTE() + `trace_search_matches AS MATERIALIZED (
 SELECT r.session_id,r.ts,r.uuid,r.id,r.record_type,r.profile_email,r.user_id,COALESCE(r.account_id,'') AS account_id,COALESCE(r.agent,'claude') AS agent,COALESCE(r.source_file,'') AS source_file,
 CASE WHEN r.agent='codex' THEN ` + traceTurnIDSQL + ` ELSE '' END AS native_turn_id,r.raw->>'requestRef' AS request_ref,` + traceNodeSourceOrder + ` AS source_order
 FROM session_records r WHERE r.session_id=$1 AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4)
 AND ` + traceFlowSearchCandidateSQL("r") + ` AND NOT ` + traceMetadataSQL("r") + ` AND ` + traceFlowSearchPredicateSQL("r", "$5", "$6") + `),
 trace_search_roots AS MATERIALIZED (
 SELECT COALESCE(seq.ts,native.ts,request.ts,m.ts) AS ts,COALESCE(seq.uuid,native.uuid,request.uuid,m.uuid) AS uuid,COALESCE(seq.id,native.id,request.id,m.id) AS id,
 COALESCE(seq.record_type,native.record_type,request.record_type,m.record_type) AS record_type,m.profile_email,
 COALESCE(seq.source_order,native.source_order,request.source_order,m.source_order) AS source_order
 FROM trace_search_matches m
 LEFT JOIN LATERAL (SELECT r.ts,r.uuid,r.id,r.record_type,` + traceNodeSourceOrder + ` AS source_order FROM session_records r
 WHERE m.native_turn_id='' AND ` + traceSameSourceSQL("r", "m") + ` AND ($4::text='' OR r.login_email=$4) AND ` + traceCanonicalInputSQL("r") + `
 AND (r.ts,r.uuid,r.id)<=(m.ts,m.uuid,m.id) ORDER BY r.ts DESC,r.uuid DESC,r.id DESC LIMIT 1) seq ON true
 LEFT JOIN LATERAL (SELECT i.ts,i.uuid,i.id,i.record_type,md5(jsonb_build_array(i.uuid,i.record_type,i.profile_email)::text) AS source_order FROM trace_inputs i
 WHERE m.native_turn_id<>'' AND ` + traceSameSourceSQL("i", "m") + ` AND i.native_turn_id=m.native_turn_id ORDER BY i.ts,i.uuid,i.id LIMIT 1) native ON true
 LEFT JOIN LATERAL (SELECT p.ts,p.uuid,p.id,p.record_type,p.source_order FROM (
 SELECT r.ts,r.uuid,r.id,r.record_type,` + traceNodeSourceOrder + ` AS source_order,count(*) OVER() AS candidates FROM session_records r
 WHERE seq.id IS NULL AND native.id IS NULL AND COALESCE(m.request_ref,'')<>'' AND ` + nativeSource + `
 AND ($4::text='' OR r.login_email=$4) AND r.record_type='api-request' AND r.raw->>'id'=m.request_ref) p WHERE p.candidates=1) request ON true),
 trace_search_keys AS MATERIALIZED (SELECT DISTINCT k.ts,k.uuid,k.id,k.record_type,k.profile_email,k.source_order FROM trace_search_roots k WHERE ` + cursorPredicate + `
 ORDER BY ` + order + ` LIMIT $` + fmt.Sprint(len(args)) + `) SELECT r.id,k.source_order,` + traceEmptyLegacyMetadataSQL("r") + `,` + traceNodeMetadataJSON + ` FROM trace_search_keys k JOIN visible_session_records r ON r.session_id=$1 AND r.ts=k.ts AND r.id=k.id AND r.uuid=k.uuid AND r.record_type=k.record_type AND r.profile_email=k.profile_email
 AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4) ORDER BY ` + order
	return r.collectFlowRows(keys, args)
}
