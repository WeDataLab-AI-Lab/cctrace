package main

import "cctrace/internal/store"

// sessionRetentionNotice returns a boot warning when nobody has decided how long
// session_records is kept, and "" otherwise.
//
// The asymmetry it exists to surface: migrations give otel_events/otel_metrics a
// 90-day retention policy, but session_records -- the table holding conversation
// content -- gets none. Adding one by default is not an option: ReconcileRetention
// deliberately treats an unset axis as "a code deploy never changes any policy",
// so a default would silently delete existing conversations on upgrade. The
// operator has to choose, which means the operator has to be told.
//
// A nil axis means neither SESSION_RETENTION_DAYS nor a stored admin setting was
// found (EffectiveRetentionConfig folds both in), so the table is unmanaged. An
// explicit 0 is permanent too, but it was *chosen* -- warning about a decision the
// operator already made would train them to ignore the line that matters.
func sessionRetentionNotice(cfg store.RetentionConfig) string {
	if cfg.SessionDays != nil {
		return ""
	}
	return "[cctraced] notice: session_records (conversation content) has no retention policy " +
		"and is kept indefinitely, while otel_events/otel_metrics are dropped after 90 days. " +
		"Set SESSION_RETENTION_DAYS (0 = keep forever) or choose an interval in Admin -> Storage."
}
