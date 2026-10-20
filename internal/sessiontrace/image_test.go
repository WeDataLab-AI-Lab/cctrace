package sessiontrace

import "testing"

func TestImageOnlyInputPreservesObservedPoint(t *testing.T) {
	t.Parallel()
	for _, blockType := range []string{"image", "input_image"} {
		t.Run(blockType, func(t *testing.T) {
			r := row(1, "claude", 1, "")
			r.RecordType, r.Role = "user", "user"
			r.Blocks = []Block{{Type: blockType}}
			e := only(t, Normalize([]Record{r}))
			if e.Kind != "input" || e.StartedAt != nil || e.EndedAt != nil || !e.Timestamp.Equal(r.TS) {
				t.Fatalf("image message must remain a timestamp-only input: %+v", e)
			}
			r.IsMeta = true
			if got := Normalize([]Record{r}); len(got) != 0 {
				t.Fatalf("metadata image became a user input: %+v", got)
			}
		})
	}
}

func TestToolResultImageDoesNotInventUserInput(t *testing.T) {
	t.Parallel()
	r := row(1, "claude", 1, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":[{"type":"image","source":{"type":"base64","data":"private-image"}}]}]}}`)
	r.RecordType = "user"
	if e := only(t, Normalize([]Record{r})); e.Kind != "tool" {
		t.Fatalf("nested result image became a user input: %+v", e)
	}
}
