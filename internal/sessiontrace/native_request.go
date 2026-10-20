package sessiontrace

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ReferenceLookup returns an authorized native request, message or shape. A nil
// object is missing; storage failures remain errors. No timestamp matching occurs.
type ReferenceLookup func(kind, key string) (map[string]any, string, error)

const nativeRequestBodyByteLimit = 64 << 20

func ResolveNativeRequest(recipe map[string]any, lookup ReferenceLookup) (NativeRequest, error) {
	return resolveNativeRequest(recipe, lookup, nativeRequestBodyByteLimit)
}

// ResolveNativeRequestBudget shares the reconstruction allowance across explicit comparisons.
func ResolveNativeRequestBudget(recipe map[string]any, lookup ReferenceLookup, byteLimit int) (NativeRequest, error) {
	if byteLimit < 0 || byteLimit > nativeRequestBodyByteLimit {
		byteLimit = nativeRequestBodyByteLimit
	}
	return resolveNativeRequest(recipe, lookup, byteLimit)
}

func resolveNativeRequest(recipe map[string]any, lookup ReferenceLookup, byteLimit int) (NativeRequest, error) {
	out := NativeRequest{State: "observed", MissingRefs: []string{}, SourceRefs: []string{}, Digest: DigestCheck{State: "unavailable"}}
	missing := func(key string) { out.MissingRefs = append(out.MissingRefs, key); out.State = "partial" }
	// Charge before copying source values. Object delimiters are conservatively
	// charged again so the final body, including inserted cache controls, fits.
	remaining, limited := byteLimit-len(`{"messages":[]}`), false
	reserve := func(value any) bool {
		if limited {
			return false
		}
		encoded, err := CanonicalJSON(value)
		if err != nil || len(encoded)+1 > remaining {
			limited = true
			missing("request:body_byte_limit")
			return false
		}
		remaining -= len(encoded) + 1
		return true
	}
	hidden, withheld := false, nativeFieldsWithheld(recipe)
	seenSources := map[string]bool{}
	lastSource := ""
	read := func(kind, key string) (map[string]any, error) {
		value, source, err := lookup(kind, key)
		lastSource = source
		if err != nil {
			return nil, err
		}
		if source != "" && !seenSources[source] {
			seenSources[source] = true
			out.SourceRefs = append(out.SourceRefs, source)
		}
		if value == nil {
			missing(kind + ":" + key)
		}
		if containsHidden(value) {
			hidden = true
		}
		if nativeFieldsWithheld(value) {
			withheld = true
		}
		return value, nil
	}
	visiting := map[string]bool{}
	var hashes func(map[string]any, int) ([]string, error)
	hashes = func(r map[string]any, depth int) ([]string, error) {
		if depth > 2048 {
			missing("request:dependency_limit")
			return nil, nil
		}
		var keys []string
		keep, _ := nativeInt(r["keep"])
		if keep < 0 {
			missing("request:invalid_keep")
			keep = 0
		}
		if keep > 0 {
			base, _ := r["base"].(string)
			if base == "" {
				missing("request:base_missing")
			} else if visiting[base] {
				missing("request:cycle")
			} else {
				visiting[base] = true
				previous, err := read("request", base)
				if err != nil {
					return nil, err
				}
				if previous != nil {
					keys, err = hashes(previous, depth+1)
					if err != nil {
						return nil, err
					}
				}
				delete(visiting, base)
			}
			if keep > len(keys) {
				missing("request:keep_out_of_range")
				keep = len(keys)
			}
			keys = append([]string{}, keys[:keep]...)
		}
		if tail, ok := r["tail"].([]any); ok {
			for _, v := range tail {
				if key, ok := v.(string); ok {
					keys = append(keys, key)
				} else {
					missing("request:invalid_tail")
				}
			}
		}
		return keys, nil
	}
	keys, err := hashes(recipe, 0)
	if err != nil {
		return out, err
	}
	if _, ok := recipe["tail"].([]any); !ok {
		missing("request:tail_not_collected")
	}
	params, _ := recipe["params"].(map[string]any)
	body := map[string]any{}
	if params != nil && reserve(params) {
		body = copyValue(params, false).(map[string]any)
		out.Params = copyValue(params, false)
		for name := range params {
			out.Provenance = append(out.Provenance, provenance(name, "", "params."+name, nil, nil, inputState(params[name])))
		}
	}
	messages := []any{}
	for _, key := range keys {
		if limited {
			break
		}
		row, err := read("message", key)
		if err != nil {
			return out, err
		}
		if row == nil {
			if !reserve(nil) {
				break
			}
			messages = append(messages, nil)
			continue
		}
		message := row["message"]
		if message == nil {
			message = row
		}
		// This view contains only fields NormalizeMessage retains, but shares
		// source values until the budget admits the independent message copy.
		if m, ok := message.(map[string]any); ok {
			content := m["content"]
			if text, ok := content.(string); ok {
				content = []any{map[string]any{"type": "text", "text": text}}
			}
			view := map[string]any{"role": m["role"], "content": content}
			if m["role"] == "system" {
				if config, ok := m["output_config"]; ok {
					view["output_config"] = config
				}
			}
			if !reserve(view) {
				break
			}
		} else if !reserve(nil) {
			break
		}
		normal, err := NormalizeMessage(message)
		if err != nil {
			missing("message:" + key + ":unreadable")
			messages = append(messages, nil)
			continue
		}
		index := len(messages)
		blocks, _ := normal["content"].([]any)
		for j, block := range blocks {
			out.Provenance = append(out.Provenance, provenance(fmt.Sprintf("messages[%d].content[%d]", index, j), lastSource, fmt.Sprintf("message.content[%d]", j), ordinal(index), ordinal(j), inputState(block)))
		}
		messages = append(messages, normal)
	}
	body["messages"] = messages
	if shapeKey, ok := recipe["shapeHash"].(string); ok && shapeKey != "" && !limited {
		row, err := read("shape", shapeKey)
		if err != nil {
			return out, err
		}
		if row != nil {
			shape, _ := row["shape"].(map[string]any)
			if shape == nil {
				missing("shape:" + shapeKey + ":unreadable")
			} else {
				for _, key := range []string{"system", "tools"} {
					if v, ok := shape[key]; ok {
						if !reserve(map[string]any{key: v}) {
							break
						}
						body[key] = copyValue(v, false)
						if blocks, ok := v.([]any); ok {
							for j, block := range blocks {
								out.Provenance = append(out.Provenance, provenance(fmt.Sprintf("%s[%d]", key, j), lastSource, fmt.Sprintf("shape.%s[%d]", key, j), nil, ordinal(j), inputState(block)))
							}
						} else {
							out.Provenance = append(out.Provenance, provenance(key, lastSource, "shape."+key, nil, nil, inputState(v)))
						}
					}
				}
			}
		}
	}
	if controls, ok := recipe["cacheControl"].([]any); ok {
		for i, item := range controls {
			if limited {
				break
			}
			control, ok := item.(map[string]any)
			if !ok {
				missing("cache_control:unreadable")
				continue
			}
			if !reserve(map[string]any{"cache_control": control["value"]}) {
				break
			}
			path, ok := control["path"].([]any)
			if !ok || !applyCacheControl(body, path, control["value"]) {
				missing("cache_control:invalid_path")
			} else {
				out.Provenance = append(out.Provenance, provenance(pathParts(path)+".cache_control", "", fmt.Sprintf("cacheControl[%d].value", i), nil, nil, inputState(control["value"])))
			}
		}
	}
	out.Body = body
	if hidden || containsHidden(body) {
		out.State = "redacted"
	} else if withheld {
		out.State = "withheld"
	}
	if cut, ok := recipe["cut"].(bool); ok && cut {
		out.State = "truncated"
	}
	if limited {
		out.State = "truncated"
	}
	if expected, ok := recipe["requestDigest"].(string); ok && expected != "" {
		out.Digest.Expected = expected
		if out.State == "observed" {
			checked := copyValue(body, false).(map[string]any)
			for _, key := range []string{"metadata", "stream", "thread", "diagnostics"} {
				delete(checked, key)
			}
			out.Digest.Actual, err = CanonicalHash(checked)
			if err != nil {
				return out, err
			}
			out.Digest.State = "mismatch"
			if out.Digest.Actual == expected {
				out.Digest.State = "matched"
			}
		}
	}
	return out, nil
}

func applyCacheControl(body map[string]any, path []any, value any) bool {
	var target any = body
	for _, part := range path {
		switch v := target.(type) {
		case map[string]any:
			key, ok := part.(string)
			if !ok {
				return false
			}
			target = v[key]
		case []any:
			index, ok := nativeInt(part)
			if !ok || index < 0 || index >= len(v) {
				return false
			}
			target = v[index]
		default:
			return false
		}
	}
	object, ok := target.(map[string]any)
	if !ok {
		return false
	}
	object["cache_control"] = copyValue(value, false)
	return true
}

func nativeInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case json.Number:
		n, e := strconv.Atoi(string(v))
		return n, e == nil
	case float64:
		n := int(v)
		return n, float64(n) == v
	default:
		return 0, false
	}
}

func containsHidden(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(v, "[redacted by cctrace client]") || strings.Contains(v, "[withheld by cctrace")
	case map[string]any:
		for _, child := range v {
			if containsHidden(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if containsHidden(child) {
				return true
			}
		}
	}
	return false
}

func nativeFieldsWithheld(object map[string]any) bool {
	if object == nil {
		return false
	}
	fields, _ := object["cctraceWithheld"].([]any)
	for _, field := range fields {
		path, ok := field.(string)
		if !ok {
			continue
		}
		var current any = object
		for _, part := range strings.Split(path, ".") {
			parent, ok := current.(map[string]any)
			if !ok {
				current = nil
				break
			}
			current = parent[part]
		}
		if current == nil {
			return true
		}
	}
	return false
}
