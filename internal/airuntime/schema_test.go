package airuntime

import (
	"encoding/json"
	"testing"
)

func TestValidateJSON(t *testing.T) {
	schema := `{"type":"object","additionalProperties":false,"required":["s","items","n"],"properties":{
"s":{"type":"string","maxLength":3,"minLength":1},
"n":{"type":["integer","null"],"minimum":0,"maximum":9},
"kind":{"enum":["a","b"]},
"items":{"type":"array","maxItems":2,"items":{"type":"object","required":["id"],"properties":{"id":{"type":"string"}}}}}}`
	cases := map[string]struct {
		doc string
		ok  bool
	}{
		"valid":            {`{"s":"한글임","items":[{"id":"1"}],"n":null,"kind":"a"}`, true},
		"too long":         {`{"s":"abcd","items":[],"n":1}`, false},
		"too short":        {`{"s":"","items":[],"n":1}`, false},
		"missing required": {`{"s":"a","items":[]}`, false},
		"extra property":   {`{"s":"a","items":[],"n":1,"x":true}`, false},
		"too many items":   {`{"s":"a","items":[{"id":"1"},{"id":"2"},{"id":"3"}],"n":1}`, false},
		"nested type":      {`{"s":"a","items":[{"id":1}],"n":1}`, false},
		"not integer":      {`{"s":"a","items":[],"n":1.5}`, false},
		"above maximum":    {`{"s":"a","items":[],"n":10}`, false},
		"enum":             {`{"s":"a","items":[],"n":1,"kind":"c"}`, false},
		"trailing data":    {`{"s":"a","items":[],"n":1} {}`, false},
		"not json":         {`report`, false},
	}
	for name, tc := range cases {
		err := ValidateJSON(json.RawMessage(schema), tc.doc)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, tc.ok)
		}
	}
}
