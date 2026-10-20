// @vitest-environment jsdom
import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import type { ComponentProps } from 'react';
import { SessionToolbar } from './session-toolbar';
import { statusChipCounts } from './status';

beforeAll(() => {
  // jsdom에 없는 브라우저 기능(넘치는 줄의 가장자리 페이드가 쓴다).
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
});

afterEach(cleanup);

type Props = ComponentProps<typeof SessionToolbar>;

// 완료 99, 하다 만 것 145(실행 중 4 포함), 도중 종료 14, 판정 불가 9 = 267.
const STATUSES = [
  ...Array.from({ length: 99 }, () => 'done' as const),
  ...Array.from({ length: 141 }, () => 'leftover' as const),
  ...Array.from({ length: 4 }, () => 'running' as const),
  ...Array.from({ length: 9 }, () => null),
  ...Array.from({ length: 14 }, () => 'midway' as const),
];

const props = (over: Partial<Props> = {}): Props => ({
  view: 'list',
  onViewChange: vi.fn(),
  total: STATUSES.length,
  statusCounts: statusChipCounts(STATUSES),
  statusFilter: null,
  onStatusFilterChange: vi.fn(),
  scopeLabel: '이 저장소만',
  readAt: '13:12',
  requestlessCount: 33,
  showRequestless: false,
  onShowRequestlessChange: vi.fn(),
  planFileOnly: false,
  onPlanFileOnlyChange: vi.fn(),
  sort: 'start',
  onSortChange: vi.fn(),
  onReload: vi.fn(),
  ...over,
});

describe('세션 툴바 (B1~B8)', () => {
  it('레퍼런스의 한 줄: 전환, 세션 기록 N건, 상태 칩 셋, 범위와 읽은 시각, 두 체크, 정렬, 다시 읽기', () => {
    const { container } = render(<SessionToolbar {...props()} />);
    const text = container.textContent ?? '';
    const order = ['목록', '캘린더', '세션 기록', '267', '완료 99', '하다 만 것 145', '도중 종료 14', '판정 불가 9', '이 저장소만 · 13:12에 읽음', '요청 없는 세션도 보기 (33건)', '플랜 파일 있는 것만', '정렬', '다시 읽기'];
    let at = -1;
    for (const part of order) {
      const next = text.indexOf(part, at + 1);
      expect(next, `"${part}" 이(가) 앞의 것 뒤에 있어야 한다`).toBeGreaterThan(at);
      at = next;
    }
  });

  it('목록/캘린더 전환이 맨 왼쪽이고 고른 쪽이 aria-pressed다', async () => {
    const onViewChange = vi.fn();
    render(<SessionToolbar {...props({ view: 'calendar', onViewChange })} />);
    const group = screen.getByRole('group', { name: '보기 방식' });
    const buttons = within(group).getAllByRole('button');
    expect(buttons.map((b) => b.textContent)).toEqual(['목록', '세션 요약', '캘린더']);
    expect(buttons[2].getAttribute('aria-pressed')).toBe('true');
    expect(buttons[0].getAttribute('aria-pressed')).toBe('false');
    await userEvent.setup().click(buttons[0]);
    expect(onViewChange).toHaveBeenCalledWith('list');
    // 툴바에서 가장 먼저 만나는 단추다.
    expect(screen.getAllByRole('button')[0]).toBe(buttons[0]);
  });

  it('세 칩의 합이 "세션 기록 N건"과 같다', () => {
    render(<SessionToolbar {...props()} />);
    const chips = within(screen.getByRole('group', { name: '완료 여부로 거르기' })).getAllByRole('button');
    const sum = chips.reduce((n, chip) => n + Number(chip.textContent?.match(/(\d[\d,]*)$/)?.[1]?.replace(/,/g, '') ?? 0), 0);
    expect(sum).toBe(267);
    expect(screen.getByText('267').closest('p')?.textContent).toBe('세션 기록 267건');
  });

  it('칩은 필터다: 누르면 그 상태만, 눌려 있는 칩을 다시 누르면 전체', async () => {
    const onStatusFilterChange = vi.fn();
    const { rerender } = render(<SessionToolbar {...props({ onStatusFilterChange })} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: /^완료/ }));
    expect(onStatusFilterChange).toHaveBeenLastCalledWith('done');
    rerender(<SessionToolbar {...props({ onStatusFilterChange, statusFilter: 'midway' })} />);
    expect(screen.getByRole('button', { name: /^도중 종료/ }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByRole('button', { name: /^완료/ }).getAttribute('aria-pressed')).toBe('false');
    await user.click(screen.getByRole('button', { name: /^도중 종료/ }));
    expect(onStatusFilterChange).toHaveBeenLastCalledWith(null);
  });

  it('실행 중은 하다 만 것에, 판정 없음은 별도 칩에 표시한다', () => {
    render(<SessionToolbar {...props()} />);
    expect(screen.getByRole('button', { name: /^하다 만 것/ }).title).toContain('실행 중 4건');
    expect(screen.getByRole('button', { name: /^하다 만 것/ }).title).not.toContain('판정 없음');
    expect(screen.getByRole('button', { name: /^판정 불가/ }).textContent).toContain('9');
  });

  it('남의 세션을 볼 때는 칩이 비활성이고 개수를 보이지 않는다(판정을 받지 못했다)', async () => {
    const onStatusFilterChange = vi.fn();
    render(<SessionToolbar {...props({ statusUnavailable: true, onStatusFilterChange })} />);
    const chips = within(screen.getByRole('group', { name: '완료 여부로 거르기' })).getAllByRole('button') as HTMLButtonElement[];
    expect(chips.every((c) => c.disabled)).toBe(true);
    expect(chips.map((c) => c.textContent?.trim())).toEqual(['완료', '하다 만 것', '도중 종료', '판정 불가']);
    await userEvent.setup().click(chips[0]);
    expect(onStatusFilterChange).not.toHaveBeenCalled();
  });

  it('체크 둘: "요청 없는 세션도 보기 (N건)"과 "플랜 파일 있는 것만"', async () => {
    const onShowRequestlessChange = vi.fn();
    const onPlanFileOnlyChange = vi.fn();
    render(<SessionToolbar {...props({ onShowRequestlessChange, onPlanFileOnlyChange })} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole('checkbox', { name: '요청 없는 세션도 보기 (33건)' }));
    expect(onShowRequestlessChange).toHaveBeenCalledWith(true);
    await user.click(screen.getByRole('checkbox', { name: '플랜 파일 있는 것만' }));
    expect(onPlanFileOnlyChange).toHaveBeenCalledWith(true);
  });

  it('체크 상태는 제어형이다', () => {
    render(<SessionToolbar {...props({ showRequestless: true, planFileOnly: true })} />);
    expect((screen.getByRole('checkbox', { name: /요청 없는 세션도 보기/ }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole('checkbox', { name: '플랜 파일 있는 것만' }) as HTMLInputElement).checked).toBe(true);
  });

  it('정렬: "시작 최신순"이 기본이고 고르면 부모에게 알린다', async () => {
    const onSortChange = vi.fn();
    render(<SessionToolbar {...props({ onSortChange })} />);
    const select = screen.getByRole('combobox', { name: '정렬' }) as HTMLSelectElement;
    expect(select.value).toBe('start');
    expect(within(select).getAllByRole('option').map((o) => o.textContent)).toEqual(['시작 최신순', '마지막 활동순', '비용순', '요청 수순']);
    await userEvent.setup().selectOptions(select, '비용순');
    expect(onSortChange).toHaveBeenCalledWith('cost');
  });

  it('다시 읽기는 맨 오른쪽 단추이고, 읽는 중에는 비활성이다', async () => {
    const onReload = vi.fn();
    const { rerender } = render(<SessionToolbar {...props({ onReload })} />);
    const buttons = screen.getAllByRole('button');
    expect(buttons[buttons.length - 1].textContent).toBe('다시 읽기');
    await userEvent.setup().click(buttons[buttons.length - 1]);
    expect(onReload).toHaveBeenCalledTimes(1);
    rerender(<SessionToolbar {...props({ onReload, reloading: true })} />);
    expect((screen.getByRole('button', { name: '다시 읽기' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('아직 읽지 않았으면 "에 읽음"을 적지 않는다', () => {
    render(<SessionToolbar {...props({ readAt: null })} />);
    expect(screen.getByTestId('scope-read-at').textContent).toBe('이 저장소만');
  });
});
