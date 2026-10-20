package sessionfacts

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"cctrace/internal/gitcmd"
)

// SnapshotCommits is the bounded client observation, not a server attribution.
// Subject distinguishes absent (withheld), a redaction marker, and content.
type SnapshotCommits struct {
	Trees      []CommitTree
	Items      []SnapshotCommit
	Unresolved []UnresolvedCommit
	Malformed  bool
}

type CommitTree struct{ Repository, Branch string }
type SnapshotCommit struct {
	Hash, Kind, Call, Transcript, Proof, Replaces string
	At                                            int64
	Parents, Tree                                 int
	Subject                                       *string
}
type UnresolvedCommit struct {
	Call, Transcript, Reason string
	At                       int64
}

// CommitResult must have been selected by both row id and tool_use id. Text is
// transient input, never part of the output. Truncation prevents a complete count.
type CommitResult struct {
	SrecID                 int64
	SourceFile, Call, Text string
	Truncated              bool
	Status                 string
}

// CommitClaim describes a check made in the loader's database snapshot. The
// store must fence that check again before publishing; Compute performs no I/O.
type CommitClaim struct {
	Repository, Hash string
	Conflicting      bool
}

// CommitOutput is the opt-in result; Output remains the unchanged r2 contract.
type CommitOutput struct {
	Output
	Commits CommitFacts
}

type CommitFacts struct {
	State     string
	Total     *int
	Truncated bool
	Items     []Commit
}

type Commit struct {
	Ordinal                            int
	TurnKey, Call, Transcript          string
	CallSrecID                         int64
	Hash, Short, Subject, SubjectState string
	CommittedAt                        time.Time
	Kind, Source, Repository, Branch   string
	Replaces                           string
	Refs                               []string
}

type commitCallKey struct{ file, id string }
type commitClaimKey struct{ repository, hash string }
type commitCall struct {
	line      *line
	results   []*line
	command   gitcmd.Commands
	readable  bool
	uncertain bool
	count     int
}
type commitCandidate struct {
	item SnapshotCommit
	tree CommitTree
}

func fullCommitHash(hash string) bool {
	return (len(hash) == 40 || len(hash) == 64) && gitcmd.ValidHash(hash)
}

// ValidSessionCommitHash is the store loader's bounded claim-key validator.
func ValidSessionCommitHash(hash string) bool { return fullCommitHash(hash) }

func commitKind(kind string) bool {
	switch kind {
	case "commit", "amend", "merge", "cherry_pick", "revert":
		return true
	}
	return false
}

func commitSubject(subject *string) (string, string) {
	if subject == nil {
		return "", "withheld"
	}
	if *subject == redactedMarker {
		return "", "redacted"
	}
	if *subject == "" {
		return "", "none"
	}
	text := *subject
	if len(text) > 200 {
		cut := 200
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return text, "ok"
}

func (c *computation) readCommits() CommitFacts {
	out := CommitFacts{State: "not_collected"}
	if c.in.Agent == "codex" {
		return c.readCodexCommits()
	}
	if c.in.Agent != "claude" {
		return out
	}
	collected, unknown := false, false
	uses := map[commitCallKey][]*commitCall{}
	results := map[commitCallKey][]*line{}
	invalid := map[commitCallKey]bool{}
	for _, l := range c.lines {
		c.pulse()
		if l.collectionVersion() >= 2 {
			collected = true
		}
		if c.copied(l.ID) {
			continue
		}
		if l.RecordType == "assistant" && (l.contentRedacted() || !l.blocksValid()) {
			unknown = true
		}
		for blockIndex, b := range l.blocks {
			c.pulse()
			key := commitCallKey{l.SourceFile, b.ID}
			if b.Type == "tool_result" && b.ToolUseID != "" {
				key.id = b.ToolUseID
				if l.RecordType != "user" || l.ContentKind != "array" {
					invalid[key], unknown = true, true
					continue
				}
				results[key] = append(results[key], l)
			}
			if b.Type != "tool_use" || b.Name != "Bash" {
				continue
			}
			if l.RecordType != "assistant" || l.ContentKind != "array" {
				invalid[key], unknown = true, true
				continue
			}
			if b.ID == "" {
				unknown = true
				continue
			}
			x := &commitCall{line: l}
			if c.stream != nil {
				e := c.stream.commands[l][blockIndex]
				x.readable, x.command, x.uncertain, x.count = e.readable, e.parsed, e.uncertain, e.count
			} else {
				input := jsonMembers(b.Input)
				var command string
				if raw, ok := input["gc"]; ok && json.Unmarshal(raw, &command) == nil && len(command) <= 8192 && command != redactedMarker {
					x.readable = true
					x.command = gitcmd.ParseWithMessages(command, l.CWD)
					x.uncertain = x.command.Incomplete
					for _, op := range x.command.Git {
						c.pulse()
						if op.Kind == "commit" {
							x.count++
						}
						x.uncertain = x.uncertain || op.Background
					}
				} else {
					x.uncertain = true
				}
				if string(input["bg"]) == "true" {
					x.uncertain = true
				}
			}

			uses[key] = append(uses[key], x)
			unknown = unknown || x.uncertain || key.file == ""
		}
	}
	keys := make([]commitCallKey, 0, len(uses))
	for key, calls := range uses {
		c.pulse()
		keys = append(keys, key)
		if len(calls) != 1 || key.file == "" {
			unknown = true
			continue
		}
		if len(results[key]) > 1 {
			invalid[key], unknown = true, true
			continue
		}
		calls[0].results = results[key]
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].file != keys[j].file {
			return keys[i].file < keys[j].file
		}
		return keys[i].id < keys[j].id
	})
	checks := map[commitClaimKey]bool{}
	for _, claim := range c.in.CommitClaims {
		c.pulse()
		key := commitClaimKey{claim.Repository, claim.Hash}
		checks[key] = checks[key] || claim.Conflicting
	}
	candidates := map[commitCallKey][]commitCandidate{}
	unresolvedCalls := map[commitCallKey]bool{}
	unresolvedAt := map[commitCallKey]int64{}
	disputed := map[commitClaimKey]bool{}
	claimedBy := map[commitClaimKey]commitCallKey{}
	for _, snap := range c.in.Snapshots {
		c.pulse()
		if snap.Commits == nil {
			continue
		}
		collected = true
		cs := snap.Commits
		if cs.Malformed {
			unknown = true
		}
		if len(cs.Items) > 100 || len(cs.Unresolved) > 100 || len(cs.Trees) > 100 {
			unknown, out.Truncated = true, true
			continue
		}
		for _, name := range snap.Truncated {
			c.pulse()
			if name == "commits" {
				out.Truncated = true
			}
		}
		for _, unresolved := range cs.Unresolved[:min(len(cs.Unresolved), 100)] {
			c.pulse()
			key := commitCallKey{unresolved.Transcript, unresolved.Call}
			if len(uses[key]) == 0 && c.copiedCommitCall(key) {
				continue
			}
			if key.file == "" || key.id == "" || (unresolved.At <= 0 && len(uses[key]) == 0) || invalid[key] || len(uses[key]) > 1 || !c.commitSnapshotIdentity(snap, uses[key]) {
				unknown = true
				continue
			}
			unresolvedCalls[key] = true
			if unresolvedAt[key] == 0 || unresolved.At < unresolvedAt[key] {
				unresolvedAt[key] = unresolved.At
			}
			if len(uses[key]) != 1 || !uses[key][0].readable || uses[key][0].count == 0 {
				unknown = true
			}
		}
		for _, item := range cs.Items[:min(len(cs.Items), 100)] {
			c.pulse()
			key := commitCallKey{item.Transcript, item.Call}
			calls := uses[key]
			if len(calls) == 0 && c.copiedCommitCall(key) {
				continue
			}
			if !fullCommitHash(item.Hash) || !commitKind(item.Kind) || item.Proof != "output_hash" || item.At <= 0 || item.Parents < 0 || item.Tree < 0 || item.Tree >= len(cs.Trees) || key.file == "" || key.id == "" || len(calls) > 1 || invalid[key] || (item.Replaces != "" && (!fullCommitHash(item.Replaces) || item.Replaces == item.Hash || item.Kind != "amend")) {
				unknown = true
				continue
			}
			if !c.commitSnapshotIdentity(snap, calls) {
				unknown = true
				continue
			}
			tree := cs.Trees[item.Tree]
			if tree.Repository == "" || len(tree.Repository) > 4096 || len(tree.Branch) > 4096 {
				unknown = true
				continue
			}
			claim := commitClaimKey{tree.Repository, item.Hash}
			conflict, checked := checks[claim]
			if !checked || conflict {
				unknown = true
				disputed[claim] = true
			}
			if other, ok := claimedBy[claim]; ok && other != key {
				disputed[claim], unknown = true, true
			}
			claimedBy[claim] = key
			candidates[key] = append(candidates[key], commitCandidate{item, tree})
		}
	}
	for key := range candidates {
		c.pulse()
		if len(uses[key]) == 0 {
			keys = append(keys, key)
		}
	}
	for key := range unresolvedCalls {
		c.pulse()
		if len(uses[key]) == 0 && len(candidates[key]) == 0 {
			keys = append(keys, key)
		}
	}
	threads := c.attributeThreads()
	for _, key := range keys {
		c.pulse()
		if len(uses[key]) > 1 || key.file == "" || invalid[key] {
			continue
		}
		call := &commitCall{}
		if len(uses[key]) == 1 {
			call = uses[key][0]
		}
		turnKey := ""
		if call.line != nil && call.line.IsSidechain {
			if t := threads[sidechainKey(call.line.SourceFile, call.line.AgentID)]; t != nil {
				turnKey = t.out.TurnKey
			}
		} else if call.line != nil && call.line.turn != nil {
			turnKey = call.line.turn.out.TurnKey
		}
		base := Commit{TurnKey: turnKey, Call: key.id, Transcript: key.file, Kind: "commit", SubjectState: "none"}
		if call.line != nil {
			base.CallSrecID = call.line.ID
		}
		if len(call.command.Git) == 1 {
			op := call.command.Git[0]
			if op.Amend {
				base.Kind = "amend"
			}
			if op.MessageKnown {
				base.Subject, base.SubjectState = commitSubject(&op.Message)
			}
		}
		// Privacy survives a failed git proof: a fallback must not restore a
		// previously withheld or redacted subject from the command.
		for _, candidate := range candidates[key] {
			c.pulse()
			_, state := commitSubject(candidate.item.Subject)
			if subjectRank(state) >= 2 && subjectRank(state) > subjectRank(base.SubjectState) {
				base.Subject, base.SubjectState = "", state
			}
		}
		receipts, complete, failed, nothing, resultAt := c.commitReceipts(key, call)
		if !complete && (call.count > 0 || len(candidates[key]) > 0) {
			unknown = true
		}
		// Dedup identical observations across snapshot identities. Conflicting
		// metadata is not settled by whichever snapshot happened to arrive first.
		gitRows := map[commitClaimKey]Commit{}
		bad := map[commitClaimKey]bool{}
		for _, candidate := range candidates[key] {
			c.pulse()
			item, tree := candidate.item, candidate.tree
			claim := commitClaimKey{tree.Repository, item.Hash}
			if disputed[claim] || call.uncertain && call.readable {
				continue
			}
			if call.readable && !callAllowsCommit(call, item.Kind) {
				unknown = true
				continue
			}
			if complete && len(receipts) > 0 {
				matched := false
				for _, hash := range receipts {
					c.pulse()
					if !strings.HasPrefix(item.Hash, hash) {
						continue
					}
					unique := true
					for _, other := range candidates[key] {
						c.pulse()
						if other.item.Hash != item.Hash && strings.HasPrefix(other.item.Hash, hash) {
							unique = false
						}
					}
					matched = matched || unique
				}
				if !matched {
					unknown = true
					continue
				}
			}
			r := base
			r.Hash, r.Short, r.Kind, r.Source = item.Hash, item.Hash[:7], item.Kind, "git"
			r.CommittedAt = time.Unix(item.At, 0).UTC()
			r.Repository, r.Branch, r.Replaces = tree.Repository, tree.Branch, item.Replaces
			if r.Repository == c.session.RepositoryKey {
				r.Repository = ""
			}
			r.Subject, r.SubjectState = commitSubject(item.Subject)
			if old, ok := gitRows[claim]; ok {
				if old.CommittedAt != r.CommittedAt || old.Kind != r.Kind || old.Replaces != r.Replaces || old.Branch != r.Branch {
					bad[claim], unknown = true, true
				}
				if subjectRank(old.SubjectState) > subjectRank(r.SubjectState) {
					r.Subject, r.SubjectState = old.Subject, old.SubjectState
				} else if old.SubjectState == "ok" && r.SubjectState == "ok" && old.Subject != r.Subject {
					r.Subject, r.SubjectState = "", "withheld"
				}
			}
			gitRows[claim] = r
		}
		var verified []Commit
		for claim, r := range gitRows {
			c.pulse()
			if !bad[claim] {
				verified = append(verified, r)
			}
		}
		out.Items = append(out.Items, verified...)
		consumed := 0
		for _, r := range verified {
			c.pulse()
			if r.Kind == "commit" || r.Kind == "amend" {
				consumed++
			}
		}
		remaining := call.count - consumed
		if !call.readable {
			conflict := false
			for _, candidate := range candidates[key] {
				c.pulse()
				conflict = conflict || disputed[commitClaimKey{candidate.tree.Repository, candidate.item.Hash}]
			}
			if len(verified) == 0 && unresolvedCalls[key] && !complete {
				r := base
				r.Source, r.CommittedAt = "call", time.Unix(unresolvedAt[key], 0).UTC()
				out.Items = append(out.Items, r)
				unknown = true
			}
			if complete && len(verified) == 0 {
				r := base
				r.CommittedAt = resultAt
				if !conflict && len(receipts) == 1 {
					r.Source, r.Short = "output", commitShort(receipts[0])
				} else if !failed && !nothing && (unresolvedCalls[key] || conflict) {
					r.Source = "call"
				}
				if r.Source != "" {
					out.Items = append(out.Items, r)
				}
			}
			continue
		}
		if call.uncertain || !complete || call.count == 0 {
			continue
		}
		if remaining < 0 {
			unknown = true
		}
		for _, hash := range receipts {
			c.pulse()
			used, conflict := false, false
			for _, r := range verified {
				c.pulse()
				used = used || strings.HasPrefix(r.Hash, hash)
			}
			for claim := range disputed {
				c.pulse()
				if _, ours := claimedBy[claim]; ours && strings.HasPrefix(claim.hash, hash) {
					conflict = true
				}
			}
			if used || conflict {
				continue
			}
			if remaining <= 0 {
				unknown = true
				break
			}
			r := base
			r.Source, r.Short, r.CommittedAt = "output", commitShort(hash), resultAt
			out.Items = append(out.Items, r)
			remaining--
		}
		if len(call.command.Git) > 1 && remaining > 0 {
			// A shell call's final status does not prove each operation succeeded.
			unknown = true
			continue
		}
		if !failed && !nothing {
			for i := 0; i < remaining; i++ {
				c.pulse()
				r := base
				r.Source, r.CommittedAt = "call", resultAt
				out.Items = append(out.Items, r)
			}
		}
	}
	// Fold only same-repository git rows. The consumed older call is not revived
	// as an output/call row after an amend replaces it.
	replaced := map[commitClaimKey]bool{}
	for _, r := range out.Items {
		c.pulse()
		if r.Source == "git" && r.Replaces != "" {
			replaced[commitClaimKey{r.Repository, r.Replaces}] = true
		}
	}
	var kept []Commit
	for _, r := range out.Items {
		c.pulse()
		if r.Source != "git" || !replaced[commitClaimKey{r.Repository, r.Hash}] {
			kept = append(kept, r)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
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
		if a.Hash != b.Hash {
			return a.Hash < b.Hash
		}
		return a.Short < b.Short
	})
	total := len(kept)
	if len(kept) > 500 {
		kept, out.Truncated = kept[len(kept)-500:], true
	}
	for i := range kept {
		c.pulse()
		kept[i].Ordinal = i + 1
	}
	out.Items = kept
	if unknown {
		out.State = "undeterminable"
	} else if collected {
		out.State, out.Total = "observed", &total
	}
	return out
}

// Without the exact call, corroborate the snapshot's identity against visible
// session rows. A snapshot-only session still requires a nonempty owner.
func (c *computation) commitSnapshotIdentity(snap Snapshot, calls []*commitCall) bool {
	matches := func(l *line) bool {
		return snap.UserID == l.UserID && snap.ProfileEmail == l.ProfileEmail && snap.LoginEmail == l.LoginEmail && snap.AccountID == l.AccountID && snap.BillingProvider == l.BillingProvider
	}
	if len(calls) == 1 {
		return matches(calls[0].line)
	}
	if snap.UserID == "" && snap.ProfileEmail == "" {
		return false
	}
	if len(c.lines) == 0 {
		return true
	}
	for _, l := range c.lines {
		c.pulse()
		if matches(l) {
			return true
		}
	}
	return false
}

func subjectRank(state string) int {
	switch state {
	case "redacted":
		return 3
	case "withheld":
		return 2
	case "none":
		return 1
	}
	return 0
}

func callAllowsCommit(call *commitCall, kind string) bool {
	for _, op := range call.command.Git {
		if op.Kind == kind || op.Kind == "commit" && kind == "amend" && op.Amend || op.Kind == "pull" && kind == "merge" {
			return true
		}
	}
	return false
}

func (c *computation) copiedCommitCall(key commitCallKey) bool {
	for _, l := range c.lines {
		c.pulse()
		if l.SourceFile != key.file || !c.copied(l.ID) {
			continue
		}
		for _, b := range l.blocks {
			c.pulse()
			if b.Type == "tool_use" && b.ID == key.id {
				return true
			}
		}
	}
	return false
}

func (c *computation) commitReceipts(key commitCallKey, call *commitCall) ([]string, bool, bool, bool, time.Time) {
	if c.stream != nil {
		return c.receipts(key, call)
	}
	if len(call.results) != 1 {
		return nil, false, false, false, time.Time{}
	}
	row := call.results[0]
	if row.UserID != call.line.UserID || row.ProfileEmail != call.line.ProfileEmail || row.LoginEmail != call.line.LoginEmail || row.AccountID != call.line.AccountID || row.BillingProvider != call.line.BillingProvider || !row.dated() {
		return nil, false, false, false, time.Time{}
	}
	failed := false
	for _, b := range row.blocks {
		c.pulse()
		if b.Type == "tool_result" && b.ToolUseID == key.id {
			if len(b.Err) > 0 && json.Unmarshal(b.Err, &failed) != nil {
				return nil, false, false, false, row.TS
			}
		}
	}
	var selected []CommitResult
	for _, r := range c.in.CommitResults {
		c.pulse()
		if r.SrecID == row.ID && r.SourceFile == key.file && r.Call == key.id {
			selected = append(selected, r)
		}
	}
	operation := jsonMembers(jsonMembers(row.ToolUseResult)["gitOperation"])
	structured := jsonText(jsonMembers(operation["commit"])["sha"])
	if structured != "" && !gitcmd.ValidHash(structured) {
		return nil, false, failed, false, row.TS
	}
	if len(selected) != 1 || selected[0].Truncated || selected[0].Text == redactedMarker || len(selected[0].Text) > gitcmd.MaxOutputBytes {
		if len(selected) <= 1 && structured != "" {
			return []string{structured}, true, failed, false, row.TS
		}
		return nil, false, failed, false, row.TS
	}
	parsed := gitcmd.ReadOutput(selected[0].Text)
	if structured != "" {
		if len(parsed.Hashes) == 0 {
			parsed.Hashes = []string{structured}
		} else if len(parsed.Hashes) != 1 || (!strings.HasPrefix(structured, parsed.Hashes[0]) && !strings.HasPrefix(parsed.Hashes[0], structured)) {
			return nil, false, failed, false, row.TS
		}
	}
	return parsed.Hashes, !parsed.Incomplete, failed, parsed.NothingToCommit, row.TS
}

func commitShort(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
