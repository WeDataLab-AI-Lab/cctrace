package api

import (
	"net/http"
	"strconv"
	"time"

	"cctrace/internal/sessionview"
	"cctrace/internal/store"
)

func (s *Server) handleTraceNodes(w http.ResponseWriter, r *http.Request) {
	st, a, ok := s.traceCaller(w, r)
	if !ok {
		return
	}
	nodes, ok := st.(store.TraceNodeStore)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "trace_unavailable"})
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			traceErr(w, store.ErrTraceInvalid)
			return
		}
	}
	q := r.URL.Query()
	view := q.Get("view")
	if view != "" && view != "execution" && view != "context" && view != "metadata" && view != "flow" && view != "timing" {
		traceErr(w, store.ErrTraceInvalid)
		return
	}
	requestNo := 0
	if raw := q.Get("request_no"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			traceErr(w, store.ErrTraceInvalid)
			return
		}
		requestNo = n
	}
	var from, to *time.Time
	for name, target := range map[string]**time.Time{"from": &from, "to": &to} {
		if raw := q.Get(name); raw != "" {
			value, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				traceErr(w, store.ErrTraceInvalid)
				return
			}
			*target = &value
		}
	}
	if requestNo > 0 && view != "flow" || view == "timing" && q.Get("group_id") == "" || from != nil && to != nil && !from.Before(*to) || (view != "timing" && (from != nil || to != nil)) || len(q.Get("q")) > 256 {
		traceErr(w, store.ErrTraceInvalid)
		return
	}
	v, err := nodes.TraceNodes(r.Context(), r.PathValue("session_id"), a, store.TraceNodesRequest{ParentID: q.Get("parent_id"), Cursor: q.Get("cursor"), View: view, GroupID: q.Get("group_id"), Query: q.Get("q"), RequestNo: requestNo, From: from, To: to, Limit: limit})
	if err != nil {
		traceErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) handleTraceNodeDetail(w http.ResponseWriter, r *http.Request) {
	st, a, ok := s.traceCaller(w, r)
	if !ok {
		return
	}
	nodes, ok := st.(store.TraceNodeStore)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "trace_unavailable"})
		return
	}
	q := r.URL.Query()
	options := store.TraceNodeDetailOptions{RequestParts: q.Get("request_parts"), InputCursor: q.Get("input_cursor"), CompareTo: q.Get("compare_to"), SourceRef: q.Get("source_ref"), InputLimit: 20}
	if raw := q.Get("input_limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			traceErr(w, store.ErrTraceInvalid)
			return
		}
		options.InputLimit = n
	}
	if options.RequestParts != "" && options.RequestParts != "page" {
		traceErr(w, store.ErrTraceInvalid)
		return
	}
	var v store.TraceNodeDetail
	var err error
	if options.RequestParts != "" || options.InputCursor != "" || options.CompareTo != "" || options.SourceRef != "" {
		if extended, ok := st.(store.TraceNodeDetailOptionsStore); ok {
			v, err = extended.TraceNodeDetailWithOptions(r.Context(), r.PathValue("session_id"), a, r.PathValue("node_id"), options)
		} else {
			traceErr(w, store.ErrTraceInvalid)
			return
		}
	} else {
		v, err = nodes.TraceNodeDetail(r.Context(), r.PathValue("session_id"), a, r.PathValue("node_id"))
	}
	if err != nil {
		traceErr(w, err)
		return
	}
	// Use the same conversation projection as source-first record reads, so
	// recorded UUID/call IDs retain their canonical navigation anchors.
	records := make([]sessionRecordWithView, 0, len(v.RawRecords))
	for _, record := range v.RawRecords {
		if record != nil {
			records = append(records, sessionRecordWithView{SessionRecord: record, Raw: record.Raw, View: sessionview.Normalize(record)})
		}
	}
	writeJSON(w, 200, struct {
		store.TraceNodeDetail
		RawRecords []sessionRecordWithView `json:"raw_records"`
	}{TraceNodeDetail: v, RawRecords: records})
}
