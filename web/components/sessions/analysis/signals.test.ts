import { describe, expect, it } from 'vitest';
import type { SessionSignals } from '@/lib/session-analysis-types';
import { rowSignalLines, signalLines, SIGNAL_ORDER } from './signals';

// 시각은 브라우저 시간대로 그려지므로, 기대값도 같은 시간대의 지역 시각으로 만든다.
const local = (day: number, hour: number, minute: number): string => new Date(2026, 9, day, hour, minute).toISOString();
const NOW = new Date(2026, 9, 7, 15, 0).getTime();

const sentences = (signals: SessionSignals | undefined): string[] => signalLines(signals, { now: NOW }).map((l) => l.sentence);

describe('signalLines: 계획 §5.2의 문장', () => {
  it('turn 값마다 표의 문장과 색조를 낸다', () => {
    const cases = [
      ['ended', '마지막 턴 끝남', 'quiet'],
      ['open', '턴 진행 중', 'live'],
      ['interrupted', '중단됨', 'attention'],
      ['error', '오류로 끝남', 'danger'],
      ['no_response', '응답 없음', 'attention'],
    ] as const;
    for (const [value, sentence, tone] of cases) {
      const [line] = signalLines({ turn: { state: 'observed', value } });
      expect(line.sentence).toBe(sentence);
      expect(line.tone).toBe(tone);
      expect(line.undeterminable).toBe(false);
    }
  });

  it('질문: 개수가 있으면 "답하지 않은 질문 N", 0이면 "없음"', () => {
    expect(sentences({ question: { state: 'observed', value: 1 } })).toEqual(['답하지 않은 질문 1']);
    const [zero] = signalLines({ question: { state: 'observed', value: 0 } });
    expect(zero.sentence).toBe('답하지 않은 질문 없음');
    expect(zero.tone).toBe('quiet');
  });

  it('질문: 실시간 상태가 대기이면 "응답 대기: 권한 승인"이 앞선다', () => {
    const [only] = signalLines({ question: { state: 'observed', value: 0, waiting_kind: 'permission' } });
    expect(only.sentence).toBe('응답 대기: 권한 승인');
    expect(only.tone).toBe('attention');
    expect(only.short).toBe('응답 대기');

    const [both] = signalLines({ question: { state: 'observed', value: 1, waiting_kind: 'permission' } });
    expect(both.sentence).toBe('응답 대기: 권한 승인 · 답하지 않은 질문 1');
  });

  it('질문: 대기 종류를 모르는 값이어도 원문을 보인다', () => {
    // @ts-expect-error 계약에 없는 값이 서버에서 올 때를 가정한다.
    const [line] = signalLines({ question: { state: 'observed', value: 0, waiting_kind: 'telepathy' } });
    expect(line.sentence).toBe('응답 대기: telepathy');
  });

  it('질문: 비식별화로 판단 불가여도 대기 중이라는 관측은 남긴다', () => {
    const [line] = signalLines({ question: { state: 'undeterminable', value: null, reason: 'redacted', waiting_kind: 'input' } });
    expect(line.sentence).toBe('응답 대기: 입력 필요 · 답하지 않은 질문 판단 불가');
    expect(line.undeterminable).toBe(true);
    expect(line.reason).toContain('비식별화');
  });

  it('백그라운드와 할 일', () => {
    expect(sentences({ background: { state: 'observed', value: 2 } })).toEqual(['백그라운드 작업 2']);
    expect(sentences({ background: { state: 'observed', value: 0 } })).toEqual(['백그라운드 작업 없음']);
    expect(sentences({ todo: { state: 'observed', value: 2 } })).toEqual(['미완료 할 일 2']);
    expect(sentences({ todo: { state: 'observed', value: 0 } })).toEqual(['미완료 할 일 없음']);
  });

  it('미커밋: "편집 후 미커밋 파일 3 (10:42부터)"', () => {
    const [line] = signalLines(
      { uncommitted: { state: 'observed', value: 3, at_least: false, changed_at: local(7, 10, 42), reevaluating: true } },
      { now: NOW },
    );
    expect(line.sentence).toBe('편집 후 미커밋 파일 3 (10:42부터)');
    expect(line.tone).toBe('attention');
    expect(line.note).toBeNull();
  });

  it('미커밋: 세션 중간부터 모은 값은 "3개 이상"이다', () => {
    const [line] = signalLines(
      { uncommitted: { state: 'observed', value: 3, at_least: true, changed_at: local(7, 10, 42), reevaluating: true } },
      { now: NOW },
    );
    expect(line.sentence).toBe('편집 후 미커밋 파일 3개 이상 (10:42부터)');
    expect(line.short).toBe('미커밋 3+');
    expect(line.note).toContain('놓쳤을 수 있습니다');
  });

  it('미커밋: 세션 중간부터 모은 값의 0은 "없음"이 아니라 판단 불가다 (계약 §3.18)', () => {
    const [line] = signalLines(
      { uncommitted: { state: 'observed', value: 0, at_least: true, changed_at: local(7, 10, 42), reevaluating: true } },
      { now: NOW },
    );
    expect(line.undeterminable).toBe(true);
    expect(line.tone).toBe('unknown');
    expect(line.sentence).toBe('편집 후 미커밋 파일 판단 불가');
    expect(line.sentence).not.toContain('없음');
    expect(line.reason).toContain('세션 중간부터');
    expect(line.short).toBe('미커밋 ?');
  });

  it('미커밋: 0은 0이다("없음"), 시각이 오늘이 아니면 날짜를 붙인다', () => {
    const [line] = signalLines(
      { uncommitted: { state: 'observed', value: 0, at_least: false, changed_at: local(5, 9, 5), reevaluating: true } },
      { now: NOW },
    );
    expect(line.sentence).toBe('편집 후 미커밋 파일 없음 (10/05 09:05부터)');
    expect(line.tone).toBe('quiet');
  });

  it('미커밋: 더는 다시 확인하지 않는 값은 그 사실을 보충에 적는다', () => {
    const [line] = signalLines(
      { uncommitted: { state: 'observed', value: 2, at_least: false, changed_at: local(7, 10, 42), reevaluating: false } },
      { now: NOW },
    );
    expect(line.note).toContain('다시 확인하지 않으므로');
  });

  it('미커밋: 시각을 읽을 수 없으면 "(알 수 없음부터)"를 만들지 않는다', () => {
    const [line] = signalLines({ uncommitted: { state: 'observed', value: 1, at_least: false, changed_at: null, reevaluating: true } });
    expect(line.sentence).toBe('편집 후 미커밋 파일 1');
  });
});

describe('signalLines: 판단 불가는 0이 아니다', () => {
  const undeterminable: SessionSignals = {
    turn: { state: 'undeterminable', value: null, reason: 'pre_collect' },
    question: { state: 'undeterminable', value: null, reason: 'redacted' },
    background: { state: 'undeterminable', value: null, reason: 'pre_collect', parts: null },
    uncommitted: { state: 'undeterminable', value: null, reason: 'no_git' },
    todo: { state: 'undeterminable', value: null, reason: 'no_list' },
  };

  it('다섯 신호 모두 회색(unknown)이고 "판단 불가"로 끝나며 이유가 있다', () => {
    const lines = signalLines(undeterminable);
    expect(lines.map((l) => l.key)).toEqual(['turn', 'question', 'background', 'uncommitted', 'todo']);
    for (const line of lines) {
      expect(line.undeterminable).toBe(true);
      expect(line.tone).toBe('unknown');
      expect(line.sentence.endsWith('판단 불가')).toBe(true);
      expect(line.reason).toBeTruthy();
      expect(line.value).toBe('?');
      expect(line.short).toMatch(/ \?$/);
    }
  });

  it('이유는 계획 §5.2 표의 판단 불가 조건을 따른다', () => {
    const byKey = Object.fromEntries(signalLines(undeterminable).map((l) => [l.key, l.reason]));
    expect(byKey.turn).toContain('수집 규칙 이전');
    expect(byKey.question).toContain('비식별화');
    expect(byKey.background).toContain('수집 규칙 이전');
    expect(byKey.uncommitted).toContain('수집 허용 목록');
    expect(byKey.todo).toContain('할 일 목록을 쓰지 않아');
  });

  it('신호마다 정해 둔 문장이 없는 이유 코드도 읽을 수 있는 문장이 된다', () => {
    const lines = signalLines({
      background: { state: 'undeterminable', value: null, reason: 'dropped', parts: null },
      todo: { state: 'undeterminable', value: null, reason: 'withheld' },
    });
    expect(lines[0].reason).toContain('모양이 예상과 달라');
    expect(lines[1].reason).toContain('할 일 제목을 서버에 올리지 않아');
  });

  it('관측한 0과 판단 불가는 문장과 색조가 모두 다르다', () => {
    const [zero] = signalLines({ todo: { state: 'observed', value: 0 } });
    const [unknown] = signalLines({ todo: { state: 'undeterminable', value: null, reason: 'no_list' } });
    expect(zero.sentence).not.toBe(unknown.sentence);
    expect(zero.tone).toBe('quiet');
    expect(unknown.tone).toBe('unknown');
    expect(zero.value).toBe('0');
    expect(unknown.value).toBe('?');
  });
});

describe('signalLines: 키가 없는 신호는 그리지 않는다', () => {
  it('signals 자체가 없으면(비소유 관리자) 빈 배열', () => {
    expect(signalLines(undefined)).toEqual([]);
  });

  it('Codex는 턴 신호 하나뿐이다: 나머지는 해당 없음이라 키가 없다', () => {
    const lines = signalLines({ turn: { state: 'observed', value: 'open' } });
    expect(lines.map((l) => l.key)).toEqual(['turn']);
  });

  it('객체의 키 순서와 무관하게 표의 순서를 지킨다', () => {
    const lines = signalLines({
      todo: { state: 'observed', value: 1 },
      turn: { state: 'observed', value: 'ended' },
      background: { state: 'observed', value: 0 },
    });
    expect(lines.map((l) => l.key)).toEqual(['turn', 'background', 'todo']);
    expect(SIGNAL_ORDER).toEqual(['turn', 'question', 'background', 'uncommitted', 'todo']);
  });
});

describe('rowSignalLines: 목록 행은 값만 온다', () => {
  it('null은 판단 불가, 키가 없으면 그리지 않는다', () => {
    const lines = rowSignalLines({ turn: 'ended', question: 1, background: null, todo: 0 });
    expect(lines.map((l) => [l.key, l.value, l.undeterminable])).toEqual([
      ['turn', '끝남', false],
      ['question', '1', false],
      ['background', '?', true],
      ['todo', '0', false],
    ]);
  });

  it('행의 판단 불가 이유는 분석 탭을 가리킨다(목록에는 이유가 오지 않는다)', () => {
    const [line] = rowSignalLines({ uncommitted: null });
    expect(line.reason).toContain('분석 탭');
  });

  it('신호가 없으면 빈 배열', () => {
    expect(rowSignalLines(undefined)).toEqual([]);
    expect(rowSignalLines({})).toEqual([]);
  });

  it('짧은 꼴: "질문 1", "미커밋 3", "턴 끝남"', () => {
    const shorts = rowSignalLines({ turn: 'ended', question: 1, uncommitted: 3 }).map((l) => l.short);
    expect(shorts).toEqual(['턴 끝남', '질문 1', '미커밋 3']);
  });
});
