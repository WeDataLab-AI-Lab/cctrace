package aireport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"cctrace/internal/store"
)

const (
	maxSummaryRunes = 2000
	maxItems        = 5
	maxTitleRunes   = 120
	maxReasonRunes  = 400
)

// ErrInvalidOutput marks a final answer that is not a usable report. The run
// fails and the previous report stays.
var ErrInvalidOutput = errors.New("invalid report output")

// ErrEmptyReport marks an answer that fits the schema but carries nothing: no
// summary and no items. The run failed on the model's content, not its format,
// and the two need different words on screen.
var ErrEmptyReport = errors.New("empty report output")

// OutputSchema is sent as the turn's outputSchema. ValidateOutput re-checks it,
// because the runtime's enforcement is not verified.
var OutputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary","items"],"properties":{
"summary":{"type":"string","maxLength":2000},
"items":{"type":"array","maxItems":5,"items":{"type":"object","additionalProperties":false,"required":["segment_id","title","reason"],"properties":{
"segment_id":{"type":"string"},
"title":{"type":"string","maxLength":120},
"reason":{"type":"string","maxLength":400}}}}}}`)

// ValidatedReport is a report whose items all name the caller's segments in the week.
type ValidatedReport struct {
	Summary string
	Items   []store.AIReportItem
}

type outputDoc struct {
	Summary *string               `json:"summary"`
	Items   *[]store.AIReportItem `json:"items"`
}

// ValidateOutput parses the final text strictly. Document-level problems (not
// JSON, unknown fields, bad summary, more than five items) are ErrInvalidOutput.
// Item-level problems -- a bad or duplicate id, an empty or oversized title or
// reason, a segment that is not the caller's in this week -- drop the item and
// count it.
func ValidateOutput(ctx context.Context, st Store, sc Scope, wk Week, final string) (ValidatedReport, int, error) {
	dec := json.NewDecoder(strings.NewReader(final))
	dec.DisallowUnknownFields()
	var doc outputDoc
	if err := dec.Decode(&doc); err != nil {
		return ValidatedReport{}, 0, fmt.Errorf("%w: %v", ErrInvalidOutput, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return ValidatedReport{}, 0, fmt.Errorf("%w: trailing data", ErrInvalidOutput)
	}
	if doc.Summary == nil || doc.Items == nil {
		return ValidatedReport{}, 0, fmt.Errorf("%w: summary and items are required", ErrInvalidOutput)
	}
	summary := strings.TrimSpace(*doc.Summary)
	// An empty summary with no items is a different failure from a malformed
	// one: the shape was right and the model simply wrote nothing. Reported as
	// "invalid format" it sends the reader after the schema, when the thing to
	// question is the model -- z-ai/glm-5.3 returned {"items":[],"summary":""}
	// after calling tools twice.
	if summary == "" && len(*doc.Items) == 0 {
		return ValidatedReport{}, 0, fmt.Errorf("%w: model returned an empty report", ErrEmptyReport)
	}
	if summary == "" || utf8.RuneCountInString(summary) > maxSummaryRunes {
		return ValidatedReport{}, 0, fmt.Errorf("%w: summary must be 1..%d characters", ErrInvalidOutput, maxSummaryRunes)
	}
	items := *doc.Items
	if len(items) > maxItems {
		return ValidatedReport{}, 0, fmt.Errorf("%w: %d items, at most %d", ErrInvalidOutput, len(items), maxItems)
	}

	type candidate struct {
		id   int64
		item store.AIReportItem
	}
	var candidates []candidate
	seen := map[int64]bool{}
	for _, it := range items {
		id, err := strconv.ParseInt(strings.TrimSpace(it.SegmentID), 10, 64)
		it.Title, it.Reason = strings.TrimSpace(it.Title), strings.TrimSpace(it.Reason)
		if err != nil || id <= 0 || seen[id] ||
			it.Title == "" || utf8.RuneCountInString(it.Title) > maxTitleRunes ||
			it.Reason == "" || utf8.RuneCountInString(it.Reason) > maxReasonRunes {
			continue
		}
		seen[id] = true
		it.SegmentID = strconv.FormatInt(id, 10)
		candidates = append(candidates, candidate{id, it})
	}

	kept := []store.AIReportItem{}
	if len(candidates) > 0 {
		ids := make([]int64, len(candidates))
		for i, c := range candidates {
			ids[i] = c.id
		}
		found, err := st.AISegmentsByIDs(ctx, segmentScope(sc, wk), ids)
		if err != nil {
			return ValidatedReport{}, 0, fmt.Errorf("validate report items: %w", err)
		}
		for _, c := range candidates {
			if _, ok := found[c.id]; ok {
				kept = append(kept, c.item)
			}
		}
	}
	return ValidatedReport{Summary: summary, Items: kept}, len(items) - len(kept), nil
}
