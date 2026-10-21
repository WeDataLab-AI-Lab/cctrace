package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Claude Code writes a slash command and its local output as lines of tag
// scaffolding, and types its system lines by subtype. These read both into a
// title and an interpretation, so the trace says what a line is instead of
// echoing tags or a subtype code.
//
// A list row holds the first 96 characters of a line's text while the detail
// reads all of it, and both must title the row the same way. So the leading tag
// alone decides the kind, the command name falls back to the one parsed at
// collection, and output previews stop well inside the cut.

var (
	traceCommandNameRE = regexp.MustCompile(`<command-name>\s*([^<]*?)\s*</command-name>`)
	traceCommandArgsRE = regexp.MustCompile(`(?s)<command-args>(.*?)</command-args>`)
	traceANSIRE        = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")
)

const traceOutputPreviewRunes = 60

func traceClaudeRow(row traceNodeRow) bool {
	return row.record.Agent == "" || row.record.Agent == "claude"
}

func traceHarnessText(text, commandName string) (string, *TraceInterpretation) {
	text = strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(text, "<command-name>"), strings.HasPrefix(text, "<command-message>"):
		if m := traceCommandNameRE.FindStringSubmatch(text); m != nil {
			commandName = m[1]
		}
		facts := map[string]any{}
		title := "슬래시 명령"
		if name := strings.TrimPrefix(commandName, "/"); name != "" {
			facts["command"] = "/" + name
			title = "/" + name + " 명령"
		}
		if m := traceCommandArgsRE.FindStringSubmatch(text); m != nil {
			facts["args"] = strings.TrimSpace(m[1])
		}
		return title, &TraceInterpretation{Kind: "slash_command", Facts: facts}
	case strings.HasPrefix(text, "<local-command-caveat>"):
		return "로컬 명령 안내문", &TraceInterpretation{Kind: "local_command_caveat"}
	case strings.HasPrefix(text, "<local-command-stdout>"):
		return traceCommandOutput(text, "local-command-stdout", "명령 출력", "local_command_output")
	case strings.HasPrefix(text, "<local-command-stderr>"):
		return traceCommandOutput(text, "local-command-stderr", "명령 오류 출력", "local_command_error")
	}
	return "", nil
}

func traceCommandOutput(text, tag, label, kind string) (string, *TraceInterpretation) {
	output, _, _ := strings.Cut(strings.TrimPrefix(text, "<"+tag+">"), "</"+tag+">")
	output = strings.TrimSpace(traceANSIRE.ReplaceAllString(output, ""))
	interpretation := &TraceInterpretation{Kind: kind, Facts: map[string]any{"output": output}}
	if output == "" {
		return label + " 없음", interpretation
	}
	preview := strings.Join(strings.Fields(output), " ")
	if runes := []rune(preview); len(runes) > traceOutputPreviewRunes {
		preview = string(runes[:traceOutputPreviewRunes]) + "…"
	}
	return label + " · " + preview, interpretation
}

// traceSystemText titles a Claude Code system line by its subtype. A line with
// neither subtype nor collection mark came from a client older than both (the
// mark arrived with kept subtypes), so its kind is unknown rather than absent.
func traceSystemText(raw map[string]any, text string) (string, *TraceInterpretation) {
	preview := nodePreview(text)
	with := func(label string) string {
		if preview == "" {
			return label
		}
		return label + " · " + preview
	}
	switch subtype := nodeString(raw, "subtype"); subtype {
	case "":
		if _, collected := raw["cctraceCollect"]; !collected {
			return "시스템 기록 · 종류 미수집", &TraceInterpretation{Kind: "system_kind_not_collected"}
		}
		return with("시스템 기록"), nil
	case "local_command":
		if title, interpretation := traceHarnessText(text, ""); interpretation != nil {
			return title, interpretation
		}
		return with("명령 출력"), nil
	case "turn_duration":
		title := "턴 소요 시간"
		if ms, ok := traceInt(raw["durationMs"]); ok {
			title += " · " + traceDurationLabel(ms)
		}
		return title, &TraceInterpretation{Kind: "system_turn_duration", Facts: traceFacts(raw, "durationMs", "duration_ms", "messageCount", "message_count")}
	case "compact_boundary":
		meta, _ := raw["compactMetadata"].(map[string]any)
		title := "대화 압축"
		switch nodeString(meta, "trigger") {
		case "auto":
			title += " · 자동"
		case "manual":
			title += " · 수동"
		}
		pre, hasPre := traceInt(meta["preTokens"])
		post, hasPost := traceInt(meta["postTokens"])
		switch {
		case hasPre && hasPost:
			title += " · " + traceTokenLabel(pre) + "→" + traceTokenLabel(post) + " 토큰"
		case hasPre:
			title += " · 압축 전 " + traceTokenLabel(pre) + " 토큰"
		}
		return title, &TraceInterpretation{Kind: "system_compact_boundary", Facts: traceFacts(meta, "trigger", "trigger", "preTokens", "pre_tokens", "postTokens", "post_tokens")}
	case "informational":
		return with("안내"), nil
	case "away_summary":
		return with("자리 비운 사이 요약"), nil
	case "bridge_status":
		return with("원격 제어 상태"), nil
	case "agents_killed":
		return with("하위 에이전트 중지"), nil
	case "scheduled_task_fire":
		return with("예약 작업 실행"), nil
	default:
		return "시스템 · " + subtype, nil
	}
}

// traceRowLabel names a line the way the trace titles it, for pointing at it
// from another line.
func traceRowLabel(row traceNodeRow) string {
	text := nodeText(nodeMessage(row)["content"])
	with := func(label string) string {
		if preview := nodePreview(text); preview != "" {
			return label + " · " + preview
		}
		return label
	}
	switch row.record.RecordType {
	case "user":
		if title, _ := traceHarnessText(text, row.record.CommandName); title != "" {
			return title
		}
		return with("사용자 입력")
	case "assistant":
		return with("모델 응답")
	case "system":
		title, _ := traceSystemText(row.raw, text)
		return title
	}
	return "기록 · " + row.record.RecordType
}

// interpretation reads a Codex-written message or a Claude user or system line
// into what it is. A Claude system line whose kind was never collected keeps
// its parentUuid, the one fact left about it: the line it was written after.
func (r traceNodeReader) interpretation(row traceNodeRow) (*TraceInterpretation, error) {
	if _, items := traceCodexItems(row); items != nil {
		return items, nil
	}
	if !traceClaudeRow(row) {
		return nil, nil
	}
	text := nodeText(nodeMessage(row)["content"])
	switch row.record.RecordType {
	case "user":
		_, interpretation := traceHarnessText(text, row.record.CommandName)
		return interpretation, nil
	case "system":
	default:
		return nil, nil
	}
	_, interpretation := traceSystemText(row.raw, text)
	if interpretation == nil || interpretation.Kind != "system_kind_not_collected" {
		return interpretation, nil
	}
	parent := row.record.ParentUUID
	if parent == "" {
		parent = nodeString(row.raw, "parentUuid")
	}
	if parent == "" {
		return interpretation, nil
	}
	rows, err := r.scoped(row, `r.uuid=$5`, []any{parent}, 2, true)
	if err != nil {
		return nil, err
	}
	interpretation.Facts = map[string]any{"parent_uuid": parent}
	if len(rows) == 1 {
		interpretation.Facts["parent"] = traceRowLabel(rows[0])
	}
	return interpretation, nil
}

// traceFacts copies the named keys present in m under their fact names, given
// as source/fact pairs.
func traceFacts(m map[string]any, pairs ...string) map[string]any {
	facts := map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if value, ok := m[pairs[i]]; ok && value != nil {
			facts[pairs[i+1]] = value
		}
	}
	if len(facts) == 0 {
		return nil
	}
	return facts
}

func traceInt(value any) (int64, bool) {
	switch v := value.(type) {
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	case float64:
		return int64(v), true
	}
	return 0, false
}

func traceDurationLabel(ms int64) string {
	if ms < 60_000 {
		return strings.TrimSuffix(strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64), ".0") + "초"
	}
	seconds := (ms + 500) / 1000
	if seconds >= 3600 {
		return fmt.Sprintf("%d시간 %d분", seconds/3600, seconds%3600/60)
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%d분", seconds/60)
	}
	return fmt.Sprintf("%d분 %d초", seconds/60, seconds%60)
}

func traceTokenLabel(n int64) string {
	if n >= 10_000 {
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/10_000, 'f', 1, 64), ".0") + "만"
	}
	digits := strconv.FormatInt(n, 10)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}

// Codex labels each content block of a message it writes itself -- hook output,
// sandbox rules, the skill list -- in content_item_kinds, aligned with the
// blocks. A message is named by those kinds instead of calling every developer
// message an instruction.
var traceCodexItemLabels = map[string]string{
	"hooks.additional_context":                  "훅 추가 문맥",
	"host_skills.instructions":                  "스킬 목록",
	"skills.selected_skill_instructions":        "선택한 스킬 지시",
	"collaboration_mode.instructions":           "협업 모드",
	"permissions.instructions":                  "권한·샌드박스 지시",
	"permissions.approved_command_prefix_saved": "승인된 명령 접두어 저장",
	"memories.instructions":                     "메모리 안내",
	"multi_agent.mode_instructions":             "멀티 에이전트 모드",
	"multi_agent.role_instructions":             "멀티 에이전트 역할",
	"multi_agent.usage_hint":                    "멀티 에이전트 사용 안내",
	"multi_agent.subagent_notification":         "하위 에이전트 알림",
	"plugins.recommendations":                   "플러그인 추천",
	"plugins.usage_instructions":                "플러그인 사용 지시",
	"apps.instructions":                         "앱 사용 지시",
	"tools.deferred_namespaces":                 "지연 로드 도구 목록",
	"generic.turn_aborted":                      "턴 중단 알림",
	"generic.developer_instructions":            "개발자 지시문",
	"model_switch.instructions":                 "모델 전환 안내",
	"token_budget.context_window":               "컨텍스트 창 정보",
	"token_budget.context_window_guidance":      "컨텍스트 창 안내",
	"token_budget.reminder":                     "컨텍스트 창 경고",
	"compaction.auto_fallback_prompt":           "자동 압축 안내",
	"environments.environment_context":          "실행 환경 정보",
	"agents_md.instructions":                    "AGENTS.md 지시문",
	"goal.internal_context":                     "목표 내부 문맥",
	"daemon_recovery.internal_context":          "데몬 복구 문맥",
	"user.goal":                                 "사용자 목표",
	"user.image":                                "이미지",
	"shell.user_command":                        "사용자 셸 명령",
}

func traceCodexItemLabel(kind string) string {
	if label, ok := traceCodexItemLabels[kind]; ok {
		return label
	}
	if strings.HasPrefix(kind, "additional_content.") {
		return "앱 추가 문맥"
	}
	return kind
}

// traceCodexItems names a message Codex wrote itself by its item kinds. A
// message holding anything typed (user.text) or produced by the model
// (unknown) keeps its own title and has no items to name.
func traceCodexItems(row traceNodeRow) (string, *TraceInterpretation) {
	if row.record.Agent != "codex" {
		return "", nil
	}
	m := nodeMessage(row)
	meta, _ := m["internal_chat_message_metadata_passthrough"].(map[string]any)
	kinds, _ := meta["content_item_kinds"].([]any)
	if len(kinds) == 0 {
		return "", nil
	}
	items := make([]map[string]any, 0, len(kinds))
	var labels []string
	seen := map[string]bool{}
	for _, value := range kinds {
		kind, _ := value.(string)
		if kind == "user.text" || kind == "unknown" {
			return "", nil
		}
		label := traceCodexItemLabel(kind)
		items = append(items, map[string]any{"kind": kind, "label": label})
		if !seen[label] {
			seen[label] = true
			labels = append(labels, label)
		}
	}
	title := strings.Join(labels, " · ")
	if len(kinds) == 1 && kinds[0] == "hooks.additional_context" {
		if preview := nodePreview(m["content"]); preview != "" {
			title += " · " + preview
		}
	}
	return title, &TraceInterpretation{Kind: "codex_message_items", Facts: map[string]any{"items": items}}
}
