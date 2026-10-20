package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestOtelEnvelopeStoredSeparatelyFromGeneralEventReads(t *testing.T) {
	// The shared database fixture is serialized by acquireTestStore.
	s := acquireTestStore(t)
	ctx := context.Background()
	id := "envelope-" + t.Name()
	e := &OtelEvent{Ts: time.Now().UTC(), EventName: "api_request", SessionID: id, Envelope: json.RawMessage(`{"log_record":{"body":{"stringValue":"private source"}}}`)}
	if err := s.InsertEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT source_envelope FROM otel_events WHERE session_id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value["log_record"] == nil {
		t.Fatalf("source envelope lost: %s %v", raw, err)
	}
	events, err := s.ListEvents(ctx, EventFilter{SessionID: id})
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
	if len(events[0].Envelope) != 0 {
		t.Fatal("general event read selected private envelope")
	}
}
