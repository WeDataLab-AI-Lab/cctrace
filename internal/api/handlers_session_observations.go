package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cctrace/internal/auth"
	"cctrace/internal/projecthash"
	"cctrace/internal/store"
)

// Ceilings for one POST /api/session-observations request. The counts bound how
// many rows one transaction writes; the byte caps bound what one row can store,
// since a snapshot or payload is stored whole. They are per item, not a budget
// for the whole request -- maxSessionObservationsRequestBodyBytes is that, and a
// request within the counts can still be too large and draw a 413.
//
// The text caps work the same way. A key or an identity over its cap is a 400 --
// shortened, it would name a different row -- while a live state's name, which
// is only ever shown, is cut to fit.
//
// The number caps bound what jsonb makes of a number in a snapshot or payload.
// It keeps the decimal value, so an exponent is digits: 1e999999, 1e-999999 and
// a fraction of 20,000 digits fail the insert -- a 500 the client retries
// forever -- and 1e131071 is stored but reads back as 131,072 digits. A literal
// over 64 bytes, or an exponent past 308 either way, is a 400.
const (
	maxSessionSnapshotsPerRequest  = 200
	maxSessionLiveStatesPerRequest = 200
	maxSessionEventsPerRequest     = 500
	maxSessionSnapshotBytes        = 64 << 10
	maxSessionEventPayloadBytes    = 8 << 10
	maxSessionObservationKeyBytes  = 256
	maxSessionObservationTagBytes  = 64
	maxSessionLiveStateNameBytes   = 512
	maxSessionJSONNumberBytes      = 64
	maxSessionJSONNumberExponent   = 308
)

// maxSessionObservationClockAhead is how far past the server's clock an item may
// be dated. Times are compared and stored as the client sent them, so a clock
// running ahead wins every newer-wins comparison until real time catches up with
// it. Skipping what is dated further ahead bounds that to five minutes, and a
// clock that is badly wrong shows up as missing data instead of corrupted order.
const maxSessionObservationClockAhead = 5 * time.Minute

// maxEchoedDecodeErrorBytes is how much of a decode error a 400 echoes. The
// error can quote the input -- a time.ParseError quotes the value twice, so a
// 100,000-byte heartbeat_at drew a 200,111-byte answer -- and the start of it is
// enough to find what was wrong.
const maxEchoedDecodeErrorBytes = 256

// sessionObservationItem is what every item carries besides its own fields. All
// of it is stored. login_email and the billing pair decide an account exclusion
// here, and a read needs them again for an exclusion made after the row landed.
type sessionObservationItem struct {
	SessionID       string `json:"session_id"`
	Agent           string `json:"agent"`
	ProjectHash     string `json:"project_hash"`
	LoginEmail      string `json:"login_email"`
	BillingProvider string `json:"billing_provider"`
	AccountID       string `json:"account_id"`
}

type sessionSnapshotItem struct {
	sessionObservationItem
	ObservedAt time.Time `json:"observed_at"`
	// int32 because the column is INT: a larger number fails to decode, a 400,
	// instead of failing the insert as a 500 the client would retry forever.
	CollectVersion int32           `json:"collect_version"`
	Snapshot       json.RawMessage `json:"snapshot"`
}

type sessionLiveStateItem struct {
	sessionObservationItem
	PID             int64      `json:"pid"`
	ProcStart       string     `json:"proc_start"`
	Status          string     `json:"status"`
	StatusSource    string     `json:"status_source"`
	WaitingKind     string     `json:"waiting_kind"`
	Name            string     `json:"name"`
	NameSource      string     `json:"name_source"`
	StatusChangedAt *time.Time `json:"status_changed_at"`
	HeartbeatAt     time.Time  `json:"heartbeat_at"`
	IOEpoch         string     `json:"io_epoch"`
	IOAsOf          *time.Time `json:"io_as_of"`
	IOSource        string     `json:"io_source"`
	IOMode          string     `json:"io_mode"`
	IOPartial       bool       `json:"io_partial"`
	IOFailureCode   string     `json:"io_failure_code"`
	InputBytes      *int64     `json:"input_bytes"`
	InputLines      *int64     `json:"input_lines"`
	OutputBytes     *int64     `json:"output_bytes"`
	OutputLines     *int64     `json:"output_lines"`
	StdoutBytes     *int64     `json:"stdout_bytes"`
	StdoutLines     *int64     `json:"stdout_lines"`
	StderrBytes     *int64     `json:"stderr_bytes"`
	StderrLines     *int64     `json:"stderr_lines"`
}

type sessionObservationEventItem struct {
	sessionObservationItem
	EventID    string          `json:"event_id"`
	Kind       string          `json:"kind"`
	ObservedAt time.Time       `json:"observed_at"`
	Payload    json.RawMessage `json:"payload"`
}

type sessionObservationsRequest struct {
	ProfileEmail string `json:"profile_email"`
	UserID       string `json:"user_id"`
	LoginEmail   string `json:"login_email"`
	// HeartbeatAt says the request carries the collector's own heartbeat. Only its
	// presence counts: the row is dated by the server's clock on receipt. A request
	// may carry nothing else: the collector is alive even while no session has
	// anything to report.
	HeartbeatAt *time.Time                    `json:"heartbeat_at"`
	Snapshots   []sessionSnapshotItem         `json:"snapshots"`
	LiveStates  []sessionLiveStateItem        `json:"live_states"`
	Events      []sessionObservationEventItem `json:"events"`
}

// sessionObservationsResponse counts items: snapshots, live states and events.
// Accepted is every item in the request, Skipped those ingest refused -- blocked,
// deleted, excluded, or dated too far ahead -- and Stored those that changed a
// row. The rest found something newer already there -- an older retry, an event
// already recorded -- or a session deleted while the request was in flight.
// That is a normal outcome, not a failure. The heartbeat is not an item and is
// in none of them.
type sessionObservationsResponse struct {
	Accepted int `json:"accepted"`
	Stored   int `json:"stored"`
	Skipped  int `json:"skipped"`
}

var (
	sessionLiveStatuses  = map[string]bool{"busy": true, "idle": true, "waiting": true, "shell": true, "unknown": true, "gone": true}
	sessionStatusSources = map[string]bool{
		"legacy": true, "claude_process_file": true, "process_liveness": true,
		"process_io_unknown": true, "process_io_lifecycle": true,
	}
	sessionWaitingKinds = map[string]bool{"": true, "input": true, "permission": true, "dialog": true,
		"sandbox": true, "worker": true, "goal": true, "other": true}
	sessionIOFailureCodes = map[string]bool{
		"input_relay_failed": true, "output_relay_failed": true, "upload_failed": true,
		"resize_failed": true, "terminal_restore_failed": true,
	}
	// sessionEventKinds are the kinds a client may send. The table also holds
	// state_change and gone, but those are the server's: it derives them from
	// live states (store.IngestSessionObservations), and a client sending its
	// own would record the same change twice under two ids.
	sessionEventKinds = map[string]bool{"session_end": true}
)

const maxSessionIOCounter = int64(^uint64(0) >> 1)

func hasProcessIO(states []sessionLiveStateItem) bool {
	for _, it := range states {
		if it.IOEpoch != "" || it.IOAsOf != nil || it.IOSource != "" || it.IOMode != "" || it.IOPartial || it.IOFailureCode != "" ||
			it.InputBytes != nil || it.InputLines != nil || it.OutputBytes != nil || it.OutputLines != nil ||
			it.StdoutBytes != nil || it.StdoutLines != nil || it.StderrBytes != nil || it.StderrLines != nil {
			return true
		}
	}
	return false
}

func validateProcessIOMetrics(where string, it *sessionLiveStateItem) error {
	if !hasProcessIO([]sessionLiveStateItem{*it}) {
		return nil
	}
	if it.IOEpoch == "" {
		return fmt.Errorf("%s: io_epoch required when process I/O fields are present", where)
	}
	for _, c := range it.IOEpoch {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return fmt.Errorf("%s: io_epoch has an invalid character", where)
		}
	}
	if it.IOAsOf == nil || it.IOAsOf.IsZero() {
		return fmt.Errorf("%s: io_as_of required", where)
	}
	if it.IOSource != "process_stream" {
		return fmt.Errorf("%s: io_source must be process_stream", where)
	}
	if it.IOMode != "pty" && it.IOMode != "pipes" {
		return fmt.Errorf("%s: io_mode must be pty or pipes", where)
	}
	for _, c := range []*int64{it.InputBytes, it.InputLines, it.OutputBytes, it.OutputLines} {
		if c == nil || *c < 0 || *c > maxSessionIOCounter {
			return fmt.Errorf("%s: input/output counters must be non-negative integers", where)
		}
	}
	if it.IOPartial != (it.IOFailureCode != "") || it.IOFailureCode != "" && !sessionIOFailureCodes[it.IOFailureCode] {
		return fmt.Errorf("%s: io_partial and io_failure_code disagree", where)
	}
	if it.IOMode == "pty" {
		if it.StdoutBytes != nil || it.StdoutLines != nil || it.StderrBytes != nil || it.StderrLines != nil {
			return fmt.Errorf("%s: PTY output is merged and cannot claim separate stdout/stderr counters", where)
		}
		return nil
	}
	for _, c := range []*int64{it.StdoutBytes, it.StdoutLines, it.StderrBytes, it.StderrLines} {
		if c == nil || *c < 0 || *c > maxSessionIOCounter {
			return fmt.Errorf("%s: pipes mode needs non-negative stdout/stderr counters", where)
		}
	}
	if *it.StdoutBytes > maxSessionIOCounter-*it.StderrBytes || *it.StdoutLines > maxSessionIOCounter-*it.StderrLines ||
		*it.StdoutBytes+*it.StderrBytes != *it.OutputBytes || *it.StdoutLines+*it.StderrLines != *it.OutputLines {
		return fmt.Errorf("%s: combined output counters do not match stdout and stderr", where)
	}
	return nil
}

// handleSessionObservationsWrite stores what the collector observed about
// sessions outside their JSONL lines.
//
// A separate route rather than more fields on /api/sync. Sync carries records
// with stable keys and its counts feed every aggregate; observations are
// overwritten in place and must not move those counts. An older server answers
// 404 here, which tells a newer client to hold its observations without
// disturbing sync.
//
// It follows handleSync's ingest rules but one. Identity comes from the envelope
// -- an item names no user or profile of its own -- and a per-user token
// overrides it (resolveIngestIdentity). Sync leaves the payload's identity on its
// records, but sync only adds rows; an observation overwrites one, so a token
// that could name another user could rewrite that user's sessions. A refused
// item is counted in skipped rather than answered with a 4xx, because the client
// would retry a decision that is not going to change. Only a malformed request
// or one over the limits is a 400, and a token whose account cannot be resolved
// is a 503; either way nothing in the request is stored.
func (s *Server) handleSessionObservationsWrite(w http.ResponseWriter, r *http.Request) {
	receivedAt := time.Now()
	var req sessionObservationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request: " + cutAtRuneBoundary(err.Error(), maxEchoedDecodeErrorBytes)})
		return
	}
	if err := validateSessionObservations(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	profileEmail, userID, err := s.resolveIngestIdentity(r, s.aliases.Resolve(req.ProfileEmail), req.UserID)
	if err != nil {
		log.Printf("[api] session observations: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "identity lookup unavailable"})
		return
	}
	req.ProfileEmail, req.UserID = profileEmail, userID
	provenance := "shared_key_claim"
	if auth.IngestTokenFromContext(r.Context()) != "" {
		provenance = "personal_token"
	}
	if provenance != "personal_token" && hasProcessIO(req.LiveStates) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "process_io_requires_personal_token"})
		return
	}

	// Times are kept as sent. Cut to the receipt time, every report from a clock
	// running ahead would be dated by its arrival, and an older retry landing after
	// a newer report would take the row. What is dated past the allowance is
	// skipped instead.
	horizon := receivedAt.Add(maxSessionObservationClockAhead)
	ahead := 0
	var aheadBy time.Duration
	tooFarAhead := func(times ...*time.Time) bool {
		for _, t := range times {
			if t != nil && t.After(horizon) {
				ahead++
				aheadBy = max(aheadBy, t.Sub(receivedAt))
				return true
			}
		}
		return false
	}

	var batch store.SessionObservationBatch
	skipped := 0
	for i := range req.Snapshots {
		it := &req.Snapshots[i]
		if tooFarAhead(&it.ObservedAt) || s.refuseSessionObservation(&it.sessionObservationItem, req.LoginEmail) {
			skipped++
			continue
		}
		batch.Snapshots = append(batch.Snapshots, &store.SessionSnapshot{
			SessionID:          it.SessionID,
			UserID:             req.UserID,
			ProfileEmail:       req.ProfileEmail,
			LoginEmail:         it.LoginEmail,
			Agent:              it.Agent,
			ProjectHash:        it.ProjectHash,
			BillingProvider:    it.BillingProvider,
			AccountID:          it.AccountID,
			CollectVersion:     int(it.CollectVersion),
			IdentityProvenance: provenance,
			Snapshot:           it.Snapshot,
			ObservedAt:         it.ObservedAt,
		})
	}
	for i := range req.LiveStates {
		it := &req.LiveStates[i]
		if tooFarAhead(&it.HeartbeatAt, it.StatusChangedAt, it.IOAsOf) || s.refuseSessionObservation(&it.sessionObservationItem, req.LoginEmail) {
			skipped++
			continue
		}
		batch.LiveStates = append(batch.LiveStates, &store.SessionLiveState{
			SessionID:          it.SessionID,
			UserID:             req.UserID,
			ProfileEmail:       req.ProfileEmail,
			LoginEmail:         it.LoginEmail,
			Agent:              it.Agent,
			ProjectHash:        it.ProjectHash,
			BillingProvider:    it.BillingProvider,
			AccountID:          it.AccountID,
			PID:                it.PID,
			ProcStart:          it.ProcStart,
			IdentityProvenance: provenance,
			Status:             it.Status,
			StatusSource:       it.StatusSource,
			WaitingKind:        it.WaitingKind,
			Name:               it.Name,
			NameSource:         it.NameSource,
			StatusChangedAt:    it.StatusChangedAt,
			HeartbeatAt:        it.HeartbeatAt,
			IOEpoch:            it.IOEpoch,
			IOAsOf:             it.IOAsOf,
			IOSource:           it.IOSource,
			IOMode:             it.IOMode,
			IOPartial:          it.IOPartial,
			IOFailureCode:      it.IOFailureCode,
			InputBytes:         it.InputBytes,
			InputLines:         it.InputLines,
			OutputBytes:        it.OutputBytes,
			OutputLines:        it.OutputLines,
			StdoutBytes:        it.StdoutBytes,
			StdoutLines:        it.StdoutLines,
			StderrBytes:        it.StderrBytes,
			StderrLines:        it.StderrLines,
		})
	}
	for i := range req.Events {
		it := &req.Events[i]
		if tooFarAhead(&it.ObservedAt) || s.refuseSessionObservation(&it.sessionObservationItem, req.LoginEmail) {
			skipped++
			continue
		}
		batch.Events = append(batch.Events, &store.SessionObservationEvent{
			SessionID:          it.SessionID,
			UserID:             req.UserID,
			ProfileEmail:       req.ProfileEmail,
			LoginEmail:         it.LoginEmail,
			Agent:              it.Agent,
			ProjectHash:        it.ProjectHash,
			BillingProvider:    it.BillingProvider,
			AccountID:          it.AccountID,
			EventID:            it.EventID,
			IdentityProvenance: provenance,
			Kind:               it.Kind,
			ObservedAt:         it.ObservedAt,
			Payload:            it.Payload,
		})
	}
	// Once per request, however many items it skipped: a wrong clock is one
	// fact about one client.
	if ahead > 0 {
		log.Printf("[api] session observations: skipped %d item(s) dated more than %s past receipt (up to %s ahead) from user_id=%q profile_email=%q; check the client's clock",
			ahead, maxSessionObservationClockAhead, aheadBy.Round(time.Second), req.UserID, req.ProfileEmail)
	}
	// No profile_email, no heartbeat row -- the guard handleSync keeps on
	// UpsertClientVersion. The row is keyed by identity, and anonymous clients
	// would otherwise write into rows they share. Dated on receipt: a client's
	// clock is the one thing a heartbeat cannot vouch for.
	if req.HeartbeatAt != nil && req.ProfileEmail != "" {
		batch.Heartbeat = &store.CollectorHeartbeat{
			UserID:        req.UserID,
			ProfileEmail:  req.ProfileEmail,
			HeartbeatAt:   receivedAt,
			ClientVersion: r.Header.Get("X-Cctrace-Version"),
		}
	}

	res, err := s.store.IngestSessionObservations(r.Context(), batch)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSessionIOConflict):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "process_io_timestamp_conflict"})
		case errors.Is(err, store.ErrSessionIOEpochMismatch):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "process_io_epoch_mismatch"})
		case errors.Is(err, store.ErrSessionIOCounterRegression):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "process_io_counter_regression"})
		default:
			writeErr(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, sessionObservationsResponse{
		Accepted: len(req.Snapshots) + len(req.LiveStates) + len(req.Events),
		Stored:   res.Stored,
		Skipped:  skipped,
	})
}

// resolveIngestIdentity returns the identity that rows keyed by identity are
// written under: the session observations, and the collector_heartbeats row that
// both they and handleSync write. profileEmail and userID are the payload's,
// alias-resolved.
//
// A per-user token decides it, as it decides attribution on the OTLP receivers
// (#535). Here the token's account replaces both: profile_email, alias-resolved,
// and user_id, even when the account has none. The token is the one the ingest
// middleware accepted and left in the context (auth.HTTPMiddleware). It leaves
// none for the global API key, which names no user, and none when
// authentication is disabled, since then nothing authenticated a token; either
// way the payload's identity stands, with no lookup.
//
// A per-user token whose lookup fails, or whose account is not active or has no
// address, is an error, never the payload's identity. The middleware accepted the token before
// the body was read; one revoked or rotated in between would otherwise write
// under whatever name the payload gives. The OTLP HTTP receiver answers the same
// failure with a 503 and stores nothing (internal/otelrecv/http.go); its gRPC
// path falls back to the payload instead.
func (s *Server) resolveIngestIdentity(r *http.Request, profileEmail, userID string) (string, string, error) {
	token := auth.IngestTokenFromContext(r.Context())
	if token == "" {
		return profileEmail, userID, nil
	}
	user, err := s.store.GetDashboardUserByApiToken(r.Context(), token)
	if err != nil {
		return "", "", fmt.Errorf("resolve ingest token: %w", err)
	}
	if user == nil || !user.IsActive {
		return "", "", errors.New("resolve ingest token: it belongs to no active user")
	}
	// An account always has an address -- creating one requires it and nothing
	// changes it -- so an empty one is a row edited by hand. Refused like an
	// inactive account rather than falling back to the payload's address, the one
	// path that would otherwise leave a per-user token writing under a name it
	// did not prove.
	if user.Email == "" {
		return "", "", errors.New("resolve ingest token: its account has no address")
	}
	// Alias-resolved like the payload's address. The token's address is not
	// always the canonical one: MergeUsers leaves dashboard_users alone, so a
	// merged-away address can keep a live account and token, and without this
	// its observations and heartbeat would land under the old address while its
	// sync records land under the canonical one.
	profileEmail = s.aliases.Resolve(user.Email)
	// The account's, even when it has none. Keeping the payload's would let a
	// token whose account lost its cctrace_user_id write under another user's,
	// and observations are joined to the rest on (session_id, user_id).
	userID = user.CctraceUserID
	return profileEmail, userID, nil
}

// validateSessionObservations rejects what no item can be stored from and puts
// the rest in the form the tables store. One mistake takes the whole request, so
// a client learns of it instead of losing part of a batch without a word.
//
// Rejected: counts over the limits; an item with no session or no project_hash
// -- ingestBlock judges a project by its hash, so an item without one could never
// be refused for a block; a status or waiting kind the tables do not know, or an
// event kind other than session_end; a pid that names no process; a missing
// time; a key or identity over its cap; a snapshot or payload that is not a JSON
// object, is not valid UTF-8, holds a number past the number caps, or is larger
// than its cap.
//
// Rewritten on the way: NUL in any text becomes U+FFFD; a snapshot or payload is
// re-encoded from its decoded form; a live state's name is cut to its cap; a zero
// status_changed_at becomes no time; an event without a payload gets the empty
// object the column stores.
func validateSessionObservations(req *sessionObservationsRequest) error {
	if len(req.Snapshots) > maxSessionSnapshotsPerRequest {
		return fmt.Errorf("too many snapshots in one request (max %d)", maxSessionSnapshotsPerRequest)
	}
	if len(req.LiveStates) > maxSessionLiveStatesPerRequest {
		return fmt.Errorf("too many live_states in one request (max %d)", maxSessionLiveStatesPerRequest)
	}
	if len(req.Events) > maxSessionEventsPerRequest {
		return fmt.Errorf("too many events in one request (max %d)", maxSessionEventsPerRequest)
	}
	if req.HeartbeatAt != nil && req.HeartbeatAt.IsZero() {
		return fmt.Errorf("heartbeat_at: zero time")
	}
	replaceNUL(&req.ProfileEmail, &req.UserID, &req.LoginEmail)
	if err := checkTextCaps("",
		textCap{"profile_email", req.ProfileEmail, maxSessionObservationKeyBytes},
		textCap{"user_id", req.UserID, maxSessionObservationKeyBytes},
		textCap{"login_email", req.LoginEmail, maxSessionObservationKeyBytes}); err != nil {
		return err
	}
	for i := range req.Snapshots {
		it := &req.Snapshots[i]
		where := fmt.Sprintf("snapshots[%d]", i)
		if err := it.check(where); err != nil {
			return err
		}
		if it.ObservedAt.IsZero() {
			return fmt.Errorf("%s: observed_at required", where)
		}
		snapshot, err := normalizeJSONObject(it.Snapshot)
		if err != nil {
			return fmt.Errorf("%s: snapshot %v", where, err)
		}
		if len(snapshot) > maxSessionSnapshotBytes {
			return fmt.Errorf("%s: snapshot is %d bytes (max %d)", where, len(snapshot), maxSessionSnapshotBytes)
		}
		it.Snapshot = snapshot
	}
	for i := range req.LiveStates {
		it := &req.LiveStates[i]
		where := fmt.Sprintf("live_states[%d]", i)
		replaceNUL(&it.ProcStart, &it.Status, &it.StatusSource, &it.WaitingKind, &it.Name, &it.NameSource, &it.IOEpoch, &it.IOSource, &it.IOMode, &it.IOFailureCode)
		if it.StatusSource == "" {
			it.StatusSource = "legacy"
		}
		if err := it.check(where,
			textCap{"proc_start", it.ProcStart, maxSessionObservationKeyBytes},
			textCap{"status", it.Status, maxSessionObservationTagBytes},
			textCap{"status_source", it.StatusSource, maxSessionObservationTagBytes},
			textCap{"waiting_kind", it.WaitingKind, maxSessionObservationTagBytes},
			textCap{"io_epoch", it.IOEpoch, 64},
			textCap{"io_source", it.IOSource, maxSessionObservationTagBytes},
			textCap{"io_mode", it.IOMode, maxSessionObservationTagBytes},
			textCap{"io_failure_code", it.IOFailureCode, maxSessionObservationTagBytes},
			textCap{"name_source", it.NameSource, maxSessionObservationTagBytes}); err != nil {
			return err
		}
		switch {
		case it.PID <= 0:
			return fmt.Errorf("%s: pid must be positive", where)
		case !sessionLiveStatuses[it.Status]:
			return fmt.Errorf("%s: unknown status %q", where, it.Status)
		case !sessionStatusSources[it.StatusSource]:
			return fmt.Errorf("%s: unknown status_source %q", where, it.StatusSource)
		case it.Status == "unknown" && (!hasProcessIO([]sessionLiveStateItem{*it}) || it.StatusSource != "process_io_unknown"):
			return fmt.Errorf("%s: unknown status needs process_io_unknown source and process I/O metrics", where)
		case it.StatusSource == "process_io_unknown" && it.Status != "unknown":
			return fmt.Errorf("%s: process_io_unknown source requires unknown status", where)
		case it.StatusSource == "process_io_lifecycle" && (it.Status != "gone" || !hasProcessIO([]sessionLiveStateItem{*it})):
			return fmt.Errorf("%s: process_io_lifecycle source requires a final process I/O report", where)
		case it.StatusSource == "process_liveness" && it.Status != "gone":
			return fmt.Errorf("%s: process_liveness source requires gone status", where)
		case !sessionWaitingKinds[it.WaitingKind]:
			return fmt.Errorf("%s: unknown waiting_kind %q", where, it.WaitingKind)
		case it.HeartbeatAt.IsZero():
			return fmt.Errorf("%s: heartbeat_at required", where)
		}
		it.Name = cutAtRuneBoundary(it.Name, maxSessionLiveStateNameBytes)
		if it.StatusChangedAt != nil && it.StatusChangedAt.IsZero() {
			it.StatusChangedAt = nil
		}
		if err := validateProcessIOMetrics(where, it); err != nil {
			return err
		}
	}
	for i := range req.Events {
		it := &req.Events[i]
		where := fmt.Sprintf("events[%d]", i)
		replaceNUL(&it.EventID, &it.Kind)
		// kind is capped before the switch below can echo it.
		if err := it.check(where,
			textCap{"event_id", it.EventID, maxSessionObservationKeyBytes},
			textCap{"kind", it.Kind, maxSessionObservationTagBytes}); err != nil {
			return err
		}
		switch {
		case it.EventID == "":
			return fmt.Errorf("%s: event_id required", where)
		case !sessionEventKinds[it.Kind]:
			return fmt.Errorf("%s: unknown kind %q", where, it.Kind)
		case it.ObservedAt.IsZero():
			return fmt.Errorf("%s: observed_at required", where)
		}
		// An absent payload is an empty one; anything present has to be an object.
		if len(it.Payload) == 0 || string(it.Payload) == "null" {
			it.Payload = json.RawMessage(`{}`)
		}
		payload, err := normalizeJSONObject(it.Payload)
		if err != nil {
			return fmt.Errorf("%s: payload %v", where, err)
		}
		if len(payload) > maxSessionEventPayloadBytes {
			return fmt.Errorf("%s: payload is %d bytes (max %d)", where, len(payload), maxSessionEventPayloadBytes)
		}
		it.Payload = payload
	}
	return nil
}

// textCap is one text field and the most bytes it may hold.
type textCap struct {
	name  string
	value string
	max   int
}

func checkTextCaps(prefix string, caps ...textCap) error {
	for _, c := range caps {
		if len(c.value) > c.max {
			return fmt.Errorf("%s%s is %d bytes (max %d)", prefix, c.name, len(c.value), c.max)
		}
	}
	return nil
}

// check replaces NUL in the fields every item carries, then refuses an item with
// no session or no project_hash, or with any of those fields or more over its cap.
func (it *sessionObservationItem) check(where string, more ...textCap) error {
	replaceNUL(&it.SessionID, &it.Agent, &it.ProjectHash, &it.LoginEmail, &it.BillingProvider, &it.AccountID)
	switch {
	case it.SessionID == "":
		return fmt.Errorf("%s: session_id required", where)
	case it.ProjectHash == "":
		return fmt.Errorf("%s: project_hash required", where)
	}
	return checkTextCaps(where+": ", append([]textCap{
		{"session_id", it.SessionID, maxSessionObservationKeyBytes},
		{"project_hash", it.ProjectHash, maxSessionObservationKeyBytes},
		{"login_email", it.LoginEmail, maxSessionObservationKeyBytes},
		{"billing_provider", it.BillingProvider, maxSessionObservationKeyBytes},
		{"account_id", it.AccountID, maxSessionObservationKeyBytes},
		{"agent", it.Agent, maxSessionObservationTagBytes},
	}, more...)...)
}

// cutAtRuneBoundary cuts s to at most limit bytes at a rune boundary, so what is
// kept is still valid UTF-8.
func cutAtRuneBoundary(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// replaceNUL swaps NUL for U+FFFD, as session_records does for its tool fields:
// a TEXT column refuses the byte, and one row carrying it fails the whole batch
// the client then resends.
func replaceNUL(fields ...*string) {
	for _, f := range fields {
		*f = strings.ReplaceAll(*f, "\x00", "\uFFFD")
	}
}

// normalizeJSONObject re-encodes a snapshot or payload from its decoded form, so
// what reaches jsonb is what Go writes rather than what the client did. That is
// what removes a lone surrogate escape, which jsonb refuses and Go decodes to
// U+FFFD. Numbers stay as written (UseNumber) rather than going through float64,
// and one past the number caps is refused. Invalid UTF-8 is refused, not
// repaired: it is a malformed request. The NUL escape is left to the store,
// which replaces it in session_records too.
func normalizeJSONObject(raw json.RawMessage) (json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, errors.New("must be a JSON object")
	}
	if err := checkJSONNumbers(obj); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// checkJSONNumbers refuses a decoded value holding a number past the number caps.
// strconv.ParseFloat cannot judge it: 1e-999999 parses as 0 without an error,
// and jsonb still refuses it. The literal is not echoed -- it can be megabytes.
func checkJSONNumbers(v any) error {
	switch v := v.(type) {
	case map[string]any:
		for _, e := range v {
			if err := checkJSONNumbers(e); err != nil {
				return err
			}
		}
	case []any:
		for _, e := range v {
			if err := checkJSONNumbers(e); err != nil {
				return err
			}
		}
	case json.Number:
		if len(v) > maxSessionJSONNumberBytes {
			return fmt.Errorf("holds a number of %d bytes (max %d)", len(v), maxSessionJSONNumberBytes)
		}
		// The decoder has checked the syntax, so whatever follows e or E is a
		// signed run of digits; Atoi fails only on one too long for an int.
		if i := strings.IndexAny(string(v), "eE"); i >= 0 {
			exp, err := strconv.Atoi(string(v[i+1:]))
			if err != nil || exp > maxSessionJSONNumberExponent || exp < -maxSessionJSONNumberExponent {
				return fmt.Errorf("holds a number whose exponent is outside -%[1]d..%[1]d", maxSessionJSONNumberExponent)
			}
		}
	}
	return nil
}

// refuseSessionObservation fills what the item leaves to the envelope or to a
// default and then asks handleSync's question: is this session deleted, its
// project blocked, or its account excluded? An empty agent is claude and the
// billing provider falls back to the agent's default, both as they do for sync,
// so an account named by id alone is still matched.
func (s *Server) refuseSessionObservation(it *sessionObservationItem, envelopeLoginEmail string) bool {
	if it.LoginEmail == "" {
		it.LoginEmail = envelopeLoginEmail
	}
	it.ProjectHash = projecthash.Repair(it.ProjectHash)
	if it.Agent == "" {
		it.Agent = "claude"
	}
	if it.BillingProvider == "" {
		it.BillingProvider = defaultBillingProviderFor(it.Agent)
	}
	if s.ingestBlock == nil {
		return false
	}
	return s.ingestBlock.SessionDeleted(it.SessionID) ||
		s.ingestBlock.ProjectBlocked(it.ProjectHash) ||
		s.ingestBlock.AccountExcluded(it.BillingProvider, it.AccountID) ||
		s.ingestBlock.EmailExcluded(it.LoginEmail)
}
