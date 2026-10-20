import { describe, expect, it } from 'vitest';
import { calSession } from '@/app/test/session-analysis/calendar/calendar-builders';
import type { CalendarColors, CalendarSession } from '@/lib/session-calendar-types';
import { BASE_COLORS, COLOR_KEYS, NONE_LABEL, assignColors, colorKeyAvailable, colorValueOf, paletteToken } from './colors';

const withColors = (id: string, colors: Partial<CalendarColors>): CalendarSession => calSession(id, { colors });
const labels = (legend: { label: string; sessions: number }[]) => legend.map((e) => `${e.label} ${e.sessions}`);

describe('색 기준 선택지(계획 §3.1 C4)', () => {
  it('레퍼런스의 다섯이 앞에, 프로젝트·에이전트·모델이 뒤에 온다', () => {
    expect(COLOR_KEYS.map((k) => k.label)).toEqual(['완료 여부', '코드 영역', '대상 화면', '작업 유형', '프로필', '프로젝트', '에이전트', '모델']);
    expect(COLOR_KEYS.map((k) => k.key)).toEqual(['status', 'component', 'screen', 'kind', 'profile', 'project', 'agent', 'model']);
  });

  it('완료 여부는 세션 주인 범위에서만 쓸 수 있다', () => {
    expect(colorKeyAvailable('status', true)).toBe(true);
    expect(colorKeyAvailable('status', false)).toBe(false);
    expect(colorKeyAvailable('screen', false)).toBe(true);
  });
});

describe('값 읽기: 태그는 첫 값, 없으면 모름', () => {
  it('컴포넌트·화면·종별은 첫 값이다', () => {
    const s = withColors('s', { components: ['API', '배치'], screens: ['주문'], kinds: ['구현', '상담'] });
    expect(colorValueOf(s, 'component')).toEqual({ key: 'API', label: 'API' });
    expect(colorValueOf(s, 'screen')).toEqual({ key: '주문', label: '주문' });
    expect(colorValueOf(s, 'kind')).toEqual({ key: '구현', label: '구현' });
  });

  it('빈 배열과 null은 값이 없는 것이다(0이나 빈 이름으로 읽지 않는다)', () => {
    const s = withColors('s', { components: [], profile: null, model: null, project: null, status: null });
    for (const key of ['component', 'screen', 'kind', 'profile', 'project', 'model', 'status'] as const) {
      expect(colorValueOf(s, key), key).toBeNull();
    }
  });

  it('프로젝트는 해시로 가르고 이름을 보인다', () => {
    const s = withColors('s', { project: { hash: '-work-billing', name: 'billing-api' } });
    expect(colorValueOf(s, 'project')).toEqual({ key: '-work-billing', label: 'billing-api' });
  });

  it('완료 여부는 한국어 이름으로 보인다', () => {
    expect(colorValueOf(withColors('s', { status: 'midway' }), 'status')).toEqual({ key: 'midway', label: '도중 종료' });
  });
});

describe('범례: 모든 값을 세션 수 순으로, "(없음)" 칸 포함, 합 = 세션 수', () => {
  const sessions = [
    ...Array.from({ length: 5 }, (_, i) => withColors(`a${i}`, { screens: ['대시보드'] })),
    ...Array.from({ length: 3 }, (_, i) => withColors(`b${i}`, { screens: ['세션'] })),
    ...Array.from({ length: 4 }, (_, i) => withColors(`n${i}`, { screens: [] })),
    withColors('c0', { screens: ['설정'] }),
    withColors('c1', { screens: ['검색'] }),
  ];

  it('개수 순이고 "(없음)"도 그 순서에 든다', () => {
    const { legend } = assignColors(sessions, 'screen');
    expect(labels(legend)).toEqual(['대시보드 5', `${NONE_LABEL} 4`, '세션 3', '검색 1', '설정 1']);
  });

  it('범례의 합은 그 주의 세션 수와 같다', () => {
    const a = assignColors(sessions, 'screen');
    expect(a.legend.reduce((sum, e) => sum + e.sessions, 0)).toBe(sessions.length);
    expect(a.total).toBe(sessions.length);
  });

  it('같은 개수는 이름 순이고, 개수가 같으면 값이 있는 칸이 "(없음)"보다 먼저다', () => {
    const tie = [
      withColors('x', { screens: ['b'] }),
      withColors('y', { screens: ['a'] }),
      withColors('z', { screens: [] }),
    ];
    expect(labels(assignColors(tie, 'screen').legend)).toEqual(['a 1', 'b 1', `${NONE_LABEL} 1`]);
  });

  it('세션마다 칸이 정확히 하나다', () => {
    const a = assignColors(sessions, 'screen');
    expect(a.bySession.size).toBe(sessions.length);
    const counted = new Map<string, number>();
    for (const c of a.bySession.values()) counted.set(c.entryKey, (counted.get(c.entryKey) ?? 0) + 1);
    for (const e of a.legend) expect(counted.get(e.key), e.label).toBe(e.sessions);
  });

  it('세션이 없으면 범례도 비었다', () => {
    const a = assignColors([], 'screen');
    expect(a.legend).toEqual([]);
    expect(a.total).toBe(0);
  });

  it('값이 모두 있으면 "(없음)" 칸이 없다', () => {
    const a = assignColors([withColors('a', { agent: 'claude' }), withColors('b', { agent: 'codex' })], 'agent');
    expect(a.legend.some((e) => e.none)).toBe(false);
  });
});

describe('색: 순위대로, 값이 많으면 변형, 완료 여부는 고정', () => {
  const many = (n: number): CalendarSession[] =>
    Array.from({ length: n }, (_, i) => withColors(`s${i}`, { screens: [`화면${String(i).padStart(2, '0')}`] }));

  it('1~8위는 범주 색 그대로, 순위가 높을수록 앞선 색', () => {
    const { legend } = assignColors(many(8), 'screen');
    expect(legend.map((e) => e.token)).toEqual(Array.from({ length: 8 }, (_, i) => `var(--cat-${i + 1})`));
    expect(paletteToken(1)).toBe('var(--cat-1)');
    expect(paletteToken(BASE_COLORS)).toBe(`var(--cat-${BASE_COLORS})`);
  });

  it('9~16위는 어둡게, 17~24위는 밝게 섞어 쓰고 25위부터 다시 돈다', () => {
    expect(paletteToken(9)).toBe('color-mix(in oklch, var(--cat-1) 68%, black)');
    expect(paletteToken(16)).toBe('color-mix(in oklch, var(--cat-8) 68%, black)');
    expect(paletteToken(17)).toBe('color-mix(in oklch, var(--cat-1) 52%, white)');
    expect(paletteToken(25)).toBe('var(--cat-1)');
  });

  it('한 주에 값이 열다섯이어도 모두 범례에 오르고 서로 다른 토큰이다(레퍼런스는 15칸)', () => {
    const { legend } = assignColors(many(15), 'screen');
    expect(legend).toHaveLength(15);
    expect(new Set(legend.map((e) => e.token)).size).toBe(15);
  });

  it('"(없음)"은 중립 회색이고 순위 색을 가져가지 않는다', () => {
    const sessions = [...many(3), withColors('n', { screens: [] }), withColors('n2', { screens: [] }), withColors('n3', { screens: [] })];
    const { legend } = assignColors(sessions, 'screen');
    const none = legend.find((e) => e.none);
    expect(none?.token).toBe('var(--ink-4)');
    expect(legend.filter((e) => !e.none).map((e) => e.token)).toEqual(['var(--cat-1)', 'var(--cat-2)', 'var(--cat-3)']);
  });

  it('완료 여부는 값마다 고정된 색이다(개수와 상관없이)', () => {
    const sessions = [
      withColors('a', { status: 'midway' }),
      withColors('b', { status: 'midway' }),
      withColors('c', { status: 'done' }),
      withColors('d', { status: 'running' }),
    ];
    const byLabel = Object.fromEntries(assignColors(sessions, 'status').legend.map((e) => [e.label, e.token]));
    expect(byLabel).toEqual({ '도중 종료': 'var(--warning)', 완료: 'var(--success)', '실행 중': 'var(--cat-1)' });
  });

  it('주마다 순위를 다시 매긴다: 같은 값도 다른 주에는 다른 색일 수 있다', () => {
    const week1 = [withColors('a', { screens: ['X'] }), withColors('b', { screens: ['X'] }), withColors('c', { screens: ['Y'] })];
    const week2 = [withColors('d', { screens: ['Y'] }), withColors('e', { screens: ['Y'] }), withColors('f', { screens: ['X'] })];
    const colorOfX = (sessions: CalendarSession[]) => assignColors(sessions, 'screen').legend.find((e) => e.key === 'X')?.token;
    expect(colorOfX(week1)).toBe('var(--cat-1)');
    expect(colorOfX(week2)).toBe('var(--cat-2)');
  });
});

describe('세션의 색', () => {
  it('세션은 자기 값의 칸 색을 받고, 값이 없으면 "(없음)" 색이다', () => {
    const sessions = [withColors('a', { agent: 'claude' }), withColors('b', { model: null })];
    const byAgent = assignColors(sessions, 'agent');
    expect(byAgent.bySession.get('a')).toMatchObject({ none: false, token: 'var(--cat-1)' });
    const byModel = assignColors([withColors('x', { model: null })], 'model');
    expect(byModel.bySession.get('x')).toMatchObject({ none: true, token: 'var(--ink-4)' });
  });
});
