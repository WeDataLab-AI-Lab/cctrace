package sessionlog

import "encoding/json"

type collectionKeeper func(string, json.RawMessage, json.RawMessage) json.RawMessage

func isRequestProtocol(typ string) bool {
	return typ == "api-request" || typ == "api-request-shape" || typ == "api-request-blob"
}

func jsonObject(v json.RawMessage) json.RawMessage {
	if present(v) && v[0] == '{' {
		return v
	}
	return nil
}

func jsonArray(v json.RawMessage) json.RawMessage {
	if present(v) && v[0] == '[' {
		return v
	}
	return nil
}

func (r *Record) keepNativeAssistant(in collectedFields, keep collectionKeeper) {
	r.RequestRef = keep("requestRef", in.RequestRef, jsonString(in.RequestRef))
	r.APIBlockIndex = keep("apiBlockIndex", in.APIBlockIndex, jsonNumber(in.APIBlockIndex))
	r.WireToolInputs = keep("wireToolInputs", in.WireToolInputs, jsonObject(in.WireToolInputs))
	r.WireIngestContext = keep("wireIngestContext", in.WireIngestContext, jsonObject(in.WireIngestContext))
	r.Effort = keep("effort", in.Effort, jsonString(in.Effort))
	r.APIErrorStatus = keep("apiErrorStatus", in.APIErrorStatus, jsonNumber(in.APIErrorStatus))
	r.APIErrorIsTransient = keep("apiErrorIsTransient", in.APIErrorIsTransient, jsonBool(in.APIErrorIsTransient))
	r.APIError = keep("apiError", in.APIError, r.keepNativeError("apiError", in.APIError))
	r.APIErrorParams = keep("apiErrorParams", in.APIErrorParams, r.keepNativeError("apiErrorParams", in.APIErrorParams))
}

func (r *Record) keepNativeError(key string, value json.RawMessage) json.RawMessage {
	kept := capJSON(value, maxErrorBytes)
	if present(value) && len(value) > maxErrorBytes {
		r.CctraceTruncated = append(r.CctraceTruncated, key)
	}
	return kept
}

func (r *Record) keepStopSummary(in collectedFields, keep collectionKeeper) {
	var subtype string
	if json.Unmarshal(in.Subtype, &subtype) != nil || subtype != "stop_hook_summary" {
		return
	}
	r.HookCount = keep("hookCount", in.HookCount, jsonNumber(in.HookCount))
	r.HookInfos = keep("hookInfos", in.HookInfos, pickEach(in.HookInfos, "command", "durationMs", "promptText"))
	r.HookErrors = keep("hookErrors", in.HookErrors, jsonArray(in.HookErrors))
	r.HookAdditionalContext = keep("hookAdditionalContext", in.HookAdditionalContext, jsonArray(in.HookAdditionalContext))
	r.PreventedContinuation = keep("preventedContinuation", in.PreventedContinuation, jsonBool(in.PreventedContinuation))
	r.StopReason = keep("stopReason", in.StopReason, jsonString(in.StopReason))
	r.HasOutput = keep("hasOutput", in.HasOutput, jsonBool(in.HasOutput))
	r.Level = keep("level", in.Level, jsonString(in.Level))
	r.ToolUseID = keep("toolUseID", in.ToolUseID, jsonString(in.ToolUseID))
}

func (r *Record) keepNativeRequest(in collectedFields, keep collectionKeeper) {
	switch r.Type {
	case "api-request":
		r.ID = keep("id", in.ID, jsonString(in.ID))
		r.QuerySource = keep("querySource", in.QuerySource, jsonString(in.QuerySource))
		r.ShapeHash = keep("shapeHash", in.ShapeHash, jsonString(in.ShapeHash))
		r.Params = keep("params", in.Params, jsonObject(in.Params))
		r.Base = keep("base", in.Base, jsonString(in.Base))
		r.Keep = keep("keep", in.Keep, jsonNumber(in.Keep))
		r.Tail = keep("tail", in.Tail, jsonArray(in.Tail))
		r.CacheControl = keep("cacheControl", in.CacheControl, jsonArray(in.CacheControl))
		r.RequestDigest = keep("requestDigest", in.RequestDigest, jsonString(in.RequestDigest))
		r.Cut = keep("cut", in.Cut, jsonBool(in.Cut))
		r.CutRequestDigest = keep("cutRequestDigest", in.CutRequestDigest, jsonString(in.CutRequestDigest))
		// Native withheld may be a reason, a flag or a field list. Its shape is
		// source evidence and must not be confused with the collector's own marker.
		if present(in.Withheld) {
			r.Withheld = in.Withheld
		}
	case "api-request-shape":
		r.ShapeHash = keep("shapeHash", in.ShapeHash, jsonString(in.ShapeHash))
		r.Shape = keep("shape", in.Shape, pick(in.Shape, "system", "tools"))
	case "api-request-blob":
		r.Hash = keep("hash", in.Hash, jsonString(in.Hash))
		// System blobs also carry source output_config in their native hash.
		// Other message metadata is not part of this compact blob contract.
		keys := []string{"role", "content"}
		var head struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(r.Message, &head) == nil && head.Role == "system" {
			keys = append(keys, "output_config")
		}
		r.Message = keep("message", r.Message, pick(r.Message, keys...))
	}
}

func (r *Record) withholdNativeContent() []string {
	var removed []string
	for _, field := range []struct {
		name  string
		value *json.RawMessage
	}{
		{"wireToolInputs", &r.WireToolInputs}, {"wireIngestContext", &r.WireIngestContext},
		{"error", &r.Error}, {"apiError", &r.APIError}, {"apiErrorParams", &r.APIErrorParams},
		{"hookInfos", &r.HookInfos}, {"hookErrors", &r.HookErrors},
		{"hookAdditionalContext", &r.HookAdditionalContext}, {"stopReason", &r.StopReason},
		{"params", &r.Params},
	} {
		if present(*field.value) {
			removed = append(removed, field.name)
			*field.value = nil
		}
	}
	var shapeRemoved []string
	r.Shape, shapeRemoved = without(r.Shape, "system", "tools")
	for _, key := range shapeRemoved {
		removed = append(removed, "shape."+key)
	}
	if r.Type == "api-request-blob" && present(r.Message) {
		removed = append(removed, "message")
		r.Message = nil
	}
	return removed
}
