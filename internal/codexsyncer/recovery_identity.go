package codexsyncer

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"cctrace/internal/sessiontrace"
)

// Supplemental usage has no legacy row or timestamp ordinal. Its source UUID
// is independent of JSON object order/whitespace and timestamp spelling, so a
// mirrored rollout and a live rescan land on the same recovered row.
func supplementalUsageUUID(raw json.RawMessage) string {
	object, err := sessiontrace.DecodeObject(raw)
	if err == nil {
		delete(object, "timestamp")
		if canonical, err := sessiontrace.CanonicalJSON(object); err == nil {
			raw = canonical
		}
	}
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("usage-%x", digest[:16])
}

// Context rows collected before lineage/settings expansion were keyed by the
// instruction-only raw projection. Reproduce that projection for identity,
// while retaining every newly allowed field in the actual uploaded Raw.
func contextIdentityRaw(raw json.RawMessage) json.RawMessage {
	var source struct {
		Type      string                     `json:"type"`
		Timestamp string                     `json:"timestamp,omitempty"`
		Payload   map[string]json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(raw, &source) != nil {
		return raw
	}
	legacy := make(map[string]json.RawMessage)
	for _, key := range []string{"base_instructions", "developer_instructions", "user_instructions", "collaboration_mode"} {
		if value, ok := source.Payload[key]; ok {
			legacy[key] = value
		}
	}
	if len(legacy) == 0 {
		return raw
	} // This metadata-only context had no old row.
	for _, key := range []string{"id", "turn_id", "cwd", "model", "cli_version", "originator", "model_provider"} {
		if value, ok := source.Payload[key]; ok {
			legacy[key] = value
		}
	}
	source.Payload = legacy
	identity, err := json.Marshal(source)
	if err != nil {
		return raw
	}
	return identity
}
