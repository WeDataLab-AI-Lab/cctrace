// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { SummaryCards } from './summary-cards';

const note = 'API 사용량 기반 추정치이며, 구독 요금제 사용시 별도 금액이 사전결제됩니다.';
const props = { totalCost: 12.34, totalTokens: 2_500_000, activeUsers: 7, totalEvents: 12345, subtitle: 'Last 72h' };

beforeEach(() => {
  // Radix measures its trigger with ResizeObserver, which jsdom does not implement.
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    unobserve() {}
    disconnect() {}
  });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it.each(['cost', 'token'] as const)('keeps %s metrics visible while moving supplementary copy out of the cards', (viewMode) => {
  const { container } = render(<SummaryCards {...props} viewMode={viewMode} />);
  for (const text of ['Total Cost', '$12.34', 'Total Tokens', '2.5M', 'Active Users', '7', 'Total Events', '12345']) {
    expect(screen.getByText(text)).toBeTruthy();
  }
  expect(screen.getAllByRole('button')).toHaveLength(4);
  expect(screen.queryByRole('tooltip')).toBeNull();
  expect(container.textContent).not.toContain(props.subtitle);
  expect(container.textContent).not.toContain(note);
});

it('lays out four metrics as one, two, then four columns without clipping large amounts', () => {
  const { container } = render(<SummaryCards {...props} totalCost={16484.02} viewMode="cost" />);
  const grid = container.firstElementChild as HTMLElement;
  expect(grid.className).toContain('grid-cols-1');
  expect(grid.className).toContain('@min-[640px]:grid-cols-2');
  expect(grid.className).toContain('@min-[900px]:grid-cols-4');
  expect(screen.getByText('$16,484.02').className).toContain('break-');
});

it('shows each card explanation on keyboard focus and dismisses it with Escape', async () => {
  render(<SummaryCards {...props} viewMode="cost" />);
  const user = userEvent.setup();
  for (const title of ['Total Cost', 'Total Tokens', 'Active Users', 'Total Events']) {
    await user.tab();
    const trigger = screen.getByRole('button', { name: `${title} 설명` });
    expect(document.activeElement).toBe(trigger);
    const tooltip = await screen.findByRole('tooltip');
    expect(tooltip.textContent).toContain(props.subtitle);
    expect(trigger.getAttribute('aria-describedby')).toBe(tooltip.id);
    if (title === 'Total Cost') expect(tooltip.textContent).toContain(note);
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('tooltip')).toBeNull();
  }
});

it('shows the time range and estimate caveat on hover without putting copy back into the card row', async () => {
  const { container } = render(<SummaryCards {...props} viewMode="cost" />);
  const user = userEvent.setup();
  const trigger = screen.getByRole('button', { name: 'Total Cost 설명' });
  await user.hover(trigger);
  const tooltip = await screen.findByRole('tooltip');
  expect(tooltip.textContent).toContain(props.subtitle);
  expect(tooltip.textContent).toContain(note);
  expect(container.contains(tooltip)).toBe(false);
  expect(container.textContent).not.toContain(note);
  await user.unhover(trigger);
  // Leave Radix's hover grace area explicitly; jsdom gives the trigger and portal zero-sized rectangles.
  await user.pointer({ target: document.body, coords: { clientX: window.innerWidth, clientY: window.innerHeight } });
  await waitFor(() => expect(screen.queryByRole('tooltip')).toBeNull());
});
