// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SourceFilter, type SourceValue } from './source-filter';

afterEach(cleanup);

describe('SourceFilter hidden-session count', () => {
  it('shows the count for the current source scope', () => {
    render(<SourceFilter selected="interactive" onSelect={vi.fn()} hiddenSessionCount={4} hiddenCountLoading={false} />);

    expect(screen.getByRole('status').textContent).toContain('현재 조건에서 숨긴 세션 4건');
  });

  it('shows an indeterminate message instead of zero when the count is unavailable', () => {
    render(<SourceFilter selected="headless" onSelect={vi.fn()} hiddenSessionCount={null} hiddenCountLoading={false} />);

    expect(screen.getByRole('status').textContent).toBe('숨긴 세션 수 판단 불가');
  });

  it('shows zero and all-sessions copy when All is selected', () => {
    render(<SourceFilter selected="all" onSelect={vi.fn()} hiddenSessionCount={0} hiddenCountLoading={false} />);

    expect(screen.getByRole('status').textContent).toBe('모든 세션 표시 중 · 숨김 0건');
    expect(screen.getByRole('button', { name: '전체' })).toBeTruthy();
  });

  it('keeps the Interactive, Headless, and All selections', async () => {
    const onSelect = vi.fn<(value: SourceValue) => void>();
    render(<SourceFilter selected="interactive" onSelect={onSelect} hiddenSessionCount={0} hiddenCountLoading={false} />);

    await userEvent.click(screen.getByRole('button', { name: '자동 실행' }));
    await userEvent.click(screen.getByRole('button', { name: '전체' }));

    expect(onSelect.mock.calls.map(([value]) => value)).toEqual(['headless', 'all']);
  });
});
