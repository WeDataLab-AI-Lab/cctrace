import { describe, expect, it } from 'vitest';
import { CALENDAR_MAX_INPUTS, type CalendarSession } from '@/lib/session-calendar-types';
import { calInput, calSeg, calSession } from '@/app/test/session-analysis/calendar/calendar-builders';
import { bandHover, bandLabel, inputHover, rangeText } from './hover';
import { prepareSession } from './prepare';

const t = (iso: string): number => Date.parse(iso);
const TZ = 'Asia/Seoul';

describe('캘린더 제목', () => {
  it('제목이 없으면 관측된 첫 요청의 첫 줄을 제목으로 사용한다', () => {
    const session = prepareSession(calSession('no-title', {
      title: { text: null, source: 'none', state: 'none' },
      segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z')],
      inputs: [calInput('2026-10-05T00:10:00Z', { text: '  주문 목록을 고쳐줘\n이후 문장  ' })],
    }));
    expect(bandHover(session, 0, TZ)).toMatchObject({ title: '주문 목록을 고쳐줘', titleMuted: false });
  });

  it('가려진 제목은 입력 미리보기로 대신 노출하지 않는다', () => {
    const session = prepareSession(calSession('redacted-title', {
      title: { text: null, source: 'none', state: 'redacted' },
      segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z')],
      inputs: [calInput('2026-10-05T00:10:00Z', { text: '노출하면 안 되는 대체 제목' })],
    }));
    expect(bandHover(session, 0, TZ).titleMuted).toBe(true);
    expect(bandHover(session, 0, TZ).title).not.toContain('노출하면');
  });
});

describe('범위 표기', () => {
  it('같은 날이면 끝은 시각만, 다른 날이면 날짜까지', () => {
    expect(rangeText(TZ, t('2026-10-05T00:00:00Z'), t('2026-10-05T01:05:00Z'))).toBe('10/05 09:00 – 10:05');
    expect(rangeText(TZ, t('2026-10-05T14:00:00Z'), t('2026-10-05T16:30:00Z'))).toBe('10/05 23:00 – 10/06 01:30');
  });

  it('시간대가 다르면 같은 순간도 다른 날이다', () => {
    expect(rangeText('America/New_York', t('2026-10-05T00:00:00Z'), t('2026-10-05T01:05:00Z'))).toBe('10/04 20:00 – 21:05');
  });
});

describe('띠 호버(C14): 제목, 시간, 요청 수, 비용', () => {
  const session = prepareSession(
    calSession('ad704e10-0000', {
      title: { text: '주문 목록 필터 개선', source: 'ai', state: 'ok' },
      segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:05:00Z', { human_inputs: 7 }), calSeg('2026-10-05T05:00:00Z', '2026-10-05T05:30:00Z')],
      requests: 12,
      cost: { usd: 1.25, source: 'otel' },
      colors: { status: 'done', components: ['웹', 'API'], screens: ['주문 관리'], kinds: ['구현', '상담'], profile: '메인' },
    }),
  );

  it('제목과 짧은 ID, 시간(조각의 범위와 길이)', () => {
    const h = bandHover(session, 0, TZ);
    expect(h.title).toBe('주문 목록 필터 개선');
    expect(h.titleMuted).toBe(false);
    expect(h.shortId).toBe('ad704e10');
    expect(h.time).toBe('10/05 09:00 – 10:05 · 1시간 5분');
  });

  it('요청 수, 비용(출처 포함), 그 조각의 사람 입력', () => {
    const h = bandHover(session, 0, TZ);
    expect(h.stats).toEqual([
      { label: '세션 전체 요청', value: '12', unknown: false },
      { label: '세션 전체 비용', value: '$1.2500 OTEL', unknown: false },
      { label: '선택 구간 입력', value: '7', unknown: false },
    ]);
  });

  it('색 기준 값들과 세션 전체 범위(조각이 여럿이라서)', () => {
    const h = bandHover(session, 0, TZ);
    expect(h.tags).toEqual(
      expect.arrayContaining([
        { label: '완료 여부', value: '완료' },
        { label: '컴포넌트', value: '웹, API' },
        { label: '화면', value: '주문 관리' },
        { label: '종별', value: '구현, 상담' },
        { label: '프로필', value: '메인' },
      ]),
    );
    expect(h.whole).toBe('세션 전체 10/05 09:00 – 14:30 · 조각 2개');
  });

  it('조각이 하나뿐이고 세션 범위와 같으면 "세션 전체"를 되풀이하지 않는다', () => {
    const one = prepareSession(calSession('s', { segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z')] }));
    expect(bandHover(one, 0, TZ).whole).toBeNull();
  });

  it('모르는 값은 0이 아니라 "알 수 없음"이다', () => {
    const unknown = prepareSession(
      calSession('s', {
        segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z', { human_inputs: null })],
        requests: null,
        cost: { usd: null, source: 'none' },
      }),
    );
    const h = bandHover(unknown, 0, TZ);
    expect(h.stats.every((s) => s.value === '알 수 없음' && s.unknown)).toBe(true);
  });

  it('요청 0과 비용 $0은 관측한 값이라 숫자로 적는다', () => {
    const zero = prepareSession(
      calSession('s', {
        segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z', { human_inputs: 0 })],
        requests: 0,
        cost: { usd: 0, source: 'otel' },
      }),
    );
    const h = bandHover(zero, 0, TZ);
    expect(h.stats[0]).toEqual({ label: '세션 전체 요청', value: '0', unknown: false });
    expect(h.stats[1].value).toBe('$0 OTEL');
    expect(h.stats[2]).toEqual({ label: '선택 구간 입력', value: '0', unknown: false });
  });

  it('estimate_pending의 0은 알 수 없는 비용으로 표시하고 출처를 남긴다', () => {
    const pending = prepareSession(calSession('pending', {
      segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z')],
      requests: 1,
      cost: { usd: 0, source: 'estimate_pending' },
    }));
    expect(bandHover(pending, 0, TZ).stats[1]).toEqual({ label: '세션 전체 비용', value: '알 수 없음 (추정 · 반영 대기)', unknown: true });
  });

  it('제목이 가려졌거나 없으면 자리표시 문구를 흐리게', () => {
    const redacted = prepareSession(calSession('s', { title: { text: null, source: 'none', state: 'redacted' }, segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T00:30:00Z')] }));
    expect(bandHover(redacted, 0, TZ)).toMatchObject({ title: '제목 가려짐', titleMuted: true });
    const none = prepareSession(calSession('s', { title: { text: null, source: 'none', state: 'none' }, segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T00:30:00Z')] }));
    expect(bandHover(none, 0, TZ)).toMatchObject({ title: '제목 없음', titleMuted: true });
  });

  it('조각이 없는 세션(truncated)은 범위만 보이고 그 사실을 밝힌다', () => {
    const faint = prepareSession(
      calSession('g', { facts: 'truncated', range: ['2026-10-05T00:00:00Z', '2026-10-06T03:00:00Z'], inputsState: 'undeterminable' }),
    );
    const h = bandHover(faint, -1, TZ);
    expect(h.note).toContain('범위만');
    expect(h.stats.map((s) => s.label)).toEqual(['세션 전체 요청', '세션 전체 비용']);
    expect(h.time).toBe('10/05 09:00 – 10/06 12:00 · 1일 3시간');
  });

  it('입력 상한과 판별 불가, 건너뛴 자료를 0건처럼 숨기지 않는다', () => {
    const truncated = calSession('large', {
      segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z')],
      inputs: [calInput('2026-10-05T00:20:00Z')],
      inputsTruncated: true,
    });
    const prepared = prepareSession(truncated);
    expect(bandHover(prepared, 0, TZ).warnings.join(' ')).toContain('뒤의 입력');
    expect(inputHover(prepared, prepared.inputs[0], TZ).band.warnings.join(' ')).toContain('뒤의 입력');
    truncated.inputs.state = 'undeterminable';
    truncated.inputs.truncated = false;
    truncated.segments.push({ start: 'invalid', end: 'invalid', records: 1, human_inputs: null });
    expect(bandHover(prepareSession(truncated), 0, TZ).warnings.join(' ')).toContain('판별할 수 없습니다');
    expect(bandHover(prepareSession(truncated), 0, TZ).warnings.join(' ')).toContain('자료 1건');
  });

  it('서버가 상한을 넘게 보낸 입력은 시각순으로 이른 2,000건만 두고 경고한다', () => {
    const session = calSession('overflow', { segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z')] });
    session.inputs.items = Array.from({ length: CALENDAR_MAX_INPUTS + 2 }, (_, i) => calInput(Date.parse('2026-10-05T00:00:00Z') + i * 1_000));
    const prepared = prepareSession(session);
    expect(prepared.inputs).toHaveLength(CALENDAR_MAX_INPUTS);
    expect(prepared.inputs[0].at).toBe(Date.parse(session.inputs.items[0].ts));
    expect(prepared.inputOverflow).toBe(true);
    expect(prepared.skipped).toBe(0);
    expect(bandHover(prepared, 0, TZ).warnings.join(' ')).toContain('시각순으로 이른 입력 2,000건만 표시');
    expect(session.inputs.items).toHaveLength(CALENDAR_MAX_INPUTS + 2);
    session.inputs.items.reverse();
    const reversed = prepareSession(session);
    expect(reversed.inputs[0].at).toBe(Date.parse(session.inputs.items[CALENDAR_MAX_INPUTS + 1].ts));
  });

  it('중첩 응답의 null과 잘못된 숫자를 모르는 값으로 안전하게 표시한다', () => {
    const broken = { ...calSession('broken', { segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z')] }),
      title: null, colors: { project: null, components: [null, '정상'], screens: null, kinds: null, status: null },
      cost: { usd: NaN, source: null }, requests: NaN,
      segments: [{ start: '2026-10-05T00:00:00Z', end: '2026-10-05T01:00:00Z', records: null, human_inputs: null }],
      inputs: { state: 'observed', items: [{ ts: '2026-10-05T00:05:00Z', turn_key: null, preview: null }], truncated: false },
    } as unknown as CalendarSession;
    const prepared = prepareSession(broken);
    expect(prepared.skipped).toBeGreaterThan(0);
    expect(prepared.segments[0]).toMatchObject({ records: null, humanInputs: null });
    expect(prepared.inputs[0].preview).toMatchObject({ text: null, state: 'none' });
    expect(bandHover(prepared, 0, TZ).stats.every((stat) => stat.unknown)).toBe(true);
    expect(bandHover(prepared, 0, TZ).warnings.join(' ')).toContain('읽을 수 없는 활동 자료');
  });

  it('완료 여부가 없는 범위(비소유 관리자)는 그 줄이 없다', () => {
    const admin = prepareSession(calSession('s', { segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z')] }));
    delete admin.source.colors.status;
    expect(bandHover(admin, 0, TZ).tags.some((tag) => tag.label === '완료 여부')).toBe(false);
  });
});

describe('입력선 호버(C16): 첫 줄, 가려졌으면 시각만', () => {
  const session = prepareSession(
    calSession('s', {
      title: { text: '로그 샘플 정리', source: 'ai', state: 'ok' },
      segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z')],
      inputs: [
        calInput('2026-10-05T00:10:00Z', { text: '로그 샘플 정리해줘' }),
        calInput('2026-10-05T00:20:00Z', { state: 'redacted' }),
        calInput('2026-10-05T00:30:00Z', { state: 'withheld' }),
        calInput('2026-10-05T00:40:00Z', { state: 'none' }),
      ],
    }),
  );

  it('첫 줄이 있으면 시각과 첫 줄', () => {
    expect(inputHover(session, session.inputs[0], TZ)).toMatchObject({ time: '10/05 09:10', line: '로그 샘플 정리해줘', note: null, title: '로그 샘플 정리' });
  });

  it('비식별화·보류·없음이면 시각만(첫 줄 null)이고 이유를 적는다', () => {
    expect(inputHover(session, session.inputs[1], TZ)).toMatchObject({ time: '10/05 09:20', line: null, note: '첫 줄은 비식별화로 가려졌습니다.' });
    expect(inputHover(session, session.inputs[2], TZ)).toMatchObject({ line: null, note: '첫 줄을 서버에 올리지 않았습니다(보류됨).' });
    expect(inputHover(session, session.inputs[3], TZ)).toMatchObject({ line: null, note: '첫 줄이 없는 입력입니다.' });
  });
});

describe('접근성 이름', () => {
  it('제목, 시간, 요청, 비용, 입력, 완료 여부를 한 줄로', () => {
    const s = prepareSession(
      calSession('s', {
        title: { text: '주문 목록 필터 개선', source: 'ai', state: 'ok' },
        segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:05:00Z', { human_inputs: 7 })],
        requests: 12,
        colors: { status: 'leftover' },
      }),
    );
    expect(bandLabel(s, 0, TZ)).toBe('주문 목록 필터 개선, 10/05 09:00 – 10:05 · 1시간 5분, 세션 전체 요청 12, 세션 전체 비용 $1.2500 OTEL, 선택 구간 입력 7, 하다 만 것');
  });
});
