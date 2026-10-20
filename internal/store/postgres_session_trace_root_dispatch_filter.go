package store

import "encoding/json"

const traceRootDispatchRefLimit = 128

type traceRootDispatchRef struct {
	ProfileEmail string `json:"profile_email"`
	UserID       string `json:"user_id"`
	AccountID    string `json:"account_id"`
	Agent        string `json:"agent"`
	CallID       string `json:"call_id"`
}

// Only authorized whole-session root reads may inspect these raw source keys.
// Keep the existing transaction deadline; this lookup never projects raw bodies.
// Parent resolution still checks visibility and the caller before its key limit.
func (r traceNodeReader) rootResolvedDispatches() ([]byte, error) {
	rows, err := r.tx.Query(r.ctx, `SELECT DISTINCT r.profile_email,r.user_id,COALESCE(r.account_id,''),COALESCE(r.agent,'claude'),r.tool_use_id
 FROM session_records r WHERE r.session_id=$1 AND ($2::text='' OR r.user_id=$2)
 AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4)
 AND COALESCE(r.agent,'claude')='claude' AND r.record_type<>'tool_output' AND r.tool_use_id<>'' LIMIT $5`, r.session, r.access.UserID, r.access.ProfileEmail, r.access.LoginEmail, traceRootDispatchRefLimit+1)
	if err != nil {
		return nil, err
	}
	var refs []traceRootDispatchRef
	for rows.Next() {
		var ref traceRootDispatchRef
		if err := rows.Scan(&ref.ProfileEmail, &ref.UserID, &ref.AccountID, &ref.Agent, &ref.CallID); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 || len(refs) > traceRootDispatchRefLimit {
		return []byte("[]"), nil
	}
	if r.agentParents == nil {
		r.agentParents = map[string]*traceNodeKey{}
	}
	page := make([]traceNodeRow, len(refs))
	for i, ref := range refs {
		page[i].record = &SessionRecord{SessionID: r.session, ProfileEmail: ref.ProfileEmail, UserID: ref.UserID, AccountID: ref.AccountID, Agent: ref.Agent, ToolUseID: ref.CallID}
	}
	if err := r.primeAgentParents(page); err != nil {
		return nil, err
	}
	resolved := make([]traceRootDispatchRef, 0, len(refs))
	for i, ref := range refs {
		if r.agentParents[traceAgentRelationKey(page[i], ref.CallID)] != nil {
			resolved = append(resolved, ref)
		}
	}
	return json.Marshal(resolved)
}

func traceRootResolvedDispatchCTE(arg string) string {
	return `trace_resolved_dispatch AS MATERIALIZED (SELECT * FROM jsonb_to_recordset(` + arg + `::jsonb)
 AS d(profile_email text,user_id text,account_id text,agent text,call_id text)), `
}

func traceRootDispatchFilterSQL(a string) string {
	// tool_output can replace an Agent parent with an unresolved same-file parent.
	return `(` + a + `.record_type='tool_output' OR COALESCE(` + a + `.agent,'claude')<>'claude' OR NOT EXISTS (
 SELECT 1 FROM trace_resolved_dispatch d WHERE d.profile_email=` + a + `.profile_email AND d.user_id=` + a + `.user_id
 AND d.account_id=COALESCE(` + a + `.account_id,'') AND d.agent=COALESCE(` + a + `.agent,'claude') AND d.call_id=` + a + `.tool_use_id))`
}
