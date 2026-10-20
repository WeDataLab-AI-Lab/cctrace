// @vitest-environment jsdom
import { act, cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { BlockBreakerOverlay } from './block-breaker-overlay';
import { AXIS_WIDTH, HEADER_HEIGHT } from './calendar-grid';
import { collectBricks } from './session-calendar';

beforeAll(() => {
  HTMLCanvasElement.prototype.getContext = (() => null) as unknown as HTMLCanvasElement['getContext'];
});

afterEach(cleanup);

const rect = (left: number, top: number, right: number, bottom: number): DOMRect =>
  ({ left, top, right, bottom, width: right - left, height: bottom - top, x: left, y: top, toJSON: () => ({}) }) as DOMRect;

const band = (parent: HTMLElement, r: DOMRect, opts: { faint?: boolean; color?: string; id?: string } = {}): HTMLElement => {
  const el = document.createElement('button');
  el.setAttribute('data-calendar-band', '');
  if (opts.faint) el.setAttribute('data-faint', '');
  el.dataset.sessionId = opts.id ?? 's';
  el.style.borderLeftColor = opts.color ?? 'rgb(10, 20, 30)';
  el.getBoundingClientRect = () => r;
  parent.appendChild(el);
  return el;
};

describe('벽돌 모으기 (C19)', () => {
  const setup = () => {
    const wrapper = document.createElement('div');
    wrapper.getBoundingClientRect = () => rect(100, 50, 900, 650);
    const scroller = document.createElement('div');
    scroller.getBoundingClientRect = () => rect(100, 50, 900, 650);
    wrapper.appendChild(scroller);
    document.body.appendChild(wrapper);
    return { wrapper, scroller };
  };

  it('판은 날짜 머리 아래 전체이고, 벽돌 좌표는 그 판의 왼쪽 위가 원점이다', () => {
    const { wrapper, scroller } = setup();
    band(scroller, rect(300, 200, 340, 260), { id: 'a' });
    const game = collectBricks(wrapper, scroller);
    expect(game.width).toBe(800);
    expect(game.height).toBe(600 - HEADER_HEIGHT);
    expect(game.bricks).toHaveLength(1);
    expect(game.bricks[0]).toMatchObject({ x: 200, y: 200 - (50 + HEADER_HEIGHT), w: 40, h: 60 });
  });

  it('띠의 테두리 색을 벽돌 색으로 쓴다', () => {
    const { wrapper, scroller } = setup();
    band(scroller, rect(300, 200, 340, 260), { color: 'rgb(200, 30, 40)' });
    expect(collectBricks(wrapper, scroller).bricks[0].token).toBe('rgb(200, 30, 40)');
  });

  it('시간 축 아래로 들어간 부분과 날짜 머리 위의 부분은 자른다', () => {
    const { wrapper, scroller } = setup();
    // 시간 축(왼쪽 46px)에 걸친 띠: 축 오른쪽부터.
    band(scroller, rect(120, 200, 200, 260), { id: 'axis' });
    // 날짜 머리에 걸친 띠: 머리 아래부터.
    band(scroller, rect(300, 60, 340, 120), { id: 'header' });
    const { bricks } = collectBricks(wrapper, scroller);
    const axis = bricks.find((b) => b.id.startsWith('axis'));
    const header = bricks.find((b) => b.id.startsWith('header'));
    expect(axis?.x).toBe(AXIS_WIDTH);
    expect(axis?.w).toBe(200 - (100 + AXIS_WIDTH));
    expect(header?.y).toBe(0);
    expect(header?.h).toBe(120 - (50 + HEADER_HEIGHT));
  });

  it('범위만 그리는 흐린 띠와 보이지 않는 띠는 벽돌이 아니다', () => {
    const { wrapper, scroller } = setup();
    band(scroller, rect(300, 200, 340, 260), { faint: true });
    band(scroller, rect(300, 700, 340, 760), { id: 'below' }); // 보이는 영역 아래
    band(scroller, rect(300, 55, 340, 80), { id: 'above' }); // 날짜 머리 속
    expect(collectBricks(wrapper, scroller).bricks).toHaveLength(0);
  });

  it('벽돌 수에는 상한이 있다', () => {
    const { wrapper, scroller } = setup();
    for (let i = 0; i < 450; i++) band(scroller, rect(300 + (i % 20) * 10, 200 + Math.floor(i / 20) * 5, 305 + (i % 20) * 10, 204 + Math.floor(i / 20) * 5), { id: `b${i}` });
    expect(collectBricks(wrapper, scroller).bricks.length).toBe(400);
  });
});

describe('블록 깨기 오버레이', () => {
  const start = {
    width: 400,
    height: 300,
    bricks: [
      { id: 'a', x: 20, y: 20, w: 60, h: 20, token: 'rgb(1, 2, 3)' },
      { id: 'b', x: 120, y: 20, w: 60, h: 20, token: 'rgb(4, 5, 6)' },
    ],
  };

  it('벽돌 수와 목숨, 조작 안내를 보인다', () => {
    render(<BlockBreakerOverlay start={start} onClose={vi.fn()} />);
    const dialog = screen.getByRole('dialog', { name: '블록 깨기' });
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    expect(dialog.textContent).toContain('깬 띠 0 / 2');
    expect(screen.getByLabelText('목숨 3개')).toBeTruthy();
    expect(dialog.textContent).toContain('클릭하거나 Space를 눌러 공을 쏘세요');
    expect(dialog.textContent).toContain('P 일시정지');
    // 그릴 판은 접근 가능한 이름이 있고 키보드 포커스를 받는다.
    expect(screen.getByLabelText('블록 깨기 화면').getAttribute('tabindex')).toBe('0');
  });

  it('Esc로 닫는다 — 그리기 맥락이 없어도', async () => {
    const onClose = vi.fn();
    render(<BlockBreakerOverlay start={start} onClose={onClose} />);
    await userEvent.setup().keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('닫기 단추로도 닫는다', async () => {
    const onClose = vi.fn();
    render(<BlockBreakerOverlay start={start} onClose={onClose} />);
    await userEvent.setup().click(screen.getByRole('button', { name: '블록 깨기 닫기' }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('P로 일시정지했다가 다시 P로 푼다', async () => {
    render(<BlockBreakerOverlay start={start} onClose={vi.fn()} />);
    const user = userEvent.setup();
    // 안내줄의 "P 일시정지"와 구별하려고 문구 전체가 같은 요소만 찾는다.
    expect(screen.queryByText('일시정지', { exact: true })).toBeNull();
    await user.keyboard('p');
    expect(screen.getByText('일시정지', { exact: true })).toBeTruthy();
    await user.keyboard('p');
    expect(screen.queryByText('일시정지', { exact: true })).toBeNull();
  });

  it('단추의 Enter/Space를 게임 입력으로 삼지 않고 외부 키도 가로채지 않는다', async () => {
    const onClose = vi.fn();
    const outside = document.createElement('input');
    document.body.appendChild(outside);
    render(<BlockBreakerOverlay start={start} onClose={onClose} />);
    await userEvent.setup().keyboard('p');
    expect(screen.getByText('일시정지', { exact: true })).toBeTruthy();
    await userEvent.setup().click(screen.getByRole('button', { name: '블록 깨기 닫기' }));
    expect(onClose).toHaveBeenCalledTimes(1);
    outside.focus();
    await userEvent.setup().keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
    outside.remove();
  });

  it('열릴 때 게임 판에 포커스를 둔다. 닫힌 뒤 복귀는 실행 단추를 아는 부모가 맡는다', () => {
    const { unmount } = render(<BlockBreakerOverlay start={start} onClose={vi.fn()} />);
    expect(document.activeElement).toBe(screen.getByLabelText('블록 깨기 화면'));
    unmount();
  });

  it('다시 하기 뒤에도 게임 판으로 키보드 포커스를 돌린다', async () => {
    const frames: FrameRequestCallback[] = [];
    vi.stubGlobal('requestAnimationFrame', vi.fn((callback: FrameRequestCallback) => {
      frames.push(callback);
      return frames.length;
    }));
    vi.stubGlobal('cancelAnimationFrame', vi.fn());
    const user = userEvent.setup();
    render(<BlockBreakerOverlay start={{ ...start, bricks: [] }} onClose={vi.fn()} />);
    await act(async () => { frames.shift()?.(performance.now() + 16); });
    const retry = screen.getByRole('button', { name: '다시 하기' });
    await user.click(retry);
    expect(document.activeElement).toBe(screen.getByLabelText('블록 깨기 화면'));
    vi.unstubAllGlobals();
  });

  it('닫으면 키 입력 감시도 풀린다(닫은 뒤의 Esc는 다시 알리지 않는다)', async () => {
    const onClose = vi.fn();
    const { unmount } = render(<BlockBreakerOverlay start={start} onClose={onClose} />);
    unmount();
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    expect(onClose).not.toHaveBeenCalled();
  });
});
