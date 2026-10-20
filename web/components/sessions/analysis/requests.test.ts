import { describe, expect, it } from 'vitest';
import { ctxObserved, ctxTokensOnly, estimated, noApiCall, otel, text, tok, turn } from '@/app/test/session-analysis/builders';
import {
  editedView,
  orderTurns,
  previewView,
  repeatedTurnTimes,
  spansMultipleDays,
  turnKindBadge,
  turnExtraTasks,
  turnMetrics,
  turnNumberLabel,
  turnQuestionView,
  turnTimeLabel,
  turnTotals,
  unknownReasons,
} from './requests';

describe('orderTurns: 버려진 분기는 따로', () => {
  const a1 = turn({ key: 'a1', no: 1, at: 100, request_no: 1 });
  const a2 = turn({ key: 'a2', no: 2, at: 90 });
  const x1 = turn({ key: 'x1', no: 3, at: 80, branch: 'abandoned' });
  const a3 = turn({ key: 'a3', no: 4, at: 70, request_no: 2 });
  const x2 = turn({ key: 'x2', no: 5, at: 60, branch: 'abandoned' });

  it('활성 턴과 버려진 턴을 각자의 순서로 놓는다', () => {
    const ordered = orderTurns([a3, a1, a2], [x2, x1]);
    expect(ordered.active.map((t) => t.key)).toEqual(['a1', 'a2', 'a3']);
    expect(ordered.abandoned.map((t) => t.key)).toEqual(['x1', 'x2']);
  });

  it('입력 순서가 달라도 결과가 같다', () => {
    const one = orderTurns([a1, a2, a3], [x1, x2]);
    const two = orderTurns([a3, a2, a1], [x2, x1]);
    expect(two).toEqual(one);
  });

  it('한쪽 배열에 다른 분기의 턴이 섞여 와도 branch로 다시 가른다', () => {
    const ordered = orderTurns([a1, x1, a2], [a3, x2]);
    expect(ordered.active.map((t) => t.key)).toEqual(['a1', 'a2', 'a3']);
    expect(ordered.abandoned.map((t) => t.key)).toEqual(['x1', 'x2']);
  });

  it('같은 키가 두 배열에 있으면 한 번만 센다', () => {
    const ordered = orderTurns([a1, a2], [a2, x1]);
    expect(ordered.active).toHaveLength(2);
    expect(ordered.abandoned).toHaveLength(1);
  });

  it('시작 순번이 같으면 시작 시각, 그래도 같으면 키로 정한다', () => {
    const early = turn({ key: 'b', no: 1, at: 100 });
    const late = turn({ key: 'a', no: 1, at: 50 });
    expect(orderTurns([late, early], []).active.map((t) => t.key)).toEqual(['b', 'a']);
    const sameA = turn({ key: 'b', no: 1, at: 100 });
    const sameB = turn({ key: 'a', no: 1, at: 100 });
    expect(orderTurns([sameA, sameB], []).active.map((t) => t.key)).toEqual(['a', 'b']);
  });

  it('입력 배열을 바꾸지 않는다', () => {
    const active = [a3, a1];
    orderTurns(active, []);
    expect(active.map((t) => t.key)).toEqual(['a3', 'a1']);
  });
});

describe('turnTotals: 버려진 분기의 비용도 센다', () => {
  it('비용을 아는 턴만 더하고 모르는 턴은 따로 센다', () => {
    const totals = turnTotals([
      turn({ key: 't1', no: 1, at: 5, cost: otel(0.5), tokens: tok(10, 5, 100, 1), is_request: true }),
      turn({ key: 't2', no: 2, at: 4, cost: estimated(0.25), tokens: tok(1, 1, 1, 1), is_request: true }),
      turn({ key: 't3', no: 3, at: 3, cost: { usd: null, source: 'unattributable', reason: 'pre_p0' }, is_request: false }),
      turn({ key: 't4', no: 4, at: 2, cost: { usd: null, source: 'expired', reason: null } }),
      turn({ key: 't5', no: 5, at: 1, cost: noApiCall }),
    ]);
    expect(totals.turns).toBe(5);
    expect(totals.requests).toBe(4);
    expect(totals.usd).toBeCloseTo(0.75);
    // API 호출이 없는 턴은 모르는 것이 아니다.
    expect(totals.unknownCost).toBe(2);
    expect(totals.tokens).toBe(116 + 4);
  });

  it('서버가 0을 실어 보내도 값 없는 출처는 모르는 값으로 센다', () => {
    const totals = turnTotals([turn({ key: 'z', no: 1, at: 1, cost: { usd: 0, source: 'no_rate', reason: null } })]);
    expect(totals.unknownCost).toBe(1);
    expect(totals.usd).toBe(0);
  });

  it('estimate_pending 0은 미반영 비용이라 unknownCost로 센다', () => {
    const totals = turnTotals([turn({ key: 'p', no: 1, at: 1, cost: { usd: 0, source: 'estimate_pending', reason: null } })]);
    expect(totals.knownCost).toBe(0);
    expect(totals.unknownCost).toBe(1);
    expect(totals.usd).toBe(0);
  });

  it('아는 비용이 하나도 없으면 knownCost가 0이라 합계 0을 "0달러"로 읽지 않게 한다', () => {
    const totals = turnTotals([
      turn({ key: 'u1', no: 1, at: 2, cost: { usd: null, source: 'expired', reason: null } }),
      turn({ key: 'u2', no: 2, at: 1, cost: { usd: null, source: 'unattributable', reason: 'pre_p0' } }),
    ]);
    expect(totals.knownCost).toBe(0);
    expect(totals.unknownCost).toBe(2);
    expect(totals.usd).toBe(0);
  });

  it('API 호출이 없는 턴은 모르는 값이 아니라서 어느 쪽으로도 세지 않는다', () => {
    const totals = turnTotals([turn({ key: 'n1', no: 1, at: 1, cost: noApiCall })]);
    expect(totals.knownCost).toBe(0);
    expect(totals.unknownCost).toBe(0);
    expect(totals.usd).toBe(0);
  });

  it('빈 목록', () => {
    expect(turnTotals([])).toEqual({ turns: 0, requests: 0, usd: 0, knownCost: 0, unknownCost: 0, tokens: 0 });
  });
});

describe('번호와 출처 라벨', () => {
  it('요청이면 #번호, 아니면 점', () => {
    expect(turnNumberLabel(turn({ key: 'r', no: 3, at: 1, request_no: 3 }))).toBe('#3');
    expect(turnNumberLabel(turn({ key: 'n', no: 4, at: 1, is_request: false, kind: 'notification' }))).toBe('·');
  });

  it('요청이 아닌 턴은 무엇인지 늘 적는다(kind)', () => {
    const labels = (
      [
        ['command', '명령'],
        ['notification', '백그라운드 알림'],
        ['peer', '다른 세션'],
        ['scheduled', '예약 실행'],
        ['channel', '외부 채널'],
        ['auto', '자동 이어가기'],
        ['sdk', 'SDK 호출'],
        ['system', '시스템'],
      ] as const
    ).map(([kind, label]) => [turnKindBadge(turn({ key: kind, no: 1, at: 1, kind, is_request: false })), label]);
    for (const [actual, expected] of labels) expect(actual).toBe(expected);
  });

  it('요청인 턴은 직접 입력(typed)이 기본이라 적지 않고, 작업 중 입력이나 명령처럼 알아야 할 때만 적는다', () => {
    expect(turnKindBadge(turn({ key: 'a', no: 1, at: 1, kind: 'typed', request_no: 1 }))).toBeNull();
    expect(turnKindBadge(turn({ key: 'b', no: 1, at: 1, kind: 'unknown', request_no: 1 }))).toBeNull();
    expect(turnKindBadge(turn({ key: 'c', no: 1, at: 1, kind: 'queued', request_no: 1 }))).toBe('작업 중 입력');
    expect(turnKindBadge(turn({ key: 'd', no: 1, at: 1, kind: 'command', request_no: 1 }))).toBe('명령');
  });
});

describe('첫 줄', () => {
  it('ok: 첫 줄만', () => {
    expect(previewView(text('첫 줄\n둘째 줄'))).toEqual({ kind: 'text', text: '첫 줄' });
  });

  it('가려졌거나 보류됐거나 없으면 빈칸이 아니라 무슨 일인지를 적는다', () => {
    expect(previewView({ text: null, state: 'redacted' })).toEqual({ kind: 'placeholder', text: '첫 줄이 가려졌습니다 (비식별화)' });
    expect(previewView({ text: null, state: 'withheld' }).text).toContain('보류됨');
    expect(previewView({ text: null, state: 'none' }).text).toBe('첫 줄 없음');
    expect(previewView({ text: '   ', state: 'ok' }).text).toBe('첫 줄 없음');
  });

  it('가려진 상태면 본문이 와도 쓰지 않는다', () => {
    expect(previewView({ text: '새면 안 되는 문장', state: 'redacted' }).kind).toBe('placeholder');
  });
});

describe('시각과 편집 요약', () => {
  it('세션이 여러 날에 걸치면 날짜를 붙인다', () => {
    const day = (d: number, h: number) => new Date(2026, 9, d, h, 0).toISOString();
    const t1 = turn({ key: 'a', no: 1, at: 1 });
    const t2 = turn({ key: 'b', no: 2, at: 1 });
    const withDate = (iso: string) => ({ ...t1, started_at: iso });
    expect(spansMultipleDays([withDate(day(7, 9)), { ...t2, started_at: day(7, 18) }])).toBe(false);
    expect(spansMultipleDays([withDate(day(6, 23)), { ...t2, started_at: day(7, 1) }])).toBe(true);
    expect(turnTimeLabel(day(7, 9), false)).toBe('09:00');
    expect(turnTimeLabel(day(7, 9), true)).toBe('10/07 09:00');
  });

  it('여러 날 여부는 선택한 시간대의 날짜로 계산한다', () => {
    const t1 = { ...turn({ key: 'a', no: 1, at: 1 }), started_at: '2026-10-08T03:20:00Z' };
    const t2 = { ...turn({ key: 'b', no: 2, at: 1 }), started_at: '2026-10-08T04:20:00Z' };
    expect(spansMultipleDays([t1, t2], 'America/New_York')).toBe(true);
    expect(spansMultipleDays([t1, t2], 'Asia/Seoul')).toBe(false);
    expect(turnTimeLabel(t2.started_at, true, 'America/New_York')).toBe('10/08 00:20 GMT-04:00');
  });

  it('짧은 턴 시각은 오프셋을 툴팁으로 보내고 반복 시각에서만 표시한다', () => {
    const summer = { ...turn({ key: 'summer', no: 1, at: 1 }), started_at: '2026-10-08T04:20:00Z' };
    const first = { ...turn({ key: 'first', no: 2, at: 1 }), started_at: '2026-11-01T05:30:00Z' };
    const second = { ...turn({ key: 'second', no: 3, at: 1 }), started_at: '2026-11-01T06:30:00Z' };
    expect(turnTimeLabel(summer.started_at, false, 'America/New_York', false)).toBe('00:20');
    expect(turnTimeLabel(summer.started_at, true, 'America/New_York', false)).toBe('10/08 00:20');
    const repeated = repeatedTurnTimes([summer, first, second], 'America/New_York');
    expect(repeated.has(summer.started_at)).toBe(false);
    expect(repeated.has(first.started_at)).toBe(true);
    expect(repeated.has(second.started_at)).toBe(true);
    expect(turnTimeLabel(first.started_at, false, 'America/New_York')).toBe('01:30 GMT-04:00');
    expect(turnTimeLabel(second.started_at, false, 'America/New_York')).toBe('01:30 GMT-05:00');
    expect(repeatedTurnTimes([first, second], 'Asia/Seoul').size).toBe(0);
  });

  it('편집 이벤트 수와 경로 표본을 함께 보존한다', () => {
    expect(editedView({ count: 0, sample: [], abandoned: 0 })).toBeNull();
    expect(editedView({ count: 4, sample: ['src/a/lock.ts', 'src/b/ledger.ts', 'x/c.ts'], abandoned: 0 })).toEqual({
      count: 4,
      files: 'lock.ts, ledger.ts',
      more: 2,
      abandoned: 0,
    });
    expect(editedView({ count: 0, sample: [], abandoned: 2 })).toEqual({ count: 0, files: '', more: 0, abandoned: 2 });
  });

  it('같은 경로 두 번은 두 편집 이벤트와 두 경로 표본으로 남는다', () => {
    expect(editedView({ count: 2, sample: ['fixture.txt', 'fixture.txt'], abandoned: 0 })).toEqual({
      count: 2, files: 'fixture.txt, fixture.txt', more: 0, abandoned: 0,
    });
  });
});

describe('unknownReasons: 값을 모르는 턴을 이유별로 모은다', () => {
  it('비용 출처와 이유, 판단 불가의 이유가 따로 센다', () => {
    const reasons = unknownReasons([
      turn({ key: 'u1', no: 1, at: 9, cost: { usd: null, source: 'unattributable', reason: 'pre_p0' }, status: 'undeterminable', status_reason: 'pre_collect' }),
      turn({ key: 'u2', no: 2, at: 8, cost: { usd: null, source: 'unattributable', reason: 'pre_p0' } }),
      turn({ key: 'u3', no: 3, at: 7, cost: { usd: null, source: 'unattributable', reason: 'shared_prompt_id' } }),
      turn({ key: 'u4', no: 4, at: 6, cost: { usd: null, source: 'expired', reason: null } }),
      turn({ key: 'u5', no: 5, at: 5, cost: otel(0.1) }),
      turn({ key: 'u6', no: 6, at: 4, cost: noApiCall }),
    ]);
    const byKey = Object.fromEntries(reasons.map((r) => [r.key, r]));
    expect(byKey['cost:unattributable:pre_p0']).toMatchObject({ label: '비용 귀속 불가', turns: 2 });
    expect(byKey['cost:unattributable:pre_p0'].text).toContain('P0 이전');
    expect(byKey['cost:unattributable:shared_prompt_id']).toMatchObject({ turns: 1 });
    expect(byKey['cost:unattributable:shared_prompt_id'].text).toContain('prompt_id');
    expect(byKey['cost:expired:']).toMatchObject({ label: '비용 만료', turns: 1 });
    expect(byKey['status:pre_collect']).toMatchObject({ label: '턴 상태 판단 불가', turns: 1 });
    // 값이 있는 비용과 API 호출 없음은 "모르는 값"이 아니다.
    expect(reasons).toHaveLength(4);
  });

  it('많이 나온 이유가 먼저다', () => {
    const reasons = unknownReasons([
      turn({ key: 'a', no: 1, at: 3, cost: { usd: null, source: 'expired', reason: null } }),
      turn({ key: 'b', no: 2, at: 2, cost: { usd: null, source: 'no_rate', reason: null } }),
      turn({ key: 'c', no: 3, at: 1, cost: { usd: null, source: 'no_rate', reason: null } }),
    ]);
    expect(reasons.map((r) => r.key)).toEqual(['cost:no_rate:', 'cost:expired:']);
  });

  it('모르는 값이 없으면 빈 배열', () => {
    expect(unknownReasons([turn({ key: 'ok', no: 1, at: 1, cost: otel(0.5) })])).toEqual([]);
    expect(unknownReasons([])).toEqual([]);
  });
});

describe('turnMetrics: "6.2M tok · $2.26 · ctx 18%"', () => {
  it('토큰 합, 비용, 컨텍스트 퍼센트를 한 줄로 만든다', () => {
    const t = turn({
      key: 'm1',
      no: 1,
      at: 10,
      api_requests: 4,
      tokens: tok(30_000, 12_000, 5_000_000, 1_158_000),
      cost: otel(2.26),
      context: ctxObserved(180_000, 1_000_000),
    });
    const m = turnMetrics(t);
    expect(m.parts.map((p) => p.text)).toEqual(['6.2M tok', '$2.26', 'ctx 18%']);
    expect(m.parts.every((p) => !p.unknown)).toBe(true);
    expect(m.title).toContain('입력 30,000');
    expect(m.title).toContain('$2.26 (OTEL)');
  });

  it('모르는 값은 0이 아니라 "?"다(비용 귀속 불가, 컨텍스트 모름)', () => {
    const t = turn({
      key: 'm2',
      no: 1,
      at: 10,
      api_requests: 2,
      tokens: tok(1_000, 500, 200_000, 10_000),
      cost: { usd: null, source: 'unattributable', reason: 'pre_p0' },
      context: ctxTokensOnly(96_400),
    });
    const m = turnMetrics(t);
    expect(m.parts.map((p) => p.text)).toEqual(['211.5K tok', '$?', 'ctx 96.4K']);
    expect(m.parts[1].unknown).toBe(true);
    const none = turnMetrics(turn({ key: 'm3', no: 1, at: 10, api_requests: 1, tokens: tok(1, 1, 1, 1), cost: otel(0.1) }));
    expect(none.parts[2]).toEqual({ key: 'ctx', text: 'ctx ?', unknown: true });
  });

  it('API 호출이 있는데 비용 출처가 없으면 비용을 빼지 않고 "$?"로 둔다', () => {
    const m = turnMetrics(turn({ key: 'm6', no: 1, at: 10, api_requests: 3, tokens: tok(1, 1, 1, 1), cost: noApiCall, context: ctxObserved(10_000, 200_000) }));
    expect(m.parts.map((p) => p.text)).toEqual(['4 tok', '$?', 'ctx 5%']);
    expect(m.parts[1].unknown).toBe(true);
  });

  it('API 호출이 없는 턴은 칩이 없다', () => {
    const m = turnMetrics(turn({ key: 'm4', no: 1, at: 10, api_requests: 0, cost: noApiCall }));
    expect(m.parts).toEqual([]);
    expect(m.title).toContain('API 호출이 없는');
  });

  it('작은 금액은 소수 셋까지, 추정 출처는 툴팁에 밝힌다', () => {
    const t = turn({ key: 'm5', no: 1, at: 10, api_requests: 1, tokens: tok(1, 1, 1, 1), cost: estimated(0.004), context: ctxObserved(10_000, 200_000) });
    const m = turnMetrics(t);
    expect(m.parts[1].text).toBe('$0.004');
    expect(m.title).toContain('(추정)');
  });
});

describe('turnQuestionView: "질문: 머리글 / 머리글"', () => {
  it('머리글을 " / "로 잇는다', () => {
    const t = turn({
      key: 'q1',
      no: 1,
      at: 10,
      question_headers: [
        { tool_use_id: 'a', headers: ['표시 위치', '읽음 저장'], status: 'answered' },
        { tool_use_id: 'b', headers: ['배포 방식'], status: 'answered' },
      ],
    });
    expect(turnQuestionView(t)).toEqual({ text: '질문: 표시 위치 / 읽음 저장 / 배포 방식', flags: [] });
  });

  it('답하지 않았거나 거절된 질문은 flags로 알린다', () => {
    const t = turn({
      key: 'q2',
      no: 1,
      at: 10,
      question_headers: [
        { tool_use_id: 'a', headers: ['A'], status: 'unanswered' },
        { tool_use_id: 'b', headers: ['B'], status: 'rejected' },
      ],
    });
    expect(turnQuestionView(t)?.flags).toEqual(['답 없음', '거절됨']);
  });

  it('질문이 없으면 null', () => {
    expect(turnQuestionView(turn({ key: 'q3', no: 1, at: 10 }))).toBeNull();
  });
});

describe('turnExtraTasks: 커밋 줄에 이미 나온 task는 뺀다', () => {
  it('커밋의 task와 겹치지 않는 것만 남기고 중복을 없앤다', () => {
    const t = turn({
      key: 'x1',
      no: 1,
      at: 10,
      commits: [{ short: 'a1b2c3d', subject: 's', tasks: ['task-1'] }],
      tasks: ['task-1', 'issue-2', 'issue-2'],
    });
    expect(turnExtraTasks(t)).toEqual(['issue-2']);
    expect(turnExtraTasks(turn({ key: 'x2', no: 1, at: 10 }))).toEqual([]);
  });
});
