package clauderuntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"
)

// unsupportedKeywords are constraints structured outputs does not compile.
// The official SDKs strip them before sending and validate the answer against
// the original schema afterwards (structured-outputs docs, "How SDK
// transformation works"); sendableSchema and validateJSON do the same, so
// validateValue checks every keyword listed here.
var unsupportedKeywords = []string{
	"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
	"minLength", "maxLength", "minItems", "maxItems", "uniqueItems",
}

// literalKeywords hold values, not schemas, so nothing inside them is stripped.
var literalKeywords = map[string]bool{"const": true, "enum": true, "default": true, "examples": true}

// unfollowedKeywords are valid for the API but validateValue does not follow
// them, so a constraint stripped beneath one would go unchecked.
var unfollowedKeywords = []string{"$ref", "allOf", "$defs", "definitions", "patternProperties", "not", "oneOf", "if"}

var errSchemaUnsupported = errors.New("output schema uses a keyword the runtime cannot validate")

// sendableSchema returns a copy of schema without unsupportedKeywords. It
// rejects a schema that is not an object or uses unfollowedKeywords.
func sendableSchema(schema json.RawMessage) (json.RawMessage, error) {
	var doc any
	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil, fmt.Errorf("output schema: %w", err)
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, fmt.Errorf("output schema must be a JSON object")
	}
	stripped, err := stripKeywords(doc)
	if err != nil {
		return nil, err
	}
	return json.Marshal(stripped)
}

// stripKeywords walks v as a schema: an object's keys are keywords, except
// under "properties", whose keys are property names and whose values are schemas.
func stripKeywords(v any) (any, error) {
	switch n := v.(type) {
	case map[string]any:
		for _, kw := range unfollowedKeywords {
			if _, ok := n[kw]; ok {
				return nil, fmt.Errorf("%w: %s", errSchemaUnsupported, kw)
			}
		}
		out := make(map[string]any, len(n))
		for k, child := range n {
			switch {
			case literalKeywords[k]:
				out[k] = child
			case k == "properties":
				props, _ := child.(map[string]any)
				kept := make(map[string]any, len(props))
				for name, p := range props {
					s, err := stripKeywords(p)
					if err != nil {
						return nil, err
					}
					kept[name] = s
				}
				out[k] = kept
			default:
				s, err := stripKeywords(child)
				if err != nil {
					return nil, err
				}
				out[k] = s
			}
		}
		for _, kw := range unsupportedKeywords {
			delete(out, kw)
		}
		return out, nil
	case []any:
		out := make([]any, len(n))
		for i, child := range n {
			s, err := stripKeywords(child)
			if err != nil {
				return nil, err
			}
			out[i] = s
		}
		return out, nil
	default:
		return v, nil
	}
}

// validateJSON checks doc against schema. It covers the subset sendableSchema
// accepts: type, properties, required, additionalProperties:false, items, enum,
// const, anyOf and unsupportedKeywords. Messages name the path and rule, never
// a value or key the answer made up.
func validateJSON(schema json.RawMessage, doc string) error {
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("output schema: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("not JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing data after JSON")
	}
	return validateValue(s, v, "$")
}

func validateValue(s map[string]any, v any, path string) error {
	if err := checkType(s["type"], v, path); err != nil {
		return err
	}
	if enum, ok := s["enum"].([]any); ok && !containsJSON(enum, v) {
		return fmt.Errorf("%s: not one of the allowed values", path)
	}
	if c, ok := s["const"]; ok && !containsJSON([]any{c}, v) {
		return fmt.Errorf("%s: not the constant value", path)
	}
	if anyOf, ok := s["anyOf"].([]any); ok {
		matched := false
		for _, alt := range anyOf {
			if m, ok := alt.(map[string]any); ok && validateValue(m, v, path) == nil {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: matches no anyOf alternative", path)
		}
	}
	switch x := v.(type) {
	case string:
		n := float64(utf8.RuneCountInString(x))
		if max, ok := number(s["maxLength"]); ok && n > max {
			return fmt.Errorf("%s: longer than %v characters", path, max)
		}
		if min, ok := number(s["minLength"]); ok && n < min {
			return fmt.Errorf("%s: shorter than %v characters", path, min)
		}
	case json.Number:
		f, _ := x.Float64()
		if max, ok := number(s["maximum"]); ok && f > max {
			return fmt.Errorf("%s: above maximum %v", path, max)
		}
		if min, ok := number(s["minimum"]); ok && f < min {
			return fmt.Errorf("%s: below minimum %v", path, min)
		}
		if max, ok := number(s["exclusiveMaximum"]); ok && f >= max {
			return fmt.Errorf("%s: not below %v", path, max)
		}
		if min, ok := number(s["exclusiveMinimum"]); ok && f <= min {
			return fmt.Errorf("%s: not above %v", path, min)
		}
		if m, ok := number(s["multipleOf"]); ok && m > 0 {
			q := f / m
			if math.Abs(q-math.Round(q)) > 1e-9 {
				return fmt.Errorf("%s: not a multiple of %v", path, m)
			}
		}
	case []any:
		n := float64(len(x))
		if max, ok := number(s["maxItems"]); ok && n > max {
			return fmt.Errorf("%s: more than %v items", path, max)
		}
		if min, ok := number(s["minItems"]); ok && n < min {
			return fmt.Errorf("%s: fewer than %v items", path, min)
		}
		if unique, _ := s["uniqueItems"].(bool); unique {
			seen := map[string]bool{}
			for _, el := range x {
				b, _ := json.Marshal(normalize(el))
				if seen[string(b)] {
					return fmt.Errorf("%s: items are not unique", path)
				}
				seen[string(b)] = true
			}
		}
		if items, ok := s["items"].(map[string]any); ok {
			for i, el := range x {
				if err := validateValue(items, el, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if name, _ := r.(string); name != "" {
					if _, present := x[name]; !present {
						return fmt.Errorf("%s: missing required %q", path, name)
					}
				}
			}
		}
		for name, el := range x {
			ps, known := props[name].(map[string]any)
			if !known {
				if ap, ok := s["additionalProperties"].(bool); ok && !ap {
					return fmt.Errorf("%s: unexpected property", path)
				}
				continue
			}
			if err := validateValue(ps, el, path+"."+name); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkType(t any, v any, path string) error {
	var names []string
	switch tt := t.(type) {
	case nil:
		return nil
	case string:
		names = []string{tt}
	case []any:
		for _, n := range tt {
			if s, ok := n.(string); ok {
				names = append(names, s)
			}
		}
	}
	for _, name := range names {
		if typeMatches(name, v) {
			return nil
		}
	}
	return fmt.Errorf("%s: want type %s", path, strings.Join(names, "|"))
}

func typeMatches(name string, v any) bool {
	switch name {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		// JSON Schema counts 1.0 and 1e2 as integers.
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		f, err := n.Float64()
		return err == nil && math.Trunc(f) == f
	}
	return false
}

func number(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

func containsJSON(list []any, v any) bool {
	want, _ := json.Marshal(normalize(v))
	for _, el := range list {
		got, _ := json.Marshal(normalize(el))
		if string(got) == string(want) {
			return true
		}
	}
	return false
}

// normalize turns json.Number into float64 so 1 and 1.0 compare equal.
func normalize(v any) any {
	if n, ok := v.(json.Number); ok {
		f, _ := n.Float64()
		return f
	}
	return v
}
