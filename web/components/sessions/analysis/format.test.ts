import { describe, expect, it } from 'vitest';
import type { CostSource, TurnCost, TurnCostSource } from '@/lib/session-analysis-types';
import {
  contextView,
  costView,
  countsLine,
  countsParts,
  formatClock,
  formatClockNear,
  formatCount,
  formatDateTime,
  formatDuration,
  formatRelative,
  formatTokenCount,
  formatUsd,
  safeHref,
  shareText,
  splitPath,
  taskTypeLabel,
  titleView,
  tokenView,
  turnCostView,
} from './format';

const local = (day: number, hour: number, minute: number): string => new Date(2026, 9, day, hour, minute).toISOString();

describe('0과 모름은 다르다', () => {
  it('개수·토큰·금액: null은 "알 수 없음", 0은 0', () => {
    expect(formatCount(null)).toBe('알 수 없음');
    expect(formatCount(undefined)).toBe('알 수 없음');
    expect(formatCount(0)).toBe('0');
    expect(formatCount(12345)).toBe('12,345');
    expect(formatTokenCount(null)).toBe('알 수 없음');
    expect(formatTokenCount(0)).toBe('0');
    expect(formatUsd(null)).toBe('알 수 없음');
    expect(formatUsd(0)).toBe('$0');
  });

  it('양수는 목록과 같은 모양(1.2K, 3.4M, $0.1234)', () => {
    expect(formatTokenCount(537)).toBe('537');
    expect(formatTokenCount(12_345)).toBe('12.3K');
    expect(formatTokenCount(3_400_000)).toBe('3.4M');
    expect(formatUsd(7.57)).toBe('$7.5700');
    expect(formatUsd(0.0042)).toBe('$0.0042');
  });

  it('NaN과 무한대도 모름이다', () => {
    expect(formatCount(Number.NaN)).toBe('알 수 없음');
    expect(formatUsd(Number.POSITIVE_INFINITY)).toBe('알 수 없음');
  });
});

describe('요청 · 입력 · 턴 줄', () => {
  it('"요청 N · 입력 M · 턴 K"', () => {
    expect(countsLine({ requests: 12, inputs: 14, turns: 15 })).toBe('요청 12 · 입력 14 · 턴 15');
  });

  it('턴 경계를 판단할 수 없으면 세 수는 null이고 0이 아니라 "알 수 없음"이다', () => {
    expect(countsLine({ requests: null, inputs: null, turns: null })).toBe('요청 알 수 없음 · 입력 알 수 없음 · 턴 알 수 없음');
    expect(countsParts({ requests: null, inputs: null, turns: null }).map((p) => p.value)).toEqual([null, null, null]);
  });

  it('관측한 0은 0으로 적는다', () => {
    expect(countsLine({ requests: 0, inputs: 3, turns: 3 })).toBe('요청 0 · 입력 3 · 턴 3');
  });

  it('일부만 null이어도 그 칸만 모른다', () => {
    expect(countsLine({ requests: 2, inputs: null, turns: 2 })).toBe('요청 2 · 입력 알 수 없음 · 턴 2');
  });

  it('counts가 통째로 없어도(롤업 머리) 모두 모른다', () => {
    expect(countsLine(undefined)).toBe('요청 알 수 없음 · 입력 알 수 없음 · 턴 알 수 없음');
  });
});

describe('토큰', () => {
  it('네 칸을 모두 알면 합을 내고, 하나라도 모르면 합을 내지 않는다', () => {
    expect(tokenView({ input: 10, output: 20, cache_read: 300, cache_create: 4 }).total).toBe(334);
    expect(tokenView({ input: 10, output: 20 }).total).toBeNull();
    expect(tokenView(undefined).total).toBeNull();
  });

  it('칸마다 값이 없으면 null이다(0이 아니다)', () => {
    const view = tokenView({ input: 0, output: 20 });
    expect(view.parts.map((p) => p.value)).toEqual([0, 20, null, null]);
  });
});

describe('비용 출처 (세션, §4.4)', () => {
  const sources: [CostSource, { label: string; tone: string }][] = [
    ['otel', { label: 'OTEL', tone: 'measured' }],
    ['otel_partial', { label: 'OTEL 일부', tone: 'partial' }],
    ['estimate', { label: '추정', tone: 'estimated' }],
    ['estimate_pending', { label: '추정 · 반영 대기', tone: 'partial' }],
  ];

  it.each(sources)('%s: 금액과 출처 라벨과 설명이 나온다', (source, expected) => {
    const view = costView({ usd: 7.57, source, turn_attached_usd: 6.8, unattached_turn_usd: 0.77, note: null });
    expect(view.amount).toBe('$7.5700');
    expect(view.known).toBe(true);
    expect(view.sourceLabel).toBe(expected.label);
    expect(view.tone).toBe(expected.tone);
    expect(view.sourceHint.length).toBeGreaterThan(10);
    expect(view.split).toEqual({ attached: '$6.8000', unattached: '$0.7700' });
  });

  it('none: 금액을 모른다(0이 아니다)', () => {
    const view = costView({ usd: null, source: 'none', turn_attached_usd: null, unattached_turn_usd: null, note: null });
    expect(view.amount).toBe('알 수 없음');
    expect(view.known).toBe(false);
    expect(view.tone).toBe('unknown');
    expect(view.split).toBeNull();
    expect(view.sourceLabel).toBe('출처 없음');
  });

  it('none인데 서버가 0을 실어 보내도 "$0"이 아니라 모른다', () => {
    const view = costView({ usd: 0, source: 'none', turn_attached_usd: 0, unattached_turn_usd: 0, note: null });
    expect(view.amount).toBe('알 수 없음');
    expect(view.known).toBe(false);
    expect(view.tone).toBe('unknown');
  });

  it('estimate_pending의 0은 미반영 상태라 확인된 $0으로 보이지 않는다', () => {
    const view = costView({ usd: 0, source: 'estimate_pending', turn_attached_usd: 0, unattached_turn_usd: 0, note: null });
    expect(view.amount).toBe('알 수 없음');
    expect(view.known).toBe(false);
    expect(view.sourceLabel).toBe('추정 · 반영 대기');
    expect(view.sourceHint).toContain('확인된 0달러가 아닙니다');
    expect(view.split).toBeNull();
  });

  it('값이 0원이면 $0이고 아는 값이다', () => {
    const view = costView({ usd: 0, source: 'otel', turn_attached_usd: 0, unattached_turn_usd: 0, note: null });
    expect(view.amount).toBe('$0');
    expect(view.known).toBe(true);
  });

  it('OTEL이 모두 만료된 세션은 주석을 붙인다', () => {
    const view = costView({ usd: 0.3, source: 'estimate', turn_attached_usd: 0.28, unattached_turn_usd: 0.02, note: 'otel_expired' });
    expect(view.note).toContain('보존 기간을 넘어 지워져');
  });

  it('턴에 붙은 몫과 붙지 않은 몫 중 하나만 알아도 나눔을 보이고 모르는 쪽은 모른다고 적는다', () => {
    const view = costView({ usd: 1, source: 'otel', turn_attached_usd: 1, unattached_turn_usd: null, note: null });
    expect(view.split).toEqual({ attached: '$1.0000', unattached: '알 수 없음' });
  });

  it('롤업 머리만 온 응답(일부 필드)도 깨지지 않는다', () => {
    const view = costView({ usd: 0.84, source: 'otel' });
    expect(view.amount).toBe('$0.8400');
    expect(view.split).toBeNull();
    expect(costView(undefined).known).toBe(false);
  });
});

describe('비용 출처 (턴, §4.4)', () => {
  const cost = (source: TurnCostSource, usd: number | null, reason: TurnCost['reason'] = null): TurnCost => ({ usd, source, reason });

  it('otel · otel_partial · estimate · estimate_pending: 금액을 보인다', () => {
    expect(turnCostView(cost('otel', 0.42))).toMatchObject({ kind: 'amount', amount: '$0.4200', sourceLabel: 'OTEL', tone: 'measured' });
    expect(turnCostView(cost('otel_partial', 0.12))).toMatchObject({ kind: 'amount', sourceLabel: 'OTEL 일부', tone: 'partial' });
    expect(turnCostView(cost('estimate', 0.2))).toMatchObject({ kind: 'amount', sourceLabel: '추정', tone: 'estimated' });
    expect(turnCostView(cost('estimate_pending', 0.1))).toMatchObject({ kind: 'amount', sourceLabel: '추정 · 대기', tone: 'partial' });
  });

  it('estimate_pending 0은 미반영 대기라 금액으로 보이지 않는다', () => {
    expect(turnCostView(cost('estimate_pending', 0))).toMatchObject({ kind: 'unknown', amount: '', sourceLabel: '추정 · 대기' });
    expect(turnCostView(cost('estimate_pending', 0)).hint).toContain('확인된 0달러가 아닙니다');
  });

  it('expired · no_rate: 값이 없다. 서버가 0을 실어 보내도 모르는 값이다', () => {
    for (const source of ['expired', 'no_rate'] as const) {
      const withNull = turnCostView(cost(source, null));
      const withZero = turnCostView(cost(source, 0));
      expect(withNull.kind).toBe('unknown');
      expect(withZero.kind).toBe('unknown');
      expect(withZero.amount).toBe('');
    }
    expect(turnCostView(cost('expired', null)).hint).toContain('세션 합계에서는 0으로 셉니다');
    expect(turnCostView(cost('no_rate', null)).sourceLabel).toBe('요율 없음');
  });

  it('unattributable: 이유가 문장으로 풀린다', () => {
    const pre = turnCostView(cost('unattributable', null, 'pre_p0'));
    const shared = turnCostView(cost('unattributable', null, 'shared_prompt_id'));
    expect(pre.kind).toBe('unknown');
    expect(pre.hint).toContain('P0 이전');
    expect(shared.hint).toContain('prompt_id');
    expect(pre.hint).not.toBe(shared.hint);
  });

  it('none: API 호출이 없는 턴이다(모름도 0도 아니다)', () => {
    const view = turnCostView(cost('none', null));
    expect(view.kind).toBe('none');
    expect(view.sourceLabel).toBe('API 호출 없음');
  });

  it('값이 있어야 하는 출처에 금액이 없으면 모른다', () => {
    expect(turnCostView(cost('otel', null)).kind).toBe('unknown');
  });
});

describe('컨텍스트 (§3.11)', () => {
  it('창을 알면 퍼센트와 "토큰 / 창"과 출처(관측)', () => {
    const view = contextView({ tokens: 142_000, window: 1_000_000, percent: 14.2, source: 'observed', large_window_seen: true });
    expect(view.kind).toBe('percent');
    expect(view.percentLabel).toBe('14%');
    expect(view.windowLabel).toBe('1.0M');
    expect(view.sourceLabel).toBe('관측');
    expect(view.largeWindowHint).toBeNull();
    expect(view.summary).toBe('컨텍스트 14% (142.0K / 1.0M · 관측)');
  });

  it('모델 표기(추정)도 퍼센트로 그리고 출처를 "모델 표기"로 적는다', () => {
    const view = contextView({ tokens: 143_000, window: 200_000, percent: 71.5, source: 'model_default', large_window_seen: false });
    expect(view.kind).toBe('percent');
    expect(view.sourceLabel).toBe('모델 표기');
    expect(view.sourceHint).toContain('추정');
  });

  it('창을 모르면 퍼센트 없이 토큰 수만, 큰 창 흔적이 있으면 힌트를 붙인다', () => {
    const view = contextView({ tokens: 238_000, window: null, percent: null, source: 'unknown', large_window_seen: true });
    expect(view.kind).toBe('tokens_only');
    expect(view.percentLabel).toBe('');
    expect(view.percentValue).toBeNull();
    expect(view.tokensLabel).toBe('238.0K');
    expect(view.largeWindowHint).toContain('큰 창(1M)');
    expect(view.sourceLabel).toBe('모름');
  });

  it('큰 창 흔적이 없으면 힌트도 없다', () => {
    expect(contextView({ tokens: 58_000, window: null, percent: null, source: 'unknown', large_window_seen: false }).largeWindowHint).toBeNull();
  });

  it('토큰 수도 모르면 모름이다(0이 아니다)', () => {
    const view = contextView({ tokens: null, window: null, percent: null, source: 'unknown', large_window_seen: true });
    expect(view.kind).toBe('unknown');
    expect(view.tokensLabel).toBe('알 수 없음');
    expect(view.largeWindowHint).toContain('큰 창(1M)');
  });

  it('목록 행처럼 창 크기 없이 퍼센트만 와도 퍼센트로 그린다', () => {
    const view = contextView({ tokens: 520_000, percent: 52, source: 'observed', large_window_seen: true });
    expect(view.kind).toBe('percent');
    expect(view.percentLabel).toBe('52%');
    expect(view.summary).toBe('컨텍스트 52% (520.0K · 관측)');
  });

  it('1% 미만은 "<1%", 범위를 넘는 값은 막대에서만 잘라낸다', () => {
    expect(contextView({ tokens: 900, window: 1_000_000, percent: 0.09, source: 'observed' }).percentLabel).toBe('<1%');
    const over = contextView({ tokens: 1_200_000, window: 1_000_000, percent: 120, source: 'observed' });
    expect(over.percentLabel).toBe('120%');
    expect(over.percentValue).toBe(100);
  });

  it('퍼센트가 있어도 출처가 모름이면 퍼센트를 믿지 않는다', () => {
    expect(contextView({ tokens: 10, window: 100, percent: 10, source: 'unknown' }).kind).toBe('tokens_only');
  });
});

describe('제목 (§3.18)', () => {
  it('ok: 값과 출처', () => {
    const ai = titleView({ text: ' 결제 웹훅 정리 ', source: 'ai', state: 'ok' });
    expect(ai).toMatchObject({ kind: 'text', text: '결제 웹훅 정리', tag: 'AI 제목' });
    expect(titleView({ text: 'x', source: 'custom', state: 'ok' }).tag).toBe('직접 정한 제목');
    expect(titleView({ text: 'x', source: 'first_request', state: 'ok' }).tag).toBe('첫 요청');
  });

  it('withheld: 첫 요청을 대신 보이고 "제목 보류됨(첫 요청)"이라 적는다', () => {
    const view = titleView({ text: '첫 요청 문장', source: 'first_request', state: 'withheld' });
    expect(view.kind).toBe('withheld');
    expect(view.text).toBe('첫 요청 문장');
    expect(view.tag).toBe('제목 보류됨(첫 요청)');
    expect(view.hint).toContain('제목이 없다는 뜻이 아닙니다');
  });

  it('redacted: 첫 요청으로 대신하지 않는다', () => {
    const view = titleView({ text: '새면 안 되는 문장', source: 'first_request', state: 'redacted' });
    expect(view.kind).toBe('redacted');
    expect(view.text).toBeNull();
    expect(view.placeholder).toBe('제목 가려짐');
  });

  it('none과 빈 제목: 제목 없음', () => {
    expect(titleView({ text: null, source: 'none', state: 'none' })).toMatchObject({ kind: 'none', placeholder: '제목 없음' });
    expect(titleView({ text: '  ', source: 'ai', state: 'ok' }).kind).toBe('none');
    expect(titleView(undefined).placeholder).toBe('제목 없음');
  });
});

describe('시각과 시간', () => {
  it('HH:MM, MM/DD HH:MM', () => {
    expect(formatClock(local(7, 9, 5))).toBe('09:05');
    expect(formatDateTime(local(7, 15, 39))).toBe('10/07 15:39');
    expect(formatClock(null)).toBe('알 수 없음');
    expect(formatDateTime('not a date')).toBe('알 수 없음');
  });

  it('기준과 같은 날이면 시각만, 아니면 날짜를 붙인다', () => {
    const now = new Date(2026, 9, 7, 16, 0).getTime();
    expect(formatClockNear(local(7, 10, 42), now)).toBe('10:42');
    expect(formatClockNear(local(5, 10, 42), now)).toBe('10/05 10:42');
  });

  it('선택한 시간대에서 날짜와 시각을 계산하고, 상대 시각은 같은 epoch를 쓴다', () => {
    const instant = '2026-10-08T04:20:00Z';
    const now = Date.parse('2026-10-08T05:20:00Z');
    expect(formatClock(instant, 'America/New_York')).toBe('00:20 GMT-04:00');
    expect(formatDateTime(instant, 'Asia/Seoul')).toBe('10/08 13:20 GMT+09:00');
    expect(formatClockNear(instant, now, 'America/New_York')).toBe('00:20 GMT-04:00');
    expect(formatClockNear(instant, now, 'Asia/Seoul')).toBe('13:20 GMT+09:00');
    expect(formatRelative(instant, now)).toBe('1시간 전');
    expect(formatClockNear('2026-10-08T03:20:00Z', Date.parse('2026-10-08T05:20:00Z'), 'America/New_York')).toBe('10/07 23:20 GMT-04:00');
    expect(formatClock('2026-12-08T05:20:00Z', 'America/New_York')).toBe('00:20 GMT-05:00');
    expect(formatClock('2026-12-08T05:20:00Z', 'Asia/Seoul')).toBe('14:20 GMT+09:00');
    expect(formatClock(instant, 'Not/A_Zone')).toBe('04:20 UTC');
  });

  it('DST 반복 01:30을 오프셋으로 구별한다', () => {
    expect(formatDateTime('2026-11-01T05:30:00Z', 'America/New_York')).toBe('11/01 01:30 GMT-04:00');
    expect(formatDateTime('2026-11-01T06:30:00Z', 'America/New_York')).toBe('11/01 01:30 GMT-05:00');
  });

  it('걸린 시간: 가장 큰 두 단위까지', () => {
    expect(formatDuration(0)).toBe('0초');
    expect(formatDuration(42)).toBe('42초');
    expect(formatDuration(60 * 19)).toBe('19분');
    expect(formatDuration(3600)).toBe('1시간');
    expect(formatDuration(3600 + 19 * 60)).toBe('1시간 19분');
    expect(formatDuration(24 * 3600)).toBe('1일');
    expect(formatDuration(100 * 24 * 3600 + 3 * 3600)).toBe('100일 3시간');
    expect(formatDuration(null)).toBe('알 수 없음');
    expect(formatDuration(-1)).toBe('알 수 없음');
  });

  it('상대 시각: 방금 · 분 · 시간 · 일, 미래의 시계 어긋남은 방금', () => {
    const now = new Date(2026, 9, 7, 16, 0).getTime();
    expect(formatRelative(new Date(now - 10_000).toISOString(), now)).toBe('방금');
    expect(formatRelative(new Date(now + 90_000).toISOString(), now)).toBe('방금');
    expect(formatRelative(new Date(now - 5 * 60_000).toISOString(), now)).toBe('5분 전');
    expect(formatRelative(new Date(now - 3 * 3600_000).toISOString(), now)).toBe('3시간 전');
    expect(formatRelative(new Date(now - 40 * 24 * 3600_000).toISOString(), now)).toBe('40일 전');
    expect(formatRelative(null, now)).toBe('알 수 없음');
  });
});

describe('그 밖의 서식', () => {
  it('경로를 폴더와 파일 이름으로 가른다', () => {
    expect(splitPath('src/webhooks/retry.ts')).toEqual({ dir: 'src/webhooks/', base: 'retry.ts' });
    expect(splitPath('README.md')).toEqual({ dir: '', base: 'README.md' });
    expect(splitPath('C:\\work\\a.ts')).toEqual({ dir: 'C:/work/', base: 'a.ts' });
  });

  it('http(s) URL만 링크가 된다', () => {
    expect(safeHref('https://git.example.com/acme/x/pull/1')).toBe('https://git.example.com/acme/x/pull/1');
    expect(safeHref('javascript:alert(1)')).toBeUndefined();
    expect(safeHref('')).toBeUndefined();
    expect(safeHref(null)).toBeUndefined();
  });

  it('작업 유형 비율과 라벨', () => {
    expect(shareText(0.45)).toBe('45%');
    expect(shareText(1)).toBe('100%');
    expect(shareText(null)).toBe('알 수 없음');
    expect(taskTypeLabel('unknown')).toBe('분류 못함');
    expect(taskTypeLabel('debugging')).toBe('debugging');
  });
});
