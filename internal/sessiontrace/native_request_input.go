package sessiontrace

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// SourceLocation identifies a freshly authorized source record. SourceRef is an
// opaque identity; clients must use NodeID, never reinterpret it as a UUID/path.
type SourceLocation struct {
	SourceRef  string `json:"source_ref"`
	NodeID     string `json:"node_id"`
	RawPath    string `json:"raw_path"`
	UUID       string `json:"uuid,omitempty"`
	BlockStart *int   `json:"block_start,omitempty"`
	BlockEnd   *int   `json:"block_end,omitempty"`
	State      string `json:"state"`
}
type RequestProvenance struct {
	RequestPath    string `json:"request_path"`
	SourceRef      string `json:"source_ref"`
	RawPath        string `json:"raw_path"`
	UUID           string `json:"uuid,omitempty"`
	BlockStart     *int   `json:"block_start,omitempty"`
	BlockEnd       *int   `json:"block_end,omitempty"`
	MessageOrdinal *int   `json:"message_ordinal,omitempty"`
	BlockOrdinal   *int   `json:"block_ordinal,omitempty"`
	State          string `json:"state"`
}
type RequestInputItem struct {
	RequestPath    string   `json:"request_path"`
	Kind           string   `json:"kind"`
	Role           string   `json:"role,omitempty"`
	MessageOrdinal *int     `json:"message_ordinal,omitempty"`
	BlockOrdinal   *int     `json:"block_ordinal,omitempty"`
	Value          any      `json:"value,omitempty"`
	State          string   `json:"state"`
	SourceRefs     []string `json:"source_refs"`
	OffsetBytes    int      `json:"offset_bytes,omitempty"`
	TotalBytes     int      `json:"total_bytes,omitempty"`
	Truncated      bool     `json:"truncated,omitempty"`
}
type RequestInputPage struct {
	Items      []RequestInputItem `json:"items"`
	NextCursor *string            `json:"next_cursor"`
	Digest     DigestCheck        `json:"digest"`
	Coverage   string             `json:"coverage"`
}
type RequestComparisonItem struct {
	RequestPath string `json:"request_path"`
	ComparePath string `json:"compare_path,omitempty"`
	Change      string `json:"change"`
	Reason      string `json:"reason,omitempty"`
}
type RequestComparison struct {
	CompareTo string                  `json:"compare_to"`
	State     string                  `json:"state"`
	Items     []RequestComparisonItem `json:"items"`
}
type NativeInputOptions struct {
	Limit        int
	Cursor       string
	PayloadLimit int
}
type inputPosition struct {
	Section int `json:"s"`
	Message int `json:"m"`
	Block   int `json:"b"`
	Offset  int `json:"o"`
}

var ErrNativeInputCursor = errors.New("invalid native input cursor")

func inputCursor(p inputPosition) string {
	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b)
}
func ordinal(n int) *int { return &n }
func provenance(path, source, raw string, m, b *int, state string) RequestProvenance {
	return RequestProvenance{RequestPath: path, SourceRef: source, RawPath: raw, MessageOrdinal: m, BlockOrdinal: b, State: state}
}
func inputState(value any) string {
	if containsHidden(value) {
		return "redacted"
	}
	if value == nil {
		return "unresolved"
	}
	return "observed"
}

// nativeMessageKeys traverses only recipes. It never fetches message bodies.
func nativeMessageKeys(recipe map[string]any, lookup ReferenceLookup) ([]string, []string, error) {
	var missing []string
	visiting := map[string]bool{}
	var walk func(map[string]any, int) ([]string, error)
	walk = func(r map[string]any, depth int) ([]string, error) {
		if depth > 2048 {
			missing = append(missing, "request:dependency_limit")
			return nil, nil
		}
		var keys []string
		keep, _ := nativeInt(r["keep"])
		if keep < 0 {
			missing = append(missing, "request:invalid_keep")
			keep = 0
		}
		if keep > 0 {
			base, _ := r["base"].(string)
			if base == "" || visiting[base] {
				missing = append(missing, "request:base_unresolved")
			} else {
				visiting[base] = true
				previous, _, err := lookup("request", base)
				if err != nil {
					return nil, err
				}
				if previous == nil {
					missing = append(missing, "request:"+base)
				} else {
					keys, err = walk(previous, depth+1)
					if err != nil {
						return nil, err
					}
				}
				delete(visiting, base)
			}
			if keep > len(keys) {
				missing = append(missing, "request:keep_out_of_range")
				keep = len(keys)
			}
			keys = keys[:keep]
		}
		tail, ok := r["tail"].([]any)
		if !ok {
			missing = append(missing, "request:tail_not_collected")
		}
		for _, value := range tail {
			if key, ok := value.(string); ok {
				keys = append(keys, key)
			} else {
				missing = append(missing, "request:invalid_tail")
			}
		}
		return keys, nil
	}
	keys, err := walk(recipe, 0)
	return keys, missing, err
}

// ResolveNativeRequestPage reads only the shape and selected message bodies.
// Its digest deliberately remains unchecked even when the last page is reached.
func ResolveNativeRequestPage(recipe map[string]any, lookup ReferenceLookup, opts NativeInputOptions) (NativeRequest, error) {
	out := NativeRequest{State: "observed", MissingRefs: []string{}, SourceRefs: []string{}, Digest: DigestCheck{State: "not_checked"}}
	page := &RequestInputPage{Items: []RequestInputItem{}, Digest: out.Digest, Coverage: "page"}
	out.InputPage = page
	if digest, ok := recipe["requestDigest"].(string); ok {
		out.Digest.Expected = digest
		page.Digest = out.Digest
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	capBytes := opts.PayloadLimit
	if capBytes <= 0 || capBytes > 1<<20 {
		capBytes = 1 << 20
	}
	if capBytes < 256 {
		return out, ErrNativeInputCursor
	}
	p := inputPosition{}
	if opts.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(opts.Cursor)
		if e != nil || json.Unmarshal(raw, &p) != nil || p.Section < 0 || p.Section > 4 || p.Message < 0 || p.Block < 0 || p.Offset < 0 {
			return out, ErrNativeInputCursor
		}
	}
	keys, missing, err := nativeMessageKeys(recipe, lookup)
	if err != nil {
		return out, err
	}
	out.MissingRefs = append(out.MissingRefs, missing...)
	if len(missing) > 0 {
		out.State = "partial"
	}
	if nativeFieldsWithheld(recipe) {
		out.State = "withheld"
	}
	if recipe["cut"] == true {
		out.State = "truncated"
	}
	used := 2
	seen := map[string]bool{}
	var shape map[string]any
	shapeRef := ""
	shapeLoaded := false
	finish := func() { cursor := inputCursor(p); page.NextCursor = &cursor }
	for len(page.Items) < limit {
		var value any
		var path, raw, source, kind, role string
		var mi, bi *int
		withheld := false
		next := p
		next.Offset = 0
		switch p.Section {
		case 3:
			params, _ := recipe["params"].(map[string]any)
			names := make([]string, 0, len(params))
			for name := range params {
				names = append(names, name)
			}
			sort.Strings(names)
			if p.Block >= len(names) {
				return out, nil
			}
			name := names[p.Block]
			value = params[name]
			path = name
			raw = "params." + name
			kind = "params"
			next.Block++
		case 0, 2:
			if !shapeLoaded {
				shapeLoaded = true
				if key, _ := recipe["shapeHash"].(string); key != "" {
					row, ref, e := lookup("shape", key)
					if e != nil {
						return out, e
					}
					shapeRef = ref
					shape, _ = row["shape"].(map[string]any)
					if shape == nil {
						out.MissingRefs = append(out.MissingRefs, "shape:"+key)
						out.State = "partial"
					}
				}
			}
			name := "system"
			if p.Section == 2 {
				name = "tools"
			}
			v, exists := shape[name]
			if !exists {
				p = inputPosition{Section: p.Section + 1}
				continue
			}
			source = shapeRef
			kind = name
			raw = "shape." + name
			if blocks, ok := v.([]any); ok {
				if len(blocks) == 0 && p.Block == 0 {
					value = blocks
					path = name
					next.Block++
					break
				}
				if p.Block >= len(blocks) {
					p = inputPosition{Section: p.Section + 1}
					continue
				}
				value = blocks[p.Block]
				path = fmt.Sprintf("%s[%d]", name, p.Block)
				raw = fmt.Sprintf("%s[%d]", raw, p.Block)
				bi = ordinal(p.Block)
				next.Block++
			} else {
				if p.Block > 0 {
					p = inputPosition{Section: p.Section + 1}
					continue
				}
				value = v
				path = name
				next.Block++
			}
		case 1:
			if p.Message >= len(keys) {
				p = inputPosition{Section: 2}
				continue
			}
			row, ref, e := lookup("message", keys[p.Message])
			if e != nil {
				return out, e
			}
			source = ref
			withheld = nativeFieldsWithheld(row)
			kind = "message"
			mi = ordinal(p.Message)
			if row == nil {
				path = fmt.Sprintf("messages[%d]", p.Message)
				raw = "message"
				next.Message++
				next.Block = 0
				out.State = "partial"
				out.MissingRefs = append(out.MissingRefs, "message:"+keys[p.Message])
			} else {
				message, _ := row["message"].(map[string]any)
				if message == nil {
					message = row
				}
				role, _ = message["role"].(string)
				content := message["content"]
				if text, ok := content.(string); ok {
					content = []any{map[string]any{"type": "text", "text": text}}
				}
				blocks, ok := content.([]any)
				if !ok {
					if p.Block > 0 {
						p.Message++
						p.Block = 0
						p.Offset = 0
						continue
					}
					value = content
					path = fmt.Sprintf("messages[%d].content", p.Message)
					raw = "message.content"
					next.Message++
					next.Block = 0
				} else {
					if p.Block >= len(blocks) {
						type extraField struct {
							name, raw string
							value     any
							recipe    bool
						}
						extras := []extraField{}
						if len(blocks) == 0 {
							extras = append(extras, extraField{name: "content", raw: "message.content", value: blocks})
						}
						if config, exists := message["output_config"]; role == "system" && exists {
							extras = append(extras, extraField{name: "output_config", raw: "message.output_config", value: config})
						}
						controls, _ := recipe["cacheControl"].([]any)
						for i, entry := range controls {
							control, _ := entry.(map[string]any)
							parts, _ := control["path"].([]any)
							if pathParts(parts) == fmt.Sprintf("messages[%d]", p.Message) {
								extras = append(extras, extraField{name: "cache_control", raw: fmt.Sprintf("cacheControl[%d].value", i), value: control["value"], recipe: true})
							}
						}
						extra := p.Block - len(blocks)
						if extra < len(extras) {
							field := extras[extra]
							value = field.value
							path = fmt.Sprintf("messages[%d].%s", p.Message, field.name)
							raw = field.raw
							if field.recipe {
								source = ""
							}
							next.Block++
							break
						}
						p.Message++
						p.Block = 0
						p.Offset = 0
						continue
					}
					value = blocks[p.Block]
					bi = ordinal(p.Block)
					path = fmt.Sprintf("messages[%d].content[%d]", p.Message, p.Block)
					raw = fmt.Sprintf("message.content[%d]", p.Block)
					next.Block++
				}
			}
		}
		if p.Section == 4 {
			return out, nil
		}
		if len(page.Items) > 0 && jsonLowerBound(value, capBytes-used) > capBytes-used {
			finish()
			return out, nil
		}
		// Apply only cache controls at/below this block; a copy is made only when a
		// recorded control applies. Sibling message bodies are never materialized.
		value = pageCacheControls(value, path, recipe)
		state := inputState(value)
		if withheld {
			state = "withheld"
			out.State = "withheld"
		}
		if state == "redacted" {
			out.State = "redacted"
		}
		refs := []string{}
		if source != "" {
			refs = append(refs, source)
			if !seen[source] {
				out.SourceRefs = append(out.SourceRefs, source)
				seen[source] = true
			}
		}
		item := RequestInputItem{RequestPath: path, Kind: kind, Role: role, MessageOrdinal: mi, BlockOrdinal: bi, Value: value, State: state, SourceRefs: refs}
		encoded, e := CanonicalJSON(value)
		if e != nil {
			return out, e
		}
		if len(encoded) > nativeRequestBodyByteLimit {
			out.State = "truncated"
			out.MissingRefs = append(out.MissingRefs, "request:body_byte_limit")
			item.Value = nil
			item.State = "truncated"
			encoded = []byte("null")
		}
		full, _ := json.Marshal(item)
		if p.Offset > 0 || used+len(full)+1 > capBytes {
			if len(page.Items) > 0 {
				finish()
				return out, nil
			}
			// JSON fragments are explicitly identified by byte offsets. Chunk at UTF-8
			// boundaries; the client concatenates them before parsing the block JSON.
			if p.Offset >= len(encoded) {
				return out, ErrNativeInputCursor
			}
			item.TotalBytes = len(encoded)
			item.OffsetBytes = p.Offset
			item.Truncated = true
			end := len(encoded)
			if end-p.Offset > capBytes-used-256 {
				end = p.Offset + capBytes - used - 256
			}
			if end <= p.Offset {
				return out, ErrNativeInputCursor
			}
			for end > p.Offset && end < len(encoded) && !utf8.RuneStart(encoded[end]) {
				end--
			}
			for {
				item.Value = string(encoded[p.Offset:end])
				full, _ = json.Marshal(item)
				if used+len(full)+1 <= capBytes {
					break
				}
				end -= (len(full) + used + 1 - capBytes + 1) / 2
				if end <= p.Offset {
					return out, ErrNativeInputCursor
				}
				for end > p.Offset && end < len(encoded) && !utf8.RuneStart(encoded[end]) {
					end--
				}
			}
			if end < len(encoded) {
				next = p
				next.Offset = end
			} else {
				item.Truncated = false
			}
		}
		controls, _ := recipe["cacheControl"].([]any)
		for i, entry := range controls {
			control, _ := entry.(map[string]any)
			parts, _ := control["path"].([]any)
			if pathParts(parts) == path {
				out.Provenance = append(out.Provenance, provenance(path+".cache_control", "", fmt.Sprintf("cacheControl[%d].value", i), mi, bi, inputState(control["value"])))
			}
		}
		page.Items = append(page.Items, item)
		out.Provenance = append(out.Provenance, provenance(path, source, raw, mi, bi, state))
		used += len(full) + 1
		p = next
		if item.OffsetBytes > 0 || item.Truncated {
			finish()
			return out, nil
		}
	}
	finish()
	return out, nil
}

// Stop before serializing a later block that already cannot fit this page.
func jsonLowerBound(value any, limit int) int {
	size := 0
	switch v := value.(type) {
	case string:
		return len(v) + 2
	case map[string]any:
		size = 2
		for key, child := range v {
			size += len(key) + 4 + jsonLowerBound(child, limit-size)
			if size > limit {
				return size
			}
		}
	case []any:
		size = 2
		for _, child := range v {
			size += 1 + jsonLowerBound(child, limit-size)
			if size > limit {
				return size
			}
		}
	default:
		encoded, _ := CanonicalJSON(value)
		size = len(encoded)
	}
	return size
}

func pathParts(path []any) string {
	var out strings.Builder
	for _, part := range path {
		if n, ok := nativeInt(part); ok {
			fmt.Fprintf(&out, "[%d]", n)
		} else if s, ok := part.(string); ok {
			if out.Len() > 0 {
				out.WriteByte('.')
			}
			out.WriteString(s)
		}
	}
	return out.String()
}
func pageCacheControls(value any, path string, recipe map[string]any) any {
	controls, _ := recipe["cacheControl"].([]any)
	for _, entry := range controls {
		control, _ := entry.(map[string]any)
		parts, _ := control["path"].([]any)
		controlPath := pathParts(parts)
		if controlPath == path {
			if object, ok := value.(map[string]any); ok {
				copy := copyValue(object, false).(map[string]any)
				copy["cache_control"] = copyValue(control["value"], false)
				value = copy
			}
		}
	}
	return value
}

func requestItems(body map[string]any) []RequestInputItem {
	var items []RequestInputItem
	names := make([]string, 0, len(body))
	for name := range body {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := body[name]
		if name == "messages" {
			messages, _ := value.([]any)
			for i, entry := range messages {
				message, _ := entry.(map[string]any)
				if message == nil {
					items = append(items, RequestInputItem{RequestPath: fmt.Sprintf("messages[%d]", i), State: "unresolved"})
					continue
				}
				if config, ok := message["output_config"]; ok {
					items = append(items, RequestInputItem{RequestPath: fmt.Sprintf("messages[%d].output_config", i), Value: config, State: inputState(config)})
				}
				if control, ok := message["cache_control"]; ok {
					items = append(items, RequestInputItem{RequestPath: fmt.Sprintf("messages[%d].cache_control", i), Value: control, State: inputState(control)})
				}
				blocks, _ := message["content"].([]any)
				if len(blocks) == 0 {
					items = append(items, RequestInputItem{RequestPath: fmt.Sprintf("messages[%d].content", i), Value: map[string]any{"role": message["role"], "content": blocks}, State: "observed"})
				}
				for j, block := range blocks {
					items = append(items, RequestInputItem{RequestPath: fmt.Sprintf("messages[%d].content[%d]", i, j), Value: map[string]any{"role": message["role"], "block": block}, State: inputState(block)})
				}
			}
		} else if blocks, ok := value.([]any); ok && (name == "system" || name == "tools") {
			if len(blocks) == 0 {
				items = append(items, RequestInputItem{RequestPath: name, Value: blocks, State: "observed"})
			}
			for i, block := range blocks {
				items = append(items, RequestInputItem{RequestPath: fmt.Sprintf("%s[%d]", name, i), Value: block, State: inputState(block)})
			}
		} else {
			items = append(items, RequestInputItem{RequestPath: name, Value: value, State: inputState(value)})
		}
	}
	return items
}

func comparisonHash(item RequestInputItem) string {
	section := item.RequestPath
	if i := strings.IndexByte(section, '['); i >= 0 {
		section = section[:i]
	}
	if strings.HasPrefix(item.RequestPath, "messages[") {
		if suffix := strings.IndexByte(item.RequestPath, ']'); suffix >= 0 {
			field := item.RequestPath[suffix+1:]
			if end := strings.IndexByte(field, '['); end >= 0 {
				field = field[:end]
			}
			section += field
		}
	}
	hash, _ := CanonicalHash([]any{section, item.Value})
	return hash
}
func unknownRequestPath(request NativeRequest, path string) bool {
	if request.State == "observed" {
		return false
	}
	if len(request.MissingRefs) == 0 {
		return true
	}
	for _, missing := range request.MissingRefs {
		if strings.HasPrefix(missing, "shape:") && (strings.HasPrefix(path, "system") || strings.HasPrefix(path, "tools")) {
			return true
		}
		if strings.HasPrefix(missing, "request:") {
			return true
		}
	}
	return false
}

// CompareNativeRequests compares explicit observed values. Missing/hidden source
// regions never become additions/removals based solely on absent reconstruction.
func CompareNativeRequests(current, previous NativeRequest, compareTo string) RequestComparison {
	out := RequestComparison{CompareTo: compareTo, State: "observed", Items: []RequestComparisonItem{}}
	a, b := requestItems(current.Body), requestItems(previous.Body)
	if current.State != "observed" || previous.State != "observed" {
		out.State = "partial"
	}
	oldHashes := map[string][]int{}
	oldPaths := map[string]int{}
	used := make([]bool, len(b))
	matched := make([]bool, len(a))
	for i, item := range b {
		oldPaths[item.RequestPath] = i
		if item.State == "observed" {
			hash := comparisonHash(item)
			oldHashes[hash] = append(oldHashes[hash], i)
		}
	}
	for i, item := range a {
		if item.State != "observed" {
			continue
		}
		hash := comparisonHash(item)
		queue := oldHashes[hash]
		if len(queue) > 0 {
			j := queue[0]
			oldHashes[hash] = queue[1:]
			used[j] = true
			matched[i] = true
			out.Items = append(out.Items, RequestComparisonItem{RequestPath: item.RequestPath, ComparePath: b[j].RequestPath, Change: "common"})
		}
	}
	for i, item := range a {
		if matched[i] {
			continue
		}
		change := "added"
		reason := ""
		old := ""
		if j, ok := oldPaths[item.RequestPath]; ok && !used[j] {
			before := b[j]
			used[j] = true
			old = before.RequestPath
			change = "changed"
			if before.State != "observed" {
				change = "unknown"
				reason = "source_unresolved"
			}
		}
		if item.State != "observed" || (change == "added" && unknownRequestPath(previous, item.RequestPath)) {
			change = "unknown"
			reason = "source_unresolved"
		}
		out.Items = append(out.Items, RequestComparisonItem{RequestPath: item.RequestPath, ComparePath: old, Change: change, Reason: reason})
	}
	for j, item := range b {
		if used[j] {
			continue
		}
		change := "removed"
		reason := ""
		if item.State != "observed" || unknownRequestPath(current, item.RequestPath) {
			change = "unknown"
			reason = "source_unresolved"
		}
		out.Items = append(out.Items, RequestComparisonItem{RequestPath: item.RequestPath, ComparePath: item.RequestPath, Change: change, Reason: reason})
	}
	sort.SliceStable(out.Items, func(i, j int) bool { return out.Items[i].RequestPath < out.Items[j].RequestPath })
	return out
}
