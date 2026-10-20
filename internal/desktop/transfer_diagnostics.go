package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"time"

	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

var diagnosticVersionPattern = regexp.MustCompile(`^(dev|unknown|v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]{1,48})?)$`)

// Diagnostics are returned only to callers explicitly requesting schema 1.
func (s *Service) withTransferDiagnostics(value any) any {
	result, ok := value.(daemonStatusResult)
	if !ok || s.fixtureRoot != "" {
		return value
	}
	entries, err := (profile.Registry{Root: s.daemonRoot}).List()
	if err != nil {
		return result
	}
	dirs := map[string]string{}
	for _, entry := range entries {
		dirs[entry.ID] = entry.RuntimeDir
	}
	remaining := int64(512 << 10)
	for i := range result.Profiles {
		row := &result.Profiles[i]
		dir := dirs[row.ID]
		if dir == "" || !s.daemonProfileDirectorySafe(daemonProfilePath{id: row.ID, dir: dir}) {
			continue
		}
		data, failure := readDaemonFile(filepath.Join(dir, "transfer-diagnostics.json"), min(64<<10, remaining))
		remaining -= int64(len(data))
		if failure != nil || !lifecycleJSONHasUniqueKeys(data) {
			continue
		}
		var diagnostics syncer.TransferDiagnostics
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&diagnostics) != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) || !validTransferDiagnostics(diagnostics, s.now().UTC()) {
			continue
		}
		row.TransferDiagnostics = &diagnostics
	}
	return result
}

func validTransferDiagnostics(d syncer.TransferDiagnostics, now time.Time) bool {
	if d.SchemaVersion != 1 || d.Events == nil || len(d.Events) > 32 {
		return false
	}
	var sequence uint64
	events := append([]syncer.TransferEvent(nil), d.Events...)
	if d.LastFailure != nil {
		if d.LastFailure.Outcome == "accepted" || len(d.Events) == 0 || d.LastFailure.Sequence > d.Events[len(d.Events)-1].Sequence {
			return false
		}
		events = append(events, *d.LastFailure)
	}
	for i, e := range events {
		if (i < len(d.Events) && e.Sequence <= sequence || e.Sequence == 0) || e.At.IsZero() || e.At.After(now) || len(e.ClientVersion) > 64 || !diagnosticVersionPattern.MatchString(e.ClientVersion) || e.RequestBytes < 1 || e.RecordCount < 0 || e.DurationMS < 0 || e.DurationMS > 86400000 || e.DeadlineMS < 0 || e.DeadlineMS > 300000 || e.RetryAfterMS < 0 || e.RetryAfterMS > 86400000 || e.SplitDepth < 0 || e.SplitDepth > 64 || e.ServerBodyLimitKnown {
			return false
		}
		if !oneOf(e.Provider, "claude", "codex", "gjc", "omo", "unknown") || !oneOf(e.Outcome, "accepted", "body_too_large", "rate_limited", "auth_error", "http_error", "network_error", "response_error") || !oneOf(e.Failure, "", "timeout", "canceled", "dns", "tls", "connect", "network", "request", "response", "encode") || e.HTTPStatus != 0 && (e.HTTPStatus < 100 || e.HTTPStatus > 599) {
			return false
		}
		sequence = e.Sequence
	}
	return true
}
func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
