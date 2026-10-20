package sessionfacts

import (
	"sort"
	"strings"
	"time"
)

// CopyRecord is the bounded identity and request-key projection used to decide
// whether a persisted record is a copied line. It deliberately carries no
// prompt or output text.
type CopyRecord struct {
	ID                int64
	SessionID         string
	Agent             string
	RecordType        string
	UUID              string
	ResponseID        string
	RequestKey        string
	ForkedFromSession string
	TS                time.Time
}

// CopyMarker is the persisted ownership decision for one copied record.
type CopyMarker struct {
	SRecID            int64
	SessionID         string
	OriginalSessionID string
	RecordType        string
	TS                time.Time
}

// CopyClassification is deterministic for a complete connected copy group.
// A capped group leaves existing markers untouched in the caller.
type CopyClassification struct {
	Markers []CopyMarker
	Skipped bool
}

type copyLine struct {
	record CopyRecord
	key    string
}

// ClassifyCopies applies p1b-design §3.16 to the sessions in one connected
// component. The input must contain every non-tombstoned member and every raw
// record needed to count keys, including rows without a copy key that share a
// Claude request key. Account and session exclusions are intentionally absent.
func ClassifyCopies(records []CopyRecord, continuedIn map[string][]string, maxRows int) CopyClassification {
	if maxRows > 0 && len(records) > maxRows {
		return CopyClassification{Skipped: true}
	}
	lines := make([]copyLine, 0, len(records))
	byID := make(map[int64]CopyRecord, len(records))
	keySessions := make(map[string]map[string]struct{})
	keyRows := make(map[string][]copyLine)
	keyCount := make(map[string]int)
	for _, record := range records {
		key := copyRecordKey(record)
		line := copyLine{record: record, key: key}
		lines = append(lines, line)
		byID[record.ID] = record
		if key == "" {
			continue
		}
		if keySessions[key] == nil {
			keySessions[key] = make(map[string]struct{})
		}
		keySessions[key][record.SessionID] = struct{}{}
		keyRows[key] = append(keyRows[key], line)
		keyCount[record.SessionID]++
	}

	birth := make(map[string]time.Time)
	for key, sessions := range keySessions {
		if len(sessions) != 1 {
			continue
		}
		for session := range sessions {
			for _, line := range keyRows[key] {
				if line.record.SessionID == session && (birth[session].IsZero() || line.record.TS.Before(birth[session])) {
					birth[session] = line.record.TS
				}
			}
		}
	}

	baseMarkers := make(map[int64]CopyMarker)
	for key, rows := range keyRows {
		sessions := keySessions[key]
		if len(sessions) < 2 {
			continue
		}
		candidates := make([]string, 0, len(sessions))
		for session := range sessions {
			candidates = append(candidates, session)
		}
		sort.Strings(candidates)
		remaining := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			removed := false
			for _, row := range rows {
				if row.record.SessionID == candidate && row.record.ForkedFromSession != "" && row.record.ForkedFromSession != candidate {
					if _, inGroup := sessions[row.record.ForkedFromSession]; inGroup {
						removed = true
						break
					}
				}
			}
			if !removed {
				for other := range sessions {
					if other == candidate {
						continue
					}
					if contains(continuedIn[other], candidate) {
						removed = true
						break
					}
				}
			}
			if !removed {
				remaining = append(remaining, candidate)
			}
		}
		if len(remaining) == 0 {
			remaining = candidates
		}
		keeper := remaining[0]
		for _, candidate := range remaining[1:] {
			if copyCandidateBefore(candidate, keeper, birth, keyCount) {
				keeper = candidate
			}
		}
		for _, row := range rows {
			if row.record.SessionID == keeper {
				continue
			}
			baseMarkers[row.record.ID] = copyMarker(row.record, keeper)
		}
	}

	// A copied Claude line copies the complete request for downstream request
	// heads, MAX-token aggregation, and plugin attribution. Do not guess a single
	// owner when different shared lines in one request disagree.
	requestOwner := make(map[string]string)
	conflicted := make(map[string]bool)
	for _, marker := range baseMarkers {
		line, ok := byID[marker.SRecID]
		if !ok || line.RequestKey == "" || line.Agent != "claude" {
			continue
		}
		group := line.SessionID + "\x00" + line.RequestKey
		if previous := requestOwner[group]; previous != "" && previous != marker.OriginalSessionID {
			conflicted[group] = true
			delete(requestOwner, group)
			continue
		}
		if !conflicted[group] {
			requestOwner[group] = marker.OriginalSessionID
		}
	}
	for _, line := range lines {
		if line.record.Agent != "claude" || line.record.RequestKey == "" {
			continue
		}
		group := line.record.SessionID + "\x00" + line.record.RequestKey
		original := requestOwner[group]
		if original != "" && !conflicted[group] && line.record.SessionID != original {
			baseMarkers[line.record.ID] = copyMarker(line.record, original)
		}
	}

	markers := make([]CopyMarker, 0, len(baseMarkers))
	for _, marker := range baseMarkers {
		markers = append(markers, marker)
	}
	sort.Slice(markers, func(i, j int) bool {
		if markers[i].SessionID != markers[j].SessionID {
			return markers[i].SessionID < markers[j].SessionID
		}
		return markers[i].SRecID < markers[j].SRecID
	})
	return CopyClassification{Markers: markers}
}

func copyRecordKey(record CopyRecord) string {
	if record.SessionID == "" || record.TS.IsZero() || !record.TS.After(time.Unix(0, 0)) {
		return ""
	}
	ts := record.TS.UTC().Format(time.RFC3339Nano)
	switch {
	case record.Agent == "claude" && (record.RecordType == "user" || record.RecordType == "assistant" || record.RecordType == "system" || record.RecordType == "attachment") && record.UUID != "" && !strings.HasPrefix(record.UUID, "content-"):
		return "claude\x00" + record.UUID + "\x00" + ts
	case record.Agent == "codex" && record.RecordType == "usage" && record.ResponseID != "":
		return "codex\x00" + record.ResponseID + "\x00" + ts
	default:
		return ""
	}
}

func copyCandidateBefore(a, b string, birth map[string]time.Time, keyCount map[string]int) bool {
	ta, tb := birth[a], birth[b]
	switch {
	case ta.IsZero() && !tb.IsZero():
		return true
	case !ta.IsZero() && tb.IsZero():
		return false
	case !ta.Equal(tb):
		return ta.Before(tb)
	case keyCount[a] != keyCount[b]:
		return keyCount[a] < keyCount[b]
	default:
		return a < b
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func copyMarker(record CopyRecord, original string) CopyMarker {
	return CopyMarker{SRecID: record.ID, SessionID: record.SessionID, OriginalSessionID: original, RecordType: record.RecordType, TS: record.TS}
}
