package sessionrecord

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxTurnBytes       = 12_288
	MaxInputBytes      = 96 * 1024
	MaxRequestsPerCall = 30
	redactedMarker     = "[redacted by cctrace client]"
)

var injectedBlock = regexp.MustCompile(`(?is)<system-reminder[^>]*>.*?</system-reminder\s*>|<command-[^>]*>.*?</command-[^>]*>|<local-command-[^>]*>.*?</local-command-[^>]*>|<bash-[^>]*>.*?</bash-[^>]*>|<environment_context[^>]*>.*?</environment_context\s*>|<user_instructions[^>]*>.*?</user_instructions\s*>`)
var promptTurnName = regexp.MustCompile(`^t[1-9][0-9]*$`)

type Input struct {
	Language   string
	Vocabulary Vocabulary
	Prior      *Prior
	Turns      []Turn
}

type Vocabulary struct {
	Components       []VocabularyTerm `json:"components"`
	Screens          []VocabularyTerm `json:"screens"`
	ComponentsClosed bool             `json:"components_closed"`
	ScreensClosed    bool             `json:"screens_closed"`
}

type VocabularyTerm struct {
	Term     string `json:"term"`
	Sessions int    `json:"sessions"`
}

type Prior struct {
	Headline   string      `json:"headline"`
	Bullets    []string    `json:"bullets"`
	Review     []PriorNote `json:"review"`
	Kinds      []string    `json:"kinds"`
	Components []string    `json:"components"`
	Screens    []string    `json:"screens"`
	OpenWork   []PriorWork `json:"open_work"`
	DoneCount  int         `json:"done_count"`
}

type PriorNote struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type PriorWork struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"`
	Text     string  `json:"text"`
	Decision *string `json:"decision"`
	Note     *string `json:"note"`
}

type Turn struct {
	Name           string
	Key            string
	Ordinal        int
	Request        bool
	StartedAt      time.Time
	Status         string
	InputKind      string
	PermissionMode string
	Denied         bool
	PreviewState   string
	Inputs         []Text
	Questions      []Question
	Plan           *Plan
	Final          string
	Tools          []Tool
	Commits        []string
	Tasks          []Task
	PRs            []string
}

type Text struct {
	Kind          string
	Text          string
	CommandName   string
	CommandInvoke string
	BashInput     bool
	ProgramName   string
	Unavailable   bool
}

type Question struct {
	Headers     []string
	Text        string
	Options     []Option
	Answer      string
	Chosen      string
	Unavailable bool
}

type Option struct {
	Name        string
	Description string
}

type Plan struct {
	Text        string
	Decision    string
	Unavailable bool
}

type Tool struct {
	Name               string
	Calls              int
	Failures           *int
	Targets            []string
	TargetsUnavailable bool
}

type Task struct {
	ID     string
	Title  string
	Action string
}

type BuiltInput struct {
	JSON              []byte
	TurnKeys          map[string]string
	TurnNames         []string
	RequestTurns      []string
	UncoveredRequests int
	Gaps              []string
}

type wireInput struct {
	Language   string     `json:"language"`
	Vocabulary Vocabulary `json:"vocabulary"`
	Prior      *Prior     `json:"prior,omitempty"`
	Turns      []wireTurn `json:"turns"`
}

type wireTurn struct {
	Name      string         `json:"t"`
	Request   bool           `json:"request"`
	At        string         `json:"at"`
	Status    string         `json:"status"`
	Inputs    []wireText     `json:"inputs"`
	Questions []wireQuestion `json:"questions"`
	Plan      *wirePlan      `json:"plan"`
	Final     *string        `json:"final"`
	Tools     []wireTool     `json:"tools"`
	Commits   []string       `json:"commits"`
	Tasks     []Task         `json:"tasks"`
	PRs       []string       `json:"prs"`
}

type wireText struct {
	Kind string  `json:"kind"`
	Text *string `json:"text"`
}

type wireQuestion struct {
	Headers     []string     `json:"headers"`
	Text        *string      `json:"question"`
	Options     []wireOption `json:"options"`
	Answer      *string      `json:"answer"`
	Chosen      *string      `json:"chosen"`
	Unavailable bool         `json:"unavailable"`
}

type wireOption struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type wirePlan struct {
	Text        *string `json:"text"`
	Decision    *string `json:"decision"`
	Unavailable bool    `json:"unavailable"`
}

type wireTool struct {
	Name     string   `json:"name"`
	Calls    int      `json:"calls"`
	Failures *int     `json:"failed"`
	Targets  []string `json:"targets"`
}

// BuildInput converts already owner- and visibility-checked source rows into the
// bounded JSON document sent to an API runtime. The source turn keys remain only
// in TurnKeys; they are never part of the serialized prompt.
func BuildInput(input Input) (BuiltInput, error) {
	if input.Language == "" {
		input.Language = DetectLanguage(input.Turns)
	}
	if input.Language != "ko" && input.Language != "ja" && input.Language != "en" {
		return BuiltInput{}, fmt.Errorf("unsupported record language %q", input.Language)
	}
	w := wireInput{Language: input.Language, Vocabulary: boundVocabulary(input.Vocabulary), Prior: boundPrior(input.Prior), Turns: make([]wireTurn, 0, len(input.Turns))}
	keys := make(map[string]string, len(input.Turns))
	turnNames := make([]string, 0, len(input.Turns))
	requests := make([]string, 0, len(input.Turns))
	gaps := map[string]bool{}
	uncovered := 0
	seen := map[string]bool{}
	seenNames := map[string]bool{}
	for i, source := range input.Turns {
		if source.Key == "" || seen[source.Key] {
			return BuiltInput{}, fmt.Errorf("turn %d has empty or duplicate key", i+1)
		}
		seen[source.Key] = true
		name := source.Name
		if name == "" {
			name = fmt.Sprintf("t%d", i+1)
		}
		if !promptTurnName.MatchString(name) || seenNames[name] {
			return BuiltInput{}, fmt.Errorf("turn %d has invalid prompt name %q", i+1, name)
		}
		seenNames[name] = true
		keys[name] = source.Key
		turnNames = append(turnNames, name)
		if source.Request {
			requests = append(requests, name)
		}
		turn, inputGap, turnGaps := buildTurn(name, source)
		if source.Request && inputGap {
			uncovered++
		}
		for _, gap := range turnGaps {
			gaps[gap] = true
		}
		if err := fitTurn(&turn); err != nil {
			return BuiltInput{}, fmt.Errorf("bound %s: %w", name, err)
		}
		w.Turns = append(w.Turns, turn)
	}
	if len(requests) > MaxRequestsPerCall {
		return BuiltInput{}, fmt.Errorf("input has %d request turns; split at %d", len(requests), MaxRequestsPerCall)
	}
	b, err := json.Marshal(w)
	if err != nil {
		return BuiltInput{}, err
	}
	if len(b) > MaxInputBytes {
		return BuiltInput{}, fmt.Errorf("input is %d bytes; maximum is %d", len(b), MaxInputBytes)
	}
	gapList := make([]string, 0, len(gaps))
	for _, name := range []string{"inputs", "questions", "plans", "tool_targets"} {
		if gaps[name] {
			gapList = append(gapList, name)
		}
	}
	return BuiltInput{JSON: b, TurnKeys: keys, TurnNames: turnNames, RequestTurns: requests, UncoveredRequests: uncovered, Gaps: gapList}, nil
}

// EvidenceText removes administrative vocabulary from serialized model input
// before the output validator checks whether inline code appears in evidence.
// Vocabulary guides tag selection; it does not prove that a session mentioned a
// term.
func EvidenceText(serialized []byte) ([]byte, error) {
	var input wireInput
	if err := json.Unmarshal(serialized, &input); err != nil {
		return nil, fmt.Errorf("decode session record evidence input: %w", err)
	}
	input.Vocabulary = Vocabulary{}
	return json.Marshal(input)
}

// ChunkInput keeps session-wide prompt turn names stable while splitting the
// ordered turns at the design's byte and request-turn limits.
func ChunkInput(input Input) ([]Input, error) {
	turns := append([]Turn(nil), input.Turns...)
	for i := range turns {
		if turns[i].Name == "" {
			turns[i].Name = fmt.Sprintf("t%d", i+1)
		}
	}
	chunks := make([]Input, 0, 1)
	current := make([]Turn, 0)
	for _, turn := range turns {
		candidate := append(append([]Turn(nil), current...), turn)
		probe := input
		probe.Turns = candidate
		if _, err := BuildInput(probe); err == nil {
			current = candidate
			continue
		} else if len(current) == 0 {
			return nil, err
		}
		finished := input
		finished.Turns = current
		chunks = append(chunks, finished)
		current = []Turn{turn}
		probe.Turns = current
		if _, err := BuildInput(probe); err != nil {
			return nil, err
		}
	}
	if len(current) > 0 {
		finished := input
		finished.Turns = current
		chunks = append(chunks, finished)
	}
	return chunks, nil
}

func buildTurn(name string, source Turn) (wireTurn, bool, []string) {
	w := wireTurn{
		Name: name, Request: source.Request, Status: wireStatus(source.Status),
		Inputs: make([]wireText, 0, len(source.Inputs)), Questions: make([]wireQuestion, 0, len(source.Questions)),
		Tools: make([]wireTool, 0), Commits: make([]string, 0), Tasks: make([]Task, 0), PRs: make([]string, 0),
	}
	if !source.StartedAt.IsZero() {
		w.At = source.StartedAt.UTC().Format("2006-01-02T15:04Z")
	}
	inputGap := len(source.Inputs) == 0 && source.Request
	gaps := []string{}
	for _, raw := range source.Inputs {
		text := safeText(raw.Text)
		kind := wireInputKind(raw.Kind)
		if raw.CommandName != "" {
			invoke := strings.TrimSpace(raw.CommandInvoke)
			if invoke == "" {
				text = "/" + raw.CommandName
			} else {
				text = "/" + raw.CommandName + " " + invoke
			}
		}
		if raw.BashInput {
			text = clean(raw.ProgramName)
		}
		var value *string
		if source.Request && !raw.Unavailable && text != "" && !containsRedaction(raw.Text) {
			value = ptr(clipEdges(clean(text), 4096, 3072, 1024))
		} else if source.Request {
			inputGap = true
			gaps = append(gaps, "inputs")
		}
		w.Inputs = append(w.Inputs, wireText{Kind: kind, Text: value})
	}
	for _, q := range source.Questions {
		item := wireQuestion{Headers: boundStrings(q.Headers, 8, 160), Options: make([]wireOption, 0, min(len(q.Options), 8)), Unavailable: q.Unavailable}
		if !q.Unavailable && q.Text != "" && !containsRedaction(q.Text) {
			item.Text = ptr(clipEdges(clean(q.Text), 1024, 768, 256))
		} else {
			item.Unavailable = true
			gaps = append(gaps, "questions")
		}
		for _, option := range q.Options[:min(len(q.Options), 8)] {
			item.Options = append(item.Options, wireOption{Name: clipEdges(clean(option.Name), 64, 64, 0), Description: clipEdges(clean(option.Description), 160, 120, 40)})
		}
		if !q.Unavailable && q.Answer != "" && !containsRedaction(q.Answer) {
			item.Answer = ptr(clipEdges(clean(q.Answer), 512, 384, 128))
		} else if q.Answer != "" {
			item.Unavailable = true
			gaps = append(gaps, "questions")
		}
		if !q.Unavailable && q.Chosen != "" && !containsRedaction(q.Chosen) {
			item.Chosen = ptr(clipEdges(clean(q.Chosen), 160, 120, 40))
		}
		w.Questions = append(w.Questions, item)
	}
	if source.Plan != nil {
		p := &wirePlan{Unavailable: source.Plan.Unavailable}
		if !p.Unavailable && !containsRedaction(source.Plan.Text) {
			p.Text = ptr(clipEdges(clean(source.Plan.Text), 4096, 3072, 1024))
		} else {
			p.Unavailable = true
			gaps = append(gaps, "plans")
		}
		if !p.Unavailable && !containsRedaction(source.Plan.Decision) {
			p.Decision = ptr(clipEdges(clean(source.Plan.Decision), 240, 180, 60))
		}
		w.Plan = p
	}
	finalCap := 1024
	if source.Request {
		finalCap = 3072
	}
	if source.Final == "" || containsRedaction(source.Final) {
		w.Final = nil
	} else {
		w.Final = ptr(clipEdges(clean(source.Final), finalCap, min(finalCap, 1024), max(0, finalCap-1024)))
	}
	toolMap := map[string]*wireTool{}
	for _, tool := range source.Tools {
		if tool.Name == "" {
			continue
		}
		entry := toolMap[tool.Name]
		if entry == nil {
			entry = &wireTool{Name: clipEdges(clean(tool.Name), 80, 80, 0), Failures: cloneInt(tool.Failures), Targets: make([]string, 0)}
			toolMap[tool.Name] = entry
		} else if entry.Failures != nil && tool.Failures != nil {
			*entry.Failures += *tool.Failures
		} else {
			entry.Failures = nil
		}
		entry.Calls += max(0, tool.Calls)
		if tool.TargetsUnavailable {
			gaps = append(gaps, "tool_targets")
		}
		for _, target := range tool.Targets {
			target = safeText(target)
			if target == "" || containsRedaction(target) {
				gaps = append(gaps, "tool_targets")
				continue
			}
			entry.Targets = appendUnique(entry.Targets, clipEdges(clean(target), 200, 160, 40), 10)
		}
	}
	toolNames := make([]string, 0, len(toolMap))
	for toolName := range toolMap {
		toolNames = append(toolNames, toolName)
	}
	sortStrings(toolNames)
	for _, toolName := range toolNames[:min(len(toolNames), 12)] {
		w.Tools = append(w.Tools, *toolMap[toolName])
	}
	for _, commit := range source.Commits[:min(len(source.Commits), 10)] {
		if !containsRedaction(commit) {
			w.Commits = append(w.Commits, clipEdges(clean(commit), 200, 160, 40))
		}
	}
	for _, task := range source.Tasks[:min(len(source.Tasks), 10)] {
		w.Tasks = append(w.Tasks, Task{ID: clipEdges(clean(task.ID), 64, 64, 0), Title: clipEdges(clean(task.Title), 200, 160, 40), Action: clipEdges(clean(task.Action), 24, 24, 0)})
	}
	for _, pr := range source.PRs[:min(len(source.PRs), 10)] {
		w.PRs = append(w.PRs, clipEdges(clean(pr), 32, 32, 0))
	}
	return w, inputGap, gaps
}

func fitTurn(w *wireTurn) error {
	serializedSize := func() int {
		b, _ := json.Marshal(w)
		return len(b)
	}
	for serializedSize() > MaxTurnBytes && w.Final != nil && *w.Final != "" {
		*w.Final = clipEdges(*w.Final, max(0, len(*w.Final)-512), max(0, len(*w.Final)-512), 0)
	}
	if serializedSize() > MaxTurnBytes && w.Plan != nil && w.Plan.Text != nil {
		for serializedSize() > MaxTurnBytes && *w.Plan.Text != "" {
			*w.Plan.Text = clipEdges(*w.Plan.Text, max(0, len(*w.Plan.Text)-512), max(0, len(*w.Plan.Text)-512), 0)
		}
	}
	for i := range w.Inputs {
		for serializedSize() > MaxTurnBytes && w.Inputs[i].Text != nil && *w.Inputs[i].Text != "" {
			*w.Inputs[i].Text = clipEdges(*w.Inputs[i].Text, max(0, len(*w.Inputs[i].Text)-512), max(0, len(*w.Inputs[i].Text)-512), 0)
		}
	}
	for serializedSize() > MaxTurnBytes && len(w.Questions) > 0 {
		w.Questions = w.Questions[:len(w.Questions)-1]
	}
	for serializedSize() > MaxTurnBytes && len(w.Tools) > 0 {
		w.Tools = w.Tools[:len(w.Tools)-1]
	}
	for serializedSize() > MaxTurnBytes && len(w.Commits) > 0 {
		w.Commits = w.Commits[:len(w.Commits)-1]
	}
	for serializedSize() > MaxTurnBytes && len(w.Tasks) > 0 {
		w.Tasks = w.Tasks[:len(w.Tasks)-1]
	}
	for serializedSize() > MaxTurnBytes && len(w.PRs) > 0 {
		w.PRs = w.PRs[:len(w.PRs)-1]
	}
	if n := serializedSize(); n > MaxTurnBytes {
		return fmt.Errorf("encoded turn is %d bytes, maximum is %d", n, MaxTurnBytes)
	}
	return nil
}

func DetectLanguage(turns []Turn) string {
	requests := 0
	var runes []rune
	for _, turn := range turns {
		if !turn.Request {
			continue
		}
		requests++
		for _, input := range turn.Inputs {
			if input.Unavailable || containsRedaction(input.Text) {
				continue
			}
			runes = append(runes, []rune(safeText(input.Text))...)
		}
		if requests >= 20 {
			break
		}
	}
	if len(runes) == 0 {
		return "ko"
	}
	var hangul, kana int
	for _, r := range runes {
		if (r >= 0xAC00 && r <= 0xD7AF) || (r >= 0x1100 && r <= 0x11FF) || (r >= 0x3130 && r <= 0x318F) {
			hangul++
		}
		if (r >= 0x3040 && r <= 0x30FF) || (r >= 0x31F0 && r <= 0x31FF) {
			kana++
		}
	}
	if float64(hangul)/float64(len(runes)) >= .20 {
		return "ko"
	}
	if float64(kana)/float64(len(runes)) >= .10 {
		return "ja"
	}
	return "en"
}

func wireStatus(s string) string {
	if s == "undeterminable" || s == "" {
		return "unknown"
	}
	return s
}

func wireInputKind(s string) string {
	switch s {
	case "typed", "unknown":
		return "typed"
	case "queued":
		return "queued"
	case "command":
		return "command"
	default:
		return "notification"
	}
}

func boundVocabulary(v Vocabulary) Vocabulary {
	v.Components = boundVocabularyTerms(v.Components)
	v.Screens = boundVocabularyTerms(v.Screens)
	return v
}

func boundVocabularyTerms(terms []VocabularyTerm) []VocabularyTerm {
	out := make([]VocabularyTerm, 0, min(len(terms), 24))
	for _, term := range terms[:min(len(terms), 24)] {
		name := clipEdges(clean(term.Term), 96, 96, 0)
		if name == "" || containsRedaction(name) {
			continue
		}
		out = append(out, VocabularyTerm{Term: name, Sessions: max(0, term.Sessions)})
	}
	return out
}

func boundPrior(p *Prior) *Prior {
	if p == nil {
		return nil
	}
	c := *p
	c.Headline = clipEdges(clean(c.Headline), 240, 200, 40)
	c.Bullets = boundStrings(c.Bullets, 6, 240)
	c.Review = c.Review[:min(len(c.Review), 8)]
	c.OpenWork = c.OpenWork[:min(len(c.OpenWork), 12)]
	for i := range c.Review {
		c.Review[i].Text = clipEdges(clean(c.Review[i].Text), 240, 200, 40)
	}
	for i := range c.OpenWork {
		c.OpenWork[i].Text = clipEdges(clean(c.OpenWork[i].Text), 200, 160, 40)
	}
	for priorSize(&c) > 3072 {
		if len(c.Bullets) > 0 {
			c.Bullets = c.Bullets[:len(c.Bullets)-1]
		} else if len(c.Review) > 0 {
			c.Review = c.Review[:len(c.Review)-1]
		} else if len(c.OpenWork) > 0 {
			c.OpenWork = c.OpenWork[:len(c.OpenWork)-1]
		} else {
			c.Headline = clipEdges(c.Headline, max(0, len(c.Headline)-128), max(0, len(c.Headline)-128), 0)
			if c.Headline == "" {
				break
			}
		}
	}
	return &c
}

func priorSize(p *Prior) int {
	b, _ := json.Marshal(p)
	return len(b)
}

func boundStrings(values []string, count, chars int) []string {
	out := make([]string, 0, min(len(values), count))
	for _, value := range values[:min(len(values), count)] {
		if value = clean(value); value != "" && !containsRedaction(value) {
			out = append(out, clipEdges(value, chars*4, chars*4, 0))
		}
	}
	return out
}

func clean(s string) string {
	s = injectedBlock.ReplaceAllString(s, " ")
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			if !space {
				b.WriteByte(' ')
				space = true
			}
			continue
		}
		if unicode.IsSpace(r) {
			if !space {
				b.WriteByte(' ')
				space = true
			}
			continue
		}
		b.WriteRune(r)
		space = false
	}
	return strings.TrimSpace(b.String())
}

func safeText(s string) string {
	if strings.Contains(s, redactedMarker) {
		return ""
	}
	return s
}

func containsRedaction(s string) bool { return strings.Contains(s, redactedMarker) }

func clipEdges(s string, maxBytes, headBytes, tailBytes int) string {
	s = strings.ToValidUTF8(s, "�")
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	const marker = "…(중략)…"
	if maxBytes <= len(marker) {
		return validPrefix(s, maxBytes)
	}
	headBytes = min(headBytes, maxBytes-len(marker))
	tailBytes = min(tailBytes, maxBytes-len(marker)-headBytes)
	for len(marker)+headBytes+tailBytes > maxBytes {
		if headBytes >= tailBytes {
			headBytes--
		} else {
			tailBytes--
		}
	}
	head := validPrefix(s, headBytes)
	tail := validSuffix(s, tailBytes)
	return head + marker + tail
}

func validPrefix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	return s[:n]
}

func validSuffix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.ValidString(s[start:]) {
		start++
	}
	return s[start:]
}

func appendUnique(dst []string, value string, maxCount int) []string {
	for _, existing := range dst {
		if existing == value {
			return dst
		}
	}
	if len(dst) < maxCount {
		return append(dst, value)
	}
	return dst
}

func sortStrings(values []string) {
	sort.Strings(values)
}

func ptr(s string) *string { return &s }

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	c := max(0, *value)
	return &c
}
