package openairuntime

import (
	"encoding/json"
	"testing"
)

func TestStrictCompatible(t *testing.T) {
	cases := map[string]struct {
		schema string
		want   bool
	}{
		"report shape":       {`{"type":"object","additionalProperties":false,"required":["s","items"],"properties":{"s":{"type":"string","maxLength":5},"items":{"type":"array","maxItems":2,"items":{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"string"}}}}}}`, true},
		"optional property":  {`{"type":"object","additionalProperties":false,"required":[],"properties":{"a":{"type":"string"}}}`, false},
		"open object":        {`{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`, false},
		"nested open object": {`{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"object","properties":{}}}}`, false},
		"allOf":              {`{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"allOf":[{"type":"string"}]}}}`, false},
		"root anyOf":         {`{"anyOf":[{"type":"object","additionalProperties":false,"required":[],"properties":{}}]}`, false},
		"root array":         {`{"type":"array","items":{"type":"string"}}`, false},
		"not json":           {`{`, false},
	}
	for name, tc := range cases {
		if got := strictCompatible(json.RawMessage(tc.schema)); got != tc.want {
			t.Errorf("%s: strictCompatible = %v, want %v", name, got, tc.want)
		}
	}
}
