package sessionrecord

import (
	"fmt"
	"strings"
	"testing"
)

func TestChunkInputKeepsStableNamesAndEnforcesCallBudgets(t *testing.T) {
	turns := make([]Turn, 40)
	for i := range turns {
		turns[i] = Turn{Key: fmt.Sprintf("turn-%d", i+1), Request: true, Status: "ended", Final: strings.Repeat("x", 3000)}
	}
	chunks, err := ChunkInput(Input{Turns: turns})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("chunk count = %d, want multiple chunks", len(chunks))
	}
	for _, chunk := range chunks {
		built, err := BuildInput(chunk)
		if err != nil {
			t.Fatal(err)
		}
		if len(built.JSON) > MaxInputBytes || len(built.RequestTurns) > MaxRequestsPerCall {
			t.Fatalf("chunk exceeds budget: bytes=%d requests=%d", len(built.JSON), len(built.RequestTurns))
		}
	}
	var names []string
	for _, chunk := range chunks {
		built, err := BuildInput(chunk)
		if err != nil {
			t.Fatal(err)
		}
		for name := range built.TurnKeys {
			names = append(names, name)
		}
	}
	if len(names) != len(turns) {
		t.Fatalf("turn mapping count = %d, want %d", len(names), len(turns))
	}
	for i := range turns {
		want := fmt.Sprintf("t%d", i+1)
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("chunking lost stable prompt name %s", want)
		}
	}
}
