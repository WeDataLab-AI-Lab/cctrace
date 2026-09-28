// @vitest-environment jsdom
import { createElement } from 'react';
import type { ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderHook } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { useRefreshWhenRebuilt } from './use-refresh-when-rebuilt';

const setup = (initial: boolean | null | undefined) => {
  const client = new QueryClient();
  const invalidate = vi.spyOn(client, 'invalidateQueries').mockResolvedValue();
  const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children);
  const hook = renderHook(({ pending }: { pending: boolean | null | undefined }) => useRefreshWhenRebuilt(pending), {
    wrapper,
    initialProps: { pending: initial },
  });
  return { invalidate, rerender: (pending: boolean | null | undefined) => hook.rerender({ pending }) };
};

// The usage and cost charts read the aggregate the queued rebuild rewrites. When
// it finishes, they have to refetch without a reload -- once, and only then.
describe('useRefreshWhenRebuilt', () => {
  it('refreshes the other queries once when a pending rebuild finishes', () => {
    const { invalidate, rerender } = setup(undefined);
    rerender(true);
    rerender(true);
    expect(invalidate).not.toHaveBeenCalled();
    rerender(false);
    expect(invalidate).toHaveBeenCalledTimes(1);
    rerender(false);
    expect(invalidate).toHaveBeenCalledTimes(1);
  });

  // Unknown (the server could not read it) is not finished: no refresh, and the
  // pending state survives it.
  it('treats an unknown state as neither finished nor a reset', () => {
    const { invalidate, rerender } = setup(true);
    rerender(null);
    rerender(undefined);
    expect(invalidate).not.toHaveBeenCalled();
    rerender(false);
    expect(invalidate).toHaveBeenCalledTimes(1);
  });

  it('does nothing when no rebuild was pending', () => {
    const { invalidate, rerender } = setup(undefined);
    rerender(false);
    rerender(false);
    expect(invalidate).not.toHaveBeenCalled();
  });
});
