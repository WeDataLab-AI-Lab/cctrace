// @vitest-environment jsdom
import { row, mount, waitFor } from './page-test-fixture';
import { within, fireEvent, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { fetchSessionOverview } from '@/lib/api';

describe('sessions navigation and pagination', () => {
it('keeps basic paging and the selected viewer independent of analysis capability', async () => {
    const maintenance = { token_rule_pending: false, step: '', session_analysis: { enabled: true } };
    const firstPage = Array.from({ length: 100 }, (_, index) => row(`head${String(index).padStart(4, '0')}`));
    const tail = row('tail0001');
    vi.mocked(fetchSessionOverview).mockImplementation(async (params) => params?.offset ? [tail] : firstPage);
    mount(maintenance);

    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledTimes(1));
    const listRoot = document.querySelector('[data-session-return-scroll="list"]') as HTMLElement;
    expect(listRoot).not.toBeNull();
    const firstButton = (await within(listRoot).findByText('head0000')).closest('button');
    expect(firstButton).toHaveProperty('tagName', 'BUTTON');
    const scroller = firstButton!.closest('.overflow-y-auto');
    expect(scroller).toBeTruthy();
    fireEvent.scroll(scroller!);
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledWith(expect.objectContaining({ offset: 100 }), expect.any(AbortSignal)));
    const tailButton = (await within(listRoot).findByText('tail0001')).closest('button');
    expect(tailButton).toHaveProperty('tagName', 'BUTTON');
    fireEvent.click(tailButton!);
    expect(screen.getByTestId('viewer-session').textContent).toBe(tail.session_id);
    const viewerInstance = screen.getByTestId('viewer-instance').textContent;

    fireEvent.click(screen.getByRole('button', { name: '세션 목록 다시 읽기' }));
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenCalledTimes(4));
    expect(vi.mocked(fetchSessionOverview).mock.calls.every(([params]) => !params?.analysis)).toBe(true);
    await screen.findByText('head0000');
    expect(within(listRoot).getByText('tail0001').closest('button')).toHaveProperty('tagName', 'BUTTON');
    expect(screen.getByTestId('viewer-session').textContent).toBe(tail.session_id);
    expect(screen.getByTestId('viewer-instance').textContent).toBe(viewerInstance);
  });
});
