package desktop

import (
	"bytes"
	"encoding/json"
	"regexp"

	"cctrace/internal/localdaemon"
)

const maxDaemonSharedSources = 16

var sharedSourceIDPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)

// daemonSharedSource is a source a job does not read because another profile
// holds it. It is a diagnostic: nothing here decides Complete or the job's
// state, and the holder's ID stays inside the daemon.
type daemonSharedSource struct {
	Source      string `json:"source"`
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Reason      string `json:"reason"`
	HolderName  string `json:"holder_name,omitempty"`
	HolderState string `json:"holder_state"`
}

// daemonStatusRequest is what a daemon.status.read payload asks for beyond the
// plain snapshot.
type daemonStatusRequest struct {
	diagnostics   bool
	sharedSources bool
}

// parseDaemonStatusRequest accepts an empty payload or either opt-in or both,
// each with its one exact value.
func parseDaemonStatusRequest(raw json.RawMessage) (daemonStatusRequest, bool) {
	var fields map[string]json.RawMessage
	var request daemonStatusRequest
	if decodePayload(raw, &fields) != nil {
		return request, false
	}
	for key, value := range fields {
		value = bytes.TrimSpace(value)
		switch {
		case key == "diagnostics_version" && bytes.Equal(value, []byte("1")):
			request.diagnostics = true
		case key == "shared_sources" && bytes.Equal(value, []byte("true")):
			request.sharedSources = true
		default:
			return daemonStatusRequest{}, false
		}
	}
	return request, true
}

// withoutSharedSources keeps the key off the wire for a caller that did not
// ask for it, so its job keeps the six keys it always had.
func withoutSharedSources(value any) any {
	if result, ok := value.(daemonStatusResult); ok {
		for _, row := range result.Profiles {
			if row.Job != nil {
				row.Job.SharedSources = nil
			}
		}
	}
	return value
}

// sharedSourcesFromJob validates each item on its own: a bad item is dropped or
// has its unknown words replaced, and never makes the job invalid.
func sharedSourcesFromJob(items []localdaemon.SharedSource) []daemonSharedSource {
	var out []daemonSharedSource
	for _, item := range items {
		if len(out) == maxDaemonSharedSources {
			break
		}
		if !sharedSourceIDPattern.MatchString(item.Source) {
			continue
		}
		holder := item.HolderName
		if holder != "" && !validNamedStatusName(holder) {
			holder = ""
		}
		out = append(out, daemonSharedSource{
			Source:      item.Source,
			Kind:        knownWord(item.Kind, "claude", "codex", "gjc", "omo"),
			Label:       knownWord(item.Label, "home", "default", "env", "configured", "explicit"),
			Reason:      knownWord(item.Reason, localdaemon.ShareSame, localdaemon.ShareNested),
			HolderName:  holder,
			HolderState: knownWord(item.HolderState, localdaemon.HolderActive, localdaemon.HolderStopped, localdaemon.HolderDisabled, localdaemon.HolderBlocked, localdaemon.HolderTransferring, localdaemon.HolderRemoved, localdaemon.HolderUnowned, localdaemon.HolderUnknown),
		})
	}
	return out
}

func knownWord(value string, known ...string) string {
	for _, word := range known {
		if value == word {
			return value
		}
	}
	return "unknown"
}
