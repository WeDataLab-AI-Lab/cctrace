package syncer

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"cctrace/internal/gitcmd"
	"cctrace/internal/sessionlog"
)

const (
	maxOpenCommitCalls = 64
	maxDoneCommitCalls = 200
	maxCommitDirs      = 16
)

// SessionCommitsState holds observations, not attributed commits. No command,
// output text or commit subject is persisted here. Truncated/Partial prevent a
// later consumer from treating lost or unread evidence as an observed zero.
type SessionCommitsState struct {
	Open             []GitCall `json:"open,omitempty"`
	Done             []GitCall `json:"done,omitempty"`
	Dirs             []string  `json:"dirs,omitempty"`
	Truncated        bool      `json:"truncated,omitempty"`
	Partial          bool      `json:"partial,omitempty"`
	TriedAt          time.Time `json:"tried_at,omitzero"`
	NoAnswers        int       `json:"no_answers,omitempty"`
	RedactedCalls    []string  `json:"redacted_calls,omitempty"`
	RedactedOverflow bool      `json:"redacted_overflow,omitempty"`
}

// GitCall's identity includes the transcript and account at the source time.
// A reused tool id in a sibling transcript cannot close this call. Hashes are
// output receipts only; none is a verified full commit until work-tree lookup.
type GitCall struct {
	ID              string             `json:"id"`
	Transcript      string             `json:"transcript"`
	AccountID       string             `json:"account_id,omitempty"`
	Start           time.Time          `json:"start"`
	End             time.Time          `json:"end,omitzero"`
	Ops             []gitcmd.Operation `json:"ops,omitempty"`
	OutputHashes    []string           `json:"output_hashes,omitempty"`
	StructuredHash  string             `json:"structured_hash,omitempty"`
	ResultSeen      bool               `json:"result_seen,omitempty"`
	ResultError     bool               `json:"result_error,omitempty"`
	Background      bool               `json:"background,omitempty"`
	NothingToCommit bool               `json:"nothing_to_commit,omitempty"`
	Incomplete      bool               `json:"incomplete,omitempty"`
	ReadRedacted    bool               `json:"read_redacted,omitempty"`
	Ambiguous       bool               `json:"ambiguous,omitempty"`
	WithholdSubject bool               `json:"withhold_subject,omitempty"`
	ReadAt          time.Time          `json:"read_at,omitzero"`
}

// noteSessionCommits is called with offset advancement (collectAdvanced), with
// the records the advance collected: sent, or consumed as an excluded
// account's, never ones the allowlist kept back. Like noteSessionEdits it does
// no filesystem or git work. Failed sends leave both offset and evidence
// unchanged.
func (s *Syncer) noteSessionCommits(path string, offset int64, records []*sessionlog.Record) {
	home := normalizeHome(s.claudeDir)
	if !strings.HasPrefix(path, filepath.Join(home, "projects")+string(filepath.Separator)) {
		return
	}
	main := s.sessionTranscript(path)
	fs, mfs := s.state.Files[path], s.state.Files[main]
	if fs == nil || mfs == nil {
		return
	}
	if fs.CallsFrom == nil {
		from := offset
		fs.CallsFrom = &from
	}
	transcript, err := filepath.Rel(filepath.Join(home, "projects"), path)
	if err != nil {
		return
	}
	defer s.markOverlappingCommitCalls()
	defer func() {
		if cs := mfs.SessionCommits; cs != nil {
			for _, r := range records {
				account := s.state.ClaudeAccountAt(home, r.Timestamp)
				ref := AccountRef{BillingProvider: "anthropic", AccountID: account}
				if account != "" && (s.client.excludedLocally(ref) || s.accounts.excluded(ref)) {
					continue
				}
				if r.CWD != "" && len(r.CWD) <= 4096 && !slices.Contains(cs.Dirs, r.CWD) {
					if len(cs.Dirs) < maxCommitDirs {
						cs.Dirs = append(cs.Dirs, r.CWD)
					} else {
						cs.Truncated = true
					}
				}
			}
		}
	}()
	for _, r := range records {
		account := s.state.ClaudeAccountAt(home, r.Timestamp)
		ref := AccountRef{BillingProvider: "anthropic", AccountID: account}
		if account != "" && ((s.client != nil && s.client.excludedLocally(ref)) || (s.accounts != nil && s.accounts.excluded(ref))) {
			continue
		}
		var message struct {
			Content []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(r.Message, &message) != nil {
			continue
		}
		for _, raw := range message.Content {
			var b struct {
				Type      string `json:"type"`
				Name      string `json:"name"`
				ID        string `json:"id"`
				ToolUseID string `json:"tool_use_id"`
				Input     struct {
					Command    string `json:"command"`
					Background bool   `json:"run_in_background"`
				} `json:"input"`
				Content json.RawMessage `json:"content"`
				IsError bool            `json:"is_error"`
			}
			if json.Unmarshal(raw, &b) != nil {
				continue
			}
			if r.Type == "assistant" && b.Type == "tool_use" && b.Name == "Bash" && b.ID != "" {
				if !strings.Contains(b.Input.Command, "git") {
					continue
				}
				parsed := gitcmd.Parse(b.Input.Command, r.CWD)
				if len(parsed.Git) == 0 && !parsed.Incomplete {
					continue
				}
				if mfs.SessionCommits == nil {
					mfs.SessionCommits = &SessionCommitsState{}
				}
				state := mfs.SessionCommits
				if len(b.ID) > 512 || len(parsed.Git) > 32 {
					state.Truncated = true
					continue
				}
				state.Partial = state.Partial || *fs.CallsFrom > 0
				match := func(c GitCall) bool { return c.ID == b.ID && c.Transcript == transcript }
				if slices.ContainsFunc(state.Open, match) || slices.ContainsFunc(state.Done, match) {
					continue
				}
				// A result belongs to the shell call, not one op within it. Until
				// per-op evidence exists, multiple git ops cannot yield attribution.
				call := GitCall{ID: b.ID, Transcript: transcript, AccountID: account, Start: r.Timestamp, ReadAt: time.Now().UTC(), Ops: parsed.Git, Background: b.Input.Background, Incomplete: parsed.Incomplete || r.Timestamp.IsZero() || len(parsed.Git) != 1}
				if s.client != nil {
					call.ReadRedacted = s.client.redact.UserPrompts || s.client.redact.ToolDetails
				}
				key := call.Transcript + "\x00" + call.ID
				call.ReadRedacted = call.ReadRedacted || state.RedactedOverflow || slices.Contains(state.RedactedCalls, key)
				state.rememberRedactedCall(call)
				for i := range call.Ops {
					op := &call.Ops[i]
					call.Background = call.Background || op.Background
					if len(op.Dir) > 4096 {
						op.Dir, op.DirKnown, call.Incomplete = "", false, true
					}
					if op.DirKnown && !slices.Contains(state.Dirs, op.Dir) {
						if len(state.Dirs) < maxCommitDirs {
							state.Dirs = append(state.Dirs, op.Dir)
						} else {
							state.Truncated = true
						}
					}
				}
				state.Open = append(state.Open, call)
				if len(state.Open) > maxOpenCommitCalls {
					state.Open = state.Open[len(state.Open)-maxOpenCommitCalls:]
					state.Truncated = true
				}
			}
			if r.Type != "user" || b.Type != "tool_result" || mfs.SessionCommits == nil {
				continue
			}
			state := mfs.SessionCommits
			i := slices.IndexFunc(state.Open, func(c GitCall) bool { return c.ID == b.ToolUseID && c.Transcript == transcript })
			if i < 0 {
				continue
			}
			call := state.Open[i]
			call.End, call.ResultSeen, call.ResultError = r.Timestamp, true, b.IsError
			if s.client != nil {
				call.ReadRedacted = call.ReadRedacted || s.client.redact.Enabled()
			}
			call.Incomplete = call.Incomplete || call.End.IsZero() || call.End.Before(call.Start) || call.AccountID != account
			text, complete := commitResultText(b.Content)
			output := gitcmd.ReadOutput(text)
			call.OutputHashes, call.NothingToCommit = output.Hashes, output.NothingToCommit
			call.Incomplete = call.Incomplete || !complete || output.Incomplete
			var result struct {
				IsAsync      bool `json:"isAsync"`
				GitOperation struct {
					Commit struct {
						SHA string `json:"sha"`
					} `json:"commit"`
				} `json:"gitOperation"`
			}
			if json.Unmarshal(r.ToolUseResult, &result) == nil {
				call.Background = call.Background || result.IsAsync
				if hash := result.GitOperation.Commit.SHA; gitcmd.ValidHash(hash) {
					call.StructuredHash = hash
				}
			}
			state.rememberRedactedCall(call)
			state.Open = slices.Delete(state.Open, i, i+1)
			state.Done = append(state.Done, call)
			if len(state.Done) > maxDoneCommitCalls {
				state.Done = state.Done[len(state.Done)-maxDoneCommitCalls:]
				state.Truncated = true
			}
		}
	}
}

// Equal lexical directories are already enough to reject overlapping calls.
// Resolving subdirectories/symlinks to actual work trees belongs to the later
// git lookup; lexical inequality here never proves two calls are independent.
func (s *Syncer) markOverlappingCommitCalls() {
	byDir := make(map[string][]*GitCall)
	prefix := filepath.Join(normalizeHome(s.claudeDir), "projects") + string(filepath.Separator)
	for path, fs := range s.state.Files {
		if !strings.HasPrefix(path, prefix) || fs.SessionCommits == nil {
			continue
		}
		for _, calls := range [][]GitCall{fs.SessionCommits.Open, fs.SessionCommits.Done} {
			for i := range calls {
				call := &calls[i]
				call.Ambiguous = false
				seen := make(map[string]bool)
				for _, op := range call.Ops {
					if op.DirKnown && !seen[op.Dir] {
						byDir[op.Dir] = append(byDir[op.Dir], call)
						seen[op.Dir] = true
					}
				}
			}
		}
	}
	for _, calls := range byDir {
		sort.SliceStable(calls, func(i, j int) bool { return calls[i].Start.Before(calls[j].Start) })
		var furthest *GitCall
		for _, call := range calls {
			if furthest != nil && (furthest.End.IsZero() || furthest.Background || !call.Start.After(furthest.End)) {
				call.Ambiguous, furthest.Ambiguous = true, true
			}
			if furthest == nil || (!furthest.End.IsZero() && !furthest.Background && (call.End.IsZero() || call.Background || call.End.After(furthest.End))) {
				furthest = call
			}
		}
	}
}

func (cs *SessionCommitsState) rememberRedactedCall(call GitCall) {
	if !call.ReadRedacted {
		return
	}
	key := call.Transcript + "\x00" + call.ID
	if slices.Contains(cs.RedactedCalls, key) {
		return
	}
	if len(cs.RedactedCalls) >= maxDoneCommitCalls {
		cs.RedactedOverflow = true
		return
	}
	cs.RedactedCalls = append(cs.RedactedCalls, key)
}

func (s *Syncer) backfillSessionCommits(path string, records []*sessionlog.Record, includeContent bool) bool {
	fs := s.state.Files[path]
	main := s.state.Files[s.sessionTranscript(path)]
	if fs == nil || main == nil {
		return false
	}
	start := collectedStart(fs)
	var part []*sessionlog.Record
	for _, r := range records {
		if r.Offset >= start && r.Offset < fs.Offset {
			part = append(part, r)
		}
	}
	if len(part) == 0 {
		return false
	}
	previous := map[string]bool{}
	if cs := main.SessionCommits; cs != nil {
		for _, calls := range [][]GitCall{cs.Open, cs.Done} {
			for _, c := range calls {
				previous[c.Transcript+"\x00"+c.ID] = true
			}
		}
	}
	fs.CallsFrom = &start
	s.noteSessionCommits(path, start, part)
	if cs := main.SessionCommits; cs != nil {
		for _, calls := range [][]GitCall{cs.Open, cs.Done} {
			for i := range calls {
				if !previous[calls[i].Transcript+"\x00"+calls[i].ID] {
					calls[i].WithholdSubject = !includeContent
				} else if includeContent {
					calls[i].WithholdSubject = false
					calls[i].ReadRedacted = calls[i].ReadRedacted || s.client.redact.Enabled()
				}
				cs.rememberRedactedCall(calls[i])
			}
		}
		cs.Partial = false
		for p, f := range s.state.Files {
			if s.sessionTranscript(p) == s.sessionTranscript(path) && (f.CallsFrom == nil || *f.CallsFrom > 0 || collectedStart(f) > 0) {
				cs.Partial = true
			}
		}
	}
	return true
}

func commitResultText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return "", false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, true
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	var out strings.Builder
	for _, block := range blocks {
		if block.Type != "text" {
			continue
		}
		if out.Len()+len(block.Text)+1 > gitcmd.MaxOutputBytes {
			return out.String(), false
		}
		out.WriteString(block.Text)
		out.WriteByte('\n')
	}
	return out.String(), true
}
