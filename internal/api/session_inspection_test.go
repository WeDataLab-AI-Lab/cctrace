package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func TestSessionInspectionKeepsRawAndScopesDirectLookup(t *testing.T) {
	var filters []store.SessionRecordFilter
	m := &mockStore{listSessionRecordsFn: func(_ context.Context, f store.SessionRecordFilter) ([]*store.SessionRecord, error) {
		filters = append(filters, f)
		return []*store.SessionRecord{{SessionID: "s", UUID: "target", RecordType: "attachment", Raw: json.RawMessage(`{"attachment":{"type":"instructions","files":[{"path":"AGENTS.md","content":"injected"}]}}`)}}, nil
	}}
	response := openAPIRequest(t, newTestServer(m, nil), http.MethodGet, "/api/sessions/s/inspection?kind=context&record_id=target&limit=10", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Records  []sessionRecordWithView `json:"records"`
		Selected *sessionRecordWithView  `json:"selected_record"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Records) != 1 || body.Selected == nil || len(body.Selected.Raw) == 0 {
		t.Fatalf("raw source lost: %s", response.Body.String())
	}
	if len(filters) < 2 || !filters[0].ContextOnly || filters[1].Anchor != "target" || filters[1].ContextOnly {
		t.Fatalf("direct lookup limited to tab/page: %+v", filters)
	}
}

func TestSessionInspectionMissingAnchorIsNotEmptySuccess(t *testing.T) {
	m := &mockStore{listSessionRecordsFn: func(_ context.Context, f store.SessionRecordFilter) ([]*store.SessionRecord, error) {
		if f.StartOffset != nil {
			*f.StartOffset = -1
		}
		return nil, nil
	}}
	response := openAPIRequest(t, newTestServer(m, nil), http.MethodGet, "/api/sessions/s/inspection?record_id=missing", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSessionInspectionDirectLookupUsesCallerScope(t *testing.T) {
	for _, byID := range []bool{true, false} {
		var filters []store.SessionRecordFilter
		m := &mockStore{listSessionRecordsFn: func(_ context.Context, f store.SessionRecordFilter) ([]*store.SessionRecord, error) {
			filters = append(filters, f)
			return []*store.SessionRecord{{UUID: "r", SessionID: "s", Raw: json.RawMessage(`{}`)}}, nil
		}}
		srv := newTestServer(m, nil)
		srv.useUserIDAccessControl = byID
		response := serveScopedAs(srv, &auth.DashboardUser{Role: "user", Email: "me@example.com", CctraceUserID: "caller"}, "/api/sessions/s/inspection?record_id=r&user_id=foreign&profile_email=foreign@example.com&login_email=foreign@example.com")
		if response.Code != http.StatusOK || len(filters) != 2 {
			t.Fatalf("status=%d filters=%v", response.Code, filters)
		}
		for _, f := range filters {
			if byID && (f.UserID != "caller" || f.ProfileEmail != "" || f.LoginEmail != "") {
				t.Fatalf("caller id scope=%+v", f)
			}
			if !byID && (f.ProfileEmail != "me@example.com" || f.UserID != "" || f.LoginEmail != "") {
				t.Fatalf("caller email scope=%+v", f)
			}
		}
	}
}

func TestSessionConversationWindowDistinguishesNavigationBoundary(t *testing.T) {
	for _, origin := range []bool{true, false} {
		m := &mockStore{listSessionRecordsFn: func(_ context.Context, f store.SessionRecordFilter) ([]*store.SessionRecord, error) {
			if f.StartOffset != nil {
				*f.StartOffset = -1
			}
			if f.OriginFound != nil {
				*f.OriginFound = origin
			}
			return nil, nil
		}}
		response := openAPIRequest(t, newTestServer(m, nil), http.MethodGet, "/api/sessions?session_id=s&anchor=message&direction=previous-request", "")
		var body map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		want := "record_not_found"
		if origin {
			want = "no_adjacent_request"
		}
		if response.Code != http.StatusNotFound || body["error"] != want {
			t.Fatalf("origin=%v status=%d body=%v", origin, response.Code, body)
		}
	}
}
