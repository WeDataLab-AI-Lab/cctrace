package sessionfacts

import (
	"iter"
	"slices"
	"strings"
)

// Only models with a published single-window default belong here. A model
// missing from this table has no guessed window.
var singleWindowModels = map[string]int{"claude-haiku-4-5": 200000}

func modelWindow(model string, numerator int64, attachment string) (*int, string) {
	if strings.HasSuffix(attachment, "[1m]") {
		name := strings.TrimSuffix(attachment, "[1m]")
		if at := strings.LastIndexByte(name, '/'); at >= 0 {
			name = name[at+1:]
		}
		if name == model && numerator <= 1000000 {
			window := 1000000
			return &window, "observed"
		}
	}
	if window := singleWindowModels[model]; window > 0 && numerator <= int64(window) {
		return &window, "model_default"
	}
	return nil, "unknown"
}

func (c *computation) readContext(heads []*Head) { c.readContextRows(heads, slices.Values(c.lines)) }

func (c *computation) readContextRows(heads []*Head, rows iter.Seq[*line]) {
	s := &c.session
	s.LastContextSource = "unknown"
	attachments := make(map[string]string)
	for _, h := range heads {
		c.pulse()
		if h.InputTokens+h.CacheReadTokens+h.CacheCreateTokens > 200000 {
			s.LargeWindowSeen = true
		}
	}
	for _, snap := range c.in.Snapshots {
		c.pulse()
		for _, state := range snap.CostStates {
			c.pulse()
			for _, model := range state.Models {
				c.pulse()
				if strings.HasSuffix(model, "[1m]") {
					s.LargeWindowSeen = true
				}
			}
		}
	}
	// Each request sees only model attachments preceding its own line in the
	// same file. Head order is independent of row order.
	byID := make(map[int64]*Head, len(heads))
	for _, h := range heads {
		c.pulse()
		byID[h.ID] = h
	}
	for l := range rows {
		c.pulse()
		if l.RecordType == "attachment" && !l.IsSidechain && !l.copied {
			a := jsonMembers(l.Attachment)
			if jsonText(a["type"]) == "model" && l.SourceFile != "" {
				attachments[l.SourceFile] = jsonText(jsonMembers(a["identity"])["modelId"])
			}
		}
		h := byID[l.ID]
		if h == nil || h.IsSidechain || l.IsSidechain || l.copied {
			continue
		}
		n := h.InputTokens + h.CacheReadTokens + h.CacheCreateTokens
		if l.turn == nil {
			continue
		}
		t := l.turn
		t.out.ContextTokens = &n
		sourceFile := l.SourceFile
		if sourceFile == "" {
			sourceFile = h.SourceFile
		}
		attachment := ""
		if sourceFile != "" {
			attachment = attachments[sourceFile]
		}
		t.out.ContextWindow, t.out.ContextSource = modelWindow(h.Model, n, attachment)
		if t.out.Branch == "active" && t.out.TurnKey == s.LastTurnKey {
			s.Detail.ContextMix = ContextMix{Input: h.InputTokens, CacheRead: h.CacheReadTokens, CacheCreate: h.CacheCreateTokens}
		}
	}
	for _, t := range c.turns {
		c.pulse()
		if t.out.ContextSource == "" {
			t.out.ContextSource = "unknown"
		}
		if t.out.Branch == "active" && t.out.TurnKey == s.LastTurnKey {
			s.LastContextTokens, s.LastContextWindow, s.LastContextSource = t.out.ContextTokens, t.out.ContextWindow, t.out.ContextSource
		}
	}
}
