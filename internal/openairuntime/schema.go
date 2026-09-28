package openairuntime

import "encoding/json"

// strictUnsupported are the keywords Structured Outputs does not take at all
// (guides/structured-outputs, "Some type-specific keywords are not yet
// supported"; checked 2026-09-15). The same section lists minLength, maxLength,
// pattern, format, minimum, maximum, multipleOf, minItems and maxItems as
// unsupported only for fine-tuned models, so a schema using them stays strict.
var strictUnsupported = []string{"allOf", "not", "dependentRequired", "dependentSchemas", "if", "then", "else"}

// strictCompatible reports whether schema fits the documented strict subset:
// an object root without anyOf, every object closed with
// additionalProperties:false and listing all its properties as required, and
// none of strictUnsupported. A strict request with a schema outside it is
// rejected, so such a schema is sent with strict:false and checked by
// airuntime.ValidateJSON alone.
func strictCompatible(schema json.RawMessage) bool {
	var root map[string]any
	if json.Unmarshal(schema, &root) != nil || root["type"] != "object" {
		return false
	}
	if _, ok := root["anyOf"]; ok {
		return false
	}
	return strictNode(root)
}

func strictNode(n map[string]any) bool {
	for _, k := range strictUnsupported {
		if _, ok := n[k]; ok {
			return false
		}
	}
	props, hasProps := n["properties"].(map[string]any)
	if hasProps || n["type"] == "object" {
		if n["additionalProperties"] != false {
			return false
		}
		required := map[string]bool{}
		list, _ := n["required"].([]any)
		for _, name := range list {
			if s, ok := name.(string); ok {
				required[s] = true
			}
		}
		for name, p := range props {
			sub, ok := p.(map[string]any)
			if !ok || !required[name] || !strictNode(sub) {
				return false
			}
		}
	}
	if sub, ok := n["items"].(map[string]any); ok && !strictNode(sub) {
		return false
	}
	subs, _ := n["anyOf"].([]any)
	for _, key := range []string{"$defs", "definitions"} {
		defs, _ := n[key].(map[string]any)
		for _, d := range defs {
			subs = append(subs, d)
		}
	}
	for _, s := range subs {
		if sub, ok := s.(map[string]any); !ok || !strictNode(sub) {
			return false
		}
	}
	return true
}
