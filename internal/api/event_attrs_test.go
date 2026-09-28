package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cctrace/internal/store"
)

type eventAttrsStore struct {
	mockStore
	events []*store.OtelEvent
}

func (m *eventAttrsStore) ListEvents(_ context.Context, _ store.EventFilter) ([]*store.OtelEvent, error) {
	return m.events, nil
}

// The event routes are not scoped to the session owner and session ids are
// printed in the Logs table, so anything free-text in attrs is readable by every
// signed-in account. Client-side redaction cannot be the defence: RedactUserPrompts
// defaults to false, so a fresh install stores prompts in the clear (#596 follow-up).
func TestListEventsDropsTextBearingAttrs(t *testing.T) {
	m := &eventAttrsStore{events: []*store.OtelEvent{{
		Ts:        time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		EventName: "user_prompt",
		SessionID: "other-persons-session",
		UserID:    "someone-else",
		Attrs: map[string]interface{}{
			"prompt":                "what a person typed",
			"content":               "assistant text",
			"stdout":                "tool output",
			"prompt_length":         "19",
			"tool_input_size_bytes": 42,
			"terminal.type":         "zed",
		},
	}}}

	req := httptest.NewRequest(http.MethodGet, "/api/events?session_id=other-persons-session", nil)
	rr := httptest.NewRecorder()
	newTestServer(m, nil).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var got []struct {
		Attrs map[string]interface{} `json:"attrs"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("events=%d", len(got))
	}
	for _, key := range []string{"prompt", "content", "stdout"} {
		if _, present := got[0].Attrs[key]; present {
			t.Errorf("%s survived the response: %v", key, got[0].Attrs)
		}
	}
	// Accounting and shape keys are different names and have to survive, or the
	// Logs view and every aggregate lose columns they already show.
	for _, key := range []string{"prompt_length", "tool_input_size_bytes", "terminal.type"} {
		if _, present := got[0].Attrs[key]; !present {
			t.Errorf("%s was dropped: %v", key, got[0].Attrs)
		}
	}
}

type toolFailureAttrsStore struct {
	mockStore
	failures []*store.ToolFailure
}

func (m *toolFailureAttrsStore) ToolFailures(context.Context, string, time.Time, time.Time, string, string, string, int) ([]*store.ToolFailure, error) {
	return m.failures, nil
}

func (m *toolFailureAttrsStore) ToolTimeSeries(context.Context, string, time.Time, time.Time, string, string, string, string, string) ([]*store.ToolTimeBucket, error) {
	return nil, nil
}

// /api/tools/detail hands out the same overflow bag from the same events, and is
// no more scoped to the session owner than /api/events is -- so the protection
// has to be on both routes or it is on neither. Production failures carry only
// accounting keys today; the list exists for what an agent exports tomorrow.
func TestToolDetailDropsTextBearingAttrs(t *testing.T) {
	m := &toolFailureAttrsStore{failures: []*store.ToolFailure{{
		Ts:        time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		SessionID: "other-persons-session",
		UserID:    "someone-else",
		Attrs: map[string]interface{}{
			"command":               "rm -rf /tmp/theirs",
			"stderr":                "permission denied",
			"input":                 "what they asked the tool to do",
			"error_type":            "permission",
			"tool_input_size_bytes": 42,
			"decision_type":         "deny",
		},
	}}}

	req := httptest.NewRequest(http.MethodGet, "/api/tools/detail?tool_name=Bash", nil)
	rr := httptest.NewRecorder()
	newTestServer(m, nil).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var got struct {
		Failures []struct {
			Attrs map[string]interface{} `json:"attrs"`
		} `json:"failures"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if len(got.Failures) != 1 {
		t.Fatalf("failures=%d body=%s", len(got.Failures), rr.Body.String())
	}
	for _, key := range []string{"command", "stderr", "input"} {
		if _, present := got.Failures[0].Attrs[key]; present {
			t.Errorf("%s survived the response: %v", key, got.Failures[0].Attrs)
		}
	}
	// The Tools view expands this bag to show why a call failed. Dropping it
	// wholesale would be a simpler defence and a worse product.
	for _, key := range []string{"error_type", "tool_input_size_bytes", "decision_type"} {
		if _, present := got.Failures[0].Attrs[key]; !present {
			t.Errorf("%s was dropped: %v", key, got.Failures[0].Attrs)
		}
	}
}
