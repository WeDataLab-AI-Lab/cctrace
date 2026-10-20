// @vitest-environment jsdom
import { act, cleanup, createEvent, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import type { ComponentProps } from 'react';
import { calInput, calSeg, calSession } from '@/app/test/session-analysis/calendar/calendar-builders';
import type { SessionCalendar as SessionCalendarData } from '@/lib/session-calendar-types';
import { SessionCalendar } from './session-calendar';
import type { LocalDate } from './time-zone';
import { wheelZoom } from './zoom';

beforeAll(() => {
  Object.defineProperty(window, 'PointerEvent', { value: window.MouseEvent, configurable: true });
  // jsdom에는 캔버스가 없다. 게임 오버레이는 그리기 맥락이 없으면 그리지 않고 넘어간다.
  HTMLCanvasElement.prototype.getContext = (() => null) as unknown as HTMLCanvasElement['getContext'];
});

afterEach(cleanup);

const SEOUL = 'Asia/Seoul';
const NOW = Date.parse('2026-10-07T06:40:00Z');
const MON: LocalDate = { year: 2026, month: 10, day: 5 };

const DATA: SessionCalendarData = {
  from: '2026-10-04T15:00:00Z',
  to: '2026-10-11T15:00:00Z',
  owner_view: true,
  sessions: [
    // 서울 월요일 14:00~16:00 (UTC 05:00~07:00): 일한 시간대가 오후다.
    calSession('afternoon', {
      title: { text: '오후 작업', source: 'ai', state: 'ok' },
      segments: [calSeg('2026-10-05T05:00:00Z', '2026-10-05T07:00:00Z', { human_inputs: 2 })],
      inputs: [calInput('2026-10-05T05:30:00Z', { text: '오후 작업을 시작하자' })],
      colors: { screens: ['대시보드'], status: 'done' },
    }),
    calSession('second', {
      title: { text: '두 번째', source: 'ai', state: 'ok' },
      segments: [calSeg('2026-10-06T05:00:00Z', '2026-10-06T06:00:00Z')],
      colors: { screens: ['설정'], status: 'leftover' },
    }),
  ],
};

type Props = ComponentProps<typeof SessionCalendar>;

const props = (over: Partial<Props> = {}): Props => ({
  timeZone: SEOUL,
  weekStart: MON,
  nowMs: NOW,
  data: DATA,
  onWeekChange: vi.fn(),
  pxPerHour: 30,
  onPxPerHourChange: vi.fn(),
  colorKey: 'screen',
  onColorKeyChange: vi.fn(),
  selectedId: null,
  onSelect: vi.fn(),
  ...over,
});

const scroller = (): HTMLElement => screen.getByRole('region', { name: '주간 세션 캘린더' });

describe('주 이동 (C1)', () => {
  it('날짜 범위와 세션 수, 좁은 화면의 가로 스크롤 안내가 보인다', () => {
    render(<SessionCalendar {...props()} />);
    expect(screen.getByRole('heading', { name: '2026년 10/5 – 10/11' })).toBeTruthy();
    expect(screen.getByTestId('week-session-count').textContent).toBe('2 세션');
    expect(screen.getByText('좁은 캘린더에서는 격자 안을 가로로 스크롤하여 다른 요일을 볼 수 있습니다.')).toBeTruthy();
  });

  it('이전/다음 주는 월요일을 일곱 날씩 옮기고, 이번 주는 오늘이 든 주로 간다', async () => {
    const onWeekChange = vi.fn();
    render(<SessionCalendar {...props({ onWeekChange, weekStart: { year: 2026, month: 9, day: 28 } })} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: '이전 주' }));
    expect(onWeekChange).toHaveBeenLastCalledWith({ year: 2026, month: 9, day: 21 });
    await user.click(screen.getByRole('button', { name: '다음 주' }));
    expect(onWeekChange).toHaveBeenLastCalledWith({ year: 2026, month: 10, day: 5 });
    await user.click(screen.getByRole('button', { name: '이번 주' }));
    expect(onWeekChange).toHaveBeenLastCalledWith(MON);
  });

  it('월 경계: 10/5에서 이전 주는 9/28', async () => {
    const onWeekChange = vi.fn();
    render(<SessionCalendar {...props({ onWeekChange })} />);
    await userEvent.setup().click(screen.getByRole('button', { name: '이전 주' }));
    expect(onWeekChange).toHaveBeenCalledWith({ year: 2026, month: 9, day: 28 });
  });
});

describe('확대·축소 (C2, C3)', () => {
  it('+ 단추는 ×1.25, − 단추는 ÷1.25', async () => {
    const onPxPerHourChange = vi.fn();
    render(<SessionCalendar {...props({ onPxPerHourChange })} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: '확대' }));
    expect(onPxPerHourChange).toHaveBeenLastCalledWith(37.5);
    await user.click(screen.getByRole('button', { name: '축소' }));
    expect(onPxPerHourChange).toHaveBeenLastCalledWith(24);
  });

  it('표준 배율에서는 "표준"이 비활성, 아니면 30으로 돌아간다', async () => {
    const onPxPerHourChange = vi.fn();
    const { rerender } = render(<SessionCalendar {...props({ onPxPerHourChange })} />);
    expect((screen.getByRole('button', { name: '표준' }) as HTMLButtonElement).disabled).toBe(true);
    rerender(<SessionCalendar {...props({ onPxPerHourChange, pxPerHour: 60 })} />);
    await userEvent.setup().click(screen.getByRole('button', { name: '표준' }));
    expect(onPxPerHourChange).toHaveBeenLastCalledWith(30);
  });

  it('12~240 px/h 한계에서는 더 못 간다', () => {
    const { rerender } = render(<SessionCalendar {...props({ pxPerHour: 240 })} />);
    expect((screen.getByRole('button', { name: '확대' }) as HTMLButtonElement).disabled).toBe(true);
    rerender(<SessionCalendar {...props({ pxPerHour: 12 })} />);
    expect((screen.getByRole('button', { name: '축소' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('Ctrl+휠: 브라우저 확대를 막고 ×1.1 단위로 확대한다', () => {
    const onPxPerHourChange = vi.fn();
    render(<SessionCalendar {...props({ onPxPerHourChange })} />);
    const event = createEvent.wheel(scroller(), { deltaY: -100, ctrlKey: true, clientY: 120 });
    fireEvent(scroller(), event);
    expect(event.defaultPrevented).toBe(true);
    expect(onPxPerHourChange).toHaveBeenCalledTimes(1);
    expect(onPxPerHourChange).toHaveBeenCalledWith(wheelZoom(30, -100));
    expect(onPxPerHourChange.mock.calls[0][0]).toBeCloseTo(33, 5);
  });

  it('Cmd+휠도 같다, 아래로 굴리면 축소', () => {
    const onPxPerHourChange = vi.fn();
    render(<SessionCalendar {...props({ onPxPerHourChange, pxPerHour: 60 })} />);
    const event = createEvent.wheel(scroller(), { deltaY: 100, metaKey: true, clientY: 120 });
    fireEvent(scroller(), event);
    expect(event.defaultPrevented).toBe(true);
    expect(onPxPerHourChange.mock.calls[0][0]).toBeLessThan(60);
  });

  it('연속된 Ctrl+휠은 이전 배율을 누적하고 마지막 배율에 맞춰 이동한다', () => {
    const onPxPerHourChange = vi.fn();
    render(<SessionCalendar {...props({ onPxPerHourChange })} />);
    const first = createEvent.wheel(scroller(), { deltaY: -100, ctrlKey: true, clientY: 120 });
    fireEvent(scroller(), first);
    const second = createEvent.wheel(scroller(), { deltaY: -100, ctrlKey: true, clientY: 120 });
    fireEvent(scroller(), second);
    expect(onPxPerHourChange).toHaveBeenLastCalledWith(wheelZoom(wheelZoom(30, -100), -100));
  });

  it('보통 휠은 확대가 아니라 스크롤이다(막지 않는다)', () => {
    const onPxPerHourChange = vi.fn();
    render(<SessionCalendar {...props({ onPxPerHourChange })} />);
    const event = createEvent.wheel(scroller(), { deltaY: 100, clientY: 120 });
    fireEvent(scroller(), event);
    expect(event.defaultPrevented).toBe(false);
    expect(onPxPerHourChange).not.toHaveBeenCalled();
  });

  it('한계에서 Ctrl+휠은 값을 바꾸지 않는다', () => {
    const onPxPerHourChange = vi.fn();
    render(<SessionCalendar {...props({ onPxPerHourChange, pxPerHour: 240 })} />);
    fireEvent(scroller(), createEvent.wheel(scroller(), { deltaY: -100, ctrlKey: true }));
    expect(onPxPerHourChange).not.toHaveBeenCalled();
  });
});

describe('처음 그릴 때의 스크롤 (C6)', () => {
  it('그 주 데이터의 일한 시간대로 간다: 오후 작업뿐이면 14시 한 시간 앞(13시)', () => {
    render(<SessionCalendar {...props()} />);
    expect(scroller().scrollTop).toBe(13 * 30);
  });

  it('배율을 바꿔도(부모가 새 값을 줘도) 처음 위치로 되돌아가지 않는다', () => {
    const { rerender } = render(<SessionCalendar {...props()} />);
    scroller().scrollTop = 500;
    rerender(<SessionCalendar {...props({ pxPerHour: 60 })} />);
    // 확대 anchor가 없는 외부 변경이라 스크롤을 건드리지 않는다.
    expect(scroller().scrollTop).toBe(500);
  });

  it('세션 상세를 열고 닫거나 같은 주 자료를 갱신해도 확대와 스크롤 위치를 유지한다', () => {
    const current = props({ pxPerHour: 60 });
    const { rerender } = render(<SessionCalendar {...current} />);
    scroller().scrollTop = 720;
    scroller().scrollLeft = 180;
    rerender(<SessionCalendar {...current} selectedId="afternoon" />);
    expect(scroller().scrollTop).toBe(720);
    expect(scroller().scrollLeft).toBe(180);
    rerender(<SessionCalendar {...current} selectedId={null} data={{ ...DATA, sessions: [...DATA.sessions] }} />);
    expect(scroller().scrollTop).toBe(720);
    expect(scroller().scrollLeft).toBe(180);
    expect(screen.getByRole('heading', { name: '2026년 10/5 – 10/11' })).toBeTruthy();
    expect((screen.getByRole('button', { name: '표준' }) as HTMLButtonElement).disabled).toBe(false);
    expect(current.onWeekChange).not.toHaveBeenCalled();
    expect(current.onPxPerHourChange).not.toHaveBeenCalled();
  });

  it('다른 주로 옮기면 그 주의 일한 시간대로 다시 간다', () => {
    const { rerender } = render(<SessionCalendar {...props()} />);
    scroller().scrollTop = 0;
    rerender(<SessionCalendar {...props({ weekStart: { year: 2026, month: 9, day: 28 }, data: { ...DATA, sessions: [] } })} />);
    // 활동이 없는 주는 업무 시간 8시의 한 시간 앞.
    expect(scroller().scrollTop).toBe(7 * 30);
  });
});

describe('읽는 중과 빈 주', () => {
  it('data가 없으면 "읽는 중"을 알린다', () => {
    render(<SessionCalendar {...props({ data: undefined })} />);
    expect(screen.getByRole('status').textContent).toContain('캘린더를 읽는 중입니다');
    expect((screen.getByRole('button', { name: '블록 깨기' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('입력이 잘리거나 판별되지 않은 주는 불완전 자료 경고를 보인다', () => {
    const truncated = calSession('large', {
      segments: [calSeg('2026-10-05T05:00:00Z', '2026-10-05T06:00:00Z')],
      inputsTruncated: true,
    });
    render(<SessionCalendar {...props({ data: { ...DATA, sessions: [truncated] } })} />);
    expect(screen.getByRole('status').textContent).toContain('확인이 필요한 세션 1개');
    expect(screen.getByRole('status').textContent).toContain('입력 시각 상한 1개');
    expect(screen.queryByText('이 주에는 활동한 세션이 없습니다')).toBeNull();
  });

  it('입력 미확인과 활동 구간 없음, 잘못된 응답을 구분해 세고 중복 세션 수를 늘리지 않는다', () => {
    const unknown = calSession('unknown', { facts: 'ready', inputsState: 'undeterminable' });
    const malformed = calSession('malformed', { segments: [calSeg('2026-10-05T05:00:00Z', '2026-10-05T06:00:00Z')] });
    malformed.cost.usd = NaN;
    render(<SessionCalendar {...props({ data: { ...DATA, sessions: [unknown, malformed] } })} />);
    const notice = screen.getByRole('status').textContent;
    expect(notice).toContain('확인이 필요한 세션 2개');
    expect(notice).toContain('입력 시각 미확인 1개');
    expect(notice).toContain('활동 구간 미확인 1개');
    expect(notice).toContain('응답 형식 확인 1개');
    expect(notice).not.toContain('자료가 불완전');
  });

  it('유효하지 않은 시간대는 UTC로 표시하고 시각과 오늘 기준도 함께 바꾼다', async () => {
    const onWeekChange = vi.fn();
    render(<SessionCalendar {...props({ timeZone: 'Mars/Olympus', onWeekChange })} />);
    expect(screen.getByText('시간대 이름을 확인할 수 없어 UTC로 표시합니다.')).toBeTruthy();
    expect(screen.getByRole('group', { name: '월 10/5' })).toBeTruthy();
    await userEvent.setup().click(screen.getByRole('button', { name: '이번 주' }));
    expect(onWeekChange).toHaveBeenCalledWith(MON);
  });

  it('응답 범위가 주를 덮지 않거나 14일을 넘으면 다른 주의 띠를 표시하지 않는다', () => {
    const { rerender } = render(<SessionCalendar {...props({ data: { ...DATA, from: '2026-10-05T00:00:00Z' } })} />);
    expect(screen.getByText('응답 구간이 현재 주와 맞지 않습니다')).toBeTruthy();
    expect(document.querySelectorAll('[data-calendar-band]')).toHaveLength(0);
    rerender(<SessionCalendar {...props({ data: { ...DATA, from: '2026-09-01T00:00:00Z' } })} />);
    expect(screen.getByText('응답 구간이 현재 주와 맞지 않습니다')).toBeTruthy();
    expect(document.querySelectorAll('[data-calendar-band]')).toHaveLength(0);
    rerender(<SessionCalendar {...props({ data: { ...DATA, from: '2026-10-04T14:00:00Z' } })} />);
    expect(screen.getByText('응답 구간이 현재 주와 맞지 않습니다')).toBeTruthy();
    expect(document.querySelectorAll('[data-calendar-band]')).toHaveLength(0);
  });

  it('null 세션 배열은 빈 주(세션 0건)로 오인하지 않는다', () => {
    const data = { ...DATA, sessions: null } as unknown as SessionCalendarData;
    render(<SessionCalendar {...props({ data })} />);
    expect(screen.getByText('세션 목록을 읽을 수 없습니다')).toBeTruthy();
    expect(screen.queryByText('이 주에는 활동한 세션이 없습니다')).toBeNull();
  });

  it('null 세션 행은 전체 0건으로 오인하지 않고 제외 이유를 밝힌다', () => {
    const data = { ...DATA, sessions: [null, DATA.sessions[0]] } as unknown as SessionCalendarData;
    render(<SessionCalendar {...props({ data })} />);
    expect(screen.getByText('읽을 수 없는 세션 1건을 표시하지 못했습니다.')).toBeTruthy();
    expect(screen.getByTestId('week-session-count').textContent).toBe('1 세션');
    expect(document.querySelectorAll('[data-calendar-band]').length).toBeGreaterThan(0);
  });

  it('부모가 현재 시각을 갱신할 때 현재 시각 선도 이동하고 자정에 오늘 열이 바뀐다', () => {
    const before = Date.parse('2026-10-10T14:59:00Z');
    const after = Date.parse('2026-10-10T15:01:00Z');
    const { rerender } = render(<SessionCalendar {...props({ nowMs: before })} />);
    expect(document.querySelector('[data-day="2026-10-10"] [data-now-line]')).toBeTruthy();
    rerender(<SessionCalendar {...props({ nowMs: after })} />);
    expect(document.querySelector('[data-day="2026-10-10"] [data-now-line]')).toBeNull();
    expect(document.querySelector('[data-day="2026-10-11"] [data-now-line]')).toBeTruthy();
  });

  it('세션이 없는 주는 빈 주라고 안내하고 필터를 풀어 보라고 한다', () => {
    render(<SessionCalendar {...props({ data: { ...DATA, sessions: [] } })} />);
    expect(screen.getByRole('status').textContent).toContain('이 주에는 활동한 세션이 없습니다');
    expect(screen.getByRole('status').textContent).toContain('필터를 풀거나');
    expect(screen.getByTestId('week-session-count').textContent).toBe('0 세션');
  });
});

describe('색 기준과 범례', () => {
  it('색상 기준과 범례는 기본으로 접히고 주 이동은 바로 사용할 수 있다', () => {
    render(<SessionCalendar {...props()} />);
    const disclosure = screen.getByText('색상 기준·범례').closest('details');
    expect(disclosure?.open).toBe(false);
    expect(screen.getByRole('list', { name: '범례' }).closest('details')).toBe(disclosure);
    expect(screen.getByRole('button', { name: '이전 주' })).toBeTruthy();
    fireEvent.click(screen.getByText('색상 기준·범례'));
    expect(disclosure?.open).toBe(true);
    expect(screen.getByRole('list', { name: '범례' })).toBeTruthy();
    expect(screen.getByText('막대 색상만 바뀝니다. 표시되는 세션은 그대로입니다.')).toBeTruthy();
  });

  it('범례에 고른 기준의 값과 개수가 보인다', () => {
    render(<SessionCalendar {...props()} />);
    const legend = within(screen.getByRole('list', { name: '범례' }));
    expect(legend.getByRole('button', { name: '대시보드 1세션' })).toBeTruthy();
    expect(legend.getByRole('button', { name: '설정 1세션' })).toBeTruthy();
  });

  it('남의 세션(owner_view=false)에서 "완료 여부"를 골라 둔 채 와도 프로젝트로 대신 보인다', () => {
    render(<SessionCalendar {...props({ colorKey: 'status', data: { ...DATA, owner_view: false } })} />);
    expect(screen.getByText('고른 기준을 쓸 수 없어 프로젝트로 보입니다.')).toBeTruthy();
    expect((screen.getByRole('button', { name: '완료 여부' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('범례 칸에 포커스를 두면 그 값이 아닌 띠가 흐려진다', async () => {
    render(<SessionCalendar {...props()} />);
    const afternoon = document.querySelector('[data-session-id="afternoon"]') as HTMLElement;
    const second = document.querySelector('[data-session-id="second"]') as HTMLElement;
    expect(second.className).not.toContain('opacity-20');
    await act(async () => screen.getByRole('button', { name: '대시보드 1세션' }).focus());
    expect(afternoon.className).not.toContain('opacity-20');
    expect(second.className).toContain('opacity-20');
  });
});

describe('개요 생성 상태 (C18)가 툴바까지 이어진다', () => {
  it('부모가 준 상태를 그대로 보이고, 다시 만들기를 누르면 부모에게 알린다', async () => {
    const onRegenerateSummaries = vi.fn();
    render(<SessionCalendar {...props({ summary: { state: 'stale' }, onRegenerateSummaries })} />);
    await userEvent.setup().click(screen.getByRole('button', { name: '개요 다시 만들기' }));
    expect(onRegenerateSummaries).toHaveBeenCalledTimes(1);
  });
});

describe('블록 깨기 (C19)', () => {
  it('실행 단추의 포커스를 기억해 Esc와 닫기 모두 복귀한다 (캘린더는 그대로)', async () => {
    render(<SessionCalendar {...props()} />);
    const user = userEvent.setup();
    const launcher = screen.getByRole('button', { name: '블록 깨기' });
    launcher.focus();
    await user.keyboard('{Enter}');
    const dialog = screen.getByRole('dialog', { name: '블록 깨기' });
    expect(dialog.textContent).toContain('깬 띠 0 / ');
    expect(dialog.textContent).toContain('목숨');
    expect(document.activeElement).toBe(screen.getByLabelText('블록 깨기 화면'));
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog', { name: '블록 깨기' })).toBeNull();
    expect(document.activeElement).toBe(launcher);
    await user.keyboard('{Enter}');
    await user.click(screen.getByRole('button', { name: '블록 깨기 닫기' }));
    expect(screen.queryByRole('dialog', { name: '블록 깨기' })).toBeNull();
    expect(document.activeElement).toBe(launcher);
    launcher.blur();
    expect(document.activeElement).toBe(document.body);
    await user.click(launcher);
    expect(document.activeElement).toBe(screen.getByLabelText('블록 깨기 화면'));
    await user.keyboard('{Escape}');
    expect(document.activeElement).toBe(launcher);
    expect(document.querySelectorAll('[data-calendar-band]').length).toBeGreaterThan(0);
  });
});

describe('띠 선택', () => {
  it('띠를 누르면 세션 id를 부모에게 알린다', async () => {
    const onSelect = vi.fn();
    render(<SessionCalendar {...props({ onSelect })} />);
    await userEvent.setup().click(document.querySelector('[data-session-id="second"]') as HTMLElement);
    expect(onSelect).toHaveBeenCalledWith('second');
  });
});
