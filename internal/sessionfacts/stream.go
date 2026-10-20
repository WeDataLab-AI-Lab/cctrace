package sessionfacts

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sort"
	"strings"
	"time"
)

// Progress describes completed work. Callbacks run synchronously, at least
// every 1,024 rows/nodes; callers own their watchdog and snapshot lifetime.
type Progress struct {
	Phase string
	Rows  int64
}

// RowRef preserves both the source ordinal and the complete source storage key.
type RowRef struct {
	Ordinal                                    int64
	ID                                         int64
	TS                                         time.Time
	RecordType, ProfileEmail, UUID, SourceFile string
}
type indexedClaude struct {
	ref  RowRef
	line *line
}
type indexedCodex struct {
	ref RowRef
	row CodexRow
}

// IndexBuilder builds only a compact, global index. Ordinals are zero-based
// source encounter positions. Add borrows each payload
// for the duration of the call and never retains the caller's row or JSON.
type IndexBuilder struct {
	meta     Input
	claude   []indexedClaude
	codex    []indexedCodex
	ordinals map[int64]bool
	copies   map[int64]bool
	progress func(Progress)
	sealed   bool
	work     int64
}

// NewIndexBuilder takes session metadata. Payload slices in meta are ignored;
// rows, heads, events and selected results must be supplied through their Add APIs.
func NewIndexBuilder(meta Input, progress func(Progress)) *IndexBuilder {
	meta.Rows = nil
	meta.CodexRows = nil
	meta.Heads = nil
	meta.OTEL = nil
	meta.CommitResults = nil
	// Snapshots and copy/claim metadata belong to the snapshot, not the borrower.
	owned := cloneMetadata(meta)
	copies := map[int64]bool{}
	if owned.CopyGate {
		for _, id := range owned.CopiedIDs {
			copies[id] = true
		}
	}
	return &IndexBuilder{meta: owned, progress: progress, ordinals: map[int64]bool{}, copies: copies}
}
func (b *IndexBuilder) check(ctx context.Context, ordinal int64, agent string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.sealed {
		return errors.New("sessionfacts: index already sealed")
	}
	if (agent == "codex" && b.meta.Agent != "codex") || (agent == "claude" && b.meta.Agent == "codex") {
		return fmt.Errorf("sessionfacts: %s row for %s index", agent, b.meta.Agent)
	}
	if ordinal < 0 {
		return errors.New("sessionfacts: negative source ordinal")
	}
	if b.ordinals[ordinal] {
		return errors.New("sessionfacts: duplicate source ordinal")
	}
	b.ordinals[ordinal] = true
	return nil
}
func (b *IndexBuilder) AddClaude(ctx context.Context, ordinal int64, row ClaudeRow) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if x, ok := r.(cancelledComputation); ok {
				err = x.err
			} else {
				panic(r)
			}
		}
	}()
	if err := b.check(ctx, ordinal, "claude"); err != nil {
		return err
	}
	copied := b.copies[row.ID]

	b.claude = append(b.claude, indexedClaude{RowRef{ordinal, row.ID, row.TS, row.RecordType, row.ProfileEmail, row.UUID, row.SourceFile}, projectClaude(row, copied, func() {
		b.work++
		if b.work%1024 == 0 {
			if err := ctx.Err(); err != nil {
				panic(cancelledComputation{err})
			}
			if b.progress != nil {
				b.progress(Progress{"index", int64(len(b.claude))})
			}
			if err := ctx.Err(); err != nil {
				panic(cancelledComputation{err})
			}
		}
	})})
	b.tick("index", len(b.claude))
	return ctx.Err()
}
func (b *IndexBuilder) AddCodex(ctx context.Context, ordinal int64, row CodexRow) error {
	if err := b.check(ctx, ordinal, "codex"); err != nil {
		return err
	}
	p := &codexProjection{codexHuman(&row), codexCommand(&row)}
	row.TextHead = ""
	row.ToolArgs = ""
	row.ToolOutput = ""
	row.projection = p
	if row.ContextWindow != nil {
		w := *row.ContextWindow
		row.ContextWindow = &w
	}
	b.codex = append(b.codex, indexedCodex{RowRef{ordinal, row.ID, row.TS, row.RecordType, row.ProfileEmail, row.UUID, row.SourceFile}, row})
	b.tick("index", len(b.codex))
	return ctx.Err()
}
func (b *IndexBuilder) tick(phase string, n int) {
	if b.progress != nil && (n%1024 == 0) {
		b.progress(Progress{phase, int64(n)})
	}
}

// Seal resolves the complete forest, final active paths, turns and exact
// source mapping before any payload fact is accumulated.
func (b *IndexBuilder) Seal(ctx context.Context) (acc *FactAccumulator, err error) {
	defer func() {
		if r := recover(); r != nil {
			if x, ok := r.(cancelledComputation); ok {
				acc = nil
				err = x.err
			} else {
				panic(r)
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.sealed {
		return nil, errors.New("sessionfacts: index already sealed")
	}
	b.sealed = true
	c := &computation{in: b.meta, copies: map[int64]bool{}, strictCodexTurnAttribution: true}
	if b.meta.CopyGate {
		for _, id := range b.meta.CopiedIDs {
			c.copies[id] = true
		}
	}
	c.session.Agent, c.session.Coverage, c.session.RulesVersion = b.meta.Agent, "full", baseRulesVersion
	if b.meta.CopyState != "" {
		c.session.Detail.Copy = &CopyStateDetail{State: b.meta.CopyState, Source: b.meta.CopySource}
	}
	a := &FactAccumulator{c: c, claude: b.claude, codex: b.codex, progress: b.progress, selected: map[resultKey][]resultEvidence{}, commands: map[*line]map[int]commandEvidence{}, codexCommands: map[*CodexRow]commandEvidence{}, structured: map[*line]structuredResult{}}
	c.stream = a
	a.context(ctx, "seal")
	if b.meta.Agent == "codex" {
		sort.Slice(a.codex, func(i, j int) bool {
			c.pulse()
			x, y := a.codex[i].row, a.codex[j].row
			if !x.TS.Equal(y.TS) {
				return x.TS.Before(y.TS)
			}
			return x.ID < y.ID
		})
		// CodexRows is the compact lifecycle index here; AddCodex removed
		// TextHead, ToolArgs and ToolOutput before this metadata-only append.
		for _, r := range a.codex {
			c.in.CodexRows = append(c.in.CodexRows, r.row)
		}
		c.readCodex()
		c.readCodexTurns()
	} else {
		sort.Slice(a.claude, func(i, j int) bool { c.pulse(); return a.claude[i].line.before(a.claude[j].line) })
		for i, r := range a.claude {
			c.lines = append(c.lines, r.line)
			if i%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				b.tick("seal", i)
			}
		}
		a.segments = c.prepareClaude()
		c.readTurnMetadata()
	}
	a.timeline = c.readTimeline()
	a.codexPreview = map[*CodexRow][]*turn{}
	for _, t := range c.turns {
		c.pulse()
		if t.codexInput != nil {
			a.codexPreview[t.codexInput] = append(a.codexPreview[t.codexInput], t)
		}
	}
	for _, t := range c.turns {
		t.members = nil
		t.read = nil
	}
	for _, t := range c.dropped {
		t.members = nil
		t.read = nil
	}
	c.dropped = nil
	c.stream = a
	b.claude = nil
	b.codex = nil
	b.ordinals = nil
	b.copies = nil
	b.meta = Input{}
	for i := range a.codex {
		a.codex[i].row = CodexRow{}
	}
	if b.progress != nil {
		b.progress(Progress{"sealed", int64(len(c.lines) + len(c.in.CodexRows))})
	}
	return a, ctx.Err()
}

// FactAccumulator owns compact references and derived facts only. Apply calls
// must follow the Order APIs; all rows must be consumed before Finish.
type FactAccumulator struct {
	codexPreview      map[*CodexRow][]*turn
	timeline          TimelineFacts
	ctx               context.Context
	phase             string
	work              int64
	c                 *computation
	claude            []indexedClaude
	codex             []indexedCodex
	next              int
	progress          func(Progress)
	segments          []Segment
	started, finished bool
	folds             []*lineFold
	selected          map[resultKey][]resultEvidence
	selectedCount     int
	commands          map[*line]map[int]commandEvidence
	codexCommands     map[*CodexRow]commandEvidence
	structured        map[*line]structuredResult
	taskStream        *streamTasks
}

func (a *FactAccumulator) ClaudeOrder() []RowRef {
	out := make([]RowRef, len(a.claude))
	for i, r := range a.claude {
		out[i] = r.ref
	}
	return out
}
func (a *FactAccumulator) CodexOrder() []RowRef {
	out := make([]RowRef, len(a.codex))
	for i, r := range a.codex {
		out[i] = r.ref
	}
	return out
}
func (a *FactAccumulator) canAdd(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.started || a.finished {
		return errors.New("sessionfacts: evidence must precede payload pass")
	}
	return nil
}
func (a *FactAccumulator) AddHead(ctx context.Context, h Head) error {
	if err := a.canAdd(ctx); err != nil {
		return err
	}
	a.c.in.Heads = append(a.c.in.Heads, h)
	return nil
}
func (a *FactAccumulator) AddEvent(ctx context.Context, e OTELRow) error {
	if err := a.canAdd(ctx); err != nil {
		return err
	}
	a.c.in.OTEL = append(a.c.in.OTEL, e)
	return nil
}
func (a *FactAccumulator) start() {
	if a.started {
		return
	}
	a.started = true
	c := a.c
	heads := c.requests()
	c.attachRequests(heads)
	c.session.APIRequests = c.total.requests
	c.session.InputTokens, c.session.OutputTokens = c.total.tokens[0], c.total.tokens[1]
	c.session.CacheReadTokens, c.session.CacheCreateTokens = c.total.tokens[2], c.total.tokens[3]
	// Identity and activity include events supplied after Seal.
	var contributors []contributor
	if c.in.Agent == "codex" {
		for _, r := range c.in.CodexRows {
			a.c.pulse()
			contributors = append(contributors, contributor{r.UserID, r.ProfileEmail, AccountKey{r.LoginEmail, r.BillingProvider, r.AccountID}})
		}
		c.session.AccountKeys = nil
		c.identify(contributors)
		c.codexActivity()
		c.readCodexContext(heads)
		c.session.Detail.Signals = Signals{Turn: observed(*c.session.Turns), Question: Signal{State: "not_applicable"}, Background: Signal{State: "not_applicable"}, Todo: Signal{State: "not_applicable"}}
	} else {
		for _, l := range c.lines {
			a.c.pulse()
			contributors = append(contributors, contributor{l.UserID, l.ProfileEmail, AccountKey{l.LoginEmail, l.BillingProvider, l.AccountID}})
		}
		c.session.AccountKeys = nil
		c.identify(contributors)
		a.segments = c.claudeActivity()
		s := &c.session
		s.Detail.Signals = Signals{Turn: unknown("pre_collect"), Background: unknown("pre_collect"), Todo: unknown("no_list"), Question: unknown("pre_collect")}
		if s.TurnBoundaries == "observed" {
			s.Detail.Signals.Turn = observed(*s.Turns)
		}
		a.folds = []*lineFold{newLineFold(c.readBackgroundRows), newLineFold(c.readTodoRows), newLineFold(c.readQuestionsAndPlansRows), newLineFold(c.readEditsRows), newLineFold(func(rows iter.Seq[*line]) { c.readContextRows(heads, rows) })}
	}
	for _, h := range heads {
		a.c.pulse()
		if !h.IsSidechain {
			c.session.Model = h.Model
		}
	}
	a.taskStream = newStreamTasks(c)
}
func (a *FactAccumulator) ApplyClaude(ctx context.Context, ordinal int64, row ClaudeRow) (err error) {
	defer a.recoverCancellation(&err)
	a.context(ctx, "payload")
	if err := ctx.Err(); err != nil {
		a.abort()
		return err
	}
	if a.finished || a.c.in.Agent == "codex" || a.next >= len(a.claude) {
		return errors.New("sessionfacts: unexpected Claude payload")
	}
	item := a.claude[a.next]
	if ordinal != item.ref.Ordinal || !matchesClaude(item.ref, row) {
		return errors.New("sessionfacts: Claude source order/key mismatch")
	}
	a.start()
	l := item.line
	owned, p, blocks := l.ClaudeRow, l.projection, l.blocks
	defer func() { l.ClaudeRow, l.projection, l.blocks = owned, p, blocks }()
	raw := newLine(&row, l.copied)
	l.ClaudeRow, l.blocks, l.projection = &row, raw.blocks, nil
	if p.attachmentType == "plan_file_reference" {
		p.planPath = l.planFilePath()
	}
	if l.turn != nil && l.turn.input == l {
		l.turn.out.Preview, l.turn.out.PreviewState = l.preview()
		if l.turn.out.Preview != nil {
			p := strings.Clone(*l.turn.out.Preview)
			l.turn.out.Preview = &p
		}
	}
	a.readCommandEvidence(l)
	a.taskStream.applyClaude(l, blocks)
	for _, f := range a.folds {
		f.apply(l)
	}
	l.ClaudeRow, l.blocks, l.projection = owned, blocks, p
	a.next++
	if a.progress != nil && a.next%1024 == 0 {
		a.progress(Progress{"payload", int64(a.next)})
	}
	return ctx.Err()
}
func matchesClaude(ref RowRef, r ClaudeRow) bool {
	return ref.ID == r.ID && ref.TS.Equal(r.TS) && ref.RecordType == r.RecordType && ref.ProfileEmail == r.ProfileEmail && ref.UUID == r.UUID && ref.SourceFile == r.SourceFile
}
func (a *FactAccumulator) ApplyCodex(ctx context.Context, ordinal int64, row CodexRow) (err error) {
	defer a.recoverCancellation(&err)
	a.context(ctx, "payload")
	if err := ctx.Err(); err != nil {
		a.abort()
		return err
	}
	if a.finished || a.c.in.Agent != "codex" || a.next >= len(a.codex) {
		return errors.New("sessionfacts: unexpected Codex payload")
	}
	item := a.codex[a.next]
	ref := item.ref
	if ordinal != ref.Ordinal || ref.ID != row.ID || !ref.TS.Equal(row.TS) || ref.RecordType != row.RecordType || ref.ProfileEmail != row.ProfileEmail || ref.UUID != row.UUID || ref.SourceFile != row.SourceFile {
		return errors.New("sessionfacts: Codex source order/key mismatch")
	}
	a.start()
	compact := &a.c.in.CodexRows[a.next]
	for _, t := range a.codexPreview[compact] {
		a.c.pulse()
		if t.codexInput == compact {
			p := strings.Clone(row.TextHead)
			if codexCommand(&row) && row.CommandName != "" {
				p = "/" + row.CommandName
			}
			t.out.Preview = &p
		}
	}
	a.readCodexEvidence(compact, &row)
	a.taskStream.applyCodex(compact, &row)
	a.next++
	if a.progress != nil && a.next%1024 == 0 {
		a.progress(Progress{"payload", int64(a.next)})
	}
	return ctx.Err()
}
func (a *FactAccumulator) abort() {
	for _, f := range a.folds {
		f.abort()
	}
	a.folds = nil
	a.finished = true
}

// Finish publishes no partial output. The returned hashes cover the same rule
// version and fields as ComputeWithTimelineAndCommits.
func (a *FactAccumulator) Finish(ctx context.Context) (result TimelineCommitOutput, err error) {
	defer func() {
		if r := recover(); r != nil {
			if x, ok := r.(cancelledComputation); ok {
				a.abort()
				result = TimelineCommitOutput{}
				err = x.err
			} else {
				panic(r)
			}
		}
	}()
	a.context(ctx, "finish")
	if err := ctx.Err(); err != nil {
		a.abort()
		return TimelineCommitOutput{}, err
	}
	if a.finished {
		return TimelineCommitOutput{}, errors.New("sessionfacts: accumulator already finished")
	}
	if a.next != len(a.claude)+len(a.codex) {
		return TimelineCommitOutput{}, errors.New("sessionfacts: incomplete payload pass")
	}
	a.start()
	for _, f := range a.folds {
		a.c.pulse()
		f.close()
	}
	a.folds = nil
	c := a.c
	c.priceSession()
	c.entitle()
	out := TimelineCommitOutput{Timeline: a.timeline, Commits: c.readCommits()}
	out.Tasks = a.taskStream.finish(out.Commits)
	c.applyCodexTurnAttributionState()
	c.session.Detail.TranscriptIO = c.in.TranscriptIO
	c.session.RulesVersion = RulesVersion
	out.Output = Output{Session: c.session, Segments: a.segments}
	for _, t := range c.turns {
		a.c.pulse()
		t.out.APIRequests = t.cost.requests
		t.out.InputTokens, t.out.OutputTokens = t.cost.tokens[0], t.cost.tokens[1]
		t.out.CacheReadTokens, t.out.CacheCreateTokens = t.cost.tokens[2], t.cost.tokens[3]
		out.Turns = append(out.Turns, t.out)
	}
	if err := ctx.Err(); err != nil {
		a.abort()
		return TimelineCommitOutput{}, err
	}
	out.Hash = outputHashChecked(c.pulse, out.Session, out.Turns, out.Segments, out.Commits, out.Tasks, out.Session.Detail.Copy, out.Session.Detail.CodexTurnAttribution, out.Session.Detail.TranscriptIO)
	out.TimelineHash = outputHashChecked(c.pulse, out.Session, out.Turns, out.Segments, out.Timeline, out.Commits, out.Tasks, out.Session.Detail.Copy, out.Session.Detail.CodexTurnAttribution, out.Session.Detail.TranscriptIO)
	if err := ctx.Err(); err != nil {
		a.abort()
		return TimelineCommitOutput{}, err
	}
	a.finished = true
	if a.progress != nil {
		a.progress(Progress{"finished", int64(a.next)})
	}
	if err := ctx.Err(); err != nil {
		return TimelineCommitOutput{}, err
	}
	return out, nil
}

// A pull coroutine gives the existing fold rule exactly one borrowed row at a
// time. Each Apply resumes it until that row is fully consumed; close runs its
// finalization. There is no per-turn payload queue; Close releases suspended
// folds when a loader fails before Finish.
type foldAborted struct{}
type lineFold struct {
	aborting bool
	row      *line
	next     func() (struct{}, bool)
	stop     func()
}

func newLineFold(run func(iter.Seq[*line])) *lineFold {
	f := &lineFold{}
	f.next, f.stop = iter.Pull(func(yield func(struct{}) bool) {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(foldAborted); !ok {
					panic(r)
				}
			}
		}()
		run(func(consume func(*line) bool) {
			for yield(struct{}{}) {
				if !consume(f.row) {
					return
				}
			}
			if f.aborting {
				panic(foldAborted{})
			}
		})
	})
	f.next()
	return f
}
func (f *lineFold) apply(l *line) { f.row = l; f.next(); f.row = nil }
func (f *lineFold) close()        { f.stop(); f.row = nil }

func (f *lineFold) abort() { f.aborting = true; f.stop(); f.row = nil }

// Close releases suspended folds after an external loader error. Callers should
// defer Close immediately after Seal; it is also safe after a successful Finish.
// The accumulator is sequential and must not be used concurrently.
func (a *FactAccumulator) Close() { a.abort() }
