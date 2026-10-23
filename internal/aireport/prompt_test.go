package aireport

import (
	"strings"
	"testing"
)

// closingLine and closingNone are the fixed shapes the summary must end with.
// A live run (gpt-5.6-luna, 2026-W42) described a pattern seen in two segments
// and still wrote no class or change while the rule was prose only.
const (
	closingLine = "되돌아온 지점: <관찰 한 문장> — 분류: <사람 판단 유지|가르치기|기록하기|자동화|예외만 보고> — 해볼 것: <~해보기 한 가지, 사람 판단 유지면 남길 이유>"
	closingNone = "되돌아온 반복 지점 없음."
)

// The summary names where work came back to the person and classifies it with
// one change to try. Nothing checks the output for these rules, so the text
// that states them is what keeps them.
func TestInstructionsCarryHumanDependencyLens(t *testing.T) {
	for _, want := range []string{
		"일이 사용자에게 되돌아온 지점",
		"사람 판단 유지(KEEP ME IN)",
		"가르치기(TEACH)",
		"기록하기(SYSTEMISE)",
		"자동화(AUTOMATE)",
		"예외만 보고(ESCALATE ONLY)",
		"지점은 최대 2개",
		"두 구간 이상에서 반복된 것만",
		"대표 구간 하나를 items 에",
		"items 맨 앞",
		"이미 규칙·자동화에 있을 수 있음 — 확인 후 ~해보기",
		"경과 시간",
		"확인 질문",
		"보안·권한 통제를 우회",
		"4~10문장",
		`"~하세요" 같은 명령형 처방`,
		"완료·생산성·효율 판정, 절약하거나 낭비한 시간",
		"관찰한 것에 근거합니다. 사람 개입 지점의 분류와 해볼 것은 관찰이 아니어도 됩니다",
		"요약에서 분류하지 않습니다. 다시 볼 만하면 items 에 넣을 수 있습니다",
		"지점마다 다른 구간",
		"분류와 해볼 것은 여기에 쓰지 않습니다",
	} {
		if !strings.Contains(Instructions, want) {
			t.Errorf("Instructions missing %q", want)
		}
	}
	// A rule that every sentence be an observation leaves no room for the
	// classification and the change to try.
	if strings.Contains(Instructions, "관찰한 것이어야 합니다") {
		t.Error("Instructions still require every sentence to be an observation")
	}
}

// The closing lines are a required last step in a fixed shape, so a small
// model cannot skip them and a reader or a later check can find them.
func TestInstructionsRequireClosingLines(t *testing.T) {
	for _, want := range []string{
		closingLine,
		closingNone,
		"마지막 단계: 요약의 마지막 줄",
		"반드시",
		"지점마다 한 줄, 최대 2줄",
		"이 줄도 4~10문장에 들어갑니다",
		"마지막 줄까지 합쳐 2000자 이내",
	} {
		if !strings.Contains(Instructions, want) {
			t.Errorf("Instructions missing %q", want)
		}
	}
	// Every class the line may name is one the instructions define.
	_, classes, _ := strings.Cut(closingLine, "분류: <")
	classes, _, _ = strings.Cut(classes, ">")
	for _, class := range strings.Split(classes, "|") {
		if !strings.Contains(Instructions, "- "+class+"(") {
			t.Errorf("closing line names %q, which the instructions do not define", class)
		}
	}
	// The summary step must come last among the summary rules: nothing after it
	// may tell the model it can leave the section out.
	if strings.Contains(Instructions, "이 질문은 건너뜁니다") {
		t.Error("Instructions still let the model skip the returned-work question")
	}
	if strings.Index(Instructions, closingNone) < strings.Index(Instructions, "- summary:") {
		t.Error("closing format is not part of the summary output step")
	}
}
