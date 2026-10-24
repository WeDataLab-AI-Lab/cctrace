// @vitest-environment jsdom
import { fireEvent, render, screen } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { bucketKey } from '@/lib/trend-axis';

const selected = vi.fn();
const day = bucketKey(new Date(Date.now() - 86400_000), 'day');
vi.mock('@tanstack/react-query', () => ({
  useQuery: ({ queryKey }: { queryKey: string[] }) => {
    if (queryKey[0] === 'timeseries-by-model') return {
      data: [{ date: day, model: 'claude-test', cost_usd: 5, input_tokens: 10, output_tokens: 2, event_count: 1 }],
      isFetching: false, isPending: false,
    };
    return { data: queryKey[0] === 'latest-activity' ? { has_data: true, latest_ts: day } : [], isFetching: false, isPending: false };
  },
}));
vi.mock('./agent-context', () => ({ useAgent: () => ({ selectedAgent: '' }) }));

import { TrendChart } from './trend-chart';

it('offers touch and keyboard users a point selector that drives the existing detail callback', () => {
  render(<TrendChart defaultGranularity="day" onPointSelect={selected} />);
  const picker = screen.getByRole('combobox', { name: '차트 시점 선택' });
  expect(picker.querySelector(`option[value="${day}"]`)).toBeTruthy();
  fireEvent.change(picker, { target: { value: day } });
  expect(selected).toHaveBeenCalledWith(expect.objectContaining({ date: day }));
  expect(screen.getByRole('button', { name: '이전 기간' })).toBeTruthy();
  expect(screen.getByRole('button', { name: '다음 기간' })).toBeTruthy();
});
