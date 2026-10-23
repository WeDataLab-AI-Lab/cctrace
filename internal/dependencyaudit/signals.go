package dependencyaudit

import (
	"encoding/json"
	"hash/maphash"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"cctrace/internal/gitcmd"
)

// The fixed thresholds every signal states in its output.
const (
	maxExamples          = 5
	repeatedRunes        = 30
	repeatedSessions     = 3
	maxRepeated          = 10
	pasteRunes           = 20
	pasteLines           = 2
	stockSessions        = 3
	commandSessions      = 5
	maxCommands          = 15
	maxBreakdown         = 15
	afterCompactionRunes = 300
)

// goAheadWords are inputs that only let the agent go on. An input matches when
// all of it, lower-cased and without punctuation or spaces, is one of them.
var goAheadWords = []string{
	"yes", "y", "yep", "yeah", "ok", "okay", "sure", "go", "go ahead", "continue", "proceed",
	"please continue", "please proceed", "do it", "sounds good", "lgtm",
	"응", "네", "예", "넵", "ㅇㅇ", "ㅇㅋ", "ㄱ", "ㄱㄱ", "고", "고고", "좋아", "좋아요", "좋습니다",
	"그래", "그래요", "오케이", "확인", "해", "해줘", "진행", "진행해", "진행해줘", "진행해주세요",
	"진행하세요", "계속", "계속해", "계속해줘", "계속 진행해", "그렇게 해", "그렇게 해줘",
}

// announceWords mark an assistant's last line as announcing its next step.
// English entries match at an ASCII word boundary, Korean ones anywhere.
var announceWords = []string{
	"i'll", "i will", "let me", "i'm going to", "i am going to", "next, i",
	"하겠습니다", "할게요", "할께요", "드리겠습니다", "드릴게요", "진행합니다", "시작합니다", "보겠습니다", "볼게요",
}

// correctionWords are first words that turn the agent back. The first word of
// an input matches when it equals one, punctuation at its ends removed.
var correctionWords = []string{
	"no", "nope", "wrong", "actually", "wait", "stop", "undo", "revert", "don't", "dont", "incorrect",
	"아니", "아니야", "아니요", "아뇨", "아니고", "아니라", "아닌데", "틀렸어", "틀렸다", "틀렸네",
	"잘못", "잘못됐어", "다시", "잠깐", "잠깐만", "멈춰", "그만", "취소", "되돌려",
}

// recommendMarkers mark the option an AskUserQuestion recommends, matched
// without regard to case.
var recommendMarkers = []string{"(recommended)", "(권장)", "(추천)"}

var humanDenials = map[string]bool{"user-rejected": true, "interrupted": true}

// permissionSources are the permissionDecision sources Claude Code writes; any
// other value is counted as "other" rather than printed.
var permissionSources = map[string]bool{"config": true, "hook": true, "user_temporary": true, "user_permanent": true, "user_reject": true, "user_abort": true}

// denialSources name the decision source a person's toolDenialKind is
// recorded under on the server.
var denialSources = map[string]string{"user-rejected": "user_reject", "interrupted": "user_abort"}
var machineDenials = map[string]bool{"permission-rule": true, "automode-blocked": true, "automode-unavailable": true}

var goAheadSet = func() map[string]bool {
	set := map[string]bool{}
	for _, w := range goAheadWords {
		set[squash(w)] = true
	}
	return set
}()

var hashSeed = maphash.MakeSeed()

// squash lower-cases text and drops punctuation, symbols and spaces.
func squash(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		if !unicode.IsPunct(r) && !unicode.IsSymbol(r) && !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func normalizeLine(line string) string {
	return strings.ToLower(strings.Join(strings.Fields(line), " "))
}

func isGoAhead(text string) bool { return goAheadSet[squash(text)] }

func isCorrection(text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	return slices.Contains(correctionWords, strings.ToLower(strings.TrimFunc(fields[0], unicode.IsPunct)))
}

// afterKind reads the assistant text before an input: asked when its last line
// is a question, announced when it says what comes next, other otherwise.
func afterKind(prev string) string {
	lines := strings.Split(strings.TrimSpace(prev), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if strings.HasSuffix(last, "?") || strings.HasSuffix(last, "？") {
		return "asked"
	}
	lower := strings.ToLower(last)
	for _, w := range announceWords {
		if w[0] < utf8.RuneSelf {
			if hasWord(lower, w) {
				return "announced"
			}
		} else if strings.Contains(lower, w) {
			return "announced"
		}
	}
	return "other"
}

// hasWord reports phrase in text with no ASCII letter or digit right before it.
func hasWord(text, phrase string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], phrase)
		if j < 0 {
			return false
		}
		at := i + j
		if at == 0 || !isASCIIWord(text[at-1]) {
			return true
		}
		i = at + 1
	}
}

func isASCIIWord(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

func recommendedLabel(label string) bool {
	lower := strings.ToLower(label)
	for _, m := range recommendMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// classifyAnswer places one AskUserQuestion answer against its options.
func classifyAnswer(q question, answer string, answered bool) string {
	recommended := ""
	labels := map[string]bool{}
	for _, o := range q.Options {
		labels[o.Label] = true
		if recommended == "" && recommendedLabel(o.Label) {
			recommended = o.Label
		}
	}
	switch {
	case !answered:
		return "unanswered"
	case q.MultiSelect:
		parts := strings.Split(answer, ", ")
		for _, p := range parts {
			if !labels[p] {
				return "unparsed"
			}
		}
		if recommended != "" && slices.Contains(parts, recommended) {
			return "recommended"
		}
		return "other_option"
	case recommended != "" && answer == recommended:
		return "recommended"
	case labels[answer]:
		return "other_option"
	}
	return "free_text"
}

func (c *toolCall) answered() bool {
	switch c.name {
	case "AskUserQuestion":
		return c.hasResult && !c.isError && len(c.answers) > 0
	case "request_user_input":
		return c.userAnswered
	}
	return false
}

func userInputAnswered(output string) bool {
	var v struct {
		Answers map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	if json.Unmarshal([]byte(output), &v) != nil {
		return false
	}
	for _, a := range v.Answers {
		if len(a.Answers) > 0 {
			return true
		}
	}
	return false
}

// commandKey is a shell line's first two words, past any cd and variable
// assignments; empty when the line has no readable command.
func commandKey(command string) string {
	commands, _ := gitcmd.CommandWords(command)
	for _, words := range commands {
		if words[0] == "cd" {
			continue
		}
		key := words[0]
		// A second word that carries a URL, a user@host or an assignment
		// can hold a credential and says little about the command.
		if len(words) > 1 && !strings.ContainsAny(words[1], "=@") && !strings.Contains(words[1], "://") {
			key += " " + words[1]
		}
		return Mask(key)
	}
	return ""
}

type repeat struct {
	text        string
	sessions    int
	occurrences int
	last        *fileSummary
	latest      *humanInput
	// sessionSet identifies the sessions the line was typed in. Files are
	// merged in one order, so lines typed in the same sessions hash alike.
	sessionSet uint64
}

type sessionStep struct {
	prev uint64
	file *fileSummary
}

// source is where a normalised assistant line was said: how many interactive
// sessions said it, and the first two with the time each first did.
type source struct {
	sessions uint8
	last     *fileSummary
	file     [2]*fileSummary
	ts       [2]int64
}

type pasteCandidate struct {
	input  *humanInput
	hashes []uint64
}

// index adds an interactive file's lines to the cross-session signals.
func (a *audit) index(f *fileData, file *fileSummary, inputs []*humanInput) {
	for _, in := range inputs {
		var hashes []uint64
		here := map[string]bool{}
		for _, line := range strings.Split(in.text, "\n") {
			n := normalizeLine(line)
			if here[n] {
				continue
			}
			here[n] = true
			runes := utf8.RuneCountInString(n)
			if runes >= repeatedRunes {
				r := a.repeated[n]
				if r == nil {
					r = &repeat{text: strings.TrimSpace(line)}
					a.repeated[n] = r
				}
				if r.last != file {
					r.sessions++
					r.last = file
					r.sessionSet = maphash.Comparable(hashSeed, sessionStep{r.sessionSet, file})
				}
				r.occurrences++
				if r.latest == nil || in.ts.After(r.latest.ts) {
					r.latest = in
				}
			}
			if runes >= pasteRunes {
				hashes = append(hashes, maphash.String(hashSeed, n))
			}
		}
		if len(hashes) >= pasteLines {
			a.pastes = append(a.pastes, pasteCandidate{in, hashes})
		}
	}
	// A Claude record a file read earlier holds is a resumed session's copy of
	// the same assistant, not another session saying it.
	copied := map[string]bool{}
	for _, o := range f.assistant {
		if o.uuid != "" {
			skip, decided := copied[o.uuid]
			if !decided {
				skip = !a.once("claude:assistant:" + o.uuid)
				copied[o.uuid] = skip
			}
			if skip {
				continue
			}
		}
		n := normalizeLine(o.text)
		if utf8.RuneCountInString(n) < pasteRunes {
			continue
		}
		h := maphash.String(hashSeed, n)
		s := a.sources[h]
		if s.last == file {
			continue
		}
		s.last = file
		if s.sessions < 255 {
			s.sessions++
		}
		if s.sessions <= 2 {
			s.file[s.sessions-1], s.ts[s.sessions-1] = file, o.ts.UnixNano()
		}
		a.sources[h] = s
	}
}

type signalHead struct {
	// Evidence is "candidate" for a signal that matches fixed words or
	// adjacency, "observed" for one the records state.
	Evidence   string   `json:"evidence"`
	Categories []string `json:"categories"`
	Agents     []string `json:"agents"`
}

type Signals struct {
	GoAhead           GoAhead           `json:"go_ahead"`
	RecommendedAnswer RecommendedAnswer `json:"recommended_answer"`
	Permission        Permission        `json:"permission"`
	Rejected          Rejected          `json:"rejected"`
	Interrupt         Interrupt         `json:"interrupt"`
	ErrorHandoff      ErrorHandoff      `json:"error_handoff"`
	Correction        Correction        `json:"correction"`
	RepeatedLine      RepeatedLine      `json:"repeated_line"`
	PastedOutput      PastedOutput      `json:"pasted_output"`
	RecurringCommand  RecurringCommand  `json:"recurring_command"`
	AfterCompaction   AfterCompaction   `json:"after_compaction"`
}

type GoAhead struct {
	signalHead
	Count int `json:"count"`
	After struct {
		Asked     int `json:"asked"`
		Announced int `json:"announced"`
		Other     int `json:"other"`
	} `json:"after"`
	AcceptedSuggestion int       `json:"accepted_suggestion"`
	Vocabulary         []string  `json:"vocabulary"`
	AnnounceVocabulary []string  `json:"announce_vocabulary"`
	Examples           []Example `json:"examples"`
}

type RecommendedAnswer struct {
	signalHead
	Claude struct {
		Questions             int      `json:"questions"`
		WithoutRecommendation int      `json:"without_recommendation"`
		Recommended           int      `json:"recommended"`
		OtherOption           int      `json:"other_option"`
		FreeText              int      `json:"free_text"`
		Unparsed              int      `json:"unparsed"`
		Unanswered            int      `json:"unanswered"`
		WithNote              int      `json:"with_note"`
		Markers               []string `json:"markers"`
	} `json:"claude"`
	Codex struct {
		Requests   int `json:"requests"`
		Answered   int `json:"answered"`
		Unanswered int `json:"unanswered"`
		Async      int `json:"async"`
	} `json:"codex"`
	Examples []Example `json:"examples"`
}

type ToolCount struct {
	Source string `json:"source"`
	Tool   string `json:"tool"`
	Count  int    `json:"count"`
}

type CommandCount struct {
	Source  string `json:"source"`
	Command string `json:"command"`
	Count   int    `json:"count"`
}

type Permission struct {
	signalHead
	ObservedResults   int `json:"observed_results"`
	UnobservedResults int `json:"unobserved_results"`
	// Config is every decision the configuration made, as one total.
	Config   int            `json:"config"`
	BySource map[string]int `json:"by_source"`
	// RejectedBySource are the decisions in BySource that rejected; they are
	// no approval and have no example.
	RejectedBySource map[string]int `json:"rejected_by_source"`
	ByTool           []ToolCount    `json:"by_tool"`
	BashCommands     []CommandCount `json:"bash_commands"`
	Examples         []Example      `json:"examples"`
}

type Rejected struct {
	signalHead
	Human    map[string]int `json:"human"`
	Machine  map[string]int `json:"machine"`
	Unknown  map[string]int `json:"unknown"`
	ByTool   []ToolCount    `json:"by_tool"`
	Examples []Example      `json:"examples"`
}

type Interrupt struct {
	signalHead
	Claude int `json:"claude"`
	Codex  int `json:"codex"`
	// CodexOtherAborts are Codex turns aborted for a reason other than
	// "interrupted"; they are no interrupt and have no example.
	CodexOtherAborts int       `json:"codex_other_aborts"`
	Examples         []Example `json:"examples"`
}

type ErrorHandoff struct {
	signalHead
	APIError  int       `json:"api_error"`
	ToolError int       `json:"tool_error"`
	Examples  []Example `json:"examples"`
}

type Correction struct {
	signalHead
	Count      int       `json:"count"`
	Vocabulary []string  `json:"vocabulary"`
	Examples   []Example `json:"examples"`
}

type RepeatedText struct {
	Text        string `json:"text"`
	Sessions    int    `json:"sessions"`
	Occurrences int    `json:"occurrences"`
	// BlockLines counts the qualifying lines typed in exactly these sessions,
	// this one included: a block pasted again and again is listed once.
	BlockLines int     `json:"block_lines"`
	Example    Example `json:"example"`
}

type RepeatedLine struct {
	signalHead
	MinChars    int            `json:"min_chars"`
	MinSessions int            `json:"min_sessions"`
	Lines       []RepeatedText `json:"lines"`
}

type AgentPair struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Count int    `json:"count"`
}

type PastedOutput struct {
	signalHead
	MinChars      int         `json:"min_chars"`
	MinLines      int         `json:"min_lines"`
	StockSessions int         `json:"stock_sessions"`
	Count         int         `json:"count"`
	Pairs         []AgentPair `json:"pairs"`
	StockLineHits int         `json:"stock_line_hits"`
	Examples      []Example   `json:"examples"`
}

type CommandSessions struct {
	Command  string   `json:"command"`
	Sessions int      `json:"sessions"`
	Calls    int      `json:"calls"`
	Agents   []string `json:"agents"`
}

type RecurringCommand struct {
	signalHead
	MinSessions int               `json:"min_sessions"`
	Commands    []CommandSessions `json:"commands"`
	Unparsed    int               `json:"unparsed"`
}

type AfterCompaction struct {
	signalHead
	MinChars int       `json:"min_chars"`
	Count    int       `json:"count"`
	Examples []Example `json:"examples"`
}

func head(evidence string, agents []string, categories ...string) signalHead {
	return signalHead{Evidence: evidence, Categories: categories, Agents: agents}
}

// pick turns the most recent events into examples.
func (a *audit) pick(events []event) []Example {
	sort.SliceStable(events, func(i, j int) bool {
		x, y := events[i], events[j]
		if !x.ts.Equal(y.ts) {
			return x.ts.After(y.ts)
		}
		if x.file.path != y.file.path {
			return x.file.path < y.file.path
		}
		return x.line < y.line
	})
	out := []Example{}
	for _, ev := range events[:min(len(events), maxExamples)] {
		out = append(out, a.example(ev.file, ev.ts, ev.line, ev.text))
	}
	return out
}

func toolExcerpt(c *callRecord) string {
	if c.shell && c.command != "" {
		return c.name + ": " + c.command
	}
	return c.name
}

func (a *audit) result() *Result {
	both, claude := []string{"claude", "codex"}, []string{"claude"}
	s := Signals{}
	s.GoAhead.signalHead = head("candidate", both, "A", "B", "D")
	s.GoAhead.Vocabulary, s.GoAhead.AnnounceVocabulary = goAheadWords, announceWords
	s.Correction.signalHead = head("candidate", both, "H", "J")
	s.Correction.Vocabulary = correctionWords
	var goAhead, corrections []event
	for _, in := range a.inputs {
		ev := event{file: in.file, ts: in.ts, line: in.line, text: in.text}
		switch {
		case in.suggestion:
			s.GoAhead.AcceptedSuggestion++
		case isGoAhead(in.text):
			s.GoAhead.Count++
			switch afterKind(in.prev) {
			case "asked":
				s.GoAhead.After.Asked++
			case "announced":
				s.GoAhead.After.Announced++
			default:
				s.GoAhead.After.Other++
			}
			goAhead = append(goAhead, ev)
		}
		if isCorrection(in.text) {
			s.Correction.Count++
			corrections = append(corrections, ev)
		}
	}
	s.GoAhead.Examples, s.Correction.Examples = a.pick(goAhead), a.pick(corrections)

	s.RecommendedAnswer.signalHead = head("observed", both, "B", "D")
	s.RecommendedAnswer.Claude.Markers = recommendMarkers
	s.Permission.signalHead = head("observed", claude, "D")
	s.Permission.BySource, s.Permission.RejectedBySource = map[string]int{}, map[string]int{}
	s.Rejected.signalHead = head("observed", claude, "D", "I")
	s.Rejected.Human, s.Rejected.Machine, s.Rejected.Unknown = map[string]int{}, map[string]int{}, map[string]int{}
	s.RecurringCommand.signalHead = head("observed", both, "F")
	s.RecurringCommand.MinSessions = commandSessions
	var answers, approvals, rejections []event
	localDecisions := map[string]string{}
	byTool, bashCommands, rejectedTools := map[[2]string]int{}, map[[2]string]int{}, map[[2]string]int{}
	type commandIndex struct {
		sessions, calls int
		last            *fileSummary
		agents          map[string]bool
	}
	commands := map[string]*commandIndex{}
	for _, key := range a.callIDs {
		c := a.calls[key]
		if c.file.agent == "codex" && (c.name == "request_user_input" || c.name == "request_user_input_async") {
			r := &s.RecommendedAnswer.Codex
			r.Requests++
			switch {
			case c.name == "request_user_input_async":
				r.Async++
			case c.answered():
				r.Answered++
			default:
				r.Unanswered++
			}
		}
		if c.name == "AskUserQuestion" && c.file.agent == "claude" {
			questions := c.questions
			if len(questions) == 0 {
				questions = c.resultQuest
			}
			r := &s.RecommendedAnswer.Claude
			asked := map[string]bool{}
			for _, q := range questions {
				if asked[q.Question] {
					continue
				}
				asked[q.Question] = true
				r.Questions++
				if !slices.ContainsFunc(q.Options, func(o option) bool { return recommendedLabel(o.Label) }) {
					r.WithoutRecommendation++
				}
				answer, ok := c.answers[q.Question]
				class := classifyAnswer(q, answer, ok && c.hasResult && !c.isError)
				switch class {
				case "recommended":
					r.Recommended++
				case "other_option":
					r.OtherOption++
				case "free_text":
					r.FreeText++
				case "unparsed":
					r.Unparsed++
				default:
					r.Unanswered++
				}
				if c.notes[q.Question] {
					r.WithNote++
				}
				if class == "other_option" || class == "free_text" || class == "unparsed" {
					answers = append(answers, event{file: c.resultFile, ts: c.resultTS, line: c.resultLine, text: q.Question + " → " + answer})
				}
			}
		}
		if c.file.agent == "claude" && c.hasResult {
			p := &s.Permission
			if !c.observed {
				p.UnobservedResults++
				c.resultFile.home.PermissionUnobserved++
			} else {
				p.ObservedResults++
				localDecisions[c.id] = c.permission
				switch {
				case c.permission == "":
					c.permission = "unspecified"
				case !permissionSources[c.permission]:
					c.permission = "other"
				}
				if c.permission == "config" {
					p.Config++
				} else {
					p.BySource[c.permission]++
					byTool[[2]string{c.permission, c.name}]++
					if c.shell {
						if k := commandKey(c.command); k != "" {
							bashCommands[[2]string{c.permission, k}]++
						}
					}
					if c.rejectedDecision {
						p.RejectedBySource[c.permission]++
					} else if strings.HasPrefix(c.permission, "user_") {
						approvals = append(approvals, event{file: c.resultFile, ts: c.resultTS, line: c.resultLine, text: toolExcerpt(c)})
					}
				}
			}
			if _, ok := localDecisions[c.id]; !ok && c.denial != "" {
				localDecisions[c.id] = denialSources[c.denial]
			}
			switch {
			case c.denial == "":
			case humanDenials[c.denial]:
				s.Rejected.Human[c.denial]++
				rejectedTools[[2]string{c.denial, c.name}]++
				rejections = append(rejections, event{file: c.resultFile, ts: c.resultTS, line: c.resultLine, text: toolExcerpt(c)})
			case machineDenials[c.denial]:
				s.Rejected.Machine[c.denial]++
			default:
				s.Rejected.Unknown[Mask(c.denial)]++
			}
		}
		if c.shell && c.file.interactive {
			k := commandKey(c.command)
			if k == "" {
				s.RecurringCommand.Unparsed++
				continue
			}
			ci := commands[k]
			if ci == nil {
				ci = &commandIndex{agents: map[string]bool{}}
				commands[k] = ci
			}
			if ci.last != c.file {
				ci.sessions++
				ci.last = c.file
			}
			ci.calls++
			ci.agents[c.file.agent] = true
		}
	}
	s.RecommendedAnswer.Examples = a.pick(answers)
	s.Permission.ByTool = toolCounts(byTool)
	s.Permission.BashCommands = commandCounts(bashCommands)
	s.Permission.Examples = a.pick(approvals)
	s.Rejected.ByTool = toolCounts(rejectedTools)
	s.Rejected.Examples = a.pick(rejections)
	s.RecurringCommand.Commands = []CommandSessions{}
	for k, ci := range commands {
		if ci.sessions >= commandSessions {
			agents := []string{}
			for _, name := range both {
				if ci.agents[name] {
					agents = append(agents, name)
				}
			}
			s.RecurringCommand.Commands = append(s.RecurringCommand.Commands, CommandSessions{k, ci.sessions, ci.calls, agents})
		}
	}
	sort.Slice(s.RecurringCommand.Commands, func(i, j int) bool {
		x, y := s.RecurringCommand.Commands[i], s.RecurringCommand.Commands[j]
		if x.Sessions != y.Sessions {
			return x.Sessions > y.Sessions
		}
		if x.Calls != y.Calls {
			return x.Calls > y.Calls
		}
		return x.Command < y.Command
	})
	s.RecurringCommand.Commands = s.RecurringCommand.Commands[:min(len(s.RecurringCommand.Commands), maxCommands)]

	s.Interrupt.signalHead = head("observed", both, "H", "I")
	for _, ev := range a.interrupts {
		if ev.kind == "codex" {
			s.Interrupt.Codex++
		} else {
			s.Interrupt.Claude++
		}
	}
	s.Interrupt.CodexOtherAborts = a.otherAborts
	s.Interrupt.Examples = a.pick(a.interrupts)
	s.ErrorHandoff.signalHead = head("candidate", claude, "I")
	for _, ev := range a.errorHandoffs {
		if ev.kind == "api_error" {
			s.ErrorHandoff.APIError++
		} else {
			s.ErrorHandoff.ToolError++
		}
	}
	s.ErrorHandoff.Examples = a.pick(a.errorHandoffs)
	s.AfterCompaction.signalHead = head("candidate", claude, "C", "J")
	s.AfterCompaction.MinChars, s.AfterCompaction.Count = afterCompactionRunes, len(a.afterCompaction)
	s.AfterCompaction.Examples = a.pick(a.afterCompaction)

	s.RepeatedLine = a.repeatedLines()
	s.PastedOutput = a.pastedOutput()
	return &Result{
		Window:         Window{Since: a.opts.Since, Until: a.opts.Now, Agent: a.opts.Agent},
		Coverage:       a.coverage(),
		Signals:        s,
		Caveats:        a.caveats(),
		localDecisions: localDecisions,
	}
}

func toolCounts(counts map[[2]string]int) []ToolCount {
	out := []ToolCount{}
	for k, n := range counts {
		out = append(out, ToolCount{k[0], Mask(k[1]), n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Source+out[i].Tool < out[j].Source+out[j].Tool
	})
	return out[:min(len(out), maxBreakdown)]
}

func commandCounts(counts map[[2]string]int) []CommandCount {
	out := []CommandCount{}
	for k, n := range counts {
		out = append(out, CommandCount{k[0], k[1], n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Source+out[i].Command < out[j].Source+out[j].Command
	})
	return out[:min(len(out), maxBreakdown)]
}

func (a *audit) repeatedLines() RepeatedLine {
	out := RepeatedLine{signalHead: head("observed", []string{"claude", "codex"}, "C", "J"), MinChars: repeatedRunes, MinSessions: repeatedSessions, Lines: []RepeatedText{}}
	var keys []string
	for k, r := range a.repeated {
		if r.sessions >= repeatedSessions {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		x, y := a.repeated[keys[i]], a.repeated[keys[j]]
		if x.sessions != y.sessions {
			return x.sessions > y.sessions
		}
		if x.occurrences != y.occurrences {
			return x.occurrences > y.occurrences
		}
		return keys[i] < keys[j]
	})
	// A line typed in exactly the sessions of a listed one belongs to the same
	// block: it adds to that entry's count instead of taking a place.
	listed := map[uint64]int{}
	for _, k := range keys {
		r := a.repeated[k]
		if i, ok := listed[r.sessionSet]; ok {
			out.Lines[i].BlockLines++
			continue
		}
		if len(out.Lines) == maxRepeated {
			continue
		}
		listed[r.sessionSet] = len(out.Lines)
		out.Lines = append(out.Lines, RepeatedText{Text: excerpt(r.text), Sessions: r.sessions, Occurrences: r.occurrences, BlockLines: 1, Example: a.example(r.latest.file, r.latest.ts, r.latest.line, r.text)})
	}
	return out
}

// pastedOutput finds inputs with two or more lines an assistant of another
// interactive session said earlier, leaving out lines many sessions say.
func (a *audit) pastedOutput() PastedOutput {
	out := PastedOutput{signalHead: head("observed", []string{"claude", "codex"}, "E", "G"), MinChars: pasteRunes, MinLines: pasteLines, StockSessions: stockSessions, Pairs: []AgentPair{}}
	pairs := map[[2]string]int{}
	var events []event
	for _, p := range a.pastes {
		tally := map[*fileSummary]int{}
		at := p.input.ts.UnixNano()
		for _, h := range p.hashes {
			src, ok := a.sources[h]
			if !ok {
				continue
			}
			if src.sessions >= stockSessions {
				out.StockLineHits++
				continue
			}
			for i := 0; i < int(src.sessions); i++ {
				if src.file[i] != p.input.file && src.ts[i] < at {
					tally[src.file[i]]++
				}
			}
		}
		var from *fileSummary
		for file, n := range tally {
			if n >= pasteLines && (from == nil || n > tally[from] || n == tally[from] && file.path < from.path) {
				from = file
			}
		}
		if from == nil {
			continue
		}
		out.Count++
		pairs[[2]string{from.agent, p.input.file.agent}]++
		events = append(events, event{file: p.input.file, ts: p.input.ts, line: p.input.line, text: p.input.text})
	}
	for k, n := range pairs {
		out.Pairs = append(out.Pairs, AgentPair{k[0], k[1], n})
	}
	sort.Slice(out.Pairs, func(i, j int) bool { return out.Pairs[i].From+out.Pairs[i].To < out.Pairs[j].From+out.Pairs[j].To })
	out.Examples = a.pick(events)
	return out
}

func (a *audit) coverage() Coverage {
	c := a.cover
	c.Homes = a.order
	if c.Homes == nil {
		c.Homes = []*HomeCoverage{}
	}
	return c
}

func (a *audit) caveats() []Caveat {
	out := []Caveat{
		{"candidates", "go_ahead, correction, error_handoff and after_compaction match fixed words or adjacency; read the source lines before treating one as a dependency"},
		{"elapsed_time_not_evidence", "time between records is not evidence of waiting, effort or delay"},
		{"correlation_only", "the signals locate where work returned to a person; they do not say why"},
	}
	var changed, failed, oversized int
	localOnly := false
	for _, h := range a.order {
		changed += h.Changed
		failed += h.ReadErrors
		oversized += h.OversizedLines
		localOnly = localOnly || h.Transmission == "exclude" || h.Transmission == "undecided" || h.Transmission == "mixed"
	}
	if localOnly {
		out = append(out, Caveat{"homes_read_locally_only", "homes whose profile excludes or has not decided transmission are read here only: nothing of them goes to the cctrace server, but this output's excerpts reach the model provider of the agent reading it"})
	}
	if changed > 0 {
		out = append(out, Caveat{"changed_while_read", "files written to while they were read were skipped; run again after the sessions are idle to include them"})
	}
	if failed > 0 {
		out = append(out, Caveat{"read_errors", "some files could not be read and were skipped"})
	}
	if oversized > 0 {
		out = append(out, Caveat{"oversized_lines", "lines over 16 MiB were not read; the files holding them have undeterminable turns"})
	}
	for _, home := range a.codexHomes {
		if fi, err := os.Stat(filepath.Join(home, "archived_sessions")); err == nil && fi.IsDir() {
			out = append(out, Caveat{"codex_archived_not_scanned", "Codex archived_sessions directories are not read"})
			break
		}
	}
	return out
}
