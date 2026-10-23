// @vitest-environment jsdom
import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { COUNT_UP_MS, useCountUp } from './use-count-up';

// Frames run only when a test steps them, at a clock the test moves, so the
// animation is checked frame by frame without waiting on real time.
let now = 0;
let frames: FrameRequestCallback[] = [];
const step = (ms: number) => {
  now += ms;
  const due = frames.splice(0);
  act(() => { for (const cb of due) cb(now); });
};

const setup = (target: number, ready: boolean) =>
  renderHook(({ t, r }: { t: number; r: boolean }) => useCountUp(t, r), { initialProps: { t: target, r: ready } });

beforeEach(() => {
  now = 0;
  frames = [];
  vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => frames.push(cb));
  vi.stubGlobal('cancelAnimationFrame', () => {});
  vi.spyOn(performance, 'now').mockImplementation(() => now);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('useCountUp', () => {
  // The cards used to read 0 while loading; on arrival they now climb from 0 and
  // land on the exact value, not a rounded neighbour of it.
  it('climbs from 0 to the exact value when loading finishes', () => {
    const hook = setup(0, false);
    hook.rerender({ t: 1000, r: true });
    expect(hook.result.current).toBe(0);
    step(COUNT_UP_MS / 2);
    expect(hook.result.current).toBeGreaterThan(0);
    expect(hook.result.current).toBeLessThan(1000);
    step(COUNT_UP_MS);
    expect(hook.result.current).toBe(1000);
    expect(frames).toHaveLength(0);
  });

  // A page opened from cache has nothing to wait for, so nothing is animated.
  it('shows the value at once when it was ready from the start', () => {
    const hook = setup(1000, true);
    expect(hook.result.current).toBe(1000);
    expect(frames).toHaveLength(0);
  });

  it('skips the animation when the viewer asked for reduced motion', () => {
    vi.stubGlobal('matchMedia', (query: string) => ({ matches: query === '(prefers-reduced-motion: reduce)' }));
    const hook = setup(0, false);
    hook.rerender({ t: 1000, r: true });
    expect(hook.result.current).toBe(1000);
    expect(frames).toHaveLength(0);
  });

  // A five-minute refresh moves the number from where it was, not from 0.
  it('moves from the shown value when a ready value changes', () => {
    const hook = setup(100, true);
    hook.rerender({ t: 200, r: true });
    step(1);
    expect(hook.result.current).toBeGreaterThanOrEqual(100);
    expect(hook.result.current).toBeLessThan(200);
    step(COUNT_UP_MS);
    expect(hook.result.current).toBe(200);
  });

  // Switching account loads again; the new figure climbs from 0 instead of
  // starting at the previous account's number.
  it('starts from 0 again after a fresh load', () => {
    const hook = setup(500, true);
    hook.rerender({ t: 500, r: false });
    hook.rerender({ t: 800, r: true });
    expect(hook.result.current).toBe(0);
    step(COUNT_UP_MS);
    expect(hook.result.current).toBe(800);
  });
});
