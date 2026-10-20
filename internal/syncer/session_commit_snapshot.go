package syncer

import (
	"slices"
	"sort"
)

const maxSnapshotCommits = 100

type SnapshotCommits struct {
	Trees      []CommitTree       `json:"trees,omitempty"`
	Items      []SnapshotCommit   `json:"items,omitempty"`
	Unresolved []UnresolvedCommit `json:"unresolved_calls,omitempty"`
}
type CommitTree struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch,omitempty"`
}
type SnapshotCommit struct {
	Hash       string  `json:"hash"`
	At         int64   `json:"at"`
	Parents    int     `json:"parents"`
	Kind       string  `json:"kind"`
	Subject    *string `json:"subject,omitempty"`
	Call       string  `json:"call"`
	Transcript string  `json:"transcript,omitempty"`
	Tree       int     `json:"tree"`
	Proof      string  `json:"proof"`
	Replaces   string  `json:"replaces,omitempty"`
}
type UnresolvedCommit struct {
	Call       string `json:"call"`
	Transcript string `json:"transcript,omitempty"`
	At         int64  `json:"at"`
	Reason     string `json:"reason"`
}

func (c *SnapshotCommits) clone() *SnapshotCommits {
	if c == nil {
		return nil
	}
	out := *c
	out.Trees = slices.Clone(c.Trees)
	out.Items = slices.Clone(c.Items)
	out.Unresolved = slices.Clone(c.Unresolved)
	return &out
}

func (s *SessionSnapshot) mergeCommits(in *SnapshotCommits) {
	if in == nil {
		return
	}
	if s.Commits == nil {
		s.Commits = &SnapshotCommits{}
	}
	c := s.Commits
	for _, item := range in.Items {
		if item.Tree < 0 || item.Tree >= len(in.Trees) {
			s.markTruncated("commits")
			continue
		}
		existing := slices.IndexFunc(c.Items, func(v SnapshotCommit) bool {
			return v.Call == item.Call && v.Transcript == item.Transcript && v.Hash == item.Hash
		})
		if existing >= 0 {
			// Never restore a read-time redaction marker. Only withheld content can
			// gain a value when an explicit include-content re-enrichment supplies it.
			if c.Items[existing].Subject == nil && item.Subject != nil {
				subject := cutAtRune(*item.Subject, 200)
				c.Items[existing].Subject = &subject
			}
			continue
		}
		tree := in.Trees[item.Tree]
		tree.Repository = cutAtRune(tree.Repository, maxSnapshotFieldBytes)
		tree.Branch = cutAtRune(tree.Branch, maxSnapshotFieldBytes)
		item.Tree = slices.Index(c.Trees, tree)
		if item.Tree < 0 {
			item.Tree = len(c.Trees)
			c.Trees = append(c.Trees, tree)
		}
		if item.Subject != nil {
			subject := cutAtRune(*item.Subject, 200)
			item.Subject = &subject
		}
		c.Items = append(c.Items, item)
	}
	for _, u := range in.Unresolved {
		if !slices.ContainsFunc(c.Unresolved, func(v UnresolvedCommit) bool { return v.Call == u.Call && v.Transcript == u.Transcript }) {
			c.Unresolved = append(c.Unresolved, u)
		}
	}
	c.Unresolved = slices.DeleteFunc(c.Unresolved, func(u UnresolvedCommit) bool {
		return slices.ContainsFunc(c.Items, func(v SnapshotCommit) bool { return u.Call == v.Call && u.Transcript == v.Transcript })
	})
	sort.SliceStable(c.Items, func(i, j int) bool { return c.Items[i].At < c.Items[j].At })
	sort.SliceStable(c.Unresolved, func(i, j int) bool { return c.Unresolved[i].At < c.Unresolved[j].At })
	if len(c.Items) > maxSnapshotCommits {
		c.Items = c.Items[len(c.Items)-maxSnapshotCommits:]
		s.markTruncated("commits")
	}
	if len(c.Unresolved) > maxSnapshotCommits {
		c.Unresolved = c.Unresolved[len(c.Unresolved)-maxSnapshotCommits:]
		s.markTruncated("commits")
	}
	c.compactTrees()
}

func (c *SnapshotCommits) compactTrees() {
	var trees []CommitTree
	for i := range c.Items {
		old := c.Items[i].Tree
		if old < 0 || old >= len(c.Trees) {
			continue
		}
		tree := c.Trees[old]
		next := slices.Index(trees, tree)
		if next < 0 {
			next = len(trees)
			trees = append(trees, tree)
		}
		c.Items[i].Tree = next
	}
	c.Trees = trees
}
