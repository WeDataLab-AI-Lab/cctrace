package sessionfacts

import "sort"

// buildForest links the main-thread lines into the parentUuid forest and marks
// each component's active path (p1b-design §3.4). It returns the nodes in
// (ts, uuid) order, and by uuid.
//
// A node is a main-thread user, assistant, system or attachment line with a
// uuid and a timestamp of its own. A line whose parent is missing or was never
// stored is a root, so a resumed session and the conversation after a
// compaction are components of their own, each with its own active path:
// taking one root for the whole file would call 15.4% of local assistant lines
// abandoned.
func buildForestChecked(lines []*line, checkpoint func()) ([]*line, map[string]*line) {
	// The storage key is (session_id, ts, record_type, profile_email, uuid), so
	// one uuid can be stored twice. The node is the least row by (ts,
	// record_type, profile_email, parent_uuid): a choice the rows themselves
	// make, whatever order they arrived in.
	byUUID := make(map[string]*line)
	for _, l := range lines {
		checkpoint()
		if l.IsSidechain || !l.keyed() || !l.dated() {
			continue
		}
		switch l.RecordType {
		case "user", "assistant", "system", "attachment":
		default:
			continue
		}
		if cur := byUUID[l.UUID]; cur == nil || nodeBefore(l, cur) {
			byUUID[l.UUID] = l
		}
	}
	nodes := make([]*line, 0, len(byUUID))
	for _, l := range byUUID {
		checkpoint()
		nodes = append(nodes, l)
	}
	sort.Slice(nodes, func(i, j int) bool { checkpoint(); return nodes[i].before(nodes[j]) })

	for _, n := range nodes {
		checkpoint()
		n.parent = byUUID[n.ParentUUID]
	}
	cutCyclesChecked(nodes, checkpoint)

	// A compact boundary starts a new root and names, in logicalParentUuid, the
	// line the conversation continued from. Joining the two makes the turns
	// before the compaction ancestors of what followed -- but only when that
	// line is not below the boundary itself: the newer format points it at a
	// line the boundary heads (30 of 114 local boundaries), and joining those
	// would close a loop.
	for _, n := range nodes {
		checkpoint()
		if n.Subtype != "compact_boundary" {
			continue
		}
		if target := byUUID[n.LogicalParentUUID]; target != nil && !descendsChecked(target, n, checkpoint) {
			n.parent = target
		}
	}

	for _, n := range nodes {
		checkpoint()
		if n.parent != nil {
			n.parent.children++
		}
	}
	// The active path of a component runs from its latest leaf up to its root.
	// nodes are in (ts, uuid) order, so the last leaf seen for a root is that
	// leaf.
	latest := make(map[*line]*line)
	for _, n := range nodes {
		checkpoint()
		n.root = rootOfChecked(n, checkpoint)
		if n.children == 0 {
			latest[n.root] = n
		}
	}
	for _, leaf := range latest {
		checkpoint()
		for n := leaf; n != nil; n = n.parent {
			checkpoint()
			n.active = true
		}
	}
	return nodes, byUUID
}

// nodeBefore orders two rows of one uuid for the choice of its node.
func nodeBefore(a, b *line) bool {
	switch {
	case !a.TS.Equal(b.TS):
		return a.TS.Before(b.TS)
	case a.RecordType != b.RecordType:
		return a.RecordType < b.RecordType
	case a.ProfileEmail != b.ProfileEmail:
		return a.ProfileEmail < b.ProfileEmail
	case a.ParentUUID != b.ParentUUID:
		return a.ParentUUID < b.ParentUUID
	}
	return a.ID < b.ID
}

// cutCycles makes the parent links a forest. Claude Code does not write a loop,
// but everything after this walks to a root and has to end on rows that hold
// one anyway. A loop is cut above its earliest line, which is the same line
// wherever the walk entered it.
func cutCyclesChecked(nodes []*line, checkpoint func()) {
	const walking, done = 1, 2
	for _, n := range nodes {
		checkpoint()
		var path []*line
		m := n
		for ; m != nil && m.mark == 0; m = m.parent {
			checkpoint()
			m.mark = walking
			path = append(path, m)
		}
		if m != nil && m.mark == walking {
			first := m
			for c := m.parent; c != m; c = c.parent {
				checkpoint()
				if c.before(first) {
					first = c
				}
			}
			first.parent = nil
		}
		for _, p := range path {
			checkpoint()
			p.mark = done
		}
	}
}

// descends reports whether n is below ancestor, or is ancestor itself.
func descendsChecked(n, ancestor *line, checkpoint func()) bool {
	for ; n != nil; n = n.parent {
		checkpoint()
		if n == ancestor {
			return true
		}
	}
	return false
}

// rootOf finds a node's root, remembering it on every node it passes.
func rootOfChecked(n *line, checkpoint func()) *line {
	var path []*line
	for n.root == nil && n.parent != nil {
		checkpoint()
		path = append(path, n)
		n = n.parent
	}
	root := n.root
	if root == nil {
		root = n
	}
	for _, p := range path {
		checkpoint()
		p.root = root
	}
	return root
}
