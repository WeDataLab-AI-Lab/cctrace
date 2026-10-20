package sessionrecord

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"cctrace/internal/airuntime"
)

const PromptVersion = 1
const MinServablePromptVersion = 1

var OutputSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["headline", "bullets", "review", "kinds", "components", "screens", "new_component", "new_screen", "work", "skipped", "work_updates"],
  "properties": {
    "headline": {"type": "string", "maxLength": 120},
    "bullets": {"type": "array", "maxItems": 6, "items": {
      "type": "object", "additionalProperties": false, "required": ["text", "turns"],
      "properties": {
        "text": {"type": "string", "maxLength": 240},
        "turns": {"type": "array", "maxItems": 8, "items": {"type": "string", "maxLength": 8}}
      }
    }},
    "review": {"type": "array", "maxItems": 8, "items": {
      "type": "object", "additionalProperties": false, "required": ["kind", "text", "turn"],
      "properties": {
        "kind": {"type": "string", "enum": ["decision", "interpretation", "unverified"]},
        "text": {"type": "string", "maxLength": 240},
        "turn": {"type": "string", "maxLength": 8}
      }
    }},
    "kinds": {"type": "array", "maxItems": 3, "items": {"type": "string", "enum": ["implementation", "research", "maintenance", "consultation", "bugfix", "docs"]}},
    "components": {"type": "array", "maxItems": 3, "items": {"type": "string", "maxLength": 24}},
    "screens": {"type": "array", "maxItems": 3, "items": {"type": "string", "maxLength": 24}},
    "new_component": {"anyOf": [{"type": "string", "maxLength": 24}, {"type": "null"}]},
    "new_screen": {"anyOf": [{"type": "string", "maxLength": 24}, {"type": "null"}]},
    "work": {"type": "array", "maxItems": 60, "items": {
      "type": "object", "additionalProperties": false, "required": ["turn", "kind", "text", "done", "decision", "note", "children"],
      "properties": {
        "turn": {"type": "string", "maxLength": 8},
        "kind": {"type": "string", "enum": ["request", "followup", "consultation", "finishing"]},
        "text": {"type": "string", "maxLength": 200},
        "done": {"anyOf": [{"type": "boolean"}, {"type": "null"}]},
        "decision": {"anyOf": [{"type": "string", "maxLength": 200}, {"type": "null"}]},
        "note": {"anyOf": [{"type": "string", "maxLength": 120}, {"type": "null"}]},
        "children": {"type": "array", "maxItems": 5, "items": {
          "type": "object", "additionalProperties": false, "required": ["kind", "done", "text"],
          "properties": {
            "kind": {"type": "string", "enum": ["work", "check"]},
            "done": {"anyOf": [{"type": "boolean"}, {"type": "null"}]},
            "text": {"type": "string", "maxLength": 160}
          }
        }}
      }
    }},
    "skipped": {"type": "array", "maxItems": 60, "items": {
      "type": "object", "additionalProperties": false, "required": ["turn", "why"],
      "properties": {"turn": {"type": "string", "maxLength": 8}, "why": {"type": "string", "enum": ["ack", "meta"]}}
    }},
    "work_updates": {"type": "array", "maxItems": 60, "items": {
      "type": "object", "additionalProperties": false, "required": ["id", "done", "note"],
      "properties": {
        "id": {"type": "string", "maxLength": 12},
        "done": {"anyOf": [{"type": "boolean"}, {"type": "null"}]},
        "note": {"anyOf": [{"type": "string", "maxLength": 120}, {"type": "null"}]}
      }
    }}
  }
}`)

var (
	inlineCode = regexp.MustCompile("`([^`]+)`")
	hashToken  = regexp.MustCompile(`(?i)\b[0-9a-f]{7,40}\b`)
)

type TagVocabulary struct {
	Components       []string
	Screens          []string
	ComponentsClosed bool
	ScreensClosed    bool
}

type ValidationOptions struct {
	TurnKeys     map[string]string
	RequestTurns []string
	TurnStatuses map[string]string
	LastTurn     string
	PriorWorkIDs map[string]bool
	InputJSON    []byte
	Vocabulary   TagVocabulary
}

type ValidatedOutput struct {
	Headline          string
	Bullets           []Bullet
	Review            []ReviewItem
	Kinds             []string
	Components        []string
	Screens           []string
	NewComponents     []string
	NewScreens        []string
	Work              []WorkItem
	Skipped           []SkippedTurn
	WorkUpdates       []WorkUpdate
	UncoveredRequests int
	DroppedItems      int
	NewTerms          int
}

type Bullet struct {
	Text     string   `json:"text"`
	TurnKeys []string `json:"turn_keys"`
}

type ReviewItem struct {
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	TurnKey string `json:"turn_key"`
}

type WorkItem struct {
	ID       string      `json:"id"`
	Kind     string      `json:"kind"`
	Text     string      `json:"text"`
	Done     *bool       `json:"done"`
	Decision *string     `json:"decision,omitempty"`
	Note     *string     `json:"note,omitempty"`
	TurnKey  string      `json:"turn_key"`
	Children []WorkChild `json:"children"`
}

type WorkChild struct {
	Kind string `json:"kind"`
	Done *bool  `json:"done"`
	Text string `json:"text"`
}

type SkippedTurn struct {
	TurnKey string `json:"turn_key"`
	Why     string `json:"why"`
}

type WorkUpdate struct {
	ID   string  `json:"id"`
	Done *bool   `json:"done"`
	Note *string `json:"note"`
}

type RecordContent struct {
	Bullets  []Bullet      `json:"bullets"`
	Review   []ReviewItem  `json:"review"`
	Work     []WorkItem    `json:"work"`
	Skipped  []SkippedTurn `json:"skipped"`
	NewTerms NewTerms      `json:"new_terms"`
	Gaps     []string      `json:"gaps"`
}

type NewTerms struct {
	Component []string `json:"component"`
	Screen    []string `json:"screen"`
}

type modelOutput struct {
	Headline     string            `json:"headline"`
	Bullets      []modelBullet     `json:"bullets"`
	Review       []modelReview     `json:"review"`
	Kinds        []string          `json:"kinds"`
	Components   []string          `json:"components"`
	Screens      []string          `json:"screens"`
	NewComponent *string           `json:"new_component"`
	NewScreen    *string           `json:"new_screen"`
	Work         []modelWork       `json:"work"`
	Skipped      []modelSkipped    `json:"skipped"`
	WorkUpdates  []modelWorkUpdate `json:"work_updates"`
}

type modelBullet struct {
	Text  string   `json:"text"`
	Turns []string `json:"turns"`
}
type modelReview struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Turn string `json:"turn"`
}
type modelWork struct {
	Turn     string       `json:"turn"`
	Kind     string       `json:"kind"`
	Text     string       `json:"text"`
	Done     *bool        `json:"done"`
	Decision *string      `json:"decision"`
	Note     *string      `json:"note"`
	Children []modelChild `json:"children"`
}
type modelChild struct {
	Kind string `json:"kind"`
	Done *bool  `json:"done"`
	Text string `json:"text"`
}
type modelSkipped struct {
	Turn string `json:"turn"`
	Why  string `json:"why"`
}
type modelWorkUpdate struct {
	ID   string  `json:"id"`
	Done *bool   `json:"done"`
	Note *string `json:"note"`
}

// ValidateOutput first applies the exact closed schema used for structured
// output, then checks turn evidence and normalizes tags before persistence.
func ValidateOutput(raw string, opts ValidationOptions) (ValidatedOutput, error) {
	if err := airuntime.ValidateJSON(OutputSchema, raw); err != nil {
		return ValidatedOutput{}, fmt.Errorf("record schema: %w", err)
	}
	var out modelOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return ValidatedOutput{}, fmt.Errorf("record output: %w", err)
	}
	if len(opts.TurnKeys) == 0 && len(out.Work)+len(out.Skipped) > 0 {
		return ValidatedOutput{}, fmt.Errorf("record output has turns but no input turn map")
	}

	allowed := make(map[string]string, len(opts.TurnKeys))
	for name, key := range opts.TurnKeys {
		if name != "" && key != "" {
			allowed[name] = key
		}
	}
	requests := make(map[string]bool, len(opts.RequestTurns))
	for _, name := range opts.RequestTurns {
		if _, ok := allowed[name]; ok {
			requests[name] = true
		}
	}
	source := string(opts.InputJSON)
	result := ValidatedOutput{Headline: boundedText(out.Headline, 120), Bullets: make([]Bullet, 0), Review: make([]ReviewItem, 0), Kinds: allowedKinds(out.Kinds), Components: make([]string, 0), Screens: make([]string, 0), Work: make([]WorkItem, 0), Skipped: make([]SkippedTurn, 0), WorkUpdates: make([]WorkUpdate, 0)}
	if result.Headline == "" {
		return ValidatedOutput{}, fmt.Errorf("record content failure: empty headline")
	}

	for _, bullet := range out.Bullets {
		text, valid := groundedText(boundedText(bullet.Text, 240), source, true)
		turnKeys := mapTurns(bullet.Turns, allowed)
		if !valid || text == "" || len(turnKeys) == 0 {
			result.DroppedItems++
			continue
		}
		result.Bullets = append(result.Bullets, Bullet{Text: text, TurnKeys: turnKeys})
	}
	for _, review := range out.Review {
		key, ok := allowed[review.Turn]
		if !ok {
			result.DroppedItems++
			continue
		}
		text, valid := groundedText(boundedText(review.Text, 240), source, true)
		if !valid || text == "" {
			result.DroppedItems++
			continue
		}
		result.Review = append(result.Review, ReviewItem{Kind: review.Kind, Text: text, TurnKey: key})
	}

	workCountByTurn := map[string]int{}
	covered := map[string]bool{}
	droppedWork := 0
	for _, item := range out.Work {
		key, ok := allowed[item.Turn]
		if !ok || !requests[item.Turn] {
			result.DroppedItems++
			droppedWork++
			continue
		}
		text, _ := groundedText(boundedText(item.Text, 200), source, false)
		text = scrubFabricatedHashes(text, source)
		if text == "" {
			result.DroppedItems++
			droppedWork++
			continue
		}
		workCountByTurn[item.Turn]++
		id := turnWorkID(item.Turn, workCountByTurn[item.Turn])
		done := cloneBool(item.Done)
		children := make([]WorkChild, 0, len(item.Children))
		for _, child := range item.Children {
			childText, _ := groundedText(boundedText(child.Text, 160), source, false)
			childText = scrubFabricatedHashes(childText, source)
			if childText == "" {
				result.DroppedItems++
				continue
			}
			childDone := cloneBool(child.Done)
			if done != nil && *done && childDone != nil && !*childDone {
				done = nil
			}
			children = append(children, WorkChild{Kind: child.Kind, Done: childDone, Text: childText})
		}
		if item.Turn == opts.LastTurn && opts.TurnStatuses[item.Turn] != "ended" && done != nil && *done {
			done = nil
		}
		decision := boundedOptionalText(item.Decision, 200)
		note := boundedOptionalText(item.Note, 120)
		result.Work = append(result.Work, WorkItem{ID: id, Kind: item.Kind, Text: text, Done: done, Decision: decision, Note: note, TurnKey: key, Children: children})
		covered[item.Turn] = true
	}
	if len(out.Work) > 0 && droppedWork*2 > len(out.Work) {
		return ValidatedOutput{}, fmt.Errorf("record content failure: more than half of work items lacked valid evidence")
	}
	for _, item := range out.Skipped {
		key, ok := allowed[item.Turn]
		if !ok || !requests[item.Turn] {
			result.DroppedItems++
			continue
		}
		result.Skipped = append(result.Skipped, SkippedTurn{TurnKey: key, Why: item.Why})
		covered[item.Turn] = true
	}
	for _, name := range opts.RequestTurns {
		if requests[name] && !covered[name] {
			result.UncoveredRequests++
		}
	}
	for _, update := range out.WorkUpdates {
		if opts.PriorWorkIDs[update.ID] {
			result.WorkUpdates = append(result.WorkUpdates, WorkUpdate{ID: update.ID, Done: cloneBool(update.Done), Note: boundedOptionalText(update.Note, 120)})
		} else {
			result.DroppedItems++
		}
	}

	result.Components, result.NewComponents = normalizeTags(out.Components, out.NewComponent, opts.Vocabulary.Components, opts.Vocabulary.ComponentsClosed)
	result.Screens, result.NewScreens = normalizeTags(out.Screens, out.NewScreen, opts.Vocabulary.Screens, opts.Vocabulary.ScreensClosed)
	result.NewTerms = len(result.NewComponents) + len(result.NewScreens)
	if len(opts.RequestTurns) > 0 && len(result.Work) == 0 && len(result.Skipped) == 0 {
		return ValidatedOutput{}, fmt.Errorf("record content failure: request turns have no work or skipped explanation")
	}
	return result, nil
}

func allowedKinds(values []string) []string {
	allowed := map[string]bool{"implementation": true, "research": true, "maintenance": true, "consultation": true, "bugfix": true, "docs": true}
	out := make([]string, 0, min(len(values), 3))
	for _, value := range values {
		if allowed[value] && !contains(out, value) && len(out) < 3 {
			out = append(out, value)
		}
	}
	return out
}

func normalizeTags(values []string, proposed *string, known []string, closed bool) ([]string, []string) {
	canonical := map[string]string{}
	for _, term := range known {
		if normalized, ok := normalizedTag(term, false); ok {
			canonical[strings.ToLower(normalized)] = normalized
		}
	}
	newTag := ""
	if proposed != nil {
		if normalized, ok := normalizedTag(*proposed, true); ok {
			if old, exists := canonical[strings.ToLower(normalized)]; exists {
				newTag = old
			} else if !closed {
				newTag = normalized
			}
		}
	}
	out := make([]string, 0, min(len(values), 3))
	newTerms := make([]string, 0, 1)
	for _, value := range values {
		normalized, ok := normalizedTag(value, false)
		if !ok {
			continue
		}
		if old, exists := canonical[strings.ToLower(normalized)]; exists {
			normalized = old
		} else if !closed && newTag != "" && strings.EqualFold(normalized, newTag) {
			normalized = newTag
			newTerms = appendUnique(newTerms, newTag, 1)
		} else {
			continue
		}
		if !containsFold(out, normalized) && len(out) < 3 {
			out = append(out, normalized)
		}
	}
	return out, newTerms
}

func normalizedTag(value string, rejectUnsafe bool) (string, bool) {
	value = norm.NFKC.String(strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), " ")
	if value == "" || len([]rune(value)) > 24 {
		return "", false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '\n' || r == '\r' || r == '`' || (rejectUnsafe && strings.ContainsRune("/:@", r)) {
			return "", false
		}
	}
	return value, true
}

// NormalizeTag validates and canonicalizes a tag using the same Unicode and
// safety rules as model-proposed vocabulary terms.
func NormalizeTag(value string, rejectUnsafe bool) (string, bool) {
	return normalizedTag(value, rejectUnsafe)
}

func mapTurns(names []string, allowed map[string]string) []string {
	out := make([]string, 0, min(len(names), 8))
	seen := map[string]bool{}
	for _, name := range names {
		if key, ok := allowed[name]; ok && !seen[key] && len(out) < 8 {
			out = append(out, key)
			seen[key] = true
		}
	}
	return out
}

func groundedText(text, source string, dropOnFabricatedCode bool) (string, bool) {
	valid := true
	text = inlineCode.ReplaceAllStringFunc(text, func(span string) string {
		literal := strings.TrimSuffix(strings.TrimPrefix(span, "`"), "`")
		if strings.Contains(source, literal) {
			return span
		}
		if dropOnFabricatedCode {
			valid = false
			return ""
		}
		return literal
	})
	if !valid {
		return "", false
	}
	return scrubFabricatedHashes(clean(text), source), true
}

func scrubFabricatedHashes(text, source string) string {
	return hashToken.ReplaceAllStringFunc(text, func(candidate string) string {
		if strings.Contains(strings.ToLower(source), strings.ToLower(candidate)) || !hasMixedHex(candidate) {
			return candidate
		}
		return ""
	})
}

func hasMixedHex(s string) bool {
	digit, letter := false, false
	for _, r := range strings.ToLower(s) {
		if r >= '0' && r <= '9' {
			digit = true
		}
		if r >= 'a' && r <= 'f' {
			letter = true
		}
	}
	return digit && letter
}

func turnWorkID(name string, sequence int) string {
	n, err := strconv.Atoi(strings.TrimPrefix(name, "t"))
	if err != nil {
		return fmt.Sprintf("0.%d", sequence)
	}
	return fmt.Sprintf("%d.%d", n, sequence)
}

func boundedText(value string, maxChars int) string { return truncateRunes(clean(value), maxChars) }

func boundedOptionalText(value *string, maxChars int) *string {
	if value == nil {
		return nil
	}
	c := boundedText(*value, maxChars)
	return &c
}

func truncateRunes(value string, maxChars int) string {
	r := []rune(value)
	if len(r) > maxChars {
		return string(r[:maxChars])
	}
	return value
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	c := *value
	return &c
}

func contains(values []string, value string) bool {
	for _, old := range values {
		if old == value {
			return true
		}
	}
	return false
}

func containsFold(values []string, value string) bool {
	for _, old := range values {
		if strings.EqualFold(old, value) {
			return true
		}
	}
	return false
}
