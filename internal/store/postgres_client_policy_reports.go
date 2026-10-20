package store

import (
	"context"
	"fmt"
	"math"
)

// ClientPolicyReport is what one install says about itself as a recipient of
// signed server policies, after each `config policy apply` run.
//
// A policy names its recipients as (installation id, profile) pairs and lists the
// endpoint combinations it may move a client away from. Both lived only on the
// device, and so did the outcome: an operator had to run a command on every
// machine to write a policy, and had no way to see which machines then applied
// it or were deferred without a word.
//
// It is self-reported by an authenticated client and not checked against
// anything: a client can report another install's pair and overwrite that row.
// The list it produces is a starting point for writing a policy, which is signed
// and bound to one server host regardless of what was reported.
type ClientPolicyReport struct {
	ProfileEmail string `json:"profile_email,omitempty"`
	UserID       string `json:"user_id,omitempty"`

	// InstallationID and Profile are the policy target. Profile is empty for the
	// default profile.
	InstallationID string `json:"installation_id"`
	Profile        string `json:"profile"`

	// The endpoints the profile uses now, in the spelling the client compares
	// against a policy's `from` list.
	SyncEndpoint string `json:"sync_endpoint"`
	OTELEndpoint string `json:"otel_endpoint"`
	Protocol     string `json:"protocol"`

	// AppliedSequence is the last policy this profile applied; zero means none.
	// It does not move when a later policy fails, which is why the outcome of the
	// run that produced this report is carried separately.
	AppliedSequence uint64 `json:"applied_sequence"`
	// CheckResult is that outcome: "ok" (no policy for this target, already
	// applied, or applied just now), "deferred" (the client declined to touch
	// settings it does not own alone) or "failed".
	CheckResult string `json:"check_result"`
	// CheckSequence is the sequence a failed or deferred run had reached: the
	// policy's own once the client fetched and verified it, the applied one when
	// the run failed before that (a policy it could not fetch or verify).
	CheckSequence uint64 `json:"check_sequence,omitempty"`
	// CheckReason is the client's own error text. It names local paths and other
	// profiles, which is what makes it actionable. An ok run can carry one too: the
	// note that a policy left the default Codex config as it found it, naming that
	// file's absolute local path until the next policy replaces the state's reason.
	CheckReason string `json:"check_reason,omitempty"`

	// ClientVersion comes from the X-Cctrace-Version header, not the body.
	ClientVersion string `json:"-"`
}

// Values CheckResult takes.
const (
	ClientPolicyCheckOK       = "ok"
	ClientPolicyCheckDeferred = "deferred"
	ClientPolicyCheckFailed   = "failed"
)

// ClientPolicyReasonMax bounds the client's error text, on runes: a path under a
// non-ASCII home directory cut mid-character is invalid UTF-8, and Postgres
// refuses the whole row.
const ClientPolicyReasonMax = 500

// UpsertClientPolicyReport records the latest report for one policy target.
//
// The key is the target, not the account: one account runs several devices and
// one device several profiles, and each is a separate recipient. first_seen_at is
// kept; everything else is the latest value, except client_version, where an
// empty header does not erase what the install said before -- the rule
// UpsertClientVersion follows for the same header.
func (s *PgStore) UpsertClientPolicyReport(ctx context.Context, r ClientPolicyReport) error {
	if r.AppliedSequence > math.MaxInt64 || r.CheckSequence > math.MaxInt64 {
		return fmt.Errorf("client policy report: sequence exceeds the stored range")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO client_policy_reports (
			installation_id, profile, profile_email, user_id,
			sync_endpoint, otel_endpoint, protocol,
			applied_sequence, check_result, check_sequence, check_reason, client_version
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (installation_id, profile) DO UPDATE SET
			profile_email    = EXCLUDED.profile_email,
			user_id          = EXCLUDED.user_id,
			sync_endpoint    = EXCLUDED.sync_endpoint,
			otel_endpoint    = EXCLUDED.otel_endpoint,
			protocol         = EXCLUDED.protocol,
			applied_sequence = EXCLUDED.applied_sequence,
			check_result     = EXCLUDED.check_result,
			check_sequence   = EXCLUDED.check_sequence,
			check_reason     = EXCLUDED.check_reason,
			client_version   = COALESCE(NULLIF(EXCLUDED.client_version, ''), client_policy_reports.client_version),
			last_seen_at     = now()`,
		r.InstallationID, r.Profile, r.ProfileEmail, r.UserID,
		r.SyncEndpoint, r.OTELEndpoint, r.Protocol,
		int64(r.AppliedSequence), r.CheckResult, int64(r.CheckSequence), r.CheckReason, r.ClientVersion,
	)
	return err
}
