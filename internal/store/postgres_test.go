package store

import (
	"encoding/json"
	"testing"
	"time"
)

// --- buildEventWhere tests ---

func TestBuildEventWhere_Empty(t *testing.T) {
	where, args := buildEventWhere(EventFilter{})
	if where != "" {
		t.Errorf("expected empty string, got %q", where)
	}
	if args != nil {
		t.Errorf("expected nil args, got %v", args)
	}
}

func TestBuildEventWhere_SessionID(t *testing.T) {
	where, args := buildEventWhere(EventFilter{SessionID: "ses-1"})
	expected := "WHERE session_id = $1"
	if where != expected {
		t.Errorf("expected %q, got %q", expected, where)
	}
	if len(args) != 1 || args[0] != "ses-1" {
		t.Errorf("expected [ses-1], got %v", args)
	}
}

func TestBuildEventWhere_MultipleFilters(t *testing.T) {
	where, args := buildEventWhere(EventFilter{
		SessionID:    "ses-1",
		ProfileEmail: "a@b.com",
		EventName:    "api_request",
	})
	expected := "WHERE session_id = $1 AND profile_email = $2 AND event_name = $3"
	if where != expected {
		t.Errorf("expected %q, got %q", expected, where)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d", len(args))
	}
	if args[0] != "ses-1" || args[1] != "a@b.com" || args[2] != "api_request" {
		t.Errorf("unexpected args: %v", args)
	}
}

func TestBuildEventWhere_WithSince(t *testing.T) {
	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	where, args := buildEventWhere(EventFilter{Since: &since})
	expected := "WHERE ts >= $1"
	if where != expected {
		t.Errorf("expected %q, got %q", expected, where)
	}
	if len(args) != 1 || args[0] != since {
		t.Errorf("expected [%v], got %v", since, args)
	}
}

func TestBuildEventWhere_WithUntil(t *testing.T) {
	until := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	where, args := buildEventWhere(EventFilter{Until: &until})
	expected := "WHERE ts < $1"
	if where != expected {
		t.Errorf("expected %q, got %q", expected, where)
	}
	if len(args) != 1 || args[0] != until {
		t.Errorf("expected [%v], got %v", until, args)
	}
}

func TestBuildEventWhere_AllFilters(t *testing.T) {
	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	where, args := buildEventWhere(EventFilter{
		SessionID:    "ses-1",
		ProfileEmail: "a@b.com",
		UserTeam:     "eng",
		EventName:    "api_request",
		Since:        &since,
		Until:        &until,
	})
	expected := "WHERE session_id = $1 AND profile_email = $2 AND user_team = $3 AND event_name = $4 AND ts >= $5 AND ts < $6"
	if where != expected {
		t.Errorf("expected %q, got %q", expected, where)
	}
	if len(args) != 6 {
		t.Fatalf("expected 6 args, got %d", len(args))
	}
	if args[0] != "ses-1" || args[1] != "a@b.com" || args[2] != "eng" || args[3] != "api_request" {
		t.Errorf("unexpected string args: %v", args)
	}
	if args[4] != since || args[5] != until {
		t.Errorf("unexpected time args: %v", args)
	}
}

// --- buildMetricWhere tests ---

func TestBuildMetricWhere_Empty(t *testing.T) {
	where, args := buildMetricWhere(MetricFilter{})
	if where != "" {
		t.Errorf("expected empty string, got %q", where)
	}
	if args != nil {
		t.Errorf("expected nil args, got %v", args)
	}
}

func TestBuildMetricWhere_MetricName(t *testing.T) {
	where, args := buildMetricWhere(MetricFilter{MetricName: "token_count"})
	expected := "WHERE metric_name = $1"
	if where != expected {
		t.Errorf("expected %q, got %q", expected, where)
	}
	if len(args) != 1 || args[0] != "token_count" {
		t.Errorf("expected [token_count], got %v", args)
	}
}

// The model column is populated on ingest and is the axis dashboards slice by,
// so it must be filterable server-side rather than after fetching every row.
//
// Partial match, matching EventFilter.Model and the /api/stats/* endpoints: the
// dashboard's model dropdown persists values with the "claude-" prefix stripped
// ("sonnet-4-6"), and an exact comparison against the stored "claude-sonnet-4-6"
// would return zero rows without erroring.
func TestBuildMetricWhere_Model(t *testing.T) {
	where, args := buildMetricWhere(MetricFilter{MetricName: "codex.turn.token_usage", Model: "gpt-5-codex"})
	expected := `WHERE metric_name = $1 AND model LIKE $2 ESCAPE '\'`
	if where != expected {
		t.Errorf("expected %q, got %q", expected, where)
	}
	if len(args) != 2 || args[1] != "%gpt-5-codex%" {
		t.Errorf("expected [codex.turn.token_usage %%gpt-5-codex%%], got %v", args)
	}
}

// A stored "claude-opus-5[1m]" must be reachable from the dropdown value
// "opus-5[1m]", so LIKE wildcards in user input are escaped rather than
// interpreted: "_" must match a literal underscore, not any character.
func TestBuildMetricWhere_ModelEscapesWildcards(t *testing.T) {
	_, args := buildMetricWhere(MetricFilter{Model: "a_b%c"})
	if len(args) != 1 || args[0] != `%a\_b\%c%` {
		t.Errorf("wildcards not escaped, got %v", args)
	}
}

// The placeholder counter is shared with the time-range conditions, so a filter
// combining both must keep numbers aligned with the argument order.
func TestBuildMetricWhere_ModelWithTimeRange(t *testing.T) {
	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	where, args := buildMetricWhere(MetricFilter{Model: "opus", Since: &since, Until: &until})
	expected := `WHERE model LIKE $1 ESCAPE '\' AND ts >= $2 AND ts < $3`
	if where != expected {
		t.Errorf("expected %q, got %q", expected, where)
	}
	if len(args) != 3 || args[0] != "%opus%" || args[1] != since || args[2] != until {
		t.Errorf("args misaligned: %v", args)
	}
}

func TestBuildMetricWhere_WithTimeRange(t *testing.T) {
	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	where, args := buildMetricWhere(MetricFilter{Since: &since, Until: &until})
	expected := "WHERE ts >= $1 AND ts < $2"
	if where != expected {
		t.Errorf("expected %q, got %q", expected, where)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d", len(args))
	}
	if args[0] != since || args[1] != until {
		t.Errorf("unexpected args: %v", args)
	}
}

// --- CopySource tests ---

func TestEventCopySource_NextValues(t *testing.T) {
	cost := 0.05
	tokens := 100
	success := true
	events := []*OtelEvent{
		{
			Ts:           time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC),
			EventName:    "api_request",
			SessionID:    "ses-1",
			PromptID:     "p-1",
			UserID:       "u-1",
			ProfileEmail: "a@b.com",
			UserTeam:     "eng",
			OrgID:        "org-1",
			Model:        "claude-3",
			CostUSD:      &cost,
			InputTokens:  &tokens,
			ToolName:     "Read",
			ToolSuccess:  &success,
			Attrs:        map[string]interface{}{"key": "val"},
		},
		{
			Ts:        time.Date(2025, 3, 2, 12, 0, 0, 0, time.UTC),
			EventName: "tool_result",
			SessionID: "ses-2",
		},
	}

	cs := &eventCopySource{events: events}

	for i, e := range events {
		if !cs.Next() {
			t.Fatalf("Next() returned false at index %d", i)
		}
		vals, err := cs.Values()
		if err != nil {
			t.Fatalf("Values() error at index %d: %v", i, err)
		}
		// 25 columns in eventCopySource (added agent, billing_provider)
		if len(vals) != 25 {
			t.Fatalf("expected 25 values, got %d", len(vals))
		}
		// Check first few fields
		if vals[0] != e.Ts {
			t.Errorf("row %d: ts mismatch", i)
		}
		if vals[1] != e.EventName {
			t.Errorf("row %d: event_name mismatch", i)
		}
		if vals[2] != e.SessionID {
			t.Errorf("row %d: session_id mismatch", i)
		}
		// Last value is JSON-encoded attrs
		attrsJSON, ok := vals[22].([]byte)
		if !ok {
			t.Fatalf("row %d: expected []byte for attrs, got %T", i, vals[22])
		}
		var decoded map[string]interface{}
		_ = json.Unmarshal(attrsJSON, &decoded)
		if e.Attrs != nil {
			if decoded["key"] != "val" {
				t.Errorf("row %d: attrs mismatch: %v", i, decoded)
			}
		}
	}

	if cs.Next() {
		t.Error("Next() should return false after all events consumed")
	}
	if cs.Err() != nil {
		t.Errorf("Err() should be nil, got %v", cs.Err())
	}
}

func TestMetricCopySource_NextValues(t *testing.T) {
	vd := 1.5
	vi := int64(42)
	metrics := []*OtelMetric{
		{
			Ts:           time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC),
			MetricName:   "token_count",
			SessionID:    "ses-1",
			ProfileEmail: "a@b.com",
			UserTeam:     "eng",
			Model:        "claude-3",
			ValueDouble:  &vd,
			ValueInt:     &vi,
			Dimensions:   map[string]interface{}{"dim": "x"},
		},
		{
			Ts:         time.Date(2025, 3, 2, 12, 0, 0, 0, time.UTC),
			MetricName: "cost",
			SessionID:  "ses-2",
		},
	}

	cs := &metricCopySource{metrics: metrics}

	for i, m := range metrics {
		if !cs.Next() {
			t.Fatalf("Next() returned false at index %d", i)
		}
		vals, err := cs.Values()
		if err != nil {
			t.Fatalf("Values() error at index %d: %v", i, err)
		}
		// 14 columns in metricCopySource: ts, metric_name, session_id, user_id,
		// profile_email, login_email, user_team, model, value_double, value_int,
		// agent, billing_provider, dimensions, account_id.
		if len(vals) != 14 {
			t.Fatalf("expected 14 values, got %d", len(vals))
		}
		if vals[0] != m.Ts {
			t.Errorf("row %d: ts mismatch", i)
		}
		if vals[1] != m.MetricName {
			t.Errorf("row %d: metric_name mismatch", i)
		}
		if vals[2] != m.SessionID {
			t.Errorf("row %d: session_id mismatch", i)
		}
		// Last value is JSON-encoded dimensions
		dimJSON, ok := vals[12].([]byte)
		if !ok {
			t.Fatalf("row %d: expected []byte for dimensions, got %T", i, vals[12])
		}
		var decoded map[string]interface{}
		_ = json.Unmarshal(dimJSON, &decoded)
		if m.Dimensions != nil {
			if decoded["dim"] != "x" {
				t.Errorf("row %d: dimensions mismatch: %v", i, decoded)
			}
		}
	}

	if cs.Next() {
		t.Error("Next() should return false after all metrics consumed")
	}
	if cs.Err() != nil {
		t.Errorf("Err() should be nil, got %v", cs.Err())
	}
}
