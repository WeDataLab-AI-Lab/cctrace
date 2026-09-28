package syncer

import (
	"encoding/json"

	"cctrace/internal/store"
)

// RedactPolicy says which parts of a session record must not leave the machine.
// The zero value collects everything, which is what cctrace is for.
type RedactPolicy struct {
	UserPrompts bool
	ToolDetails bool
}

// Enabled reports whether the policy removes anything at all.
func (p RedactPolicy) Enabled() bool { return p.UserPrompts || p.ToolDetails }

// redactedMarker replaces text rather than deleting the key. A missing key is
// indistinguishable from a record that never had one; a marker says a person
// chose this, which is the difference between reading a dashboard as "nothing
// was said here" and "this was withheld".
const redactedMarker = "[redacted by cctrace client]"

// promptBearingKeys are the fields that carry what a person typed. They are
// listed by name rather than matched by shape because the four agents write four
// different record layouts, and a walker that tried to understand each would have
// to be updated for the fifth.
//
// Nothing here overlaps the keys the server reads for accounting -- usage,
// input_tokens, cost, model, ts all survive, so redacting does not distort a
// single number on the dashboard. That separation is the reason this can be a
// blunt key-name walk instead of a per-agent parser.
var promptBearingKeys = map[string]bool{
	"content":  true,
	"text":     true,
	"thinking": true,
	"summary":  true,
	"title":    true,
}

// toolBearingKeys carry tool arguments and results: the file paths, commands and
// outputs a tool call moved around. Separate from prompts because the two are
// separate decisions -- an operator may want the conversation and not the shell
// commands, or the reverse.
var toolBearingKeys = map[string]bool{
	"arguments":   true,
	"input":       true,
	"output":      true,
	"result":      true,
	"command":     true,
	"description": true,
	"stdout":      true,
	"stderr":      true,
}

// Redact removes the configured content from a batch, in place, before it is
// serialised for upload.
//
// It runs here -- at the one point every agent's records pass through on their
// way out -- and not in each syncer. A privacy control implemented four times is
// a control with four chances to be missed, and this repository has already been
// bitten by exactly that: a config key added to the syncers and the CLI but not
// to `cctrace status`, and another added to three enumerations but not the
// fourth. One chokepoint or none.
func Redact(records []*store.SessionRecord, policy RedactPolicy) {
	if !policy.Enabled() {
		return
	}
	for _, r := range records {
		if r == nil {
			continue
		}
		redactRecord(r, policy)
	}
}

func redactRecord(r *store.SessionRecord, policy RedactPolicy) {
	// Raw is the whole reason this exists. Every agent uploads the original JSONL
	// line unconditionally, and the dashboard reads the conversation out of it --
	// so blanking a typed field while leaving Raw intact would remove the text from
	// exactly one place and publish it from another. Whatever else changes here,
	// Raw has to be walked.
	if len(r.Raw) == 0 {
		return
	}
	var doc interface{}
	if err := json.Unmarshal(r.Raw, &doc); err != nil {
		// An unparseable Raw cannot be walked, and shipping it unread would defeat
		// the setting. Withhold the whole payload: losing a record we could not
		// inspect is the safe direction when the operator asked for redaction.
		r.Raw = json.RawMessage(`{"redacted":"` + redactedMarker + `"}`)
		return
	}
	redactValue(doc, policy)
	if cleaned, err := json.Marshal(doc); err == nil {
		r.Raw = cleaned
	} else {
		r.Raw = json.RawMessage(`{"redacted":"` + redactedMarker + `"}`)
	}
}

// redactValue walks the decoded document, replacing the values of content-bearing
// keys wherever they appear. It recurses into everything because these keys sit at
// different depths per agent: Claude nests them under message.content[], gjc under
// message with an attribution sibling, Codex deeper still.
func redactValue(v interface{}, policy RedactPolicy) {
	switch node := v.(type) {
	case map[string]interface{}:
		for key, child := range node {
			if (policy.UserPrompts && promptBearingKeys[key]) ||
				(policy.ToolDetails && toolBearingKeys[key]) {
				node[key] = redactedMarker
				continue
			}
			redactValue(child, policy)
		}
	case []interface{}:
		for _, child := range node {
			redactValue(child, policy)
		}
	}
}
