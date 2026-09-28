// Package insights derives small, non-sensitive usage labels from session input.
package insights

import (
	"encoding/json"
	"strings"
)

// KeywordClassifier implements store.TaskClassifier with the keyword rule set in
// this file. Injected only by cmd/cctraced/main.go -- see the isolation note on
// store.TaskClassifier for why cmd/cctrace must never construct one of these.
type KeywordClassifier struct{}

func (KeywordClassifier) PromptFromRaw(raw []byte) string { return PromptFromRaw(raw) }
func (KeywordClassifier) Classify(prompt string) string   { return ClassifyPrompt(prompt) }
func (KeywordClassifier) Version() string                 { return ClassifierVersion }

// ClassifierVersion tags every row this package labels. A stored "" means
// unclassified (not yet computed); a stored ClassifierVersion means the rule set
// below rendered a verdict, including "unknown" -- "unknown" is a real answer, not
// the same as no answer. Bump this string whenever `rules` changes so a future
// re-backfill can tell which rows still reflect an older rule set.
const ClassifierVersion = "keyword-v2"

// NonTask labels a turn that carries no task request at all -- an interruption
// marker, a hook's injected text, a slash command's scaffolding, another agent's
// message, an image with no words. These are user-role records, so they reach the
// classifier, but none of them is somebody asking for work.
//
// Distinct from "unknown", which means a request the rules could not place.
// Rolled together they made the chart's largest slice a category that does not
// exist: measured on 61,131 typed turns, "unknown" was 52.4% and its top entries
// were <system-reminder>, [Request interrupted by user], <teammate-message> and
// <command-name> -- not one of them a task type (#429).
const NonTask = "non-task"

// ClassifyPrompt returns a stable task label. It is deterministic and runs only
// in-process; the prompt is never sent to another service or persisted again.
func ClassifyPrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if isNonTaskInput(trimmed) {
		return NonTask
	}
	lower := strings.ToLower(trimmed)
	for _, rule := range rules {
		for _, keyword := range rule.keywords {
			if containsKeyword(lower, keyword) {
				return rule.name
			}
		}
	}
	return "unknown"
}

// containsKeyword requires a word boundary around ASCII keywords. Plain substring
// matching read file paths and machine text as requests: measured on real logs,
// "document" matched 1,742 turns through a path with a Documents directory in it,
// "spec" matched "specific", "inspect" and "suspect" 1,800+ times, and "test"
// matched "latestupdate". The Testing and Documentation shares were substantially
// those (#429).
//
// Korean keywords stay substrings: the language agglutinates, so 설계 is a prefix
// of 설계해줘 and a boundary rule would reject the ordinary form.
func containsKeyword(prompt, keyword string) bool {
	if !isASCIIWord(keyword) {
		return strings.Contains(prompt, keyword)
	}
	for i := 0; i+len(keyword) <= len(prompt); i++ {
		if prompt[i:i+len(keyword)] != keyword {
			continue
		}
		// Only where the keyword itself ends in a word character. "why " and
		// "add " already carry their own boundary, and demanding another one
		// after the trailing space rejected every ordinary use of them.
		if i > 0 && isWordByte(keyword[0]) && isWordByte(prompt[i-1]) {
			continue
		}
		end := i + len(keyword)
		if isWordByte(keyword[len(keyword)-1]) && end < len(prompt) && isWordByte(prompt[end]) {
			continue
		}
		return true
	}
	return false
}

func isASCIIWord(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

type classificationRule struct {
	name     string
	keywords []string
}

var rules = []classificationRule{
	{"testing", []string{"test", "tests", "테스트", "spec", "specs", "커버리지", "coverage"}},
	{"debugging", []string{"debug", "bug", "bugs", "fix", "regression", "stack trace", "오류", "에러", "버그", "실패 원인", "고쳐"}},
	{"planning", []string{"plan", "plans", "roadmap", "design", "architecture", "계획", "설계", "아키텍처"}},
	{"documentation", []string{"document", "docs", "readme", "api 문서", "문서", "가이드"}},
	{"drafting", []string{"draft", "write a", "작성해", "초안", "릴리스 노트", "release note"}},
	{"implementation", []string{"implement", "build", "add ", "만들어", "구현", "추가"}},
	// Added in keyword-v2 from the observed distribution. These were the largest
	// uncovered request types once machine text stopped being classified: asking
	// for an explanation, asking what something is, checking state, and the git /
	// release chores. Measured on 61,131 typed turns (#429).
	//
	// They sit after the six above deliberately -- first match wins, and "explain
	// the test failure" is better filed under testing than under question.
	{"review", []string{"리뷰", "review", "검토", "적대적 검증", "code review"}},
	{"question", []string{"설명", "질문", "뭐야", "무엇", "어떻게", "왜 ", "explain", "why ", "what is", "how do", "생각은"}},
	{"status", []string{"진행상황", "진행 상황", "상태 확인", "현황", "status", "progress"}},
	{"operations", []string{"push", "merge", "머지", "deploy", "배포", "릴리스", "release", "커밋", "commit", "rebase", "리베이스"}},
}

// IsTypedTurn reports whether raw is a genuine candidate for classification --
// distinct from PromptFromRaw returning "", which conflates "not a real prompt"
// (a tool_result block, or a message whose only text was blank) with "a real prompt
// the classifier could not read". In practice a Claude Code human message commonly
// arrives as a content block array, not a plain string: measured on prod, 8,375 of
// 370,858 array-shaped rows carry a real text/input_text block (the rest are
// tool_result). A plain-string check alone -- the condition
// task_segment_facts' turns CTE and pluginInvocationFactsInsertSQL's next_user_ts
// both use for a different purpose, segment/response-window boundaries -- misses
// that array-shaped 2.3%. PromptFromRaw already resolves both shapes correctly (and
// Codex's payload fallback), so this reuses it rather than re-deriving the check.
func IsTypedTurn(agent string, raw []byte) bool {
	if agent == "codex" {
		return true
	}
	return PromptFromRaw(raw) != ""
}

// PromptFromRaw extracts only typed user text. Tool result messages have role=user
// too, but no text block, and must not be mistaken for a new task request.
func PromptFromRaw(raw []byte) string {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return ""
	}
	if prompt := messagePrompt(root["message"]); prompt != "" {
		return prompt
	}
	return messagePrompt(root["payload"])
}

func messagePrompt(raw json.RawMessage) string {
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &message) != nil || message.Role != "user" {
		return ""
	}
	var text string
	if json.Unmarshal(message.Content, &text) == nil {
		return strings.TrimSpace(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(message.Content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" || block.Type == "input_text" {
			parts = append(parts, block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
