package store

import "time"

type activityLive struct {
	status, waitingKind string
	heartbeatAt         time.Time
	statusChangedAt     *time.Time
}

type sessionActivityInput struct {
	live                                 []activityLive
	lastActivityAt                       *time.Time
	lastTurnStatus                       string
	openQuestions, background, todo      int
	sameQuestionTurn                     bool
	endAt, allGoneAt, collectorHeartbeat *time.Time
	anyLiveRow                           bool
}

type sessionActivityResult struct {
	category string
	selected *activityLive
	endedAt  *time.Time
}

// evaluateSessionActivity applies the owner-scoped observation decision table.
// A stale instance requires both old timestamps; an unknown timestamp is not stale.
func evaluateSessionActivity(now time.Time, in sessionActivityInput) sessionActivityResult {
	var selected *activityLive
	var fresh, staleCount int
	for i := range in.live {
		row := &in.live[i]
		if row.status == "gone" || row.heartbeatAt.Before(now.Add(-3*time.Minute)) || row.heartbeatAt.After(now.Add(time.Minute)) {
			continue
		}
		fresh++
		active := row.status == "busy" || row.status == "waiting" || row.waitingKind != ""
		if active && row.statusChangedAt != nil && in.lastActivityAt != nil &&
			row.statusChangedAt.Before(now.Add(-6*time.Hour)) && in.lastActivityAt.Before(now.Add(-6*time.Hour)) {
			staleCount++
			continue
		}
		if selected == nil || row.heartbeatAt.After(selected.heartbeatAt) {
			selected = row
		}
	}
	result := sessionActivityResult{selected: selected}
	if selected != nil {
		if selected.status == "waiting" || selected.waitingKind != "" {
			result.category = "waiting"
			return result
		}
		if selected.status == "busy" {
			result.category = "running"
			return result
		}
	}
	if !in.anyLiveRow && in.lastTurnStatus == "open" && in.lastActivityAt != nil && !in.lastActivityAt.Before(now.Add(-5*time.Minute)) {
		result.category = "running"
		return result
	}
	end := in.endAt
	if in.allGoneAt != nil && (end == nil || in.allGoneAt.After(*end)) {
		end = in.allGoneAt
	}
	if selected == nil && end != nil && in.lastActivityAt != nil && end.After(*in.lastActivityAt) {
		result.category, result.endedAt = "ended", end
		return result
	}
	if fresh > 0 && fresh == staleCount {
		result.category = "stale_live"
		return result
	}
	if in.openQuestions > 0 && in.sameQuestionTurn {
		result.category = "open_question"
		return result
	}
	if selected != nil && (selected.status == "idle" || selected.status == "shell") {
		result.category = "idle"
		return result
	}
	if in.lastActivityAt != nil && !in.lastActivityAt.Before(now.Add(-24*time.Hour)) {
		if in.collectorHeartbeat != nil && in.collectorHeartbeat.Before(now.Add(-3*time.Minute)) {
			result.category = "collector_delayed"
		} else {
			result.category = "recent"
		}
		return result
	}
	result.category = "inactive"
	return result
}
