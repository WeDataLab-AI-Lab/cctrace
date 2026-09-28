package airuntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"
)

// ValidateJSON checks that doc is exactly one JSON value matching schema. It
// covers the keywords a report schema uses (type, enum, anyOf, properties,
// required, additionalProperties, items, min/maxItems, min/maxLength,
// minimum/maximum) and ignores the rest. Errors name the path, not the value.
//
// It lives here because a runtime that skips it fails silently: a provider's
// own enforcement is not verified, and chatruntime once shipped a validateJSON
// that parsed both sides and compared neither, so schema violations on the
// NVIDIA and LiteLLM paths could not be detected at all.
func ValidateJSON(schema json.RawMessage, doc string) error {
	var s any
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("output schema: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return errors.New("not a JSON value")
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after the JSON value")
	}
	return validateValue(s, v, "$")
}

func validateValue(schema, v any, path string) error {
	n, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	if t, ok := n["type"]; ok && !typeMatches(t, v) {
		return fmt.Errorf("%s: wrong type", path)
	}
	if enum, ok := n["enum"].([]any); ok && !inEnum(enum, v) {
		return fmt.Errorf("%s: not an allowed value", path)
	}
	if alts, ok := n["anyOf"].([]any); ok {
		matched := false
		for _, alt := range alts {
			if validateValue(alt, v, path) == nil {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: matches no anyOf branch", path)
		}
	}
	switch x := v.(type) {
	case string:
		l := float64(utf8.RuneCountInString(x))
		if max, ok := n["maxLength"].(float64); ok && l > max {
			return fmt.Errorf("%s: longer than %v", path, max)
		}
		if min, ok := n["minLength"].(float64); ok && l < min {
			return fmt.Errorf("%s: shorter than %v", path, min)
		}
	case json.Number:
		f, _ := x.Float64()
		if max, ok := n["maximum"].(float64); ok && f > max {
			return fmt.Errorf("%s: above %v", path, max)
		}
		if min, ok := n["minimum"].(float64); ok && f < min {
			return fmt.Errorf("%s: below %v", path, min)
		}
	case []any:
		if max, ok := n["maxItems"].(float64); ok && float64(len(x)) > max {
			return fmt.Errorf("%s: more than %v items", path, max)
		}
		if min, ok := n["minItems"].(float64); ok && float64(len(x)) < min {
			return fmt.Errorf("%s: fewer than %v items", path, min)
		}
		for i, item := range x {
			if err := validateValue(n["items"], item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case map[string]any:
		required, _ := n["required"].([]any)
		for _, name := range required {
			if s, ok := name.(string); ok {
				if _, present := x[s]; !present {
					return fmt.Errorf("%s: missing %s", path, s)
				}
			}
		}
		props, _ := n["properties"].(map[string]any)
		for k, val := range x {
			sub, known := props[k]
			if !known {
				if n["additionalProperties"] == false {
					return fmt.Errorf("%s: unexpected property %s", path, k)
				}
				sub = n["additionalProperties"]
			}
			if err := validateValue(sub, val, path+"."+k); err != nil {
				return err
			}
		}
	}
	return nil
}

func typeMatches(t, v any) bool {
	if list, ok := t.([]any); ok {
		for _, one := range list {
			if typeMatches(one, v) {
				return true
			}
		}
		return false
	}
	switch t {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		num, ok := v.(json.Number)
		if !ok {
			return false
		}
		f, err := num.Float64()
		return err == nil && f == math.Trunc(f)
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "null":
		return v == nil
	}
	return true
}

// inEnum compares by encoding, so a json.Number from the answer equals the
// float64 the schema decoded to.
func inEnum(enum []any, v any) bool {
	got, _ := json.Marshal(v)
	for _, e := range enum {
		if want, _ := json.Marshal(e); string(want) == string(got) {
			return true
		}
	}
	return false
}
