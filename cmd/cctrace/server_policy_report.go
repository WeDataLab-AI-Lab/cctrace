package main

import (
	"context"
	"strings"
	"time"

	"cctrace/internal/profile"
	"cctrace/internal/store"
)

// policyDeferredSuffix ends the error of every policy run that declines to change
// settings this profile does not own alone: a config shared with another profile,
// a managed CA someone edited, a Codex [otel] block the profile did not write
// (internal/codexconfig). policyCheckResult reads it, and
// TestPolicyDeferralMessagesKeepTheSuffixTheReportReads keeps those messages and
// this constant together.
const policyDeferredSuffix = "policy deferred"

// policyReportTimeout bounds the report. It runs at the end of a command a
// daemon spawns every five minutes; a server that accepts the connection and
// then says nothing must not leave those processes piling up.
const policyReportTimeout = 10 * time.Second

// policyCheckResult names how an apply run ended, for the server.
//
// "ok" covers a run with nothing to do -- no policy for this target, or one
// already applied -- as well as one that applied a policy just now; whether the
// profile has moved is read from the applied sequence and its endpoints, not from
// this. It may carry a note in its reason: what the applied policy left alone.
//
// "deferred" is kept apart from "failed" because it never clears by itself: the
// settings it trips on stay as they are until their owner changes them. A
// failure is most often a network that was away and gone by the next run, though
// some failures wait on a person too, and the reason is what tells them apart.
func policyCheckResult(runErr error) string {
	switch {
	case runErr == nil:
		return store.ClientPolicyCheckOK
	case strings.HasSuffix(runErr.Error(), policyDeferredSuffix):
		return store.ClientPolicyCheckDeferred
	default:
		return store.ClientPolicyCheckFailed
	}
}

// buildPolicyReport describes profile `name` as a policy target after an apply
// run that ended in runErr, and returns the profile it read.
//
// Everything is read afresh, after the run: a run that applied a policy changed
// the profile's endpoints, and those new endpoints are what the next policy's
// `from` list has to hold.
//
// It fails only when there is no target to describe -- the installation id or
// the profile cannot be read. An unreadable state file is reported instead: the
// device is still a target, and a missing row would read as a client too old to
// report.
func buildPolicyReport(name string, runErr error) (store.ClientPolicyReport, *profile.Profile, error) {
	target, err := serverPolicyTarget(name)
	if err != nil {
		return store.ClientPolicyReport{}, nil, err
	}
	p, err := loadProfile(name)
	if err != nil {
		return store.ClientPolicyReport{}, nil, err
	}
	endpoints := policyEndpoints(p)
	report := store.ClientPolicyReport{
		ProfileEmail:   policyReportAccount(name, p),
		UserID:         resolveUserID(p),
		InstallationID: target.InstallationID,
		Profile:        target.Profile,
		SyncEndpoint:   endpoints.Sync,
		OTELEndpoint:   endpoints.OTEL,
		Protocol:       endpoints.Protocol,
		CheckResult:    policyCheckResult(runErr),
	}
	if runErr != nil {
		report.CheckReason = runErr.Error()
	}
	state, err := readServerPolicyState(name)
	if err != nil {
		// The run reads this file first and fails on it with the bare decode
		// error, which names nothing. Say what could not be read.
		if runErr == nil || runErr.Error() == err.Error() {
			report.CheckResult = store.ClientPolicyCheckFailed
			report.CheckReason = "read policy state: " + err.Error()
		}
		report.CheckReason = cutPolicyReason(redactPolicyReason(report.CheckReason))
		return report, p, nil
	}
	report.AppliedSequence = state.Sequence
	// An applied policy's reason is a note on what it left alone. The server keeps
	// the reason of any result, and this is how the operator sees it.
	if runErr == nil && state.Status == "applied" {
		report.CheckReason = state.Reason
	}
	// A failed run records the sequence it had reached: the policy's own once it
	// was fetched and verified, the applied one before that. The applied sequence
	// above does not move on failure, so this is the only word on which.
	if n := len(state.History); runErr != nil && n > 0 {
		// The record holds the reason as the run's recorder wrote it: redacted.
		if last := state.History[n-1]; last.Status == "failed" && last.Reason == redactPolicyReason(runErr.Error()) {
			report.CheckSequence = last.Sequence
		}
	}
	report.CheckReason = cutPolicyReason(redactPolicyReason(report.CheckReason))
	return report, p, nil
}

// policyReportAccount is the account this profile's uploads are filed under.
//
// A daemon started with --profile-email syncs as that account, not the
// profile's. The report has to name the same one, or the operator's list shows
// the syncing account as never having reported and the reporting account as
// never having synced. The running daemon's record carries what it was started
// with.
func policyReportAccount(name string, p *profile.Profile) string {
	if rt, ok, err := readSyncRuntime(name); err == nil && ok && rt.ProfileEmail != "" {
		return rt.ProfileEmail
	}
	return resolveProfileEmail(p, "")
}

// cutPolicyReason keeps the reason within what the server stores. The server
// cuts it too; cutting here keeps an unusually long error from pushing the whole
// report past the route's body limit, which would drop the report instead.
func cutPolicyReason(reason string) string {
	if r := []rune(reason); len(r) > store.ClientPolicyReasonMax {
		return string(r[:store.ClientPolicyReasonMax])
	}
	return reason
}

// reportServerPolicy tells the server what this profile is as a policy target and
// how the apply run that just ended went.
//
// It is best effort and has no result: the run it follows is already over, and a
// server that cannot take the report must not make that run look failed. A
// profile that is not logged in sends nothing -- the route is authenticated, and
// a 401 every five minutes says nothing the sync log does not already say.
func reportServerPolicy(ctx context.Context, name string, runErr error) {
	report, p, err := buildPolicyReport(name, runErr)
	if err != nil {
		diagf("[policy] report: %v", err)
		return
	}
	if p.Server.AuthToken == "" {
		return
	}
	client, err := newSyncClient(p, profileHTTPAPIEndpoint(p), version, name)
	if err != nil {
		diagf("[policy] report: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, policyReportTimeout)
	defer cancel()
	if err := client.SendPolicyReport(ctx, report); err != nil {
		diagf("[policy] report: %v", err)
	}
}
