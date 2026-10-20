package sessionfacts

import (
	"sort"
	"strings"
	"time"

	"cctrace/internal/gitcmd"
)

type codexCommitCandidate struct {
	item SnapshotCommit
	tree CommitTree
}

func (c *computation) readCodexCommits() CommitFacts {
	out := CommitFacts{State: "observed"}
	if len(c.in.CodexRows) == 0 {
		out.State = "not_collected"
		return out
	}
	calls := make(map[commitCallKey][]*CodexRow)
	results := make(map[commitCallKey][]*CodexRow)
	commands := make(map[commitCallKey]gitcmd.Commands)
	counts := make(map[commitCallKey]int)
	unknown := false
	for i := range c.in.CodexRows {
		c.pulse()
		row := &c.in.CodexRows[i]
		if row.RecordType == "tool_output" && row.ToolCallID != "" && !c.copied(row.ID) {
			key := commitCallKey{file: row.SourceFile, id: row.ToolCallID}
			results[key] = append(results[key], row)
			continue
		}
		if row.RecordType != "tool_call" || !codexShellName(row.ToolName) || c.copied(row.ID) {
			continue
		}
		key := commitCallKey{file: row.SourceFile, id: row.ToolCallID}
		if key.file == "" || key.id == "" {
			unknown = true
			continue
		}
		parsed := gitcmd.ParseCodex(row.ToolName, row.ToolArgs, row.CWD)
		if c.stream != nil {
			parsed = c.stream.codexCommands[row].parsed
		}
		parsed.Incomplete = parsed.Incomplete || row.ToolArgsTruncated
		commands[key] = parsed
		if parsed.Incomplete {
			unknown = true
		}
		for _, op := range parsed.Git {
			c.pulse()
			if op.Kind == "commit" {
				counts[key]++
			}
		}
		calls[key] = append(calls[key], row)
	}

	checks := make(map[commitClaimKey]bool)
	checked := make(map[commitClaimKey]bool)
	for _, claim := range c.in.CommitClaims {
		c.pulse()
		key := commitClaimKey{claim.Repository, claim.Hash}
		checks[key] = checks[key] || claim.Conflicting
		checked[key] = true
	}
	candidates := make(map[commitCallKey][]codexCommitCandidate)
	disputed := make(map[commitClaimKey]bool)
	claimedBy := make(map[commitClaimKey]commitCallKey)
	for _, snap := range c.in.Snapshots {
		c.pulse()
		if snap.Commits == nil {
			continue
		}
		if len(snap.Commits.Items) > 100 || len(snap.Commits.Trees) > 100 {
			unknown, out.Truncated = true, true
			continue
		}
		if snap.Commits.Malformed {
			unknown = true
		}
		for _, name := range snap.Truncated {
			c.pulse()
			if name == "commits" {
				out.Truncated = true
			}
		}
		for _, item := range snap.Commits.Items {
			c.pulse()
			key := commitCallKey{file: item.Transcript, id: item.Call}
			rows := calls[key]
			if len(rows) != 1 || !fullCommitHash(item.Hash) || !commitKind(item.Kind) || item.Proof != "output_hash" || item.At <= 0 || item.Tree < 0 || item.Tree >= len(snap.Commits.Trees) || (item.Replaces != "" && (!fullCommitHash(item.Replaces) || item.Kind != "amend" || item.Replaces == item.Hash)) {
				unknown = true
				continue
			}
			row := rows[0]
			if row.UserID != snap.UserID || row.ProfileEmail != snap.ProfileEmail || row.LoginEmail != snap.LoginEmail || row.BillingProvider != snap.BillingProvider || row.AccountID != snap.AccountID {
				unknown = true
				continue
			}
			tree := snap.Commits.Trees[item.Tree]
			claim := commitClaimKey{tree.Repository, item.Hash}
			if tree.Repository == "" || !checked[claim] || checks[claim] {
				unknown = true
				disputed[claim] = true
			}
			if previous, ok := claimedBy[claim]; ok && previous != key {
				disputed[claim], unknown = true, true
			}
			claimedBy[claim] = key
			candidates[key] = append(candidates[key], codexCommitCandidate{item, tree})
		}
	}

	keys := make([]commitCallKey, 0, len(calls))
	for key := range calls {
		c.pulse()
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].file != keys[j].file {
			return keys[i].file < keys[j].file
		}
		return keys[i].id < keys[j].id
	})
	for _, key := range keys {
		c.pulse()
		rows := calls[key]
		relevant := counts[key] > 0 || len(candidates[key]) > 0
		if len(rows) != 1 {
			unknown = unknown || relevant
			continue
		}
		if len(results[key]) != 1 {
			unknown = unknown || relevant
			continue
		}
		call, result := rows[0], results[key][0]
		if !sameCodexIdentity(call, result) || result.TS.Before(call.TS) {
			unknown = unknown || relevant
			continue
		}
		parsed := commands[key]
		var output gitcmd.Output
		var resultTruncated, failed bool
		if c.stream != nil {
			e, found := c.codexResult(result)
			if !found || e.Redacted {
				unknown = unknown || relevant
				continue
			}
			output = e.Output
			resultTruncated = e.Truncated
			failed = e.Failed
			if resultTruncated {
				output.Incomplete = true
			}
		} else {
			resultText, resultStatus, truncated, resultFound := codexToolOutput(c, result)
			resultTruncated = truncated
			if !resultFound || resultText == redactedMarker {
				unknown = unknown || relevant
				continue
			}
			output = gitcmd.ReadOutput(resultText)
			failed = codexOutputFailed(resultStatus, resultText)
			if resultTruncated {
				output.Incomplete = true
			}
		}

		if relevant && failed && len(output.Hashes) == 0 {
			unknown = true
			continue
		}
		if output.Incomplete {
			unknown = true
		}
		turn := codexTurnForCall(c, call)
		base := Commit{TurnKey: turn, Call: key.id, Transcript: key.file, CallSrecID: call.ID, Kind: "commit", SubjectState: "none"}
		if parsedOneCommit := singleCommitOperation(parsed); parsedOneCommit != nil {
			if parsedOneCommit.Amend {
				base.Kind = "amend"
			}
			if parsedOneCommit.MessageKnown {
				base.Subject, base.SubjectState = commitSubject(&parsedOneCommit.Message)
			}
		}
		if len(parsed.Git) > 0 {
			base.Repository, base.Branch = call.RepositoryID, call.Branch
			if base.Repository == "" && strings.HasPrefix(c.session.RepositoryKey, "github.com/") {
				base.Repository = c.session.RepositoryKey
			}
		}
		verified := make(map[commitClaimKey]Commit)
		bad := make(map[commitClaimKey]bool)
		for _, candidate := range candidates[key] {
			c.pulse()
			item, tree := candidate.item, candidate.tree
			claim := commitClaimKey{tree.Repository, item.Hash}
			if disputed[claim] || (parsed.Incomplete && len(parsed.Git) > 0) || !codexAllowsCommit(parsed, item.Kind) {
				unknown = true
				continue
			}
			if len(output.Hashes) > 0 && !codexReceiptMatches(item.Hash, output.Hashes, candidates[key]) {
				unknown = true
				continue
			}
			r := base
			r.Hash, r.Short, r.Kind, r.Source = item.Hash, item.Hash[:7], item.Kind, "git"
			r.CommittedAt = time.Unix(item.At, 0).UTC()
			r.Repository, r.Branch, r.Replaces = tree.Repository, tree.Branch, item.Replaces
			if r.Repository == c.session.RepositoryKey {
				r.Repository = ""
			}
			r.Subject, r.SubjectState = commitSubject(item.Subject)
			if previous, ok := verified[claim]; ok {
				if previous.CommittedAt != r.CommittedAt || previous.Kind != r.Kind || previous.Branch != r.Branch || previous.Replaces != r.Replaces {
					bad[claim], unknown = true, true
				}
				if previous.SubjectState == "redacted" || previous.SubjectState == "withheld" {
					r.Subject, r.SubjectState = "", previous.SubjectState
				} else if previous.SubjectState == "ok" && r.SubjectState == "ok" && previous.Subject != r.Subject {
					r.Subject, r.SubjectState = "", "withheld"
				}
			}
			verified[claim] = r
		}
		for claim, item := range verified {
			c.pulse()
			if !bad[claim] {
				out.Items = append(out.Items, item)
			}
		}
		consumed := 0
		for _, item := range verified {
			c.pulse()
			if item.Kind == "commit" || item.Kind == "amend" {
				consumed++
			}
		}
		remaining := counts[key] - consumed
		for _, hash := range output.Hashes {
			c.pulse()
			used := false
			for _, item := range verified {
				c.pulse()
				used = used || strings.HasPrefix(item.Hash, hash)
			}
			if used {
				continue
			}
			if remaining <= 0 {
				unknown = true
				break
			}
			r := base
			r.Source, r.Short, r.CommittedAt = "output", commitShort(hash), result.TS.UTC()
			out.Items = append(out.Items, r)
			remaining--
		}
		if len(parsed.Git) > 1 && remaining > 0 {
			unknown = true
			continue
		}
		if !failed && !resultTruncated && !output.Incomplete && !output.NothingToCommit {
			for i := 0; i < remaining; i++ {
				c.pulse()
				r := base
				r.Source, r.CommittedAt = "call", result.TS.UTC()
				out.Items = append(out.Items, r)
			}
		}
	}
	applyAmendFolding(&out)
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if !a.CommittedAt.Equal(b.CommittedAt) {
			return a.CommittedAt.Before(b.CommittedAt)
		}
		if a.Transcript != b.Transcript {
			return a.Transcript < b.Transcript
		}
		if a.Call != b.Call {
			return a.Call < b.Call
		}
		if a.Repository != b.Repository {
			return a.Repository < b.Repository
		}
		return a.Hash < b.Hash
	})
	total := len(out.Items)
	if total > 500 {
		out.Items, out.Truncated = out.Items[total-500:], true
		unknown = true
	}
	for i := range out.Items {
		c.pulse()
		out.Items[i].Ordinal = i + 1
	}
	if unknown {
		out.State = "undeterminable"
	} else {
		out.Total = &total
	}
	return out
}

func singleCommitOperation(parsed gitcmd.Commands) *gitcmd.Operation {
	var found *gitcmd.Operation
	for i := range parsed.Git {
		if parsed.Git[i].Kind != "commit" {
			continue
		}
		if found != nil {
			return nil
		}
		found = &parsed.Git[i]
	}
	return found
}

func codexAllowsCommit(parsed gitcmd.Commands, kind string) bool {
	for _, op := range parsed.Git {
		if op.Kind == "commit" && (kind == "commit" || kind == "amend" && op.Amend) {
			return true
		}
		if op.Kind == kind {
			return true
		}
	}
	return false
}

func codexReceiptMatches(hash string, receipts []string, candidates []codexCommitCandidate) bool {
	for _, receipt := range receipts {
		if !strings.HasPrefix(hash, receipt) {
			continue
		}
		unique := true
		for _, other := range candidates {
			if other.item.Hash != hash && strings.HasPrefix(other.item.Hash, receipt) {
				unique = false
			}
		}
		if unique {
			return true
		}
	}
	return false
}

func applyAmendFolding(out *CommitFacts) {
	replaced := make(map[commitClaimKey]bool)
	for _, item := range out.Items {
		if item.Source == "git" && item.Replaces != "" {
			replaced[commitClaimKey{item.Repository, item.Replaces}] = true
		}
	}
	kept := out.Items[:0]
	for _, item := range out.Items {
		if item.Source != "git" || !replaced[commitClaimKey{item.Repository, item.Hash}] {
			kept = append(kept, item)
		}
	}
	out.Items = kept
}
