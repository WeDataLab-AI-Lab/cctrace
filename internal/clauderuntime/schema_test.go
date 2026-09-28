package clauderuntime

import (
	"encoding/json"
	"strings"
	"testing"
)

const reportSchema = `{"type":"object","additionalProperties":false,"required":["summary","items"],"properties":{
"summary":{"type":"string","maxLength":10},
"items":{"type":"array","maxItems":2,"items":{"type":"object","additionalProperties":false,"required":["id"],"properties":{
"id":{"type":"string","enum":["a","b"]},
"n":{"type":"integer","minimum":1}}}}}}`

func TestValidateJSON(t *testing.T) {
	cases := []struct {
		name, doc string
		ok        bool
	}{
		{"valid", `{"summary":"hi","items":[{"id":"a","n":2}]}`, true},
		{"not json", `hi`, false},
		{"trailing data", `{"summary":"hi","items":[]} {}`, false},
		{"missing required", `{"summary":"hi"}`, false},
		{"extra property", `{"summary":"hi","items":[],"x":1}`, false},
		{"wrong type", `{"summary":1,"items":[]}`, false},
		{"maxLength counts runes", `{"summary":"가나다라마바사아자차","items":[]}`, true},
		{"maxLength exceeded", `{"summary":"01234567890","items":[]}`, false},
		{"maxItems exceeded", `{"summary":"x","items":[{"id":"a"},{"id":"a"},{"id":"b"}]}`, false},
		{"enum", `{"summary":"x","items":[{"id":"c"}]}`, false},
		{"integer", `{"summary":"x","items":[{"id":"a","n":1.5}]}`, false},
		{"minimum", `{"summary":"x","items":[{"id":"a","n":0}]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateJSON(json.RawMessage(reportSchema), tc.doc)
			if (err == nil) != tc.ok {
				t.Fatalf("validateJSON ok=%v, err=%v", tc.ok, err)
			}
		})
	}
}

func TestSendableSchemaStripsUnsupportedConstraints(t *testing.T) {
	out, err := sendableSchema(json.RawMessage(reportSchema))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, kw := range []string{"maxLength", "maxItems", "minimum"} {
		if strings.Contains(s, `"`+kw+`"`) {
			t.Errorf("%s still in %s", kw, s)
		}
	}
	for _, kw := range []string{`"enum"`, `"required"`, `"additionalProperties":false`} {
		if !strings.Contains(s, kw) {
			t.Errorf("%s dropped from %s", kw, s)
		}
	}
	// The original must be untouched so validation still sees the limits.
	if !strings.Contains(reportSchema, "maxLength") {
		t.Fatal("original schema mutated")
	}
}

func TestValidateJSONMoreKeywords(t *testing.T) {
	cases := []struct {
		name, schema, doc string
		ok                bool
	}{
		{"integer as 1.0", `{"type":"integer"}`, `1.0`, true},
		{"exclusiveMaximum", `{"type":"number","exclusiveMaximum":3}`, `3`, false},
		{"exclusiveMinimum", `{"type":"number","exclusiveMinimum":3}`, `3`, false},
		{"multipleOf", `{"type":"integer","multipleOf":5}`, `7`, false},
		{"uniqueItems", `{"type":"array","uniqueItems":true}`, `[1,1]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateJSON(json.RawMessage(tc.schema), tc.doc); (err == nil) != tc.ok {
				t.Fatalf("ok=%v err=%v", tc.ok, err)
			}
		})
	}
}

func TestSendableSchemaRejectsWhatValidationCannotFollow(t *testing.T) {
	for _, s := range []string{`{"$ref":"#/$defs/a","$defs":{"a":{"type":"string"}}}`, `{"allOf":[{"type":"string"}]}`, `true`} {
		if _, err := sendableSchema(json.RawMessage(s)); err == nil {
			t.Errorf("%s accepted", s)
		}
	}
}

func TestSendableSchemaKeepsLiteralValues(t *testing.T) {
	out, err := sendableSchema(json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"k":{"const":{"maxLength":1}}}}`))
	if err != nil || !strings.Contains(string(out), `"maxLength":1`) {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

func TestValidateJSONMessageOmitsAnswerKeys(t *testing.T) {
	err := validateJSON(json.RawMessage(reportSchema), `{"summary":"x","items":[],"secret_name":1}`)
	if err == nil || strings.Contains(err.Error(), "secret_name") {
		t.Fatalf("err = %v", err)
	}
}
