package syncer

import "cctrace/internal/sessionlog"

// newReferenceCapture sees only this scan's selected rows. Account boundaries
// reset the group so a kept row never names sources dropped for another account.
func (s *Syncer) newReferenceCapture(fromOffset int64) func(*sessionlog.Record) {
	references := sessionlog.NewReferenceCollector(fromOffset)
	home := normalizeHome(s.claudeDir)
	var previousAccount string
	seenAccount := false
	return func(r *sessionlog.Record) {
		// Untimestamped metadata carries no new account observation. Such a line
		// must not interrupt a native user/assistant transcript prefix.
		if (r.Type == "user" || r.Type == "assistant") && !r.Timestamp.IsZero() {
			account := s.state.ClaudeAccountAt(home, r.Timestamp)
			if seenAccount && account != previousAccount {
				references.Reset()
			}
			previousAccount, seenAccount = account, true
		}
		references.Capture(r)
	}
}
