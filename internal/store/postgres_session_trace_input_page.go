package store

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"cctrace/internal/sessiontrace"
)

type traceInputCursor struct {
	Session, Scope, Node, Position string
	Revision, Epoch                int64
}

func (s *PgStore) encodeInputCursor(c traceInputCursor) (string, error) {
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	mac, err := s.turnContextMAC("trace-input-v1", body)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}
func (s *PgStore) decodeInputCursor(raw string, want traceInputCursor) (string, error) {
	if len(raw) > 4096 {
		return "", ErrTraceInvalid
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return "", ErrTraceInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", ErrTraceInvalid
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrTraceInvalid
	}
	expected, err := s.turnContextMAC("trace-input-v1", body)
	if err != nil {
		return "", err
	}
	var c traceInputCursor
	if !hmac.Equal(mac, expected) || json.Unmarshal(body, &c) != nil || c.Session != want.Session || c.Scope != want.Scope || c.Node != want.Node {
		return "", ErrTraceInvalid
	}
	if c.Revision != want.Revision || c.Epoch != want.Epoch {
		return "", ErrTraceSnapshot
	}
	return c.Position, nil
}
func traceSourceLocation(row traceNodeRow, kind, rawPath string) sessiontrace.SourceLocation {
	key := row.key("source", -1)
	return sessiontrace.SourceLocation{SourceRef: key.Ref, NodeID: encodeNodeKey(key), RawPath: rawPath, UUID: row.record.UUID, State: traceAuditValue(row.raw).State}
}
func (l *nativeNodeLookup) remember(row traceNodeRow) {
	if l.sourceRows == nil {
		l.sourceRows = map[string]traceNodeRow{}
	}
	l.sourceRows[row.key("source", -1).Ref] = row
}
func (l *nativeNodeLookup) addLocation(location sessiontrace.SourceLocation) {
	for i, before := range l.locations {
		if before.SourceRef == location.SourceRef && before.RawPath == location.RawPath {
			if location.BlockStart != nil {
				l.locations[i] = location
			}
			return
		}
	}
	l.locations = append(l.locations, location)
}

type nativeSourceSpan struct {
	location   sessiontrace.SourceLocation
	start, end int
	text       bool
}

func (l *nativeNodeLookup) enrich(request *sessiontrace.NativeRequest, recipe traceNodeRow) {
	recipeRef := recipe.key("request", -1).Ref
	l.addLocation(traceSourceLocation(recipe, "request", ""))
	expanded := []sessiontrace.RequestProvenance{}
	for _, item := range request.Provenance {
		p := item
		if p.SourceRef == "" {
			p.SourceRef = recipeRef
		}
		spans := l.spans[p.SourceRef]
		if len(spans) > 0 && p.BlockOrdinal != nil {
			for _, span := range spans {
				block := *p.BlockOrdinal
				if block < span.start || block >= span.end {
					continue
				}
				copy := p
				copy.SourceRef = span.location.SourceRef
				copy.UUID = span.location.UUID
				start := block - span.start
				if span.location.BlockStart != nil {
					start += *span.location.BlockStart
				}
				end := start + 1
				copy.BlockStart = &start
				copy.BlockEnd = &end
				copy.RawPath = fmt.Sprintf("message.content[%d]", start)
				if span.text {
					copy.RawPath = "message.content"
				}
				expanded = append(expanded, copy)
			}
		} else {
			candidates := l.aliases[p.SourceRef]
			if len(candidates) == 0 {
				candidates = l.locations
			}
			found := false
			for _, location := range candidates {
				if location.SourceRef != p.SourceRef && len(l.aliases[p.SourceRef]) == 0 {
					continue
				}
				copy := p
				copy.SourceRef = location.SourceRef
				copy.UUID = location.UUID
				copy.BlockStart = location.BlockStart
				copy.BlockEnd = location.BlockEnd
				expanded = append(expanded, copy)
				found = true
			}
			if !found {
				expanded = append(expanded, p)
			}
		}
	}
	request.Provenance = expanded
	if request.InputPage != nil {
		for i := range request.InputPage.Items {
			item := &request.InputPage.Items[i]
			refs := []string{}
			for _, p := range expanded {
				if p.RequestPath == item.RequestPath || strings.HasPrefix(p.RequestPath, item.RequestPath+".") {
					refs = append(refs, p.SourceRef)
				}
			}
			if len(refs) == 0 {
				refs = []string{recipeRef}
			}
			item.SourceRefs = refs
		}
	}
	seen := map[string]bool{}
	refs := []string{}
	for _, ref := range append(request.SourceRefs, recipeRef) {
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	for _, p := range expanded {
		if p.SourceRef != "" && !seen[p.SourceRef] {
			seen[p.SourceRef] = true
			refs = append(refs, p.SourceRef)
		}
	}
	request.SourceRefs = refs
}
