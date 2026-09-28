package airuntime

import "testing"

// Asking for the answer schema before any tool has run puts the model in a
// bind and it takes the cheaper way out: z-ai/glm-5.3 answered "I will look up
// the segments and then write", called nothing, and the run finished empty
// because that sentence fit the schema. chatruntime learned this and withholds
// the schema until a tool has run; openairuntime and clauderuntime attach it
// from the first request and are open to the same failure.
func TestSchemaIsWithheldUntilToolsHaveRun(t *testing.T) {
	withTools := SchemaTiming{HasTools: true, HasSchema: true}
	if withTools.AttachSchema(false) {
		t.Error("schema attached before any tool ran")
	}
	if !withTools.ForceTool(false) {
		t.Error("first request did not force a tool call")
	}

	if !withTools.AttachSchema(true) {
		t.Error("schema withheld after a tool ran")
	}
	if withTools.ForceTool(true) {
		t.Error("tool calls still forced after a tool ran; the turn could never end")
	}
}

// With no tools there is nothing to wait for, so the schema goes on the first
// request and nothing is forced.
func TestSchemaIsAttachedAtOnceWhenThereAreNoTools(t *testing.T) {
	noTools := SchemaTiming{HasTools: false, HasSchema: true}
	if !noTools.AttachSchema(false) {
		t.Error("schema withheld although there are no tools to wait for")
	}
	if noTools.ForceTool(false) {
		t.Error("forced a tool call with no tools to call")
	}
}

func TestNoSchemaIsNeverAttached(t *testing.T) {
	none := SchemaTiming{HasTools: true, HasSchema: false}
	if none.AttachSchema(true) || none.AttachSchema(false) {
		t.Error("attached a schema that was not asked for")
	}
}
