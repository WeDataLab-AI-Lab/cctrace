// Package dependencyaudit finds, in local Claude Code and Codex session files,
// the places where work returned to a person: approvals, answers, rejections,
// interrupts, corrections and repeated text. It reads only local files, through
// the same discovery and fact computation as the local session API, and prints
// counts with a few located examples for an agent to read further.
//
// A signal is a place to read, not a verdict. Lexical signals are labelled
// candidates; the source lines decide.
package dependencyaudit

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/semaphore"

	"cctrace/internal/localapi"
	"cctrace/internal/projecthash"
	"cctrace/internal/sessionfacts"
)

// Options selects the homes and the window.
type Options struct {
	Agent      string // claude, codex or all
	Since, Now time.Time
	ClaudeDirs []string
	CodexDirs  []string
	// Transmission is the transmission choice (allow, exclude, undecided,
	// mixed) of the profiles that register a home, by its canonical path. A
	// home missing here is unregistered.
	Transmission map[HomeKey]string
}

type HomeKey struct{ Agent, Path string }

type Result struct {
	Window   Window   `json:"window"`
	Coverage Coverage `json:"coverage"`
	Signals  Signals  `json:"signals"`
	// Server is the evidence only the server holds. Run reads local files
	// only and leaves it null; a server read fills it.
	Server  *ServerEvidence `json:"server"`
	Caveats []Caveat        `json:"caveats"`

	// localDecisions are the local Claude results that record a decision --
	// a permissionDecision, or a rejection as toolDenialKind -- by
	// tool_use_id, with the decision source the server would name for it
	// ("" when it names none of ServerDecisionSources).
	localDecisions map[string]string
}

type Window struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
	Agent string    `json:"agent"`
}

type Caveat struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type Coverage struct {
	Homes []*HomeCoverage `json:"homes"`
	// HumanInputs are the inputs counted as a person's, after duplicates.
	HumanInputs int `json:"human_inputs"`
	// QueuedPrompts are Claude prompts queued while the agent worked, by
	// origin. Human ones (origin human or absent) are in HumanInputs.
	QueuedPrompts QueuedPrompts `json:"queued_prompts"`
	// Duplicates are inputs and tool calls skipped because a file read earlier
	// (a resumed session's original) already held them.
	Duplicates int `json:"duplicates_skipped"`
	// InjectedOnly were typed or queued inputs with nothing of a person left
	// once injected blocks were removed.
	InjectedOnly int `json:"injected_only_inputs"`
	// ExcludedInputs are inputs left out before that by what wrote them: the
	// facts' kind for Claude (notification, peer, command, auto, sdk, system,
	// ...), and for Codex harness-written text (codex_harness_text), user
	// messages the facts do not count as typed (codex_not_typed) and sessions
	// not started from the CLI (codex_not_cli). Text a terminal injects that
	// is recorded as typed cannot be told from typing and is in neither.
	ExcludedInputs map[string]int `json:"excluded_inputs"`
	// UnknownSourceInputs are inputs whose source no field names (Claude
	// input kind unknown, Codex without an originator); they are not counted.
	UnknownSourceInputs int `json:"unknown_source_inputs"`
	// UnmatchedInputs are Codex inputs of the facts with no user message the
	// audit could read.
	UnmatchedInputs int `json:"unmatched_inputs"`
}

type QueuedPrompts struct {
	Human            int `json:"human"`
	Peer             int `json:"peer"`
	AutoContinuation int `json:"auto_continuation"`
	Other            int `json:"other"`
}

type HomeCoverage struct {
	Agent string `json:"agent"`
	Home  string `json:"home"`
	// Transmission is what the registering profiles chose for uploads; the
	// audit reads the home locally either way.
	Transmission string `json:"transmission"`
	// Files were modified inside the window; Sessions are those with a record
	// inside it.
	Files          int `json:"files"`
	Sessions       int `json:"sessions"`
	Interactive    int `json:"interactive"`
	NonInteractive int `json:"non_interactive"`
	Subagent       int `json:"subagent"`
	Copies         int `json:"copies"`
	Changed        int `json:"changed_while_read"`
	ReadErrors     int `json:"read_errors"`
	Incomplete     int `json:"incomplete_files"`
	InvalidLines   int `json:"invalid_lines"`
	OversizedLines int `json:"oversized_lines"`
	// PermissionUnobserved counts tool results without a permissionDecision
	// (Claude Code before 2.1.293 does not write one).
	PermissionUnobserved int `json:"permission_unobserved_results,omitempty"`
}

// Example locates one occurrence for reading around source_file:line.
type Example struct {
	SessionID  string    `json:"session_id"`
	Agent      string    `json:"agent"`
	Project    string    `json:"project"`
	TS         time.Time `json:"ts"`
	SourceFile string    `json:"source_file"`
	Line       int       `json:"line"`
	Excerpt    string    `json:"excerpt"`
}

// fileSummary is a file after its contribution was merged.
type fileSummary struct {
	agent, sessionID, project, path string
	home                            *HomeCoverage
	interactive                     bool
}

type humanInput struct {
	file         *fileSummary
	text         string
	ts           time.Time
	line         int
	prev         string
	compaction   int
	suggestion   bool
	opensRequest bool
}

type callRecord struct {
	*toolCall
	file *fileSummary
	// resultFile holds the result line: a later file when the session was
	// resumed before the call finished.
	resultFile *fileSummary
}

type event struct {
	file *fileSummary
	ts   time.Time
	line int
	text string
	kind string
}

type audit struct {
	opts    Options
	homes   map[string]*HomeCoverage
	order   []*HomeCoverage
	seen    map[string]bool
	inputs  []*humanInput
	calls   map[string]*callRecord
	callIDs []string
	cover   Coverage

	codexHomes      []string
	interrupts      []event
	otherAborts     int
	errorHandoffs   []event
	afterCompaction []event
	repeated        map[string]*repeat
	sources         map[uint64]source
	pastes          []pasteCandidate
}

// Run reads the selected homes and computes the signals.
func Run(ctx context.Context, opts Options) (*Result, error) {
	claude, codex := opts.ClaudeDirs, opts.CodexDirs
	switch opts.Agent {
	case "claude":
		codex = nil
	case "codex":
		claude = nil
	}
	entries, err := localapi.Find(claude, codex)
	if err != nil {
		return nil, err
	}
	a := &audit{opts: opts, homes: map[string]*HomeCoverage{}, seen: map[string]bool{}, calls: map[string]*callRecord{}, repeated: map[string]*repeat{}, sources: map[uint64]source{}}
	type queued struct {
		entry localapi.Entry
		first time.Time
		size  int64
	}
	var files []queued
	for _, e := range entries {
		h := a.home(e)
		if e.ModTime.Before(opts.Since) {
			continue
		}
		h.Files++
		var size int64
		if fi, err := os.Stat(e.Path); err == nil {
			size = fi.Size()
		}
		files = append(files, queued{e, firstRecordTime(e.Path), size})
	}
	// Ascending first record: a resumed session's original holds its copied
	// lines first whatever order the homes were listed in. A file whose head
	// names no time goes last.
	sort.Slice(files, func(i, j int) bool {
		a, b := files[i], files[j]
		if a.first.IsZero() != b.first.IsZero() {
			return b.first.IsZero()
		}
		if !a.first.Equal(b.first) {
			return a.first.Before(b.first)
		}
		if !a.entry.ModTime.Equal(b.entry.ModTime) {
			return a.entry.ModTime.Before(b.entry.ModTime)
		}
		return a.entry.LocalID < b.entry.LocalID
	})
	// Files are read in parallel and merged one by one in the order above. A
	// file's slot and its share of the byte budget are freed only when it is
	// merged, which bounds what is held in memory; the worker keeps only the
	// part of the analysis the merge reads.
	type outcome struct {
		f     *fileData
		facts fileFacts
		err   error
	}
	done := make([]chan outcome, len(files))
	for i := range done {
		done[i] = make(chan outcome, 1)
	}
	slots := make(chan struct{}, min(runtime.GOMAXPROCS(0), 4))
	budget := semaphore.NewWeighted(inFlightBytes)
	weight := func(size int64) int64 { return min(max(size, 1), inFlightBytes) }
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for i, q := range files {
			if budget.Acquire(ctx, weight(q.size)) != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func() {
				f := newFileData(q.entry)
				analysis, err := localapi.Analyze(ctx, q.entry, localapi.AnalyzeOptions{ReadAt: opts.Now, Visit: f.visit})
				done[i] <- outcome{f, reduceAnalysis(f, analysis), err}
			}()
		}
	}()
	for i, q := range files {
		var o outcome
		select {
		case o = <-done[i]:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		<-slots
		budget.Release(weight(q.size))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h := a.home(q.entry)
		if errors.Is(o.err, localapi.ErrChanged) {
			h.Changed++
			continue
		}
		if o.err != nil {
			h.ReadErrors++
			continue
		}
		a.add(o.f, o.facts)
	}
	return a.result(), nil
}

func (a *audit) home(e localapi.Entry) *HomeCoverage {
	key := e.Agent + "\x00" + e.Home
	h := a.homes[key]
	if h == nil {
		h = &HomeCoverage{Agent: e.Agent, Home: Mask(displayHome(e.Home)), Transmission: a.opts.Transmission[HomeKey{e.Agent, e.Home}]}
		if h.Transmission == "" {
			h.Transmission = "unregistered"
		}
		a.homes[key] = h
		a.order = append(a.order, h)
		if e.Agent == "codex" {
			a.codexHomes = append(a.codexHomes, e.Home)
		}
	}
	return h
}

func displayHome(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(os.PathSeparator)) {
			return "~" + path[len(home):]
		}
	}
	return path
}

// headTimestamp finds a record's timestamp member; one inside a string value is
// escaped and does not match.
var headTimestamp = regexp.MustCompile(`"timestamp":"([^"]+)"`)

// firstRecordTime is the first parsable timestamp in line order. A long line
// is read in pieces, so it costs no more memory than a short one, and reading
// stops at the first time found.
func firstRecordTime(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		piece, err := r.ReadSlice('\n')
		for _, m := range headTimestamp.FindAllSubmatch(piece, -1) {
			if ts := parseTS(string(m[1])); !ts.IsZero() {
				return ts
			}
		}
		if err != nil && err != bufio.ErrBufferFull {
			return time.Time{}
		}
	}
}

func (a *audit) inWindow(ts time.Time) bool {
	return !ts.Before(a.opts.Since) && ts.Before(a.opts.Now)
}

// once reports whether key is new; an empty key has no identity to repeat.
func (a *audit) once(key string) bool {
	if key == "" {
		return true
	}
	if a.seen[key] {
		return false
	}
	a.seen[key] = true
	return true
}

// inFlightBytes bounds the size of the files read but not yet merged.
const inFlightBytes = 256 << 20

// fileFacts is what the merge reads of a file's analysis: the turns, the
// turn of each tool result row, the Codex entry point and whether the file
// was incomplete.
type fileFacts struct {
	incomplete bool
	entrypoint string
	turns      []turnFacts
	turnOfRow  map[int64]string
}

type turnFacts struct {
	key, branch, status string
	isRequest           bool
	inputs              []sessionfacts.TurnInput
}

func reduceAnalysis(f *fileData, an localapi.Analysis) fileFacts {
	out := fileFacts{incomplete: an.Incomplete != "", turnOfRow: map[int64]string{}}
	for _, row := range an.Input.CodexRows {
		if row.Entrypoint != "" {
			out.entrypoint = row.Entrypoint
		}
	}
	for _, t := range an.Facts.Turns {
		out.turns = append(out.turns, turnFacts{key: t.TurnKey, branch: t.Branch, status: t.Status, isRequest: t.IsRequest, inputs: t.Detail.Inputs})
	}
	results := map[int64]bool{}
	for _, c := range f.calls {
		if c.hasResult {
			results[c.resultRow] = true
		}
	}
	for _, row := range an.Facts.Timeline.Rows {
		if row.TurnKey != "" && results[row.ID] {
			out.turnOfRow[row.ID] = row.TurnKey
		}
	}
	return out
}

// add merges one stable file. Its inputs and calls are counted only when no
// file read before it held the same native identifier.
func (a *audit) add(f *fileData, an fileFacts) {
	e := f.entry
	h := a.home(e)
	h.InvalidLines += f.invalid
	h.OversizedLines += f.oversized
	if an.incomplete {
		h.Incomplete++
	}
	file := &fileSummary{agent: e.Agent, sessionID: f.sessionID, project: Mask(projecthash.NameFromPath(f.cwd)), path: e.Path, home: h}
	entrypoint := an.entrypoint
	subagent := e.Agent == "claude" && strings.Contains(filepath.ToSlash(e.Path), "/subagents/") || entrypoint == "codex-subagent"
	if f.lastTS.Before(a.opts.Since) {
		return
	}
	h.Sessions++

	inputs, duplicates := a.humanInputs(f, an, file, entrypoint)
	interventions := len(inputs)
	for _, id := range f.callOrder {
		c := f.calls[id]
		if !a.inWindow(c.ts) && !a.inWindow(c.resultTS) {
			continue
		}
		key := e.Agent + ":tool:" + id
		if e.Agent == "codex" {
			key = "codex:call:" + id
		}
		if prior := a.calls[key]; prior != nil {
			if !prior.hasResult && c.hasResult {
				winner := prior.toolCall
				merged := *c
				merged.name, merged.ts, merged.line, merged.sidechain = winner.name, winner.ts, winner.line, winner.sidechain
				merged.shell, merged.command, merged.questions = winner.shell, winner.command, winner.questions
				*winner = merged
				prior.resultFile = file
			}
			duplicates++
			continue
		}
		a.calls[key] = &callRecord{c, file, file}
		a.callIDs = append(a.callIDs, key)
		if c.answered() || humanDenials[c.denial] {
			interventions++
		}
	}
	a.cover.Duplicates += duplicates
	requests := 0
	for _, in := range inputs {
		if in.opensRequest {
			requests++
		}
	}
	file.interactive = !subagent && requests >= 1 && interventions >= 2
	switch {
	case subagent:
		h.Subagent++
	case file.interactive:
		h.Interactive++
	case duplicates > 0 && len(inputs) == 0:
		h.Copies++
	default:
		h.NonInteractive++
	}
	a.inputs = append(a.inputs, inputs...)
	a.cover.HumanInputs += len(inputs)
	if !subagent {
		a.addMarkers(f, file, inputs)
		for _, m := range f.otherAborts {
			if a.inWindow(m.ts) && a.once(m.key) {
				a.otherAborts++
			}
		}
	}
	if e.Agent == "claude" {
		a.addErrorHandoffs(f, an, file, inputs)
		a.addAfterCompaction(file, inputs)
	}
	if file.interactive {
		a.index(f, file, inputs)
	}
}

// humanInputs are the file's inputs a person made, in file order: the facts
// name the inputs and their kind, the raw line gives the text.
func (a *audit) humanInputs(f *fileData, an fileFacts, file *fileSummary, entrypoint string) (out []*humanInput, duplicates int) {
	kinds := map[string]string{}
	openers := map[string]bool{}
	typedAt := map[int64]int{}
	openerAt := map[int64]bool{}
	for _, t := range an.turns {
		if t.branch != "active" {
			continue
		}
		for i, in := range t.inputs {
			if f.entry.Agent == "codex" {
				if in.Kind == "typed" {
					typedAt[in.TS.UnixNano()]++
					if i == 0 && t.isRequest {
						openerAt[in.TS.UnixNano()] = true
					}
				}
				continue
			}
			kinds[in.UUID] = in.Kind
			if i == 0 && t.isRequest {
				openers[in.UUID] = true
			}
		}
	}
	for _, u := range f.users {
		opens, inside := false, a.inWindow(u.ts)
		if f.entry.Agent == "codex" {
			at := u.ts.UnixNano()
			switch {
			case !codexHumanText(u.text):
				a.exclude(inside, "codex_harness_text")
				continue
			case typedAt[at] == 0:
				a.exclude(inside, "codex_not_typed")
				continue
			}
			typedAt[at]--
			if entrypoint == "" && inside {
				a.cover.UnknownSourceInputs++
			}
			if entrypoint != "cli" {
				if entrypoint != "" {
					a.exclude(inside, "codex_not_cli")
				}
				continue
			}
			opens = openerAt[at]
		} else if u.queued {
			// The local projection leaves attachment lines out of the facts, so
			// a queued prompt is read from its own line: its origin says whose
			// it was, as sessionfacts reads one (human, or none on older
			// lines). It opens no request. Which branch it sat on is unknown.
			if !inside {
				continue
			}
			if u.origin != "human" && u.origin != "" {
				if u.uuid != "" && !a.once("claude:input:"+u.uuid) {
					duplicates++
					continue
				}
				switch u.origin {
				case "peer":
					a.cover.QueuedPrompts.Peer++
				case "auto-continuation":
					a.cover.QueuedPrompts.AutoContinuation++
				default:
					a.cover.QueuedPrompts.Other++
				}
				continue
			}
		} else {
			switch kinds[u.uuid] {
			case "typed", "queued":
			case "unknown":
				if inside {
					a.cover.UnknownSourceInputs++
				}
				continue
			case "":
				continue
			default:
				if inside && a.once("claude:input:"+u.uuid) {
					a.exclude(true, kinds[u.uuid])
				}
				continue
			}
			opens = openers[u.uuid]
		}
		if !inside {
			continue
		}
		if f.entry.Agent == "claude" && u.uuid != "" && !a.once("claude:input:"+u.uuid) {
			duplicates++
			continue
		}
		text, ok := StripInjected(u.text)
		if !ok {
			a.cover.InjectedOnly++
			continue
		}
		if u.queued {
			a.cover.QueuedPrompts.Human++
		}
		out = append(out, &humanInput{file: file, text: text, ts: u.ts, line: u.line, prev: u.prev, compaction: u.compaction, suggestion: u.promptSource == "suggestion_accepted", opensRequest: opens})
	}
	for at, n := range typedAt {
		if a.inWindow(time.Unix(0, at)) {
			a.cover.UnmatchedInputs += n
		}
	}
	return out, duplicates
}

// exclude counts an input inside the window that something other than a
// person wrote.
func (a *audit) exclude(inside bool, kind string) {
	if !inside {
		return
	}
	if a.cover.ExcludedInputs == nil {
		a.cover.ExcludedInputs = map[string]int{}
	}
	a.cover.ExcludedInputs[kind]++
}

func codexHumanText(text string) bool {
	t := strings.TrimSpace(text)
	return t != "" && !strings.HasPrefix(t, "<") && !strings.HasPrefix(t, "# AGENTS.md")
}

// addMarkers records interrupts with the person's next input as the excerpt.
func (a *audit) addMarkers(f *fileData, file *fileSummary, inputs []*humanInput) {
	for _, m := range f.interrupts {
		if !a.inWindow(m.ts) || !a.once(m.key) {
			continue
		}
		next := ""
		for _, in := range inputs {
			if in.line > m.line {
				next = in.text
				break
			}
		}
		a.interrupts = append(a.interrupts, event{file: file, ts: m.ts, line: m.line, text: next, kind: file.agent})
	}
}

// addErrorHandoffs finds turns that ended in an API error, or whose last tool
// result was an error, and were followed directly by a person's input.
func (a *audit) addErrorHandoffs(f *fileData, an fileFacts, file *fileSummary, inputs []*humanInput) {
	byLine := map[int]*humanInput{}
	for _, in := range inputs {
		byLine[in.line] = in
	}
	lineOf := map[string]int{}
	for _, u := range f.users {
		lineOf[u.uuid] = u.line
	}
	turnOf := an.turnOfRow
	last := map[string]*toolCall{}
	for _, c := range f.calls {
		if k := turnOf[c.resultRow]; c.hasResult && k != "" && (last[k] == nil || c.resultLine > last[k].resultLine) {
			last[k] = c
		}
	}
	var active []int
	for i, t := range an.turns {
		if t.branch == "active" {
			active = append(active, i)
		}
	}
	for n := 0; n+1 < len(active); n++ {
		t, next := an.turns[active[n]], an.turns[active[n+1]]
		if len(next.inputs) == 0 {
			continue
		}
		in := byLine[lineOf[next.inputs[0].UUID]]
		if in == nil {
			continue
		}
		kind := ""
		if t.status == "error" {
			kind = "api_error"
		} else if c := last[t.key]; c != nil && c.isError && c.denial == "" {
			kind = "tool_error"
		}
		if kind != "" {
			a.errorHandoffs = append(a.errorHandoffs, event{file: file, ts: in.ts, line: in.line, text: in.text, kind: kind})
		}
	}
}

// addAfterCompaction finds a long input among the first two a person wrote
// after a compaction or continuation: possibly context restated by hand.
func (a *audit) addAfterCompaction(file *fileSummary, inputs []*humanInput) {
	position := map[int]int{}
	done := map[int]bool{}
	for _, in := range inputs {
		if in.compaction == 0 || done[in.compaction] {
			continue
		}
		position[in.compaction]++
		if position[in.compaction] > 2 {
			continue
		}
		if len([]rune(in.text)) >= afterCompactionRunes {
			done[in.compaction] = true
			a.afterCompaction = append(a.afterCompaction, event{file: file, ts: in.ts, line: in.line, text: in.text})
		}
	}
}

func (a *audit) example(file *fileSummary, ts time.Time, line int, text string) Example {
	return Example{SessionID: Mask(file.sessionID), Agent: file.agent, Project: file.project, TS: ts, SourceFile: Mask(file.path), Line: line, Excerpt: excerpt(text)}
}
