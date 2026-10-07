// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ModelStat, SessionOverview } from '@/lib/types';

const { fetchCostByModel, fetchSessionOverview } = vi.hoisted(() => ({
  fetchCostByModel: vi.fn(),
  fetchSessionOverview: vi.fn(),
}));

vi.mock('@/lib/api', () => ({ fetchCostByModel, fetchSessionOverview }));
vi.mock('@/components/common/trend-chart', () => ({
  TrendChart: ({ onTimeRangeChange }: { onTimeRangeChange: (since: string, until: string | undefined) => void }) =>
    createElement('button', { onClick: () => onTimeRangeChange('2026-01-01T00:00:00Z', '2026-02-01T00:00:00Z') }, '기간 변경'),
}));

import { UserDetail } from './user-detail';

const session = { session_id: 'sess-abcdef-123', model: 'claude-opus', input_tokens: 1, output_tokens: 2 } as SessionOverview;

const modelStat = {
  model: 'claude-opus', agent: 'claude', billing_provider: 'anthropic', total_cost: 1, input_tokens: 1, output_tokens: 2, request_count: 1,
} as ModelStat;

let client: QueryClient;

const renderDetail = (canViewSessions: boolean, sessionsAcrossProfiles?: boolean) => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    createElement(
      QueryClientProvider,
      { client },
      createElement(UserDetail, {
        email: 'a@example.com',
        avatarColor: 'red',
        displayName: 'a',
        viewMode: 'cost',
        canViewSessions,
        sessionsAcrossProfiles,
      }),
    ),
  );
};

beforeEach(() => {
  fetchCostByModel.mockResolvedValue([]);
  fetchSessionOverview.mockResolvedValue([session]);
});
afterEach(() => {
  cleanup();
  client?.clear();
  vi.clearAllMocks();
});

describe('UserDetail 세션 쿼리 (#799)', () => {
  it('canViewSessions=false 면 세션을 요청하지 않는다', async () => {
    renderDetail(false);
    // 같은 렌더에서 시작된 다른 쿼리가 요청된 뒤에 확인해야 "아직 안 보냄"이 아니라 "안 보냄"이 된다.
    await waitFor(() => expect(fetchCostByModel).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByText('다른 사용자의 세션은 볼 수 없습니다')).toBeTruthy());

    expect(fetchSessionOverview).not.toHaveBeenCalled();
    expect(screen.queryByText('sess-abcdef')).toBeNull();
  });

  it('canViewSessions=true 면 세션을 요청하고 표시한다', async () => {
    renderDetail(true);

    expect(await screen.findByText('sess-abcdef')).toBeTruthy();
    expect(fetchSessionOverview).toHaveBeenCalledWith(expect.objectContaining({ profileEmail: 'a@example.com' }));
  });

  // 다른 쿼리(모델별 비용)가 데이터를 받은 뒤에도 세션 쿼리는 canViewSessions 에만 묶여야 한다.
  it('canViewSessions=false 면 다른 쿼리 데이터가 도착해도 세션을 요청하지 않는다', async () => {
    fetchCostByModel.mockResolvedValue([modelStat]);
    renderDetail(false);
    expect((await screen.findAllByText('opus')).length).toBeGreaterThan(0);

    expect(fetchSessionOverview).not.toHaveBeenCalled();
  });

  // 기간 버튼이 until 도 넘기므로, enabled 에 until 이 섞이는 변형(canViewSessions || trendUntil !== undefined)은 이 테스트가 잡는다.
  it('canViewSessions=false 면 기간을 바꿔도 세션을 요청하지 않는다', async () => {
    fetchCostByModel.mockResolvedValue([modelStat]);
    renderDetail(false);
    await screen.findAllByText('opus');

    fireEvent.click(screen.getByText('기간 변경'));
    await waitFor(() => expect(fetchCostByModel).toHaveBeenLastCalledWith('2026-01-01T00:00:00Z', '2026-02-01T00:00:00Z', 'a@example.com', undefined, undefined));

    expect(fetchSessionOverview).not.toHaveBeenCalled();
  });

  // enabled 가 아니라 요청 인자를 본다: until 을 세션 요청에 넘기지 않는 변형을 잡는다.
  it('canViewSessions=true 면 기간 until 이 세션 요청에 전달된다', async () => {
    renderDetail(true);
    await screen.findByText('sess-abcdef');

    fireEvent.click(screen.getByText('기간 변경'));
    await waitFor(() => expect(fetchSessionOverview).toHaveBeenLastCalledWith(expect.objectContaining({ since: '2026-01-01T00:00:00Z', until: '2026-02-01T00:00:00Z' })));
  });

  it('canViewSessions=false 면 시간이 지나도 세션을 요청하지 않는다', async () => {
    // React Query 폴링 간격은 60초라 5초 전진은 폴링에 끼어들지 않는다. setInterval 로 늦게 켜는 변형만 잡는다.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] });
    try {
      fetchCostByModel.mockResolvedValue([modelStat]);
      renderDetail(false);
      await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
      expect(screen.getAllByText('opus').length).toBeGreaterThan(0);

      expect(fetchSessionOverview).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('UserDetail 세션 귀속 표기 (#809)', () => {
  const NOTE = '서버가 정한 본인 범위의 세션을 표시합니다. 이 카드의 프로필·계정 필터는 적용되지 않습니다.';

  it('sessionsAcrossProfiles=true 면 카드 단위가 아님을 안내하고 세션은 그대로 보인다', async () => {
    renderDetail(true, true);
    expect(await screen.findByText('sess-abcdef')).toBeTruthy();
    expect(screen.getByText(NOTE)).toBeTruthy();
  });

  // 서버가 sentinel 로 빈 결과를 줘도(user-id 모드에서 cctrace_user_id 가 비었을 때) 범위 안내는 유지한다. 의도된 동작이다.
  it('세션이 0건이어도 안내와 No sessions 가 함께 보인다', async () => {
    fetchSessionOverview.mockResolvedValue([]);
    renderDetail(true, true);
    expect(await screen.findByText('No sessions')).toBeTruthy();
    expect(screen.getByText(NOTE)).toBeTruthy();
  });

  it('기본값(admin 등)에서는 안내가 없다', async () => {
    renderDetail(true);
    await screen.findByText('sess-abcdef');
    expect(screen.queryByText(NOTE)).toBeNull();
  });

  it('canViewSessions=false 면 안내 대신 차단 문구만 보인다', async () => {
    renderDetail(false, true);
    expect(await screen.findByText('다른 사용자의 세션은 볼 수 없습니다')).toBeTruthy();
    expect(screen.queryByText(NOTE)).toBeNull();
  });
});
