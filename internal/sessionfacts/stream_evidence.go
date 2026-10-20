package sessionfacts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"cctrace/internal/gitcmd"
)

type resultKey struct {
	id         int64
	file, call string
}

// ResultEvidence is selected output normalized into commit receipts and task
// evidence. It contains no selected text, shell output or tool payload.
type ResultEvidence struct {
	Output                                gitcmd.Output
	Truncated, Redacted, Oversize, Failed bool
	URL                                   string
}
type resultEvidence = ResultEvidence

// NormalizeCommitResult consumes selected text immediately. Row identity stays
// with CommitResult; the returned evidence can safely cross a batch boundary.
func NormalizeCommitResult(r CommitResult) ResultEvidence {
	url, _, _, _ := githubResultURL(r.Text)
	output := gitcmd.ReadOutput(r.Text)
	for i, h := range output.Hashes {
		output.Hashes[i] = strings.Clone(h)
	}
	return ResultEvidence{Output: output, Truncated: r.Truncated, Redacted: r.Text == redactedMarker, Oversize: len(r.Text) > gitcmd.MaxOutputBytes, Failed: codexOutputFailed(r.Status, r.Text), URL: strings.Clone(url)}
}
func (a *FactAccumulator) AddCommitResult(ctx context.Context, r CommitResult) error {
	if err := a.canAdd(ctx); err != nil {
		return err
	}
	return a.AddResultEvidence(ctx, r.SrecID, r.SourceFile, r.Call, NormalizeCommitResult(r))
}

// AddResultEvidence is the adapter API for loaders which normalize each selected
// result before releasing their borrowed buffer. Selection remains session-wide.
func (a *FactAccumulator) AddResultEvidence(ctx context.Context, id int64, file, call string, e ResultEvidence) error {
	if err := a.canAdd(ctx); err != nil {
		return err
	}
	if a.selectedCount >= 1000 {
		return errors.New("sessionfacts: selected result count exceeds 1000")
	}
	a.selectedCount++
	e.Output.Hashes = append([]string(nil), e.Output.Hashes...)
	for i, h := range e.Output.Hashes {
		e.Output.Hashes[i] = strings.Clone(h)
	}
	e.URL = strings.Clone(e.URL)
	a.selected[resultKey{id, file, call}] = append(a.selected[resultKey{id, file, call}], e)
	return nil
}

type commandEvidence struct {
	parsed                          gitcmd.Commands
	readable, uncertain, unknownAPI bool
	count                           int
}
type structuredResult struct{ hash string }

func boundedCommands(p gitcmd.Commands) gitcmd.Commands {
	for i := range p.Git {
		p.Git[i].Dir = ""
		p.Git[i].DirKnown = false
		if p.Git[i].Message != redactedMarker {
			p.Git[i].Message = strings.Clone(boundedTaskText(p.Git[i].Message, 200))
		}
	}
	for i := range p.GitHub {
		p.GitHub[i].Title = strings.Clone(boundedTaskText(p.GitHub[i].Title, 200))
	}
	return p
}
func (a *FactAccumulator) readCommandEvidence(l *line) {
	var evidence map[int]commandEvidence
	for i, b := range l.blocks {
		a.c.pulse()
		if b.Type != "tool_use" || b.Name != "Bash" {
			continue
		}
		x := commandEvidence{}
		input := jsonMembers(b.Input)
		var command string
		if raw, ok := input["gc"]; ok && json.Unmarshal(raw, &command) == nil && len(command) <= 8192 && command != redactedMarker {
			x.readable = true
			x.parsed = boundedCommands(gitcmd.ParseWithMessages(command, l.CWD))
			x.uncertain = x.parsed.Incomplete
			for _, op := range x.parsed.Git {
				if op.Kind == "commit" {
					x.count++
				}
				x.uncertain = x.uncertain || op.Background
			}
		} else {
			x.uncertain = true
		}
		x.uncertain = x.uncertain || string(input["bg"]) == "true"
		if evidence == nil {
			evidence = map[int]commandEvidence{}
		}
		evidence[i] = x
	}
	if len(evidence) > 0 {
		a.commands[l] = evidence
	}
	operation := jsonMembers(jsonMembers(l.ToolUseResult)["gitOperation"])
	s := structuredResult{hash: jsonText(jsonMembers(operation["commit"])["sha"])}

	if s.hash != "" {
		a.structured[l] = s
	}
}
func (a *FactAccumulator) readCodexEvidence(compact, raw *CodexRow) {
	if raw.RecordType == "tool_call" && codexShellName(raw.ToolName) {
		p := gitcmd.ParseCodex(raw.ToolName, raw.ToolArgs, raw.CWD)
		p.Incomplete = p.Incomplete || raw.ToolArgsTruncated
		a.codexCommands[compact] = commandEvidence{parsed: boundedCommands(p), unknownAPI: strings.Contains(raw.ToolArgs, "gh api")}
	}
	if raw.RecordType == "tool_output" && raw.ToolOutput != "" {
		key := resultKey{raw.ID, raw.SourceFile, raw.ToolCallID}
		if len(a.selected[key]) == 0 {
			e := NormalizeCommitResult(CommitResult{Text: raw.ToolOutput, Status: raw.ToolStatus})
			a.selected[key] = []resultEvidence{e}
		}
	}
}
func (c *computation) receipts(key commitCallKey, call *commitCall) ([]string, bool, bool, bool, time.Time) {
	if len(call.results) != 1 {
		return nil, false, false, false, time.Time{}
	}
	row := call.results[0]
	if row.UserID != call.line.UserID || row.ProfileEmail != call.line.ProfileEmail || row.LoginEmail != call.line.LoginEmail || row.AccountID != call.line.AccountID || row.BillingProvider != call.line.BillingProvider || !row.dated() {
		return nil, false, false, false, time.Time{}
	}
	failed := false
	for _, b := range row.blocks {
		if b.Type == "tool_result" && b.ToolUseID == key.id {
			if len(b.Err) > 0 && json.Unmarshal(b.Err, &failed) != nil {
				return nil, false, false, false, row.TS
			}
		}
	}
	selected := c.stream.selected[resultKey{row.ID, key.file, key.id}]
	structured := c.stream.structured[row].hash
	if structured != "" && !gitcmd.ValidHash(structured) {
		return nil, false, failed, false, row.TS
	}
	if len(selected) != 1 || selected[0].Truncated || selected[0].Redacted || selected[0].Oversize {
		if len(selected) <= 1 && structured != "" {
			return []string{structured}, true, failed, false, row.TS
		}
		return nil, false, failed, false, row.TS
	}
	parsed := selected[0].Output
	if structured != "" {
		if len(parsed.Hashes) == 0 {
			parsed.Hashes = []string{structured}
		} else if len(parsed.Hashes) != 1 || (!strings.HasPrefix(structured, parsed.Hashes[0]) && !strings.HasPrefix(parsed.Hashes[0], structured)) {
			return nil, false, failed, false, row.TS
		}
	}
	return parsed.Hashes, !parsed.Incomplete, failed, parsed.NothingToCommit, row.TS
}
func (c *computation) codexResult(row *CodexRow) (resultEvidence, bool) {
	xs := c.stream.selected[resultKey{row.ID, row.SourceFile, row.ToolCallID}]
	if len(xs) != 1 {
		return resultEvidence{}, false
	}
	return xs[0], true
}
