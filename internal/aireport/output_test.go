package aireport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cctrace/internal/store"
)

func testWeek(t *testing.T) Week {
	t.Helper()
	wk, err := ParseISOWeek("2026-W37", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	return wk
}

func segAt(id int64, wk Week) store.AISegment {
	return store.AISegment{ID: id, SessionID: "s", StartTs: wk.Since.Add(time.Hour), Agent: "claude", ProjectName: "cctrace"}
}

func TestValidateOutputKeepsOnlyOwnExistingSegments(t *testing.T) {
	wk := testWeek(t)
	st := NewMemStore()
	st.AddSegment("me", segAt(10, wk))
	st.AddSegment("me", segAt(11, wk))
	st.AddSegment("other", segAt(20, wk))
	me := Scope{DashboardUserID: 1, UserID: "me"}

	final := `{"summary":"이번 주 요약","items":[
		{"segment_id":"10","title":"a","reason":"r"},
		{"segment_id":"20","title":"타인","reason":"r"},
		{"segment_id":"99","title":"없음","reason":"r"},
		{"segment_id":"10","title":"중복","reason":"r"},
		{"segment_id":"11","title":"b","reason":"r"}]}`
	rep, dropped, err := ValidateOutput(context.Background(), st, me, wk, final)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary != "이번 주 요약" {
		t.Fatalf("summary = %q", rep.Summary)
	}
	var ids []string
	for _, it := range rep.Items {
		ids = append(ids, it.SegmentID)
	}
	if strings.Join(ids, ",") != "10,11" {
		t.Fatalf("items = %v", ids)
	}
	if dropped != 3 {
		t.Fatalf("dropped = %d, want 3", dropped)
	}
}

// An answer that fits the schema but carries nothing is the model's failure,
// not a format failure, and the screen has to say so: z-ai/glm-5.3 returned
// {"items":[],"summary":""} after calling tools twice, and reporting that as
// "invalid format" sent the reader after the schema instead of the model.
func TestValidateOutputSeparatesAnEmptyReportFromABadOne(t *testing.T) {
	wk := testWeek(t)
	st := NewMemStore()
	sc := Scope{DashboardUserID: 1, UserID: "me"}

	_, _, err := ValidateOutput(context.Background(), st, sc, wk, `{"summary":"","items":[]}`)
	if !errors.Is(err, ErrEmptyReport) {
		t.Errorf("empty report: err = %v, want ErrEmptyReport", err)
	}
	if errors.Is(err, ErrInvalidOutput) {
		t.Errorf("empty report also reported as invalid output: %v", err)
	}

	long := `{"summary":"` + strings.Repeat("가", 2001) + `","items":[]}`
	if _, _, err := ValidateOutput(context.Background(), st, sc, wk, long); !errors.Is(err, ErrInvalidOutput) {
		t.Errorf("oversized summary: err = %v, want ErrInvalidOutput", err)
	}
}

func TestValidateOutputRejectsInvalidDocuments(t *testing.T) {
	wk := testWeek(t)
	st := NewMemStore()
	sc := Scope{DashboardUserID: 1, UserID: "me"}
	six := `{"summary":"s","items":[` + strings.TrimSuffix(strings.Repeat(`{"segment_id":"1","title":"t","reason":"r"},`, 6), ",") + `]}`
	for name, final := range map[string]string{
		"not json":           `요약입니다`,
		"unknown field":      `{"summary":"s","items":[],"extra":1}`,
		"unknown item field": `{"summary":"s","items":[{"segment_id":"1","title":"t","reason":"r","score":3}]}`,
		"empty summary":      `{"summary":"  ","items":[]}`,
		"long summary":       `{"summary":"` + strings.Repeat("가", 2001) + `","items":[]}`,
		"six items":          six,
		"trailing data":      `{"summary":"s","items":[]} {}`,
		"missing items":      `{"summary":"s"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ValidateOutput(context.Background(), st, sc, wk, final); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestValidateOutputDropsOversizedItems(t *testing.T) {
	wk := testWeek(t)
	st := NewMemStore()
	st.AddSegment("me", segAt(1, wk))
	st.AddSegment("me", segAt(2, wk))
	sc := Scope{DashboardUserID: 1, UserID: "me"}
	final, _ := json.Marshal(map[string]any{"summary": "s", "items": []map[string]string{
		{"segment_id": "1", "title": strings.Repeat("t", 121), "reason": "r"},
		{"segment_id": "2", "title": "t", "reason": strings.Repeat("r", 401)},
		{"segment_id": "2", "title": strings.Repeat("제", 120), "reason": strings.Repeat("유", 400)},
	}})
	rep, dropped, err := ValidateOutput(context.Background(), st, sc, wk, string(final))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 1 || dropped != 2 {
		t.Fatalf("items=%d dropped=%d", len(rep.Items), dropped)
	}
}

func TestValidateOutputEmptyItemsSkipsLookup(t *testing.T) {
	wk := testWeek(t)
	rep, dropped, err := ValidateOutput(context.Background(), nil, Scope{}, wk, `{"summary":"s","items":[]}`)
	if err != nil || len(rep.Items) != 0 || dropped != 0 {
		t.Fatalf("rep=%+v dropped=%d err=%v", rep, dropped, err)
	}
	if rep.Items == nil {
		t.Fatal("items must be an empty slice, not nil, so it stores as []")
	}
}

func TestOutputSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(OutputSchema, &v); err != nil {
		t.Fatal(err)
	}
}
