package airuntime

// SchemaTiming decides when a turn may ask for the answer schema and when it
// must force a tool call instead.
//
// Asking for both in the same breath puts the model in a bind and it takes the
// cheaper way out: z-ai/glm-5.3 answered "I will look up the segments and then
// write", called nothing, and the run finished empty because that sentence fit
// the schema. So the first request forces a call and withholds the schema;
// once data is coming in, the tools stay available and the schema joins them.
//
// Only chatruntime learned this. The decision lives here so the other runtimes
// are not each one bad turn away from finding it out again.
type SchemaTiming struct {
	HasTools  bool
	HasSchema bool
}

// AttachSchema reports whether this request may carry the answer schema.
// toolsRan is whether any tool has run in this turn.
func (s SchemaTiming) AttachSchema(toolsRan bool) bool {
	if !s.HasSchema {
		return false
	}
	return toolsRan || !s.HasTools
}

// ForceTool reports whether this request must demand a tool call. Forcing past
// the first tool run would never let the turn end.
func (s SchemaTiming) ForceTool(toolsRan bool) bool {
	return s.HasTools && !toolsRan
}
