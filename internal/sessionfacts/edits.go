package sessionfacts

import (
	"iter"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

func repositoryRoot(cwd, subpath string) string {
	if !filepath.IsAbs(cwd) {
		return ""
	}
	root := filepath.Clean(cwd)
	if subpath == "" {
		return root
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(subpath)), "/") {
		if part != "" && part != "." {
			root = filepath.Dir(root)
		}
	}
	return root
}

func (c *computation) readEdits() { c.readEditsRows(slices.Values(c.lines)) }

func (c *computation) readEditsRows(rows iter.Seq[*line]) {
	files := make(map[string]*EditedFile)
	var paths []string
	types := map[string]int{}
	requests := 0
	for l := range rows {
		c.pulse()
		if l.copied {
			continue
		}
		if !l.IsSidechain && l.turn != nil && l.turn.out.IsRequest && l.turn.opener == l && l.TaskType != "" {
			types[l.TaskType]++
			requests++
		}
		for _, b := range l.blocks {
			c.pulse()
			if b.Type != "tool_use" {
				continue
			}
			if !l.IsSidechain && l.turn != nil && l.active {
				l.turn.out.ToolCalls++
			}
			if l.IsSidechain || l.active {
				c.session.Detail.ToolCalls++
			}
			if b.Name != "Edit" && b.Name != "Write" && b.Name != "MultiEdit" && b.Name != "NotebookEdit" {
				continue
			}
			data := jsonMembers(b.Input)
			path := jsonText(data["fp"])
			if path == "" {
				c.session.Detail.EditedFiles.State = "redacted"
				continue
			}
			root := repositoryRoot(l.CWD, l.RepoSubpath)
			if filepath.IsAbs(path) {
				if root == "" {
					c.session.Detail.EditedFiles.State = "undeterminable"
					continue
				}
				rel, err := filepath.Rel(root, path)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					continue
				}
				path = rel
			} else if root != "" {
				rel, err := filepath.Rel(root, filepath.Join(l.CWD, path))
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					continue
				}
				path = rel
			} else if clean := filepath.Clean(path); clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				continue
			}
			path = filepath.ToSlash(filepath.Clean(path))
			if path == "." {
				continue
			}
			f := files[path]
			if f == nil {
				f = &EditedFile{Path: path}
				files[path] = f
				paths = append(paths, path)
			}
			switch {
			case l.IsSidechain:
				f.SubagentEdits++
				c.session.Detail.EditedFiles.ActiveTotal++
			case l.active:
				f.Edits++
				c.session.Detail.EditedFiles.ActiveTotal++
			default:
				f.AbandonedEdits++
				c.session.Detail.EditedFiles.AbandonedTotal++
			}
			if l.turn != nil && !l.IsSidechain {
				if !l.active {
					l.turn.out.Detail.Edited.Abandoned++
				} else {
					l.turn.out.Detail.Edited.Count++
					if len(l.turn.out.Detail.Edited.Sample) < 5 {
						l.turn.out.Detail.Edited.Sample = append(l.turn.out.Detail.Edited.Sample, path)
					}
				}
			}
		}
	}
	if c.session.Detail.EditedFiles.State == "" {
		c.session.Detail.EditedFiles.State = "observed"
	}
	activeRedacted := false
	for _, l := range c.lines {
		c.pulse()
		if l.IsSidechain || l.copied {
			continue
		}
		if l.contentRedacted() && l.RecordType == "assistant" {
			c.session.Detail.EditedFiles.State = "redacted"
			activeRedacted = activeRedacted || l.active
		}
	}
	sort.Strings(paths)
	c.session.Detail.EditedFiles.Total = len(paths)
	for _, path := range paths {
		c.pulse()
		if len(c.session.Detail.EditedFiles.Items) == 100 {
			c.session.Detail.EditedFiles.Truncated = true
			break
		}
		c.session.Detail.EditedFiles.Items = append(c.session.Detail.EditedFiles.Items, *files[path])
	}
	areas := map[string]int{}
	for _, path := range paths {
		c.pulse()
		p := strings.Split(filepath.ToSlash(path), "/")
		if len(p) > 2 {
			p = p[:2]
		}
		if files[path].Edits > 0 {
			areas[strings.Join(p, "/")] += files[path].Edits
		}
	}
	area, most := "", 0
	for key, n := range areas {
		c.pulse()
		if n > most || n == most && (area == "" || key < area) {
			area, most = key, n
		}
	}
	if most > 0 {
		c.session.Detail.ChangeArea = AreaFact{State: "observed", Value: area}
	} else {
		c.session.Detail.ChangeArea = AreaFact{State: "undeterminable", Reason: "no_edits"}
	}
	if activeRedacted {
		c.session.Detail.ChangeArea = AreaFact{State: "undeterminable", Reason: "redacted"}
	}
	name, count := "", 0
	for kind, n := range types {
		c.pulse()
		if n > count || n == count && (name == "" || kind < name) {
			name, count = kind, n
		}
	}
	if requests > 0 {
		c.session.Detail.TaskType = TaskTypeFact{Value: name, Share: float64(count) / float64(requests)}
	}
}
