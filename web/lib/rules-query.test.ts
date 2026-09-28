import { afterEach, describe, expect, it, vi } from 'vitest';
import { debounce } from './rules-query';

describe('Rules search debounce', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('settles once with the latest value in a typing burst', () => {
    vi.useFakeTimers();
    const settled = vi.fn();
    const update = debounce(settled, 300);

    update('r');
    vi.advanceTimersByTime(100);
    update('ru');
    vi.advanceTimersByTime(100);
    update('rules');
    vi.advanceTimersByTime(299);

    expect(settled).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(settled).toHaveBeenCalledTimes(1);
    expect(settled).toHaveBeenCalledWith('rules');

    update.cancel();
  });

  it('cancels a pending settlement on cleanup', () => {
    vi.useFakeTimers();
    const settled = vi.fn();
    const update = debounce(settled, 300);

    update('rules');
    update.cancel();
    vi.runAllTimers();

    expect(settled).not.toHaveBeenCalled();
  });
});
