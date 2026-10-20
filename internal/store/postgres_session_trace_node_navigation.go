package store

import (
	"errors"

	"cctrace/internal/sessiontrace"
)

// navigation resolves only exact parent references. Input membership supplies a
// separate root locator and never becomes a causal ancestor. Budget/cycles stop
// the walk without replacing missing evidence with a time-based relationship.
func (r traceNodeReader) navigation(row traceNodeRow, node sessiontrace.Node) (*TraceNodeNavigation, error) {
	revision, epoch, err := traceRevision(r.ctx, r.tx, r.session)
	if err != nil {
		return nil, err
	}
	out := &TraceNodeNavigation{GroupID: node.GroupID, ChildCursors: map[string]string{}, AncestorIDs: []string{}, RootNodeID: node.NodeID, Revision: revision, Epoch: epoch, State: "resolved"}
	childCursor := func(parent string, child sessiontrace.Node, source traceNodeRow) error {
		key, e := decodeNodeKey(child.NodeID)
		if e != nil {
			return e
		}
		out.ChildCursors[parent] = encodeFlowCursor(traceFlowCursor{Session: r.session, Scope: traceScope(r.access), View: "flow", Parent: parent, Revision: revision, Epoch: epoch, Key: key, UUID: source.record.UUID, Locate: true})
		return nil
	}
	current := node
	rootRow := row
	seen := map[string]bool{node.NodeID: true}
	for depth := 0; current.ParentID != nil; depth++ {
		if depth >= 64 || seen[*current.ParentID] {
			out.State = "unresolved"
			out.Reason = "ancestor_budget_or_cycle"
			break
		}
		seen[*current.ParentID] = true
		parent, key, e := r.selected(*current.ParentID)
		if e != nil {
			if errors.Is(e, ErrSessionAnalysisNotFound) {
				out.State = "unresolved"
				out.Reason = "parent_unavailable"
				break
			}
			return nil, e
		}
		if e = r.primeToolRelations([]traceNodeRow{parent}); e != nil {
			return nil, e
		}
		nodes, e := r.nodes(parent)
		if e != nil {
			return nil, e
		}
		found := false
		for _, candidate := range nodes {
			if candidate.NodeID == encodeNodeKey(key) {
				if e = childCursor(candidate.NodeID, current, rootRow); e != nil {
					return nil, e
				}
				current = candidate
				rootRow = parent
				out.AncestorIDs = append(out.AncestorIDs, current.NodeID)
				found = true
				break
			}
		}
		if !found {
			out.State = "unresolved"
			out.Reason = "parent_unresolved"
			break
		}
	}
	for i, j := 0, len(out.AncestorIDs)-1; i < j; i, j = i+1, j-1 {
		out.AncestorIDs[i], out.AncestorIDs[j] = out.AncestorIDs[j], out.AncestorIDs[i]
	}
	out.RootNodeID = current.NodeID
	if out.GroupID == "" {
		anchor, basis, e := r.inputAnchor(row)
		if e != nil {
			return nil, e
		}
		if anchor != nil {
			out.GroupID = encodeNodeKey(*anchor)
			_ = basis
		}
	}
	locatorGroup := out.GroupID
	if locatorGroup == "" {
		locatorGroup = current.GroupID
	}
	if locatorGroup != "" {
		if out.State == "resolved" && current.ParentID == nil && current.NodeID != locatorGroup {
			if e := childCursor(locatorGroup, current, rootRow); e != nil {
				return nil, e
			}
		}
		out.RootNodeID = locatorGroup
		p, _, e := r.selected(locatorGroup)
		if e != nil {
			return nil, e
		}
		rootRow = p
	}
	key, e := decodeNodeKey(out.RootNodeID)
	if e != nil {
		return nil, e
	}
	out.RootCursor = encodeFlowCursor(traceFlowCursor{Session: r.session, Scope: traceScope(r.access), View: "flow", Revision: revision, Epoch: epoch, Key: key, UUID: rootRow.record.UUID, Locate: true})
	return out, nil
}
