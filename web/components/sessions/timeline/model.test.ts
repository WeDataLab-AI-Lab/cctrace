import { describe, expect, it } from 'vitest';
import type { InputKind, TimelineMessage } from '@/lib/session-analysis-types';
import {
  DEFAULT_TIMELINE_FILTERS,
  SOURCE_LABEL,
  filterMessages,
  firstMessageOfTurn,
  inputNumberingIssues,
  inputTotal,
  inputsOf,
  isInput,
  messageMeta,
  stepInput,
  type TimelineFilters,
} from './model';
import { CONVERSATION, NOW, assistantMsg, at, systemMsg, userMsg } from './test-data';

const ids = (messages: readonly TimelineMessage[]): string[] => messages.map((m) => m.id);

describe('선택한 시간대의 타임라인 시각', () => {
  it('뉴욕과 서울에서 같은 입력 시각을 다르게 보인다', () => {
    const input = userMsg(1, 1, { ts: '2026-10-08T04:20:00Z' });
    expect(messageMeta(input, NOW, 'America/New_York').time).toBe('2026/10/08 00:20:00 GMT-04:00');
    expect(messageMeta(input, NOW, 'Asia/Seoul').time).toBe('2026/10/08 13:20:00 GMT+09:00');
  });

  it('DST 반복 시각과 잘못된 시간대를 구분한다', () => {
    const before = userMsg(1, 2, { ts: '2026-11-01T05:30:00Z' });
    const after = userMsg(2, 2, { ts: '2026-11-01T06:30:00Z' });
    expect(messageMeta(before, NOW, 'America/New_York').time).toBe('2026/11/01 01:30:00 GMT-04:00');
    expect(messageMeta(after, NOW, 'America/New_York').time).toBe('2026/11/01 01:30:00 GMT-05:00');
    expect(messageMeta(before, NOW, 'Invalid/Zone').time).toBe('2026/11/01 05:30:00 UTC');
  });
});

const FILTERS: Record<string, TimelineFilters> = {
  default: DEFAULT_TIMELINE_FILTERS,
  all: { userOnly: false, hideIntermediate: false },
  hideIntermediate: { userOnly: false, hideIntermediate: true },
  both: { userOnly: true, hideIntermediate: true },
};

describe('isInput', () => {
  it('번호가 있는 사용자 메시지만 입력이다', () => {
    expect(isInput(userMsg(1, 3))).toBe(true);
    expect(isInput(assistantMsg('a', 't1', { n: 1, of: 3 }))).toBe(false);
    expect(isInput(userMsg(1, 3, { n: undefined }))).toBe(false);
    expect(isInput(systemMsg('s', 't1', '압축'))).toBe(false);
  });

  it('번호가 NaN이면 입력이 아니다(정렬과 이동이 깨진다)', () => {
    expect(isInput(userMsg(1, 3, { n: Number.NaN }))).toBe(false);
  });
});

describe('filterMessages', () => {
  it('기본값은 사용자 입력만 보기 켜짐, 중간 출력 숨기기 꺼짐이다', () => {
    expect(DEFAULT_TIMELINE_FILTERS).toEqual({ userOnly: true, hideIntermediate: false });
  });

  it('사용자 입력만 보기: 직접 입력과 질문 답만 남긴다(외부·알림·명령·출처 불명은 숨긴다)', () => {
    const out = filterMessages(CONVERSATION, FILTERS.default);
    expect(ids(out)).toEqual(['u1', 'u2', 'u3']);
    // a5는 답하지 않은 질문 카드를 가진 assistant 메시지다.
    expect(CONVERSATION.find((m) => m.id === 'a5')?.question?.status).toBe('unanswered');
    expect(ids(out)).not.toContain('a5');
  });

  it('명령과 출처 불명은 번호가 있어도 직접 입력만 보기에서 제외한다', () => {
    const messages = [
      userMsg(1, 3, { id: 'command', source: 'command' }),
      userMsg(2, 3, { id: 'unknown', source: 'unknown' }),
      userMsg(3, 3, { id: 'unwitnessed', source: undefined }),
    ];
    expect(ids(filterMessages(messages, FILTERS.default))).toEqual([]);
    expect(ids(filterMessages(messages, FILTERS.all))).toEqual(['command', 'unknown', 'unwitnessed']);
  });

  it('둘 다 끄면 전부 보인다', () => {
    expect(ids(filterMessages(CONVERSATION, FILTERS.all))).toEqual(ids(CONVERSATION));
  });

  it('중간 출력 숨기기: intermediate인 assistant 메시지만 빠진다(마지막 응답, 질문, 플랜, system은 남는다)', () => {
    const out = filterMessages(CONVERSATION, FILTERS.hideIntermediate);
    expect(ids(out)).toEqual(['u1', 'a2', 'u2', 'u3', 'a5', 'a6', 's1', 'u4', 'a7']);
  });

  it('두 토글을 함께 켜면 사용자 입력만 보기와 같다', () => {
    expect(ids(filterMessages(CONVERSATION, FILTERS.both))).toEqual(ids(filterMessages(CONVERSATION, FILTERS.default)));
  });

  it('필터가 숨긴 입력도 전체 입력 수와 이동 기준에는 남는다', () => {
    expect(ids(inputsOf(CONVERSATION))).toEqual(['u1', 'u2', 'u3', 'u4']);
    expect(inputTotal(CONVERSATION)).toBe(4);
    expect(stepInput(CONVERSATION, 3, 1)).toBe(4);
  });

  it('남은 메시지의 상대 순서를 바꾸지 않고 입력 배열도 바꾸지 않는다', () => {
    const copy = [...CONVERSATION];
    const out = filterMessages(CONVERSATION, FILTERS.hideIntermediate);
    const positions = out.map((m) => CONVERSATION.indexOf(m));
    expect(positions).toEqual([...positions].sort((a, b) => a - b));
    expect(CONVERSATION).toEqual(copy);
  });

  it('번호와 출처가 없는 메시지는 직접 입력만 보기에서는 숨고 전체 보기에서는 남는다', () => {
    const plain = userMsg(1, 1, { id: 'plain', n: undefined, of: undefined, source: undefined });
    expect(filterMessages([plain], FILTERS.default)).toEqual([]);
    expect(ids(filterMessages([plain], FILTERS.all))).toEqual(['plain']);
  });
});

describe('inputsOf · inputTotal', () => {
  it('입력만 번호 순으로 돌려준다', () => {
    const shuffled = [userMsg(3, 3), assistantMsg('a', 't1'), userMsg(1, 3), userMsg(2, 3)];
    expect(inputsOf(shuffled).map((m) => m.n)).toEqual([1, 2, 3]);
  });

  it('전체 수는 입력이 실어 온 of의 최댓값이다', () => {
    expect(inputTotal(CONVERSATION)).toBe(4);
    expect(inputTotal([userMsg(1, 15), userMsg(2, 15)])).toBe(15);
    expect(inputTotal([userMsg(1, 3), userMsg(2, 5)])).toBe(5);
  });

  it('아무도 of를 싣지 않았으면 입력 개수, 입력이 없으면 0', () => {
    expect(inputTotal([userMsg(1, 0, { of: undefined }), userMsg(2, 0, { of: undefined })])).toBe(2);
    expect(inputTotal([assistantMsg('a', 't1')])).toBe(0);
    expect(inputTotal([])).toBe(0);
  });
});

describe('stepInput', () => {
  const gap = [userMsg(1, 4), userMsg(2, 4), userMsg(4, 4)];

  it('다음·이전 입력의 번호를 돌려준다', () => {
    expect(stepInput(gap, 1, 1)).toBe(2);
    expect(stepInput(gap, 2, -1)).toBe(1);
  });

  it('끝에서는 null이다', () => {
    expect(stepInput(gap, 4, 1)).toBeNull();
    expect(stepInput(gap, 1, -1)).toBeNull();
  });

  it('빠진 번호는 건너뛴다(1, 2, 4): 2의 다음은 4, 4의 이전은 2', () => {
    expect(stepInput(gap, 2, 1)).toBe(4);
    expect(stepInput(gap, 4, -1)).toBe(2);
  });

  it('현재 번호가 목록에 없어도 가장 가까운 쪽으로 간다(3에서 다음은 4, 이전은 2)', () => {
    expect(stepInput(gap, 3, 1)).toBe(4);
    expect(stepInput(gap, 3, -1)).toBe(2);
    expect(stepInput(gap, 99, -1)).toBe(4);
    expect(stepInput(gap, 0, 1)).toBe(1);
  });

  it('위치를 모르면(null) 다음은 첫 입력이고 이전은 null이다', () => {
    expect(stepInput(gap, null, 1)).toBe(1);
    expect(stepInput(gap, null, -1)).toBeNull();
  });

  it('입력이 없으면 어디로도 가지 않는다', () => {
    expect(stepInput([], null, 1)).toBeNull();
    expect(stepInput([assistantMsg('a', 't1')], 1, 1)).toBeNull();
  });

  it('메시지가 번호 순서로 오지 않아도 같다', () => {
    expect(stepInput([userMsg(4, 4), userMsg(1, 4), userMsg(2, 4)], 2, 1)).toBe(4);
  });
});

describe('firstMessageOfTurn', () => {
  it('그 턴의 사용자 입력 중 번호가 가장 작은 것이다(답 입력보다 요청이 먼저)', () => {
    expect(firstMessageOfTurn(CONVERSATION, 't1')?.id).toBe('u1');
  });

  it('assistant 메시지가 배열에서 먼저 와도 사용자 입력을 고른다', () => {
    const early = [assistantMsg('a0', 't1'), userMsg(1, 1)];
    expect(firstMessageOfTurn(early, 't1')?.id).toBe('u1');
  });

  it('사용자 입력이 없는 턴은 배열에서 처음 나오는 메시지다', () => {
    const only = [assistantMsg('x1', 'tx'), assistantMsg('x2', 'tx')];
    expect(firstMessageOfTurn(only, 'tx')?.id).toBe('x1');
  });

  it('없는 턴은 null이다', () => {
    expect(firstMessageOfTurn(CONVERSATION, 'nope')).toBeNull();
    expect(firstMessageOfTurn([], 't1')).toBeNull();
  });
});

describe('messageMeta', () => {
  it('직접 입력: 시각 · 상대 시각 · n/N · 나, 출처는 적지 않는다', () => {
    const meta = messageMeta(userMsg(1, 15, { ts: at(11, 56, 22) }), NOW);
    expect(meta).toEqual({ time: '2026/10/03 11:56:22', relative: '5시간 전', position: '1/15', source: null, actor: '나' });
  });

  it('질문에 답한 입력은 "질문에 답함"이고 사람이 보낸 것이라 "나"가 붙는다', () => {
    const meta = messageMeta(userMsg(2, 15, { source: 'answer' }), NOW);
    expect(meta.source).toBe('질문에 답함');
    expect(meta.position).toBe('2/15');
    expect(meta.actor).toBe('나');
  });

  it('직접 입력과 작업 중 입력은 사람이 보낸 것으로 표시하지만 명령은 분리한다', () => {
    expect(messageMeta(userMsg(3, 4, { source: 'queued' }), NOW)).toMatchObject({ source: '작업 중 입력', actor: '나' });
    expect(messageMeta(userMsg(3, 4, { source: 'command' }), NOW)).toMatchObject({ source: '명령', actor: '' });
  });

  it('알림·다른 세션·예약 같은 입력은 출처만 적고 "나"가 없다', () => {
    const nonHuman: Exclude<InputKind, 'typed'>[] = ['notification', 'peer', 'channel', 'auto', 'scheduled', 'sdk', 'system', 'unknown'];
    for (const source of nonHuman) {
      const meta = messageMeta(userMsg(1, 2, { source }), NOW);
      expect(meta.source, source).toBe(SOURCE_LABEL[source]);
      expect(meta.actor, source).toBe('');
    }
    expect(messageMeta(userMsg(1, 2, { source: 'notification' }), NOW).source).toBe('백그라운드 알림');
  });

  it('출처 이름이 열한 가지 모두 정해져 있다', () => {
    expect(SOURCE_LABEL).toEqual({
      answer: '질문에 답함',
      queued: '작업 중 입력',
      command: '명령',
      notification: '백그라운드 알림',
      peer: '다른 세션',
      channel: '외부 채널',
      auto: '자동 이어가기',
      scheduled: '예약 실행',
      sdk: 'SDK 호출',
      system: '시스템',
      unknown: '출처 모름',
    });
  });

  it('계약보다 새로운 출처 값은 받은 글자 그대로 보인다', () => {
    const newer = Object.assign({}, userMsg(1, 1), { source: 'brand_new' });
    expect(messageMeta(newer, NOW).source).toBe('brand_new');
  });

  it('출처가 없는 사용자 메시지는 직접 입력으로 단정하지 않는다', () => {
    const meta = messageMeta(userMsg(1, 1, { source: undefined }), NOW);
    expect(meta.source).toBeNull();
    expect(meta.actor).toBe('');
  });

  it('assistant: Claude, 번호와 출처는 없다', () => {
    const meta = messageMeta(assistantMsg('a', 't1', { ts: at(11, 57, 38) }), NOW);
    expect(meta).toEqual({ time: '2026/10/03 11:57:38', relative: '5시간 전', position: null, source: null, actor: 'Claude' });
  });

  it('system은 행위자가 없다', () => {
    expect(messageMeta(systemMsg('s', 't1', '압축'), NOW).actor).toBe('');
  });

  it('n이나 of 중 하나만 있으면 위치를 적지 않는다', () => {
    expect(messageMeta(userMsg(2, 5, { of: undefined }), NOW).position).toBeNull();
    expect(messageMeta(userMsg(2, 5, { n: undefined }), NOW).position).toBeNull();
  });

  it('읽을 수 없는 시각은 "알 수 없음"이고 상대 시각은 비운다', () => {
    const meta = messageMeta(userMsg(1, 1, { ts: 'not-a-time' }), NOW);
    expect(meta.time).toBe('알 수 없음');
    expect(meta.relative).toBe('');
  });

  it('상대 시각: 방금, 분, 시간, 일', () => {
    const rel = (ts: string) => messageMeta(userMsg(1, 1, { ts }), NOW).relative;
    expect(rel(new Date(NOW - 10_000).toISOString())).toBe('방금');
    expect(rel(new Date(NOW - 12 * 60_000).toISOString())).toBe('12분 전');
    expect(rel(new Date(NOW - 3 * 3_600_000).toISOString())).toBe('3시간 전');
    expect(rel(new Date(NOW - 2 * 86_400_000).toISOString())).toBe('2일 전');
  });
});

describe('inputNumberingIssues', () => {
  it('번호가 1부터 of까지 이어지는 대화는 문제가 없다', () => {
    expect(inputNumberingIssues(CONVERSATION)).toEqual([]);
    expect(inputNumberingIssues([])).toEqual([]);
    expect(inputNumberingIssues([assistantMsg('a', 't1')])).toEqual([]);
  });

  it('번호가 빠지면 알린다(1, 2, 4 / of 4)', () => {
    const issues = inputNumberingIssues([userMsg(1, 4), userMsg(2, 4), userMsg(4, 4)]);
    expect(issues).toContain('번호 3 누락');
    expect(issues.some((i) => i.includes('입력 3개인데 of는 4'))).toBe(true);
  });

  it('번호가 겹치면 알린다', () => {
    const issues = inputNumberingIssues([userMsg(1, 2), userMsg(1, 2, { id: 'dup' })]);
    expect(issues).toContain('번호 1 중복');
  });

  it('of가 없거나 서로 다르면 알린다', () => {
    expect(inputNumberingIssues([userMsg(1, 2), userMsg(2, 2, { of: undefined })])).toContain('입력 2에 of 없음');
    expect(inputNumberingIssues([userMsg(1, 2), userMsg(2, 3)])).toContain('of 불일치: 2, 3');
  });

  it('번호가 1~of 밖이면 알린다', () => {
    expect(inputNumberingIssues([userMsg(1, 2), userMsg(5, 2)]).some((i) => i.includes('범위 밖'))).toBe(true);
  });

  it('누락이 많아도 한 줄로 줄인다', () => {
    const issues = inputNumberingIssues([userMsg(1, 30)]);
    expect(issues.filter((i) => i.includes('누락'))).toHaveLength(1);
    expect(issues.find((i) => i.includes('누락'))).toContain('외 21개');
  });
});
