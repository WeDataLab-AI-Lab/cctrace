package sessiontrace

import (
	"encoding/json"
	"math/big"
)

// MergeResponseUsage folds repeated snapshots of one observed response. Each
// numeric field takes MAX, including nested detail fields; no fields are added.
func MergeResponseUsage(values []any) map[string]any {
	out := map[string]any{}
	for _, value := range values {
		object, _ := value.(map[string]any)
		for key, v := range object {
			if child, ok := v.(map[string]any); ok {
				previous, _ := out[key].(map[string]any)
				out[key] = MergeResponseUsage([]any{previous, child})
				continue
			}
			if number, ok := v.(json.Number); ok {
				current, exists := out[key].(json.Number)
				a, aok := new(big.Rat).SetString(string(number))
				b, bok := new(big.Rat).SetString(string(current))
				if !exists || !bok || (aok && a.Cmp(b) > 0) {
					out[key] = number
				}
				continue
			}
			if _, exists := out[key]; !exists {
				out[key] = copyValue(v, false)
			}
		}
	}
	return out
}
