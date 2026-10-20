package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"cctrace/internal/store"
)

// Bounds on what a client policy report may carry. The endpoints and the profile
// name are refused when they exceed theirs rather than cut -- see
// validateClientPolicyReport.
const (
	// maxClientPolicyReportBodyBytes is far above a well-formed report, whose one
	// long field is a reason the client cuts before sending.
	maxClientPolicyReportBodyBytes = 16 << 10

	clientPolicyEndpointMax = 512
	clientPolicyProtocolMax = 32
	clientPolicyAccountMax  = 320
)

// clientPolicyProfileRe is internal/profile's name rule. The server keeps a copy
// because that package reads the local home directory and has no business in the
// server; TestClientPolicyReport_profileRuleAgreesWithTheClient holds the two
// together.
var clientPolicyProfileRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

// validClientPolicyProfile reports whether name can be a policy target's profile:
// empty for the default profile, or a valid named profile. "default" is the
// client's reserved word for the former and never a name it sends.
func validClientPolicyProfile(name string) bool {
	return name == "" || (name != "default" && clientPolicyProfileRe.MatchString(name))
}

// handleClientPolicyReport records what one install says about itself as a
// recipient of signed server policies (#848).
//
// A client sends this after every `config policy apply` run -- a watching daemon
// runs one every five minutes -- whatever the run found, so a device shows up
// here before any policy names it. That is the point: the pair a policy must name
// and the endpoints its `from` list must hold existed only on the device.
func (s *Server) handleClientPolicyReport(w http.ResponseWriter, r *http.Request) {
	var report store.ClientPolicyReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request: " + err.Error()})
		return
	}
	if err := validateClientPolicyReport(&report); err != nil {
		// A refused report leaves the device looking like one that never
		// reported, and the reason is otherwise only in that device's own log.
		log.Printf("[api] client policy report refused: %v", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	report.ProfileEmail = s.aliases.Resolve(report.ProfileEmail)
	report.ClientVersion = r.Header.Get("X-Cctrace-Version")
	if err := s.store.UpsertClientPolicyReport(r.Context(), report); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateClientPolicyReport refuses a report the server could not store as
// written, and tidies the one field it may cut.
//
// Refusing instead of truncating is deliberate for the identifying fields. A cut
// profile name would merge two targets into one row, and a cut endpoint pasted
// into a policy's `from` list never matches the client it came from -- both worse
// than a missing row, which at least says the device has not reported. The reason
// is different: it is free text for a person, so its tail is cut and its line
// breaks flattened.
//
// The protocol is not checked against the values a policy can carry. A profile
// set to something else cannot be moved by policy, and the operator has to see
// that device to know it.
func validateClientPolicyReport(r *store.ClientPolicyReport) error {
	if parsed, err := uuid.Parse(r.InstallationID); err != nil || parsed.String() != r.InstallationID {
		return errors.New("installation_id is not a canonical UUID")
	}
	if !validClientPolicyProfile(r.Profile) {
		return errors.New("profile is not a valid profile name")
	}
	for _, f := range []struct {
		name, value string
		max         int
	}{
		{"sync_endpoint", r.SyncEndpoint, clientPolicyEndpointMax},
		{"otel_endpoint", r.OTELEndpoint, clientPolicyEndpointMax},
		{"protocol", r.Protocol, clientPolicyProtocolMax},
		{"profile_email", r.ProfileEmail, clientPolicyAccountMax},
		{"user_id", r.UserID, clientPolicyAccountMax},
	} {
		if len(f.value) > f.max {
			return fmt.Errorf("%s is longer than %d bytes", f.name, f.max)
		}
		if strings.IndexFunc(f.value, unicode.IsControl) >= 0 {
			return fmt.Errorf("%s carries a control character", f.name)
		}
	}
	switch r.CheckResult {
	case store.ClientPolicyCheckOK, store.ClientPolicyCheckDeferred, store.ClientPolicyCheckFailed:
	default:
		return errors.New("check_result is not ok, deferred or failed")
	}
	if r.AppliedSequence > math.MaxInt64 || r.CheckSequence > math.MaxInt64 {
		return errors.New("sequence exceeds the stored range")
	}
	r.CheckReason = tidyClientPolicyReason(r.CheckReason)
	return nil
}

// tidyClientPolicyReason turns the client's error text into one line of at most
// store.ClientPolicyReasonMax runes. Runs of whitespace and control characters
// become a single space; the cut is on runes so a multi-byte character is never
// split.
func tidyClientPolicyReason(reason string) string {
	reason = strings.Join(strings.FieldsFunc(reason, func(c rune) bool {
		return unicode.IsSpace(c) || unicode.IsControl(c)
	}), " ")
	if r := []rune(reason); len(r) > store.ClientPolicyReasonMax {
		reason = string(r[:store.ClientPolicyReasonMax])
	}
	return reason
}
