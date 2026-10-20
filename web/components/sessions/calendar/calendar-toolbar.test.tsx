// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ComponentProps } from 'react';
import { CalendarToolbar } from './calendar-toolbar';

afterEach(cleanup);

type Props = ComponentProps<typeof CalendarToolbar>;

const props = (over: Partial<Props> = {}): Props => ({
  weekLabel: '2026년 10/5 – 10/11',
  sessionCount: 132,
  isCurrentWeek: true,
  onToday: vi.fn(),
  onPrev: vi.fn(),
  onNext: vi.fn(),
  canZoomIn: true,
  canZoomOut: true,
  isDefaultZoom: false,
  onZoomIn: vi.fn(),
  onZoomOut: vi.fn(),
  onZoomReset: vi.fn(),
  ...over,
});

const buttonNames = (): string[] => screen.getAllByRole('button').map((b) => b.getAttribute('aria-label') ?? b.textContent?.trim() ?? '');

describe('캘린더 툴바 (C1~C3)', () => {
  it('레퍼런스의 순서: 이번 주, 이전, 다음, 날짜 범위, N 세션, 축소, 확대, 표준 ... 오른쪽 끝의 다시 읽기', () => {
    render(<CalendarToolbar {...props({ summary: { state: 'stale' }, onRegenerateSummaries: vi.fn(), onOpenGame: vi.fn(), onReload: vi.fn() })} />);
    expect(buttonNames()).toEqual(['이번 주', '이전 주', '다음 주', '축소', '확대', '표준', '개요 다시 만들기', '블록 깨기', '다시 읽기']);
    expect(screen.getByRole('heading', { name: '2026년 10/5 – 10/11' })).toBeTruthy();
    expect(screen.getByTestId('week-session-count').textContent).toBe('132 세션');
  });

  it('세션이 천 개를 넘으면 쉼표로 읽기 좋게', () => {
    render(<CalendarToolbar {...props({ sessionCount: 1234 })} />);
    expect(screen.getByTestId('week-session-count').textContent).toBe('1,234 세션');
  });

  it('주 이동과 배율 단추는 알리기만 한다(제어형)', async () => {
    const p = props();
    render(<CalendarToolbar {...p} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: '이번 주' }));
    await user.click(screen.getByRole('button', { name: '이전 주' }));
    await user.click(screen.getByRole('button', { name: '다음 주' }));
    await user.click(screen.getByRole('button', { name: '축소' }));
    await user.click(screen.getByRole('button', { name: '확대' }));
    await user.click(screen.getByRole('button', { name: '표준' }));
    for (const fn of [p.onToday, p.onPrev, p.onNext, p.onZoomOut, p.onZoomIn, p.onZoomReset]) expect(fn).toHaveBeenCalledTimes(1);
  });

  it('배율 한계와 표준 배율에서는 해당 단추가 비활성이다', () => {
    render(<CalendarToolbar {...props({ canZoomIn: false, canZoomOut: false, isDefaultZoom: true })} />);
    expect((screen.getByRole('button', { name: '확대' }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: '축소' }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: '표준' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('오늘이 든 주면 "이번 주"에 aria-current', () => {
    const { rerender } = render(<CalendarToolbar {...props({ isCurrentWeek: true })} />);
    expect(screen.getByRole('button', { name: '이번 주' }).getAttribute('aria-current')).toBe('date');
    rerender(<CalendarToolbar {...props({ isCurrentWeek: false })} />);
    expect(screen.getByRole('button', { name: '이번 주' }).getAttribute('aria-current')).toBeNull();
  });

  it('다시 읽기: 읽는 중에는 눌러도 되풀이되지 않게 비활성이고 aria-busy', async () => {
    const onReload = vi.fn();
    const { rerender } = render(<CalendarToolbar {...props({ onReload })} />);
    await userEvent.setup().click(screen.getByRole('button', { name: '다시 읽기' }));
    expect(onReload).toHaveBeenCalledTimes(1);
    rerender(<CalendarToolbar {...props({ onReload, reloading: true })} />);
    const busy = screen.getByRole('button', { name: '다시 읽기' }) as HTMLButtonElement;
    expect(busy.disabled).toBe(true);
    expect(busy.getAttribute('aria-busy')).toBe('true');
  });

  it('다시 읽기와 블록 깨기는 핸들러가 없으면 그리지 않는다', () => {
    render(<CalendarToolbar {...props()} />);
    expect(screen.queryByRole('button', { name: '다시 읽기' })).toBeNull();
    expect(screen.queryByRole('button', { name: '블록 깨기' })).toBeNull();
  });
});

describe('개요 생성 상태 (C18)', () => {
  it('상태를 안 주면 그리지 않는다', () => {
    render(<CalendarToolbar {...props()} />);
    expect(screen.queryByTestId('summary-state')).toBeNull();
  });

  it('오래됨: "개요 다시 만들기"를 누를 수 있고 누르면 부모에게 알린다', async () => {
    const onRegenerateSummaries = vi.fn();
    render(<CalendarToolbar {...props({ summary: { state: 'stale' }, onRegenerateSummaries })} />);
    const button = screen.getByRole('button', { name: '개요 다시 만들기' }) as HTMLButtonElement;
    expect(button.disabled).toBe(false);
    await userEvent.setup().click(button);
    expect(onRegenerateSummaries).toHaveBeenCalledTimes(1);
  });

  it('핸들러가 없으면 오래됨이어도 누를 수 없다', () => {
    render(<CalendarToolbar {...props({ summary: { state: 'stale' } })} />);
    expect((screen.getByRole('button', { name: '개요 다시 만들기' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('쓰는 중: 비활성이고 aria-busy, 아직 개수는 말하지 않는다', () => {
    render(<CalendarToolbar {...props({ summary: { state: 'writing' }, onRegenerateSummaries: vi.fn() })} />);
    const button = screen.getByRole('button', { name: /개요를 쓰는 중/ }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(button.getAttribute('aria-busy')).toBe('true');
    expect(screen.getByRole('status').textContent).toBe('');
  });

  it('최신: "N건을 썼습니다"와 비활성 "개요는 최신" 단추', () => {
    render(<CalendarToolbar {...props({ summary: { state: 'fresh', written: 12 }, onRegenerateSummaries: vi.fn() })} />);
    expect(screen.getByRole('status').textContent).toBe('마지막 범위 요청에서 12건을 썼습니다');
    const button = screen.getByRole('button', { name: '개요는 최신' }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(screen.queryByRole('button', { name: '개요 다시 만들기' })).toBeNull();
  });

  it('0건을 썼으면 0건이라고 적는다(모르는 것이 아니라 관측한 0)', () => {
    render(<CalendarToolbar {...props({ summary: { state: 'fresh', written: 0 } })} />);
    expect(screen.getByRole('status').textContent).toBe('마지막 범위 요청에서 0건을 썼습니다');
  });

  it('쓴 개수를 모르면 개수 문구를 만들지 않는다', () => {
    render(<CalendarToolbar {...props({ summary: { state: 'fresh' } })} />);
    expect(screen.getByRole('status').textContent).toBe('');
    expect(screen.getByRole('button', { name: '개요는 최신' })).toBeTruthy();
  });

  it('범위 API의 차단 사유와 예산 대기를 성공이나 0건으로 바꾸지 않는다', () => {
    render(<CalendarToolbar {...props({ summary: { state: 'stale', stale: 2, blocked: { consent_required: 3, redacted: 1 }, waiting_budget: true }, onRegenerateSummaries: vi.fn() })} />);
    const status = screen.getByRole('status');
    expect(status.textContent).toContain('미생성·갱신 대기 2건');
    expect(status.textContent).toContain('동의 필요 3건');
    expect(status.textContent).toContain('비식별화 1건');
    expect(status.textContent).toContain('일일 토큰 한도 대기');
    expect(screen.getByRole('button', { name: '개요 다시 만들기' })).toBeTruthy();
  });

  it('refresh 요청 중에는 서버 상태를 성공으로 바꾸지 않고 버튼만 잠근다', () => {
    render(<CalendarToolbar {...props({ summary: { state: 'stale', written: null }, summaryRefreshing: true, onRegenerateSummaries: vi.fn() })} />);
    const button = screen.getByRole('button', { name: '새로 만들기 요청 중' }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(button.getAttribute('aria-busy')).toBe('true');
    expect(screen.getByRole('status').textContent).toBe('');
  });
});

describe('블록 깨기 단추 (C19)', () => {
  it('작은 아이콘 단추이고 누르면 부모에게 알린다', async () => {
    const onOpenGame = vi.fn();
    render(<CalendarToolbar {...props({ onOpenGame })} />);
    await userEvent.setup().click(screen.getByRole('button', { name: '블록 깨기' }));
    expect(onOpenGame).toHaveBeenCalledTimes(1);
  });

  it('벽돌로 쓸 띠가 없으면(gameDisabled) 누를 수 없다', () => {
    render(<CalendarToolbar {...props({ onOpenGame: vi.fn(), gameDisabled: true })} />);
    expect((screen.getByRole('button', { name: '블록 깨기' }) as HTMLButtonElement).disabled).toBe(true);
  });
});
