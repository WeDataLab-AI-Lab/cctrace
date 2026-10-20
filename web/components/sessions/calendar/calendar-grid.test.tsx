// @vitest-environment jsdom
import { createRef } from 'react';
import { act, cleanup, createEvent, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { calInput, calSeg, calSession } from '@/app/test/session-analysis/calendar/calendar-builders';
import type { CalendarSession } from '@/lib/session-calendar-types';
import { CalendarGrid } from './calendar-grid';
import { assignColors } from './colors';
import { weekColumns } from './day-grid';
import { layoutWeek } from './layout';
import { prepareSessions } from './prepare';
import type { LocalDate } from './time-zone';

beforeAll(() => {
  // jsdom에는 PointerEvent가 없어 좌표가 사라진다. MouseEvent로 대신한다.
  Object.defineProperty(window, 'PointerEvent', { value: window.MouseEvent, configurable: true });
});

afterEach(cleanup);

const SEOUL = 'Asia/Seoul';
/** 서울 2026-10-07(수) 15:40. */
const NOW = Date.parse('2026-10-07T06:40:00Z');
const MON: LocalDate = { year: 2026, month: 10, day: 5 };

// 서울 월요일 09:00~10:00은 UTC 00:00~01:00이다.
const SESSIONS: CalendarSession[] = [
  calSession('mon-a', {
    title: { text: '주문 목록 정리', source: 'ai', state: 'ok' },
    segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z', { human_inputs: 3 })],
    inputs: [
      calInput('2026-10-05T00:10:00Z', { text: '주문 목록 필터를 고쳐줘' }),
      calInput('2026-10-05T00:20:00Z', { state: 'redacted' }),
      calInput('2026-10-05T00:30:00Z', { state: 'withheld' }),
    ],
    colors: { screens: ['대시보드'], status: 'done' },
  }),
  calSession('mon-b', {
    title: { text: '설정 화면 점검', source: 'ai', state: 'ok' },
    segments: [calSeg('2026-10-05T00:30:00Z', '2026-10-05T01:30:00Z')],
    colors: { screens: ['설정'], status: 'leftover' },
  }),
  calSession('tue-faint', {
    title: { text: '범위만 있는 세션', source: 'ai', state: 'ok' },
    facts: 'truncated',
    range: ['2026-10-06T13:00:00Z', '2026-10-07T03:00:00Z'],
    inputsState: 'undeterminable',
    colors: { screens: ['대시보드'], status: 'leftover' },
  }),
  calSession('wed-live', {
    title: { text: '지금 하는 일', source: 'ai', state: 'ok' },
    segments: [calSeg('2026-10-07T05:00:00Z', '2026-10-07T06:35:00Z')],
    inputs: ['2026-10-07T05:10:00Z'],
    colors: { screens: ['대시보드'], status: 'running' },
  }),
  calSession('thu-gap', {
    title: { text: '쉬었다 한 일', source: 'ai', state: 'ok' },
    segments: [calSeg('2026-10-08T01:00:00Z', '2026-10-08T01:30:00Z'), calSeg('2026-10-08T02:30:00Z', '2026-10-08T03:00:00Z')],
    colors: { screens: ['설정'], status: 'done' },
  }),
];

interface Mount {
  utils: ReturnType<typeof render>;
  layout: ReturnType<typeof layoutWeek>;
  onSelect: ReturnType<typeof vi.fn>;
}

const mount = (
  over: { sessions?: CalendarSession[]; timeZone?: string; weekStart?: LocalDate; pxPerHour?: number; nowMs?: number; selectedId?: string | null; highlightKey?: string | null } = {},
): Mount => {
  const sessions = over.sessions ?? SESSIONS;
  const timeZone = over.timeZone ?? SEOUL;
  const pxPerHour = over.pxPerHour ?? 30;
  const columns = weekColumns(timeZone, over.weekStart ?? MON, over.nowMs ?? NOW);
  const prepared = prepareSessions(sessions);
  const layout = layoutWeek({ timeZone, columns, sessions: prepared, pxPerHour, nowMs: over.nowMs ?? NOW });
  const onSelect = vi.fn();
  const utils = render(
    <CalendarGrid
      layout={layout}
      sessionsById={new Map(prepared.map((s) => [s.id, s]))}
      assignment={assignColors(sessions, 'screen')}
      highlightKey={over.highlightKey ?? null}
      selectedId={over.selectedId ?? null}
      onSelect={onSelect}
      timeZone={timeZone}
      pxPerHour={pxPerHour}
      scrollerRef={createRef<HTMLDivElement>()}
    />,
  );
  return { utils, layout, onSelect };
};

const bands = (): HTMLButtonElement[] => Array.from(document.querySelectorAll<HTMLButtonElement>('[data-calendar-band]'));
const bandOf = (sessionId: string): HTMLButtonElement[] => bands().filter((b) => b.dataset.sessionId === sessionId);
const column = (key: string): HTMLElement => document.querySelector<HTMLElement>(`[data-day="${key}"]`) as HTMLElement;
const tooltip = (): HTMLElement | null => screen.queryByRole('tooltip');

describe('날짜 머리와 열 (C6, C7)', () => {
  it('월요일부터 일곱 열: "요일 월/일", 보조 기술에는 열마다 이름 붙은 묶음이고 오늘 열만 aria-current', () => {
    mount();
    const heads = Array.from(document.querySelectorAll('[data-day-header]'));
    expect(heads.map((h) => h.textContent)).toEqual(['월 10/5', '화 10/6', '수 10/7', '목 10/8', '금 10/9', '토 10/10', '일 10/11']);
    // 눈으로 보는 머리는 같은 말을 되풀이하므로 보조 기술에서는 뺀다.
    expect(heads.every((h) => h.closest('[aria-hidden="true"]') !== null)).toBe(true);
    const groups = screen.getAllByRole('group', { name: /^[월화수목금토일] \d+\/\d+/ });
    expect(groups.map((g) => g.getAttribute('aria-label'))).toEqual(['월 10/5', '화 10/6', '수 10/7', '목 10/8', '금 10/9', '토 10/10', '일 10/11']);
    expect(groups.filter((g) => g.getAttribute('aria-current') === 'date').map((g) => g.getAttribute('aria-label'))).toEqual(['수 10/7']);
  });

  it('좁은 데스크톱과 모바일 모두 열 최소 84px을 유지하고 격자 내부를 가로 스크롤한다', () => {
    mount();
    const grid = screen.getByRole('region', { name: '주간 세션 캘린더' });
    expect(grid.className).toContain('overflow-auto');
    const content = Array.from(grid.children).find((child) => (child as HTMLElement).style.getPropertyValue('--min')) as HTMLElement;
    expect(content?.style.getPropertyValue('--min')).toBe('634px');
    expect(content?.className).toContain('min-w-[var(--min)]');
    expect(content?.className).not.toContain('lg:min-w');
    const heads = Array.from(grid.querySelectorAll<HTMLElement>('[data-day-header]'));
    expect(heads).toHaveLength(7);
    expect(heads.every((head) => head.style.getPropertyValue('--min') === '84px' && head.className.includes('basis-0'))).toBe(true);
    expect(heads[6].querySelector('span')?.title).toBe('일 10/11');
    const sunday = column('2026-10-11');
    expect(sunday.style.getPropertyValue('--min')).toBe('84px');
    expect(sunday.className).toContain('min-w-[var(--min)]');
    expect(sunday.className).not.toContain('lg:min-w');
    expect(sunday.getAttribute('aria-label')).toBe('일 10/11');
  });

  it('동시에 겹친 레인이 많으면 해당 날짜의 머리·본문을 같은 너비로 넓힌다', () => {
    const sessions = Array.from({ length: 16 }, (_, index) => calSession(`dense-${index}`, {
      segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z')],
    }));
    mount({ sessions });
    const monday = column('2026-10-05');
    const mondayHead = document.querySelector<HTMLElement>('[data-day-header="2026-10-05"]');
    const grid = screen.getByRole('region', { name: '주간 세션 캘린더' });
    const content = Array.from(grid.children).find((child) => (child as HTMLElement).style.getPropertyValue('--min')) as HTMLElement;
    expect(monday.style.getPropertyValue('--min')).toBe('140px');
    expect(mondayHead?.style.getPropertyValue('--min')).toBe('140px');
    expect(content.style.getPropertyValue('--min')).toBe('690px');
    expect(column('2026-10-06').style.getPropertyValue('--min')).toBe('84px');
    expect(bands()).toHaveLength(16);
  });

  it('오늘 열은 배경이 칠해지고 머리가 강조된다(굵은 글씨)', () => {
    mount();
    const wed = column('2026-10-07');
    expect(wed.className).toContain('bg-brand-soft');
    expect(column('2026-10-06').className).not.toContain('bg-brand-soft');
    const head = document.querySelector('[data-day-header="2026-10-07"]');
    expect(head?.className).toContain('font-bold');
    expect(head?.hasAttribute('data-today')).toBe(true);
    expect(document.querySelector('[data-day-header="2026-10-06"]')?.hasAttribute('data-today')).toBe(false);
  });

  it('열 높이는 그 날의 길이에 비례한다: 24시간 날은 24 × 배율', () => {
    mount();
    expect(column('2026-10-05').style.getPropertyValue('--h')).toBe('720px');
  });

  it('시간 축에는 정시 눈금이 시 단위로 적힌다(0시는 머리 아래라 적지 않는다)', () => {
    mount();
    const labels = Array.from(document.querySelectorAll('.sticky.left-0 div')).map((d) => d.textContent);
    expect(labels).toContain('1:00');
    expect(labels).toContain('23:00');
    expect(labels).not.toContain('0:00');
  });
});

describe('서머타임 전환일 (C7)', () => {
  it('봄: 일요일 3/8은 23시간이라 머리에 "23시간" 배지와 설명, 열이 한 시간 짧다', () => {
    mount({ timeZone: 'America/New_York', weekStart: { year: 2026, month: 3, day: 2 }, nowMs: Date.parse('2026-03-04T15:00:00Z'), sessions: [] });
    const sunday = document.querySelector('[data-day-header="2026-03-08"]') as HTMLElement;
    const badge = within(sunday).getByText('23시간');
    // 열의 이름에도 길이가 든다.
    expect(column('2026-03-08').getAttribute('aria-label')).toBe('일 3/8, 23시간');
    expect(badge.title).toContain('건너뛰어');
    expect(column('2026-03-08').style.getPropertyValue('--h')).toBe(`${23 * 30}px`);
    expect(column('2026-03-07').style.getPropertyValue('--h')).toBe(`${24 * 30}px`);
  });

  it('가을: 11/2는 25시간이고, 반복된 1시는 UTC 오프셋과 함께 열 안에 두 번 적힌다', () => {
    mount({ timeZone: 'America/New_York', weekStart: { year: 2025, month: 10, day: 27 }, nowMs: Date.parse('2025-10-29T15:00:00Z'), sessions: [] });
    const sunday = document.querySelector('[data-day-header="2025-11-02"]') as HTMLElement;
    expect(within(sunday).getByText('25시간').title).toContain('두 번');
    expect(column('2025-11-02').style.getPropertyValue('--h')).toBe(`${25 * 30}px`);
    const labels = Array.from(column('2025-11-02').querySelectorAll('div')).map((d) => d.textContent);
    expect(labels).toContain('1 UTC-4');
    expect(labels).toContain('1 UTC-5');
  });

  it('보통 날(서울)에는 길이 배지가 없다', () => {
    mount();
    expect(screen.queryByText(/시간$/)).toBeNull();
  });
});

describe('띠 (C8, C13)', () => {
  it('활동 띠마다 단추 하나 + 범위만 있는 세션의 흐린 띠', () => {
    mount();
    expect(bandOf('mon-a')).toHaveLength(1);
    expect(bandOf('thu-gap')).toHaveLength(2);
    // 화요일 22:00부터 수요일 12:00까지(서울): 자정을 넘는 흐린 띠는 두 열에 걸쳐 두 조각이다.
    expect(bandOf('tue-faint')).toHaveLength(2);
    expect(bandOf('tue-faint').every((b) => b.hasAttribute('data-faint'))).toBe(true);
    expect(bandOf('mon-a')[0].hasAttribute('data-faint')).toBe(false);
  });

  it('접근성 이름에 제목, 시간, 요청 수, 비용, 완료 여부가 든다', () => {
    mount();
    expect(bandOf('mon-a')[0].getAttribute('aria-label')).toBe(
      '주문 목록 정리, 10/05 09:00 – 10:00 · 1시간, 세션 전체 요청 3, 세션 전체 비용 $1.2500 OTEL, 선택 구간 입력 3, 완료',
    );
  });

  it('띠 안에서 제목을 읽고 좁은 띠도 전체 제목과 프로젝트를 제공한다', () => {
    mount();
    const band = bandOf('mon-a')[0];
    expect(within(band).getByText('주문 목록 정리')).toBeTruthy();
    expect(band.title).toContain('주문 목록 정리');
    expect(band.title).toContain('프로젝트');
  });

  it('입력선을 누르면 정확한 턴과 입력 시각을 목적지로 전달한다', () => {
    const { onSelect } = mount();
    const input = SESSIONS[0].inputs.items[0];
    fireEvent.click(bandOf('mon-a')[0], { clientY: 5, detail: 1 });
    expect(onSelect).toHaveBeenCalledWith('mon-a', input.turn_key, input.ts);
  });

  it('키보드로 고른 입력에서 Enter를 누르면 그 턴으로 이동한다', async () => {
    const { onSelect } = mount();
    await act(async () => bandOf('mon-a')[0].focus());
    const user = userEvent.setup();
    await user.keyboard('{PageDown}{PageDown}{Enter}');
    const input = SESSIONS[0].inputs.items[1];
    expect(onSelect).toHaveBeenCalledWith('mon-a', input.turn_key, input.ts);
  });

  it('선택된 세션의 모든 조각이 aria-current고 굵은 윤곽선이다', () => {
    mount({ selectedId: 'thu-gap' });
    expect(bandOf('thu-gap').map((b) => b.getAttribute('aria-current'))).toEqual(['true', 'true']);
    expect(bandOf('thu-gap')[0].className).toContain('ring-[3px]');
    expect(bandOf('mon-a')[0].hasAttribute('aria-current')).toBe(false);
    expect(bandOf('mon-a')[0].className).not.toContain('ring-[3px]');
    // 누를 때마다 켜졌다 꺼지는 토글이 아니다.
    expect(bandOf('thu-gap')[0].hasAttribute('aria-pressed')).toBe(false);
  });

  it('띠를 누르면 그 세션을 고른다(뷰어는 부모가 연다)', async () => {
    const { onSelect } = mount();
    await userEvent.setup().click(bandOf('mon-b')[0]);
    expect(onSelect).toHaveBeenCalledWith('mon-b');
  });

  it('키보드로: 포커스를 두고 Enter나 Space를 누르면 고른다', async () => {
    const { onSelect } = mount();
    const user = userEvent.setup();
    await act(async () => bandOf('mon-b')[0].focus());
    await user.keyboard('{Enter}');
    expect(onSelect).toHaveBeenLastCalledWith('mon-b');
    await user.keyboard(' ');
    expect(onSelect).toHaveBeenCalledTimes(2);
  });

  it('겹친 두 띠는 레인을 반씩 나눠 가진다', () => {
    mount();
    const a = bandOf('mon-a')[0];
    const b = bandOf('mon-b')[0];
    expect(a.style.getPropertyValue('--w')).toBe('50%');
    expect(b.style.getPropertyValue('--w')).toBe('50%');
    expect(a.style.getPropertyValue('--l')).toBe('0%');
    expect(b.style.getPropertyValue('--l')).toBe('50%');
  });

  it('범례 강조: 고른 값이 아닌 띠는 흐려진다', () => {
    const assignment = assignColors(SESSIONS, 'screen');
    const key = assignment.bySession.get('mon-a')?.entryKey ?? null;
    mount({ highlightKey: key });
    expect(bandOf('mon-a')[0].className).not.toContain('opacity-20');
    expect(bandOf('mon-b')[0].className).toContain('opacity-20');
  });

  it('같은 세션의 같은 날 띠 사이는 가는 연결선이다 (C8)', () => {
    const { layout } = mount();
    const thu = layout.days[3];
    expect(thu.connectors).toHaveLength(1);
    expect(column('2026-10-08').querySelectorAll('.w-px')).toHaveLength(1);
    // 월요일의 겹친 두 세션은 서로 이어지지 않는다.
    expect(column('2026-10-05').querySelectorAll('.w-px')).toHaveLength(0);
  });
});

describe('현재 시각 선과 발언량 농도 띠 (C11, C12)', () => {
  it('빨간 현재 시각 선은 오늘 열에만 하나', () => {
    mount();
    const lines = document.querySelectorAll('[data-now-line]');
    expect(lines).toHaveLength(1);
    expect(column('2026-10-07').contains(lines[0])).toBe(true);
    expect(lines[0].querySelector('line')?.getAttribute('class')).toContain('stroke-danger');
  });

  it('오늘이 아닌 주에는 현재 시각 선이 없다', () => {
    mount({ weekStart: { year: 2026, month: 9, day: 28 } });
    expect(document.querySelectorAll('[data-now-line]')).toHaveLength(0);
  });

  it('농도 띠: 입력이 있는 10분 칸마다 초록 칸이 열의 왼쪽 가장자리에 하나, 불투명도는 0.15 + 0.85 × n/최댓값', () => {
    const { layout } = mount();
    const mon = layout.days[0];
    const cells = column('2026-10-05').querySelectorAll('rect.fill-success');
    expect(mon.density.length).toBeGreaterThan(0);
    expect(cells).toHaveLength(mon.density.length);
    const opacities = Array.from(cells).map((c) => Number(c.getAttribute('fill-opacity')));
    expect(Math.max(...opacities)).toBeCloseTo(1, 5);
    for (const o of opacities) expect(o).toBeGreaterThanOrEqual(0.15);
    // 폭 6px, 왼쪽 끝.
    expect(cells[0].getAttribute('x')).toBe('0');
    expect(cells[0].getAttribute('width')).toBe('6');
  });

  it('입력이 없는 열에는 농도 칸이 없다', () => {
    mount();
    expect(column('2026-10-09').querySelectorAll('rect.fill-success')).toHaveLength(0);
  });
});

describe('입력 가로선과 말풍선 (C9, C16)', () => {
  it('표준 배율: 띠 안의 입력은 가로선이다(말풍선 없음)', () => {
    const { layout } = mount({ pxPerHour: 30 });
    const band = layout.days[0].bands.find((b) => b.sessionId === 'mon-a');
    expect(band?.stripes).toEqual([5, 10, 15]);
    expect(bandOf('mon-a')[0].querySelectorAll('rect')).toHaveLength(3);
    expect(bandOf('mon-a')[0].querySelectorAll('use')).toHaveLength(0);
  });

  it('임계 배율(96px/h) 이상: 가로선 대신 작은 말풍선', () => {
    mount({ pxPerHour: 120 });
    expect(bandOf('mon-a')[0].querySelectorAll('rect')).toHaveLength(0);
    expect(bandOf('mon-a')[0].querySelectorAll('use')).toHaveLength(3);
    // 말풍선 기호는 한 번 정의해 쓴다.
    expect(document.querySelectorAll('symbol')).toHaveLength(1);
  });

  it('조각이 없는 흐린 띠에는 입력선이 없다', () => {
    mount();
    expect(bandOf('tue-faint')[0].querySelector('svg')).toBeNull();
  });
});

describe('호버 (C14, C16)', () => {
  const hoverAt = (sessionId: string, clientY: number) => {
    const band = bandOf(sessionId)[0];
    fireEvent.pointerEnter(band, { clientX: 20, clientY });
    fireEvent.pointerMove(band, { clientX: 20, clientY });
  };

  it('띠 위(입력선에서 먼 곳): 제목, 시간, 요청 수, 비용', () => {
    mount();
    // mon-b에는 입력이 없다.
    hoverAt('mon-b', 8);
    const card = tooltip();
    expect(card?.textContent).toContain('설정 화면 점검');
    expect(card?.textContent).toContain('10/05 09:30 – 10:30 · 1시간');
    expect(card?.textContent).toContain('세션 전체 요청3');
    expect(card?.textContent).toContain('$1.2500 OTEL');
  });

  it('입력선 위: 그 요청의 첫 줄', () => {
    mount();
    hoverAt('mon-a', 5); // 09:10
    const card = tooltip();
    expect(card?.textContent).toContain('10/05 09:10');
    expect(card?.textContent).toContain('주문 목록 필터를 고쳐줘');
    expect(card?.textContent).toContain('주문 목록 정리');
    expect(card?.textContent).toContain('활동 10/05 09:00 – 10:00');
    expect(card?.textContent).toContain('세션 전체 요청3');
    expect(card?.textContent).toContain('$1.2500 OTEL');
  });

  it('빽빽한 입력선 사이에도 띠의 시간·요청·비용을 입력 카드에서 읽는다', () => {
    const dense = calSession('dense', {
      segments: [calSeg('2026-10-05T00:00:00Z', '2026-10-05T01:00:00Z')],
      inputs: Array.from({ length: 60 }, (_, minute) => calInput(Date.parse('2026-10-05T00:00:00Z') + minute * 60_000)),
      requests: 42,
    });
    mount({ sessions: [dense] });
    hoverAt('dense', 10);
    expect(tooltip()?.textContent).toContain('활동 10/05 09:00 – 10:00');
    expect(tooltip()?.textContent).toContain('세션 전체 요청42');
    expect(tooltip()?.textContent).toContain('$1.2500 OTEL');
  });

  it('첫 줄이 비식별화·보류면 시각만(가려졌다는 이유와 함께)', () => {
    mount();
    hoverAt('mon-a', 10); // 09:20 redacted
    expect(tooltip()?.textContent).toContain('10/05 09:20');
    expect(tooltip()?.textContent).toContain('첫 줄은 비식별화로 가려졌습니다.');
    expect(tooltip()?.textContent).not.toContain('주문 목록 필터를 고쳐줘');
    hoverAt('mon-a', 15); // 09:30 withheld
    expect(tooltip()?.textContent).toContain('첫 줄을 서버에 올리지 않았습니다(보류됨).');
  });

  it('포인터가 벗어나면 카드가 사라진다', () => {
    mount();
    hoverAt('mon-b', 8);
    expect(tooltip()).not.toBeNull();
    fireEvent.pointerLeave(bandOf('mon-b')[0]);
    expect(tooltip()).toBeNull();
  });

  it('키보드 포커스에도 같은 카드가 뜨고 Esc로 닫힌다', async () => {
    mount();
    const user = userEvent.setup();
    await act(async () => bandOf('mon-b')[0].focus());
    expect(tooltip()?.textContent).toContain('설정 화면 점검');
    await user.keyboard('{Escape}');
    expect(tooltip()).toBeNull();
  });

  it('PageDown과 PageUp으로 같은 띠의 각 입력 첫 줄을 읽고, 비식별화된 입력은 이유를 읽는다', async () => {
    mount();
    const band = bandOf('mon-a')[0];
    expect(band.getAttribute('aria-description')).toContain('입력 3건. PageDown과 PageUp');
    await act(async () => band.focus());
    const user = userEvent.setup();
    await user.keyboard('{PageDown}');
    expect(tooltip()?.textContent).toContain('주문 목록 필터를 고쳐줘');
    expect(tooltip()?.textContent).not.toContain('사람 입력');
    expect(screen.getByText(/입력, 주문 목록 필터를 고쳐줘/)).toBeTruthy();
    await user.keyboard('{PageDown}');
    expect(tooltip()?.textContent).toContain('첫 줄은 비식별화로 가려졌습니다.');
    await user.keyboard('{PageDown}');
    expect(tooltip()?.textContent).toContain('첫 줄을 서버에 올리지 않았습니다(보류됨).');
    await user.keyboard('{PageDown}');
    expect(tooltip()?.textContent).toContain('10/05 09:30');
    await user.keyboard('{PageUp}');
    expect(tooltip()?.textContent).toContain('10/05 09:20');
    await user.keyboard('{Escape}');
    expect(tooltip()).toBeNull();
    expect(document.activeElement).toBe(band);
  });

  it('입력이 없는 띠의 PageDown은 시간 스크롤을 막고 다른 띠로 이동하지 않는다', async () => {
    mount();
    const band = bandOf('mon-b')[0];
    await act(async () => band.focus());
    const event = createEvent.keyDown(band, { key: 'PageDown' });
    fireEvent(band, event);
    expect(event.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(band);
    expect(tooltip()?.textContent).toContain('설정 화면 점검');
  });

  it('범위만 있는 흐린 띠: 조각과 입력 시각을 알 수 없다는 안내', () => {
    mount();
    hoverAt('tue-faint', 10);
    expect(tooltip()?.textContent).toContain('범위만 흐리게 그립니다');
  });
});

describe('키보드 이동 (roving tabindex)', () => {
  const stops = (): HTMLButtonElement[] => bands().filter((b) => b.tabIndex === 0);

  it('Tab 정지점은 띠 하나뿐이다: 고른 세션이 없으면 첫 활동 띠', () => {
    mount();
    expect(stops()).toHaveLength(1);
    expect(stops()[0].dataset.sessionId).toBe('mon-a');
    expect(bands().filter((b) => b.tabIndex === -1)).toHaveLength(bands().length - 1);
  });

  it('고른 세션이 있으면 그 세션의 첫 띠가 정지점이다', () => {
    mount({ selectedId: 'thu-gap' });
    expect(stops()).toHaveLength(1);
    expect(stops()[0].dataset.sessionId).toBe('thu-gap');
  });

  it('아래 화살표: 같은 열의 다음 띠(시간 순)로 포커스가 간다', async () => {
    mount();
    const user = userEvent.setup();
    await act(async () => bandOf('mon-a')[0].focus());
    await user.keyboard('{ArrowDown}');
    expect(document.activeElement).toBe(bandOf('mon-b')[0]);
    // 포커스가 간 띠가 새 Tab 정지점이다.
    expect(stops()).toHaveLength(1);
    expect(stops()[0]).toBe(bandOf('mon-b')[0]);
    await user.keyboard('{ArrowUp}');
    expect(document.activeElement).toBe(bandOf('mon-a')[0]);
  });

  it('오른쪽/왼쪽 화살표: 이웃 열의 가까운 띠로, Home/End: 열의 처음/끝', async () => {
    mount();
    const user = userEvent.setup();
    await act(async () => bandOf('mon-a')[0].focus());
    await user.keyboard('{ArrowRight}');
    // 화요일에는 22:00에 시작하는 범위 띠뿐이다.
    expect((document.activeElement as HTMLElement).dataset.sessionId).toBe('tue-faint');
    // 되돌아올 때는 22시에 가장 가까운 월요일 띠(09:30의 mon-b)로 간다.
    await user.keyboard('{ArrowLeft}');
    expect((document.activeElement as HTMLElement).dataset.sessionId).toBe('mon-b');
    await user.keyboard('{Home}');
    expect((document.activeElement as HTMLElement).dataset.sessionId).toBe('mon-a');
    await user.keyboard('{End}');
    expect((document.activeElement as HTMLElement).dataset.sessionId).toBe('mon-b');
  });

  it('끝에서 더 가려 해도 포커스는 그대로이고, 방향키로 격자가 따로 스크롤되지 않게 막는다', async () => {
    mount();
    await act(async () => bandOf('mon-a')[0].focus());
    const event = createEvent.keyDown(bandOf('mon-a')[0], { key: 'ArrowUp' });
    fireEvent(bandOf('mon-a')[0], event);
    expect(document.activeElement).toBe(bandOf('mon-a')[0]);
    expect(event.defaultPrevented).toBe(true);
  });

  it('Ctrl이나 Cmd가 함께면 건드리지 않는다(브라우저 단축키)', async () => {
    mount();
    await act(async () => bandOf('mon-a')[0].focus());
    const event = createEvent.keyDown(bandOf('mon-a')[0], { key: 'ArrowDown', ctrlKey: true });
    fireEvent(bandOf('mon-a')[0], event);
    expect(event.defaultPrevented).toBe(false);
    expect(document.activeElement).toBe(bandOf('mon-a')[0]);
  });

  it('띠가 아닌 곳(격자 자체)의 화살표는 그대로 스크롤에 맡긴다', () => {
    mount();
    const region = screen.getByRole('region', { name: '주간 세션 캘린더' });
    const event = createEvent.keyDown(region, { key: 'ArrowDown' });
    fireEvent(region, event);
    expect(event.defaultPrevented).toBe(false);
  });
});
