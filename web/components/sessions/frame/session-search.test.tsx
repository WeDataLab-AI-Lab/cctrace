// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SessionRecordSearchResult } from '@/lib/session-analysis-types';

const mocks = vi.hoisted(() => ({ search: vi.fn() }));
vi.mock('@/lib/api', () => ({ fetchSessionRecordSearch: mocks.search }));

import { SessionSearchBox } from './session-search';

const RESULT: SessionRecordSearchResult = {
  session_id: 'session-1',
  title: '배포 절차 수정',
  last_activity_at: '2026-10-08T12:00:00Z',
  matches: [{ field: 'work', text: '검색 결과를 배포 절차에 연결', turn_key: 'turn-9' }],
};

const mount = (scopeKey: string, onSelect = vi.fn()) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  const view = render(createElement(QueryClientProvider, { client }, createElement(SessionSearchBox, { scopeKey, onSelect })));
  return { ...view, client, onSelect };
};

afterEach(() => {
  cleanup();
  mocks.search.mockReset();
});

describe('실제 세션 전역 검색', () => {
  it('서버 검색 결과의 특정 match와 turn_key를 선택 콜백에 보낸다', async () => {
    mocks.search.mockResolvedValue([RESULT]);
    const { onSelect } = mount('user:1:account-a');
    await userEvent.setup().type(screen.getByRole('searchbox', { name: '전체 검색' }), '배포');

    const result = await screen.findByRole('button', { name: /작업 항목 일치: 검색 결과를 배포 절차에 연결/ });
    await userEvent.setup().click(result);

    expect(mocks.search).toHaveBeenCalledWith('배포', 50, expect.any(AbortSignal));
    expect(onSelect).toHaveBeenCalledWith(RESULT, RESULT.matches[0]);
  });

  it('검색어가 바뀌거나 계정 범위가 바뀌면 이전 요청의 늦은 결과를 표시하지 않는다', async () => {
    let finishFirst: ((value: SessionRecordSearchResult[]) => void) | undefined;
    mocks.search
      .mockImplementationOnce(() => new Promise((resolve) => { finishFirst = resolve; }))
      .mockResolvedValueOnce([{ ...RESULT, title: '새 계정의 결과' }]);
    const view = mount('user:1:account-a');
    const input = screen.getByRole('searchbox', { name: '전체 검색' });
    await userEvent.setup().type(input, '배포');
    await waitFor(() => expect(mocks.search).toHaveBeenCalledTimes(1));

    view.rerender(createElement(QueryClientProvider, { client: view.client }, createElement(SessionSearchBox, { scopeKey: 'user:1:account-b', onSelect: view.onSelect })));
    expect(await screen.findByText('새 계정의 결과')).toBeTruthy();
    finishFirst?.([{ ...RESULT, title: '이전 계정의 늦은 결과' }]);

    await waitFor(() => expect(screen.queryByText('이전 계정의 늦은 결과')).toBeNull());
    expect(screen.getByText('새 계정의 결과')).toBeTruthy();
  });

  it('2자 미만 입력을 서버에 보내지 않는다', async () => {
    const { } = mount('user:1:all');
    await userEvent.setup().type(screen.getByRole('searchbox', { name: '전체 검색' }), '가');
    expect(mocks.search).not.toHaveBeenCalled();
    expect(screen.getByText('검색어를 2자 이상 입력하세요.')).toBeTruthy();
  });
});
