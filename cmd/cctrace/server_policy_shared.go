package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sort"

	"cctrace/internal/atomicfile"
	"cctrace/internal/codexconfig"
	"cctrace/internal/profile"
)

// writeSharedPolicyConfigFn writes a shared Codex config under the locks its
// caller holds.
var writeSharedPolicyConfigFn = atomicfile.Write

// errSharedPolicyWritePending is what a run reports while a recorded shared
// write could not be carried out (a lock, a write); the next run retries it.
var errSharedPolicyWritePending = errors.New("shared settings write pending")

type sharedWriteOutcome int

const (
	sharedWriteDone    sharedWriteOutcome = iota // nothing left to do; the record goes
	sharedWriteStale                             // the record no longer fits; it goes, noted
	sharedWriteKept                              // the default Codex config holds another's block; it goes, noted
	sharedWriteEdited                            // waits on the file's owner
	sharedWritePending                           // retried by the next run
)

// drainRecordedSharedPolicyWrites carries out recorded shared writes at the
// start of a run, whatever the server answers later. The caller holds the
// global policy lock; this takes the recorded files' locks after it, the order
// a sibling's sync startup uses, and releases them before the caller fetches.
// The locks are waited for until ctx ends; a lock that stays busy leaves the
// writes pending like any other lock it cannot get.
func drainRecordedSharedPolicyWrites(ctx context.Context, name string) error {
	// The caller let go of the global lock after checking for recovery. A
	// command interrupted since leaves a journal with the state file at its
	// After bytes; rewriting that file would leave a journal that never
	// recovers. The writes wait until recovery has run.
	if serverPolicyRecoveryPending(name) {
		return errSharedPolicyWritePending
	}
	state, err := readServerPolicyState(name)
	if err != nil || len(state.SharedWrites) == 0 {
		return err
	}
	var paths []string
	for _, w := range state.SharedWrites {
		paths = append(paths, w.Path)
	}
	sort.Strings(paths)
	release, held, err := lockPolicyFiles(ctx, paths, false)
	if err != nil {
		return errSharedPolicyWritePending
	}
	defer release()
	return drainSharedPolicyWrites(name, held)
}

// drainSharedPolicyWrites carries out the shared writes the committed state
// records, writing only paths whose locks are in held. The caller holds the
// global policy lock. It returns what the run reports while writes remain: the
// deferral of a block someone edited, else that a write is still pending.
func drainSharedPolicyWrites(name string, held map[string]bool) error {
	state, err := readServerPolicyState(name)
	if err != nil || len(state.SharedWrites) == 0 {
		return err
	}
	p, err := loadProfile(name)
	if err != nil {
		return err
	}
	// An unreadable sibling only leaves its block unrecognized.
	siblings, _ := sharedPolicySettings(p, name)
	// The failure the waiting writes left, unless a later policy failed since;
	// that one names a higher sequence. Read before the notes below join the history.
	n := len(state.History)
	leftFailure := n > 0 && state.Status == "failed" &&
		(state.Reason == errSharedPolicyWritePending.Error() || state.Reason == errSharedCodexEdited.Error()) &&
		state.History[n-1].Sequence == state.Sequence && state.History[n-1].Reason == state.Reason
	var kept []sharedPolicyWrite
	var keptNote string
	edited, pending := false, false
	for _, w := range state.SharedWrites {
		switch drainSharedPolicyWrite(p, siblings, w, held[w.Path]) {
		case sharedWriteStale:
			appendPolicyHistory(&state, state.Sequence, "cleared", redactPolicyReason("stale shared Codex write: "+w.Path))
		case sharedWriteKept:
			keptNote = defaultCodexKeptNote(w.Path)
			appendPolicyHistory(&state, state.Sequence, "cleared", keptNote)
		case sharedWriteEdited:
			kept, edited = append(kept, w), true
		case sharedWritePending:
			kept, pending = append(kept, w), true
		}
	}
	// A write still waiting changes nothing: like any run with no policy to
	// apply, it leaves the state file alone.
	if len(kept) < len(state.SharedWrites) {
		state.SharedWrites = kept
		// The failure the waiting writes left is over with them: the policy is
		// applied again, with the note its first applied record carried.
		if len(kept) == 0 && leftFailure {
			appendPolicyRecord(&state, state.Sequence, "applied", joinPolicyNotes(appliedNote(state), keptNote))
		}
		// What the policy left alone is told in an applied state's reason.
		if keptNote != "" && state.Status == "applied" {
			state.Reason = joinPolicyNotes(state.Reason, keptNote)
		}
		path, err := policyPath(name, "server-policy-state.json")
		if err != nil {
			return err
		}
		b, err := json.MarshalIndent(state, "", "  ")
		if err == nil {
			err = atomicfile.Write(path, b, 0600)
		}
		if err != nil {
			pending = true
		}
	}
	switch {
	case edited:
		return errSharedCodexEdited
	case pending:
		return errSharedPolicyWritePending
	}
	return nil
}

// appliedNote is the note the latest applied record of the state's sequence carries.
func appliedNote(state serverPolicyState) string {
	for i := len(state.History) - 1; i >= 0; i-- {
		if r := state.History[i]; r.Status == "applied" && r.Sequence == state.Sequence {
			return r.Reason
		}
	}
	return ""
}

// drainSharedPolicyWrite rebuilds the recorded move from the profile as it is
// now and the recorded old values, and carries it out when the config still
// holds the old block. The blocks this profile and its siblings write now are
// all the move can end at; anything else was edited, and waits on its owner,
// except in the default Codex folder, where the profile has moved and the config
// stays as it is.
func drainSharedPolicyWrite(p *profile.Profile, siblings []policySibling, w sharedPolicyWrite, locked bool) sharedWriteOutcome {
	content, err := os.ReadFile(w.Path)
	if os.IsNotExist(err) {
		return sharedWriteDone
	}
	if err != nil {
		return sharedWritePending
	}
	token := p.Server.AuthToken
	old := codexconfig.PolicyBlock{Endpoint: w.OldEndpoint, Token: token, CA: w.OldCAFile}
	next := codexconfig.PolicyBlock{Endpoint: p.Server.Endpoint, Token: token, CA: p.Server.CACertFile}
	var others []codexconfig.PolicyBlock
	for _, s := range siblings {
		if slices.Contains(s.codex, w.Path) {
			others = append(others, codexconfig.PolicyBlock{Endpoint: s.profile.Server.Endpoint, Token: s.profile.Server.AuthToken, CA: s.profile.Server.CACertFile})
		}
	}
	plan, err := codexconfig.PlanSharedServerPolicyConfig(content, old, next, others)
	if err != nil {
		// Not any profile's block, as in planSharedCodexMove.
		if errors.Is(err, codexconfig.ErrMultilineRootOtelKey) && isDefaultCodexConfig(w.Path) {
			return sharedWriteKept
		}
		return sharedWriteEdited
	}
	// A reissued token or a later move changes both blocks: the record no
	// longer describes a move this file can make.
	if plan.OldSHA256 != w.OldSHA256 || plan.NewSHA256 != w.NewSHA256 {
		return sharedWriteStale
	}
	switch plan.Holds {
	case codexconfig.SharedBlockOld:
		// The recorded path is canonical; a link put there since is not followed.
		if current, err := canonicalPolicyPath(w.Path); !locked || err != nil || current != w.Path {
			return sharedWritePending
		}
		if writeSharedPolicyConfigFn(w.Path, plan.Content, 0600) != nil {
			return sharedWritePending
		}
		return sharedWriteDone
	case codexconfig.SharedBlockOther:
		if isDefaultCodexConfig(w.Path) {
			return sharedWriteKept
		}
		return sharedWriteEdited
	}
	// The new block, or a sibling's, which the sibling moves itself.
	return sharedWriteDone
}
