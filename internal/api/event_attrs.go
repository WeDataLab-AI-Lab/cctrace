package api

import "cctrace/internal/store"

// textBearingEventAttrs names the OTEL attributes that carry what a person typed
// or what a tool moved around. otel_events.attrs is an overflow bag for whatever
// an agent exports, and the event routes are not scoped to the session owner, so
// any signed-in reader who knows a session id would otherwise read another
// person's prompt text -- session ids are printed in the dashboard's Logs table.
//
// Client-side redaction is not a defence here: internal/profile defaults
// RedactUserPrompts to false, so a fresh install collects prompts in the clear.
//
// The names are the union of what the collected data actually carries (`prompt`
// is the only free-text key present across 30 days of production events) and the
// two key sets internal/syncer/redact.go already treats as text-bearing, so an
// agent that exports one of those as an OTEL attribute is covered too.
//
// A deny list can miss a key an agent adds later. The durable contract is an
// allow-list DTO, which /api/open/v1/events already has (openAPIEventDTO carries
// no attrs field at all); the dashboard routes cannot take that shape while the
// Logs and Tools views render the bag for diagnosis.
var textBearingEventAttrs = map[string]bool{
	"prompt":      true,
	"prompt_text": true,
	"user_prompt": true,
	"content":     true,
	"text":        true,
	"thinking":    true,
	"summary":     true,
	"title":       true,
	"arguments":   true,
	"input":       true,
	"output":      true,
	"result":      true,
	"command":     true,
	"description": true,
	"stdout":      true,
	"stderr":      true,
	"message":     true,
}

// stripTextBearingAttrs drops those attributes from events on their way out.
// Accounting keys -- tokens, cost, duration, model, the *_size_bytes and
// *_name companions -- are different names and survive, so the Logs view and
// every aggregate keep working.
func stripTextBearingAttrs(events []*store.OtelEvent) []*store.OtelEvent {
	for _, event := range events {
		if event == nil {
			continue
		}
		for key := range event.Attrs {
			if textBearingEventAttrs[key] {
				delete(event.Attrs, key)
			}
		}
	}
	return events
}

// stripToolFailureAttrs is the same defence on the other route that hands out a
// raw attrs bag. /api/tools/detail returns store.ToolFailure, whose Attrs is the
// same overflow bag from the same events -- so the keys the list above names are
// reachable there too, and that route is no more scoped to the session owner
// than the events route is.
//
// Measured on thirty days of production tool failures, the bag carries only
// accounting-shaped keys today (error_type, tool_input_size_bytes,
// decision_type, tool_use_id). Nothing leaks right now; the list exists because
// "what an agent exports tomorrow" is not something this code controls, which is
// the same reason the events route has one.
//
// The bag is not dropped wholesale: the Tools view expands it to show why a call
// failed, and the accounting keys are what makes that useful.
func stripToolFailureAttrs(failures []*store.ToolFailure) []*store.ToolFailure {
	for _, failure := range failures {
		if failure == nil {
			continue
		}
		for key := range failure.Attrs {
			if textBearingEventAttrs[key] {
				delete(failure.Attrs, key)
			}
		}
	}
	return failures
}
