package sessiontrace

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestStorageRestartKeepsExactPairingAndSourceLookup(t *testing.T) {
	t.Parallel()
	c, r := call(1, 1, "x"), result(2, 8, "x")
	c.Raw = json.RawMessage(`{"text":"private-input","arguments":"private-argument","output":"private-output"}`)
	start := Normalize([]Record{c})
	data, err := MarshalStorage(start)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-") {
		t.Fatalf("persisted body: %s", data)
	}
	restored, err := UnmarshalStorage(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(start, restored) {
		t.Fatalf("metadata changed at restart: %+v", restored)
	}
	end := Normalize([]Record{r})
	if EventCorrelationKey(only(t, start)) != EventCorrelationKey(only(t, end)) || EventCorrelationKey(only(t, start)) == "" {
		t.Fatal("boundary bucket differs")
	}
	merged := Merge(restored, end)
	if !reflect.DeepEqual(merged, Normalize([]Record{c, r})) {
		t.Fatalf("restart broke pairing: %+v", merged)
	}
	e := only(t, merged)
	if e.SourceRef != SourceReference(c) || Sources(e)[0].ID != c.ID || Sources(e)[1].ID != r.ID {
		t.Fatal("cannot resolve both exact source endpoints")
	}
	data, err = MarshalStorage(merged)
	if err != nil {
		t.Fatal(err)
	}
	restored, err = UnmarshalStorage(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(merged, Merge(restored, end)) {
		t.Fatal("paired cache cannot replay after restart")
	}
}

func TestStorageRejectsWireOnlyUnknownVersionAndMalformedMetadata(t *testing.T) {
	t.Parallel()
	for _, data := range []string{`{`, `[]`, `null`, `{"version":999,"events":[]}`, `{"version":1,"events":[{"event":{"event_id":"fake"}}]}`} {
		if _, err := UnmarshalStorage([]byte(data)); err == nil {
			t.Fatalf("accepted invalid storage: %s", data)
		}
	}
	e := only(t, Normalize([]Record{call(1, 1, "x")}))
	wire, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var withoutMetadata Event
	if err := json.Unmarshal(wire, &withoutMetadata); err != nil {
		t.Fatal(err)
	}
	if _, err := MarshalStorage([]Event{withoutMetadata}); err == nil {
		t.Fatal("wire-only event silently lost provenance")
	}
}

func TestStorageExactBoundaryCallIDsSurviveRestart(t *testing.T) {
	t.Parallel()
	c, r := call(1, 1, "x"), result(2, 8, "x")
	c.ParentToolUseID, c.ParentSourceFile = "parent", "parent.jsonl"
	e := only(t, Normalize([]Record{c, r}))
	want := []string{"parent", "x"}
	if got := EventCallIDs(e); !reflect.DeepEqual(got, want) {
		t.Fatalf("boundary IDs absent: %v", got)
	}
	data, err := MarshalStorage([]Event{e})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := UnmarshalStorage(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := EventCallIDs(only(t, restored)); !reflect.DeepEqual(got, want) {
		t.Fatalf("restart lost exact boundary IDs: %v", got)
	}
}
