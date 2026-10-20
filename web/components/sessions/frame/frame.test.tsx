// @vitest-environment jsdom
import { createElement, useState } from 'react';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import type { FrameAccount, SessionFrame, UsageWindowSeries } from '@/lib/session-analysis-types';
import { FrameStatusBar } from './status-bar';
import { FrameTopBar } from './top-bar';
import { GlobalSearch } from './global-search';
import { UsageMeters } from './usage-meters';
import { UsageMiniChart } from './usage-mini-chart';

beforeAll(() => {
  // jsdom에 없는 브라우저 기능(툴팁이 쓴다).
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  window.matchMedia = (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  });
});

afterEach(cleanup);

const NOW = Date.parse('2026-10-07T06:40:00Z');
const at = (minutes: number): string => new Date(NOW + minutes * 60_000).toISOString();

const account = (over: Partial<FrameAccount>): FrameAccount => ({
  label: '서브',
  provider: 'claude',
  five_hour_pct: 99,
  seven_day_pct: 16,
  resets_at: { five_hour: at(130), seven_day: at(60 * 24 * 3) },
  selected: false,
  ...over,
});

const ACCOUNTS: FrameAccount[] = [
  account({ label: '메인', five_hour_pct: 0, seven_day_pct: 99 }),
  account({ label: '서브', selected: true }),
  account({ label: 'Codex', provider: 'codex', five_hour_pct: 0, seven_day_pct: 88 }),
];

const SERIES: UsageWindowSeries = {
  window: 'five_hour',
  current: [
    { x: 0, y: 0 },
    { x: 0.6, y: 17 },
  ],
  past: [
    [
      { x: 0, y: 5 },
      { x: 1, y: 40 },
    ],
    [
      { x: 0, y: 2 },
      { x: 1, y: 80 },
    ],
  ],
  median: [
    { x: 0, y: 3 },
    { x: 1, y: 50 },
  ],
  band: [
    { x: 0, low: 1, high: 6 },
    { x: 1, low: 30, high: 70 },
  ],
};

const SEVEN: UsageWindowSeries = { ...SERIES, window: 'seven_day' };

const FRAME: SessionFrame = {
  accounts: ACCOUNTS,
  series: { five_hour: SERIES, seven_day: SEVEN },
  repo: { branch: 'master', uncommitted: 5 },
};

const noop = () => undefined;

describe('UsageMeters: 계정 선택 라디오(A2)', () => {
  it('Claude 계정마다 라디오가 있고 고른 것은 checked, Codex에는 없다', () => {
    const { container } = render(createElement(UsageMeters, { accounts: ACCOUNTS, now: NOW, onSelect: noop }));
    const radios = screen.getAllByRole('radio');
    expect(radios.map((r) => r.getAttribute('type'))).toEqual(['radio', 'radio']);
    expect(screen.getByRole('radio', { name: '메인' })).toHaveProperty('checked', false);
    expect(screen.getByRole('radio', { name: '서브' })).toHaveProperty('checked', true);
    expect(screen.queryByRole('radio', { name: 'Codex' })).toBeNull();
    expect(container.querySelector('[data-provider="codex"] input')).toBeNull();
    expect(container.querySelectorAll('[data-provider="claude"] input[type="radio"]')).toHaveLength(2);
  });

  it('라디오 묶음에 이름이 있고, 같은 이름의 라디오라 화살표로 옮겨 다닌다', () => {
    render(createElement(UsageMeters, { accounts: ACCOUNTS, now: NOW, onSelect: noop }));
    expect(screen.getByRole('radiogroup', { name: '다음 세션에 쓸 계정' })).toBeTruthy();
    const names = screen.getAllByRole('radio').map((r) => r.getAttribute('name'));
    expect(new Set(names).size).toBe(1);
    expect(names[0]).toBeTruthy();
  });

  it('고르지 않은 계정을 누르면 그 계정의 label로 onSelect가 불린다', async () => {
    const onSelect = vi.fn<(label: string) => void>();
    render(createElement(UsageMeters, { accounts: ACCOUNTS, now: NOW, onSelect }));
    await userEvent.click(screen.getByRole('radio', { name: '메인' }));
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenCalledWith('메인');
  });

  it('이름 글자를 눌러도 같은 라디오가 눌린다(label로 묶여 있다)', async () => {
    const onSelect = vi.fn<(label: string) => void>();
    render(createElement(UsageMeters, { accounts: ACCOUNTS, now: NOW, onSelect }));
    await userEvent.click(screen.getByText('메인'));
    expect(onSelect).toHaveBeenCalledWith('메인');
  });

  it('실제 프로필 매핑은 승인된 계정만 선택 가능하며 Codex도 지원한다', async () => {
    const onSelect = vi.fn<(label: string) => void>();
    const linked = account({ label: 'Codex', provider: 'codex', selected: false, selectable: true, selectionKey: 'openai:acct-codex' });
    const blocked = account({ label: 'Claude', selectable: false });
    render(createElement(UsageMeters, { accounts: [linked, blocked], now: NOW, onSelect }));
    expect(screen.getAllByRole('radio')).toHaveLength(1);
    expect(screen.getByRole('radio', { name: 'Codex' })).toHaveProperty('checked', false);
    expect(screen.queryByRole('radio', { name: 'Claude' })).toBeNull();
    await userEvent.click(screen.getByRole('radio', { name: 'Codex' }));
    expect(onSelect).toHaveBeenCalledWith('openai:acct-codex');
  });

  it('전역 A1 미터는 5H·7D와 재설정까지 남은 시간을 실제 값으로 표시한다', () => {
    render(createElement(UsageMeters, { accounts: [ACCOUNTS[1]], now: NOW, onSelect: noop, showWindowDetails: true }));
    expect(screen.getByText('5H · 2시간 10분 뒤 재설정')).toBeTruthy();
    expect(screen.getByText('7D · 3일 뒤 재설정')).toBeTruthy();
  });

  it('계정이 없으면 빈 줄이 아니라 안내', () => {
    render(createElement(UsageMeters, { accounts: [], now: NOW, onSelect: noop }));
    expect(screen.getByText('연결된 계정 없음')).toBeTruthy();
    expect(screen.queryByRole('radiogroup')).toBeNull();
  });
});

describe('UsageMeters: 두 줄의 막대(A1)', () => {
  it('각 줄이 키보드로 닿는 버튼이고 접근성 이름에 창과 퍼센트와 재설정이 있다', () => {
    render(createElement(UsageMeters, { accounts: ACCOUNTS, now: NOW, onSelect: noop }));
    expect(screen.getByRole('button', { name: '서브 5시간 창 99% · 2시간 10분 뒤 재설정' })).toBeTruthy();
    expect(screen.getByRole('button', { name: '서브 7일 창 16% · 3일 뒤 재설정' })).toBeTruthy();
    // 계정마다 두 줄이다.
    expect(screen.getAllByRole('button')).toHaveLength(ACCOUNTS.length * 2);
  });

  it('퍼센트 글자: 관측한 0은 0%로 적는다', () => {
    render(createElement(UsageMeters, { accounts: ACCOUNTS, now: NOW, onSelect: noop }));
    const main = screen.getByRole('button', { name: /^메인 5시간 창 0%/ });
    expect(main.textContent).toBe('0%');
  });

  it('높은 사용률은 위험색 글자', () => {
    render(createElement(UsageMeters, { accounts: ACCOUNTS, now: NOW, onSelect: noop }));
    // 99%는 두 줄(서브 5시간, 메인 7일)에 있으니 줄로 좁혀서 본다.
    const high = within(screen.getByRole('button', { name: /^서브 5시간/ })).getByText('99%');
    expect(high.className).toContain('text-danger-strong');
    const low = within(screen.getByRole('button', { name: /^서브 7일/ })).getByText('16%');
    expect(low.className).not.toContain('text-danger-strong');
  });

  it('모르는 사용률은 점선 "?"와 점선 빈 막대이고 "0%"가 아니다', () => {
    const unknown = account({ label: '서브', five_hour_pct: null, seven_day_pct: 42, selected: true });
    const { container } = render(createElement(UsageMeters, { accounts: [unknown], now: NOW, onSelect: noop }));
    const marker = screen.getByRole('img', { name: '사용률 알 수 없음' });
    expect(marker.textContent).toBe('?');
    expect(marker.className).toContain('border-dashed');
    // 위 줄(5시간)의 막대는 채운 막대가 아니라 점선 틀이다.
    const rows = container.querySelectorAll('button');
    expect(rows[0].querySelector('svg')).toBeNull();
    expect(rows[0].querySelector('.border-dashed')).not.toBeNull();
    expect(rows[1].querySelector('svg')).not.toBeNull();
    const text = (container.textContent ?? '').replace(/\s+/g, '');
    expect(text).not.toMatch(/(^|\D)0%/);
    expect(text).toContain('42%');
    expect(screen.getByRole('button', { name: /서브 5시간 창 사용률을 알 수 없음/ })).toBeTruthy();
  });

  it('미측정·재설정 대기·클라이언트 출처를 툴팁에 보존한다', () => {
    const unknown = account({ label: '계정', five_hour_pct: null, five_hour_state: 'unmeasured', five_hour_reason: 'reset_not_yet_observed', source: 'client_reported' });
    render(createElement(UsageMeters, { accounts: [unknown], now: NOW, onSelect: noop }));
    expect(screen.getByRole('button', { name: /계정 5시간 창 사용률을 알 수 없음.*재설정 후 새 측정 대기.*클라이언트 보고/ })).toBeTruthy();
    expect(within(screen.getByRole('button', { name: /5시간 창/ })).getByText('?')).toBeTruthy();
  });

  it('막대 길이는 SVG 속성이다(인라인 style 없음), 0은 채움이 없고 1%도 보이는 최소 폭', () => {
    const accounts = [account({ label: '서브', five_hour_pct: 0, seven_day_pct: 1, selected: true })];
    const { container } = render(createElement(UsageMeters, { accounts, now: NOW, onSelect: noop }));
    expect(container.querySelector('[style]')).toBeNull();
    const [zero, one] = Array.from(container.querySelectorAll('svg'));
    expect(zero.querySelectorAll('rect')).toHaveLength(1);
    const fills = one.querySelectorAll('rect');
    expect(fills).toHaveLength(2);
    expect(Number.parseFloat(fills[1].getAttribute('width') ?? '0')).toBeGreaterThanOrEqual(2);
  });

  it('계정 식별 색: Claude는 서로 다른 색, Codex는 자기 색', () => {
    const { container } = render(createElement(UsageMeters, { accounts: ACCOUNTS, now: NOW, onSelect: noop }));
    const squares = Array.from(container.querySelectorAll('[data-provider] > :first-child span[aria-hidden="true"], [data-provider] label span[aria-hidden="true"]'));
    const colors = squares.map((s) => Array.from(s.classList).find((c) => c.startsWith('bg-')));
    expect(colors).toEqual(['bg-cat-1', 'bg-cat-4', 'bg-agent-codex']);
  });
});

describe('UsageMiniChart(A3)', () => {
  it('접근성 이름은 창, 지금 값, 지난 창 수를 말하고 <title>도 같다', () => {
    const { container } = render(createElement(UsageMiniChart, { label: '5H', series: SERIES, accountLabel: '서브' }));
    const chart = screen.getByRole('img', { name: /서브 5시간 창 사용률/ });
    expect(chart.getAttribute('aria-label')).toContain('지금 창은 17%입니다');
    expect(chart.getAttribute('aria-label')).toContain('지난 창 2개');
    expect(container.querySelector('title')?.textContent).toBe(chart.getAttribute('aria-label'));
    expect(screen.getByText('5H')).toBeTruthy();
  });

  it('범주마다 선이 있다: 지난 창 둘, 중앙값(점선), 범위 면, 지금 창(굵은 선)', () => {
    const { container } = render(createElement(UsageMiniChart, { label: '7D', series: SEVEN, accountLabel: '서브' }));
    const paths = Array.from(container.querySelectorAll('path'));
    const has = (p: Element, token: string): boolean => p.classList.contains(token);
    expect(paths.filter((p) => has(p, 'stroke-ink-4'))).toHaveLength(2);
    const median = paths.find((p) => has(p, 'stroke-ink-2'));
    expect(median?.getAttribute('stroke-dasharray')).toBeTruthy();
    expect(paths.find((p) => has(p, 'fill-ink-4'))?.getAttribute('d')).toMatch(/Z$/);
    const current = paths.find((p) => has(p, 'stroke-ink'));
    expect(Number(current?.getAttribute('stroke-width'))).toBeGreaterThan(1);
    expect(container.querySelector('[style]')).toBeNull();
  });

  it('클릭해 확장하고 범례·표·키보드 이동·닫기와 원래 버튼 초점을 제공한다', async () => {
    render(createElement(UsageMiniChart, { label: '5H', series: SERIES, accountLabel: '서브' }));
    const trigger = screen.getByRole('button', { name: /서브 5시간 창 사용률.*자세히 보기/ });
    await userEvent.click(trigger);
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('서브 5시간 창 사용률')).toBeTruthy();
    expect(within(dialog).getByLabelText('차트 범례').textContent).toContain('지난 창 2개');
    const plot = within(dialog).getByRole('img', { name: /화살표 키로 관측 위치 이동/ });
    plot.focus();
    await userEvent.keyboard('{ArrowRight}');
    expect(within(dialog).getByRole('status').textContent).toContain('지금 창17%');
    expect(within(dialog).getByRole('status').textContent).toContain('지난 창 1');
    await userEvent.click(within(dialog).getByRole('button', { name: '표로 보기' }));
    expect(within(dialog).getByRole('table').querySelectorAll('tbody tr')).toHaveLength(3);
    expect(within(dialog).getByRole('columnheader', { name: '지난 2' })).toBeTruthy();
    await userEvent.click(within(dialog).getByRole('button', { name: '닫기' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('과거 창 표본 수를 관측 위치 readout에 표시한다', async () => {
    const series: UsageWindowSeries = { ...SERIES, sampleCounts: [{ x: 0.6, n: 4 }], historyCount: 4, source: 'client_reported' };
    render(createElement(UsageMiniChart, { label: '5H', series, accountLabel: '서브' }));
    await userEvent.click(screen.getByRole('button', { name: /자세히 보기/ }));
    const plot = screen.getByRole('img', { name: /화살표 키로 관측 위치 이동/ });
    plot.focus();
    await userEvent.keyboard('{ArrowRight}');
    expect(screen.getByText('지난 창 표본 수')).toBeTruthy();
    expect(screen.getByText('4')).toBeTruthy();
  });

  it('표본 구간의 빈 값을 ARIA·포인터·키보드·표에 판단 불가로 보여주고 Esc로 닫는다', async () => {
    const series: UsageWindowSeries = {
      ...SERIES,
      median: [{ x: 0, y: 10 }, { x: 1, y: 90 }],
      band: [{ x: 0, low: 5, high: 15 }, { x: 1, low: 85, high: 95 }],
      sampleCounts: [{ x: 0, n: 4 }, { x: 0.5, n: 2 }, { x: 1, n: 4 }],
    };
    render(createElement(UsageMiniChart, { label: '5H', series, accountLabel: '서브' }));
    const trigger = screen.getByRole('button', { name: /서브 5시간 창 사용률.*자세히 보기/ });
    expect(screen.getByRole('img', { name: /서브 5시간 창 사용률/ }).getAttribute('aria-label')).toMatch(/25~75%.*판단 불가/);
    await userEvent.click(trigger);
    const dialog = screen.getByRole('dialog');
    const plot = within(dialog).getByRole('img', { name: /화살표 키로 관측 위치 이동/ });
    plot.focus();
    await userEvent.keyboard('{ArrowRight}');
    expect(within(dialog).getByRole('status').textContent).toContain('판단 불가 (n=2)');
    Object.defineProperty(plot, 'getBoundingClientRect', { value: () => ({
      left: 0, top: 0, right: 640, bottom: 240, width: 640, height: 240, x: 0, y: 0, toJSON: () => ({}),
    }) });
    fireEvent.pointerMove(plot, { clientX: 320, clientY: 120 });
    expect(within(dialog).getByRole('status').textContent).toContain('판단 불가 (n=2)');
    await userEvent.click(within(dialog).getByRole('button', { name: '표로 보기' }));
    expect(within(dialog).getAllByRole('row').some((row) => row.textContent?.includes('판단 불가 (n=2)'))).toBe(true);
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('원시 지난 창 배열이 비어도 서버 이력이 있으면 범위 범례를 숨기지 않는다', async () => {
    const summaryOnly: UsageWindowSeries = {
      ...SERIES, past: [], historyCount: 2, median: [{ x: 0.5, y: 30 }], band: [], sampleCounts: [{ x: 0.5, n: 2 }],
    };
    render(createElement(UsageMiniChart, { label: '5H', series: summaryOnly, accountLabel: '서브' }));
    expect(screen.getByRole('img', { name: /서브 5시간 창 사용률/ }).getAttribute('aria-label')).not.toContain('비교할 지난 창이 없습니다');
    await userEvent.click(screen.getByRole('button', { name: /자세히 보기/ }));
    expect(screen.getByLabelText('차트 범례').textContent).toContain('25~75% 범위 (표본 4개 이상)');
  });

  it('빈 차트도 확장하면 오해를 주는 0 대신 데이터 없음이라 표시한다', async () => {
    const empty: UsageWindowSeries = { window: 'five_hour', current: [], past: [], median: [], band: [] };
    render(createElement(UsageMiniChart, { label: '5H', series: empty, accountLabel: '서브' }));
    await userEvent.click(screen.getByRole('button', { name: /자세히 보기/ }));
    expect(within(screen.getByRole('dialog')).getByText('표시할 관측값이 없습니다.')).toBeTruthy();
  });

  it('값이 하나도 없어도 깨지지 않고 틀과 바닥선만 그린다', () => {
    const empty: UsageWindowSeries = { window: 'five_hour', current: [], past: [], median: [], band: [] };
    const { container } = render(createElement(UsageMiniChart, { label: '5H', series: empty, accountLabel: '서브' }));
    expect(container.querySelectorAll('path')).toHaveLength(0);
    expect(container.querySelectorAll('line')).toHaveLength(1);
    expect(screen.getByRole('img').getAttribute('aria-label')).toContain('지금 창의 값이 없습니다');
  });
});

const SearchHarness = ({ resultCount, initial = '' }: { resultCount?: number | null; initial?: string }) => {
  const [value, setValue] = useState(initial);
  return createElement(GlobalSearch, { value, onChange: setValue, resultCount });
};

describe('GlobalSearch(A4)', () => {
  it('"검색" 자리표시와 이름이 있는 검색 상자', () => {
    render(createElement(SearchHarness));
    const input = screen.getByRole('searchbox', { name: '전체 검색' });
    expect(input.getAttribute('placeholder')).toBe('검색');
  });

  it('입력하면 onChange로 값이 올라간다', async () => {
    const onChange = vi.fn<(v: string) => void>();
    render(createElement(GlobalSearch, { value: '', onChange }));
    await userEvent.type(screen.getByRole('searchbox'), 'ab');
    expect(onChange.mock.calls.map(([v]) => v)).toEqual(['a', 'b']);
  });

  it('Esc는 지운다. 비어 있을 때는 아무것도 하지 않는다', async () => {
    const onChange = vi.fn<(v: string) => void>();
    const { rerender } = render(createElement(GlobalSearch, { value: '정산', onChange }));
    screen.getByRole('searchbox').focus();
    await userEvent.keyboard('{Escape}');
    expect(onChange).toHaveBeenCalledWith('');
    onChange.mockClear();
    rerender(createElement(GlobalSearch, { value: '', onChange }));
    await userEvent.keyboard('{Escape}');
    expect(onChange).not.toHaveBeenCalled();
  });

  it('상태를 가진 부모 안에서 입력, Esc, 지우기 버튼이 값을 바꾼다', async () => {
    render(createElement(SearchHarness));
    const input = screen.getByRole('searchbox');
    await userEvent.type(input, '정산');
    expect(input).toHaveProperty('value', '정산');
    await userEvent.keyboard('{Escape}');
    expect(input).toHaveProperty('value', '');
    await userEvent.type(input, '커밋');
    await userEvent.click(screen.getByRole('button', { name: '검색어 지우기' }));
    expect(input).toHaveProperty('value', '');
    expect(screen.queryByRole('button', { name: '검색어 지우기' })).toBeNull();
  });

  it('건수: 검색어가 있고 숫자가 올 때만 적는다(0건도 적는다)', async () => {
    const { rerender } = render(createElement(GlobalSearch, { value: '', onChange: noop, resultCount: 3 }));
    expect(screen.queryByText('3건')).toBeNull();
    rerender(createElement(GlobalSearch, { value: '정산', onChange: noop, resultCount: 3 }));
    expect(screen.getByText('3건')).toBeTruthy();
    rerender(createElement(GlobalSearch, { value: '정산', onChange: noop, resultCount: 0 }));
    expect(screen.getByText('0건')).toBeTruthy();
    rerender(createElement(GlobalSearch, { value: '정산', onChange: noop, resultCount: null }));
    expect(screen.queryByText(/건$/)).toBeNull();
    rerender(createElement(GlobalSearch, { value: '정산', onChange: noop }));
    expect(screen.queryByText(/건$/)).toBeNull();
    rerender(createElement(GlobalSearch, { value: '   ', onChange: noop, resultCount: 5 }));
    expect(screen.queryByText('5건')).toBeNull();
  });
});

describe('FrameStatusBar(A6)', () => {
  const status = (uncommitted: number | null) => {
    render(createElement(FrameStatusBar, { repo: { branch: 'master', uncommitted } }));
    return screen.getByRole('status', { name: '저장소 상태' });
  };

  it('미커밋 5건: 앰버 점', () => {
    const bar = status(5);
    expect(within(bar).getByText('master')).toBeTruthy();
    expect(within(bar).getByText('미커밋 5건')).toBeTruthy();
    expect(bar.querySelector('.bg-warning')).not.toBeNull();
  });

  it('관측한 0: "미커밋 없음", 초록 점', () => {
    const bar = status(0);
    expect(within(bar).getByText('미커밋 없음')).toBeTruthy();
    expect(bar.querySelector('.bg-success')).not.toBeNull();
    expect(bar.querySelector('.bg-warning')).toBeNull();
  });

  it('모름: "미커밋 알 수 없음", 점선 고리이고 초록이 아니다', () => {
    const bar = status(null);
    expect(within(bar).getByText('미커밋 알 수 없음')).toBeTruthy();
    expect(bar.querySelector('.border-dashed')).not.toBeNull();
    expect(bar.querySelector('.bg-success')).toBeNull();
    expect(bar.textContent).not.toContain('미커밋 없음');
  });

  it('오른쪽에 붙고 높이가 22px이다', () => {
    const bar = status(5);
    expect(bar.className).toContain('h-[22px]');
    expect(bar.firstElementChild?.className).toContain('ml-auto');
  });
});

describe('FrameTopBar', () => {
  const renderBar = (frame: SessionFrame = FRAME) => {
    const onSelectAccount = vi.fn<(label: string) => void>();
    const onQueryChange = vi.fn<(q: string) => void>();
    render(
      createElement(FrameTopBar, {
        frame,
        now: NOW,
        onSelectAccount,
        query: '',
        onQueryChange,
        leading: createElement('span', null, '리딩'),
        trailing: createElement('span', null, '트레일링'),
      }),
    );
    return { onSelectAccount, onQueryChange };
  };

  it('왼쪽 칸, 계정 막대, 5H·7D 차트, 검색, 오른쪽 칸이 한 줄에 있다', () => {
    renderBar();
    const group = screen.getByRole('group', { name: '전역 프레임' });
    expect(within(group).getByText('리딩')).toBeTruthy();
    expect(within(group).getByRole('radiogroup')).toBeTruthy();
    expect(within(group).getByRole('img', { name: /5시간 창 사용률/ })).toBeTruthy();
    expect(within(group).getByRole('img', { name: /7일 창 사용률/ })).toBeTruthy();
    expect(within(group).getByRole('searchbox', { name: '전체 검색' })).toBeTruthy();
    expect(within(group).getByText('트레일링')).toBeTruthy();
    expect(group.className).toContain('overflow-x-auto');
    expect(group.className).toContain('border-b');
  });

  it('차트는 선택한 계정의 것이다', () => {
    renderBar();
    expect(screen.getByRole('img', { name: /^서브 5시간 창 사용률/ })).toBeTruthy();
    cleanup();
    renderBar({ ...FRAME, accounts: FRAME.accounts.map((a) => ({ ...a, selected: false })) });
    expect(screen.getByRole('img', { name: /^메인 5시간 창 사용률/ })).toBeTruthy();
  });

  it('계정을 고르면 label이, 검색어를 치면 값이 올라간다', async () => {
    const { onSelectAccount, onQueryChange } = renderBar();
    await userEvent.click(screen.getByRole('radio', { name: '메인' }));
    expect(onSelectAccount).toHaveBeenCalledWith('메인');
    await userEvent.type(screen.getByRole('searchbox'), '가');
    expect(onQueryChange).toHaveBeenCalledWith('가');
  });
});
