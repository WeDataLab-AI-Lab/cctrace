// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { LocalSessionOverview } from '@/lib/local-session-types';
import { LocalSessionApiError, validateLocalOrigin } from '@/lib/local-session-api';
import type { SessionOverview, SessionRecord } from '@/lib/types';
import { clearConnection } from '@/lib/local-session-connection';
import { LocalSessionsPanel } from './local-sessions-panel';

const row = (overrides: Partial<LocalSessionOverview> = {}): LocalSessionOverview => ({
  local_id: 'opaque-one', canonical_id: 'canonical-one', native_session_id: 'native-one',
  profile_fingerprint: 'profile-one', revision: 'r1', agent: 'claude', model: 'model-one',
  start_time: '2026-10-09T00:00:00Z', end_time: '2026-10-09T01:00:00Z',
  input_tokens: 1, output_tokens: 1, cost_usd: 0, event_count: 1, title: '내 작업',
  ...overrides,
});
const record = (text: string): SessionRecord => ({
  ts: '2026-10-09T00:00:00Z', session_id: 'native-one', project_hash: '', profile_email: '',
  model: 'model-one', record_type: 'user', view: { kind: 'message', role: 'user', text,
    anchor_id: text, dedupe_key: text },
});
const api = vi.hoisted(() => ({
  health: vi.fn(), listSessions: vi.fn(), listRecords: vi.fn(), analysis: vi.fn(), create: vi.fn(),
}));
vi.mock('@/lib/local-session-api', async (load) => {
  const real = await load<typeof import('@/lib/local-session-api')>();
  return { ...real, createLocalSessionApi: api.create };
});

const connect = (endpoint = 'http://127.0.0.1:4567') => {
  fireEvent.change(screen.getByLabelText('로컬 서비스 주소'), { target: { value: endpoint } });
  fireEvent.change(screen.getByLabelText('접속 토큰'), { target: { value: 'temporary-value' } });
  fireEvent.click(screen.getByRole('button', { name: '연결' }));
};

beforeEach(() => {
  clearConnection();
  api.create.mockImplementation(({ origin }: { origin: string }) => { validateLocalOrigin(origin); return api; });
  api.health.mockReset().mockResolvedValue({ status: 'ready' });
  api.listSessions.mockReset().mockResolvedValue({ revision: 'list-one', sessions: [row()], next_cursor: null });
  api.listRecords.mockReset().mockResolvedValue({ revision: 'r1', records: [record('첫 입력')], next_cursor: null });
  api.analysis.mockReset().mockResolvedValue({ revision: 'r1', rules_version: 1, state: 'not_collected', session: null, turns: [] });
  api.create.mockClear();
  window.sessionStorage.clear();
});
afterEach(() => {
  window.sessionStorage.clear();
  cleanup();
  clearConnection();
});

describe('LocalSessionsPanel', () => {
  it('connects only on click, keeps the token out of storage, then shows local records', async () => {
    const storage = vi.spyOn(Storage.prototype, 'setItem');
    render(<LocalSessionsPanel />);
    expect(api.create).not.toHaveBeenCalled();
    connect();
    await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
    expect(api.health).toHaveBeenCalledBefore(api.listSessions);
    expect(screen.getByLabelText('접속 토큰')).toHaveProperty('value', '');
    fireEvent.click(screen.getByRole('button', { name: /내 작업/ }));
    await waitFor(() => expect(screen.getByText('첫 입력')).toBeTruthy());
    expect(storage).not.toHaveBeenCalled();
    storage.mockRestore();
  });

  it('saves a pairing only when asked and reconnects from it on the next mount', async () => {
    render(<LocalSessionsPanel />);
    fireEvent.click(screen.getByLabelText('이 탭에 연결 저장'));
    connect();
    await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
    expect(api.create).toHaveBeenCalledWith({ origin: 'http://127.0.0.1:4567', token: 'temporary-value' });
    const saved = window.sessionStorage.getItem('cctrace:local-connection');
    expect(saved).toContain('http://127.0.0.1:4567');

    cleanup();
    api.create.mockClear();
    render(<LocalSessionsPanel />);
    await waitFor(() => expect(api.create).toHaveBeenCalledWith({ origin: 'http://127.0.0.1:4567', token: 'temporary-value' }));
    await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
  });

  it('keeps pending records and analysis alive when the selected row is clicked again', async () => {
    let resolveRecords!: (value: { revision: string; records: SessionRecord[]; next_cursor: null }) => void;
    let resolveAnalysis!: (value: { revision: string; rules_version: number; state: 'not_collected'; session: null; turns: [] }) => void;
    api.listRecords.mockReturnValueOnce(new Promise((resolve) => { resolveRecords = resolve; }));
    api.analysis.mockReturnValueOnce(new Promise((resolve) => { resolveAnalysis = resolve; }));
    render(<LocalSessionsPanel />);
    connect();
    await waitFor(() => expect(screen.getByRole('button', { name: /내 작업/ })).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /내 작업/ }));
    await waitFor(() => expect(api.listRecords).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole('button', { name: /내 작업/ }));
    await act(async () => {
      resolveRecords({ revision: 'r1', records: [record('재클릭 후 입력')], next_cursor: null });
      resolveAnalysis({ revision: 'r1', rules_version: 1, state: 'not_collected', session: null, turns: [] });
    });
    expect(screen.getByText('재클릭 후 입력')).toBeTruthy();
    fireEvent.click(screen.getByRole('tab', { name: '분석' }));
    expect(screen.getByText(/분석에 필요한 원본 기록이 수집되지 않았습니다/)).toBeTruthy();
    expect(screen.queryByText('로컬 분석을 불러오는 중입니다.')).toBeNull();
    expect(api.listRecords).toHaveBeenCalledTimes(1);
    expect(api.analysis).toHaveBeenCalledTimes(1);
  });

  it('filters agent and search without changing selected canonical identity on a revision update', async () => {
    api.listSessions.mockResolvedValueOnce({ revision: 'list-one', sessions: [row(), row({
      local_id: 'opaque-two', canonical_id: 'canonical-two', native_session_id: 'native-two',
      agent: 'codex', title: '다른 작업',
    })], next_cursor: null });
    render(<LocalSessionsPanel />);
    connect();
    await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /내 작업/ }));
    fireEvent.change(screen.getByLabelText('에이전트 필터'), { target: { value: 'codex' } });
    expect(screen.queryByRole('button', { name: /내 작업/ })).toBeNull();
    expect(screen.getByText('다른 작업')).toBeTruthy();
    fireEvent.change(screen.getByLabelText('에이전트 필터'), { target: { value: 'all' } });
    fireEvent.change(screen.getByLabelText('로컬 세션 검색'), { target: { value: '다른' } });
    expect(screen.queryByRole('button', { name: /내 작업/ })).toBeNull();
    expect(screen.getByLabelText('로컬 세션 상세')).toBeTruthy();
    expect(screen.getByLabelText('로컬 세션 상세').getAttribute('data-local-id')).toBe('opaque-one');
  });

  it('keeps the canonical selection and refetches detail when a poll changes revision', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const updated = row({ revision: 'r2', title: '갱신된 작업' });
      api.listRecords.mockResolvedValueOnce({ revision: 'r1', records: [record('이전 입력')], next_cursor: null })
      .mockResolvedValueOnce({ revision: 'r2', records: [record('새 입력')], next_cursor: null });
    api.analysis.mockResolvedValueOnce({ revision: 'r1', rules_version: 1, state: 'not_collected', session: null, turns: [] })
      .mockResolvedValueOnce({ revision: 'r2', rules_version: 1, state: 'not_collected', session: null, turns: [] });
    render(<LocalSessionsPanel />);
    connect();
    await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /내 작업/ }));
    await waitFor(() => expect(screen.getByText('이전 입력')).toBeTruthy());
    api.listSessions.mockResolvedValueOnce({ revision: 'list-two', sessions: [updated], next_cursor: null });
    await act(async () => { await vi.advanceTimersByTimeAsync(3_000); });
    await waitFor(() => expect(screen.getByText('새 입력')).toBeTruthy());
    expect(screen.queryByText('이전 입력')).toBeNull();
    expect(screen.getByLabelText('로컬 세션 상세').getAttribute('data-local-id')).toBe('opaque-one');
    expect(api.listRecords).toHaveBeenLastCalledWith('opaque-one', { revision: 'r2' });
    } finally {
      vi.useRealTimers();
    }
  });

  it('retains a loaded later page on the same revision, then reloads it and removes deleted rows on revision change', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const older = row({ local_id: 'opaque-old', canonical_id: 'canonical-old', title: '과거 작업' });
      api.listSessions.mockResolvedValueOnce({ revision: 'list-one', sessions: [row()], next_cursor: 'cursor-one' })
        .mockResolvedValueOnce({ revision: 'list-one', sessions: [older], next_cursor: null });
      render(<LocalSessionsPanel />);
      connect();
      await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
      fireEvent.click(screen.getByRole('button', { name: '세션 더 보기' }));
      await waitFor(() => expect(screen.getByText('과거 작업')).toBeTruthy());
      fireEvent.click(screen.getByRole('button', { name: /과거 작업/ }));
      api.listSessions.mockResolvedValueOnce({ revision: 'list-one', sessions: [row()], next_cursor: 'cursor-one' });
      await act(async () => { await vi.advanceTimersByTimeAsync(3_000); });
      expect(screen.getByRole('button', { name: /과거 작업/ })).toBeTruthy();
      api.listSessions.mockResolvedValueOnce({ revision: 'list-two', sessions: [row()], next_cursor: 'cursor-two' })
        .mockResolvedValueOnce({ revision: 'list-two', sessions: [row({ local_id: 'new', canonical_id: 'new', title: '새 작업' })], next_cursor: null });
      await act(async () => { await vi.advanceTimersByTimeAsync(3_000); });
      expect(api.listSessions).toHaveBeenCalledWith({ cursor: 'cursor-two' });
      expect(screen.queryByRole('button', { name: /과거 작업/ })).toBeNull();
      expect(screen.getByText('새 작업')).toBeTruthy();
      expect(screen.getByText('선택한 로컬 세션이 최신 목록에서 사라졌습니다.')).toBeTruthy();
    } finally { vi.useRealTimers(); }
  });

  it('continues to the third page after polling an unchanged revision', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      api.listSessions.mockResolvedValueOnce({ revision: 'list-one', sessions: [row()], next_cursor: 'cursor-one' })
        .mockResolvedValueOnce({ revision: 'list-one', sessions: [row({
          local_id: 'second', canonical_id: 'second', title: '두 번째 페이지',
        })], next_cursor: 'cursor-two' });
      render(<LocalSessionsPanel />);
      connect();
      await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
      fireEvent.click(screen.getByRole('button', { name: '세션 더 보기' }));
      await waitFor(() => expect(screen.getByText('두 번째 페이지')).toBeTruthy());
      api.listSessions.mockResolvedValueOnce({ revision: 'list-one', sessions: [row()], next_cursor: 'cursor-one' })
        .mockResolvedValueOnce({ revision: 'list-one', sessions: [row({
          local_id: 'third', canonical_id: 'third', title: '세 번째 페이지',
        })], next_cursor: null });
      await act(async () => { await vi.advanceTimersByTimeAsync(3_000); });
      fireEvent.click(screen.getByRole('button', { name: '세션 더 보기' }));
      await waitFor(() => expect(screen.getByText('세 번째 페이지')).toBeTruthy());
      expect(api.listSessions).toHaveBeenLastCalledWith({ cursor: 'cursor-two' });
      expect(screen.getByText('두 번째 페이지')).toBeTruthy();
    } finally { vi.useRealTimers(); }
  });

  it('ignores a late records page after switching selection', async () => {
    let resolvePage!: (page: { revision: string; records: SessionRecord[]; next_cursor: null }) => void;
    const pending = new Promise<{ revision: string; records: SessionRecord[]; next_cursor: null }>((resolve) => { resolvePage = resolve; });
    api.listSessions.mockResolvedValueOnce({ revision: 'list-one', sessions: [row(), row({
      local_id: 'opaque-two', canonical_id: 'canonical-two', revision: 'r2', title: '두 번째 작업',
    })], next_cursor: null });
    api.listRecords.mockResolvedValueOnce({ revision: 'r1', records: [record('첫 입력')], next_cursor: 'next' })
      .mockReturnValueOnce(pending).mockResolvedValueOnce({ revision: 'r2', records: [record('두 번째 입력')], next_cursor: null });
    render(<LocalSessionsPanel />);
    connect();
    await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /내 작업/ }));
    await waitFor(() => expect(screen.getByRole('button', { name: '다음 기록 불러오기' })).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: '다음 기록 불러오기' }));
    fireEvent.click(screen.getByRole('button', { name: /두 번째 작업/ }));
    await waitFor(() => expect(screen.getByText('두 번째 입력')).toBeTruthy());
    await act(async () => { resolvePage({ revision: 'r1', records: [record('늦은 입력')], next_cursor: null }); });
    expect(screen.queryByText('늦은 입력')).toBeNull();
    expect(screen.getByText('두 번째 입력')).toBeTruthy();
  });

  it('paginates records and resets stale cursor on a revision conflict', async () => {
    api.listRecords.mockResolvedValueOnce({ revision: 'r1', records: [record('첫 입력')], next_cursor: 'next' })
      .mockRejectedValueOnce(new LocalSessionApiError('revision_conflict'))
      .mockResolvedValueOnce({ revision: 'r1', records: [record('다시 읽은 입력')], next_cursor: null });
    render(<LocalSessionsPanel />);
    connect();
    await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /내 작업/ }));
    await waitFor(() => expect(screen.getByRole('button', { name: '다음 기록 불러오기' })).toBeTruthy());
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: '다음 기록 불러오기' })); });
    await waitFor(() => expect(screen.getByText('다시 읽은 입력')).toBeTruthy());
    expect(screen.queryByText('첫 입력')).toBeNull();
    expect(api.listRecords).toHaveBeenCalledWith('opaque-one', { cursor: 'next', revision: 'r1' });
  });

  it('shows an unmatched server session only in the embedded view with a verified owner scope', async () => {
    const onSelectServer = vi.fn();
    const serverSessions: SessionOverview[] = [{
      session_id: 'server-other', user_id: 'owner-one', profile_email: 'owner@example.invalid',
      agent: 'codex', model: 'model-two', start_time: '2026-10-09T00:00:00Z',
      end_time: '2026-10-09T01:00:00Z', input_tokens: 0, output_tokens: 0,
      cost_usd: 0, event_count: 1, has_sync: true, project_name: '서버 작업',
    }];
    render(<LocalSessionsPanel embedded serverSessions={serverSessions} ownerScope={{ user_id: 'owner-one' }} onSelectServer={onSelectServer} />);
    expect(screen.getByText('서버 작업')).toBeTruthy();
    expect(api.create).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: /서버 작업/ }));
    expect(onSelectServer).toHaveBeenCalledWith('server-other');
  });

  it('shows server rows without an owner scope and never deduplicates matching local IDs', async () => {
    const serverSessions: SessionOverview[] = [{
      session_id: 'native-one', user_id: 'owner-one', profile_email: 'owner@example.invalid',
      agent: 'claude', model: 'model-one', start_time: '2026-10-09T00:00:00Z',
      end_time: '2026-10-09T01:00:00Z', input_tokens: 0, output_tokens: 0,
      cost_usd: 0, event_count: 1, has_sync: true, project_name: '같은 서버 세션',
    }];
    render(<LocalSessionsPanel embedded serverSessions={serverSessions} />);
    expect(screen.getByText('같은 서버 세션')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toContain('서버 행은 별도 출처로 표시하며');
    connect();
    await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
    expect(screen.getByRole('button', { name: /같은 서버 세션/ })).toBeTruthy();
    expect(screen.getByRole('button', { name: /내 작업/ })).toBeTruthy();
  });

  it('offers an explicit reversible same-ID grouping with both source buttons', async () => {
    const onSelectServer = vi.fn();
    const serverSessions: SessionOverview[] = [{
      session_id: 'native-one', user_id: 'owner-one', profile_email: 'owner@example.invalid',
      agent: 'claude', model: 'model-one', start_time: '2026-10-09T00:00:00Z',
      end_time: '2026-10-09T01:00:00Z', input_tokens: 0, output_tokens: 0,
      cost_usd: 0, event_count: 1, has_sync: true, project_name: '같은 서버 세션',
    }];
    render(<LocalSessionsPanel embedded serverSessions={serverSessions} ownerScope={{ user_id: 'owner-one' }} onSelectServer={onSelectServer} />);
    connect();
    await waitFor(() => expect(screen.getByRole('button', { name: '같은 세션으로 묶기' })).toBeTruthy());
    expect(screen.getByText(/두 출처를 별도로 표시함/)).toBeTruthy();
    expect(screen.getByText(/같은 ID만으로 동일 세션 여부를 확인할 수 없어 직접 묶어야 합니다/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '같은 세션으로 묶기' }));
    expect(screen.getByText(/사용자가 묶어 표시함/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: /내 작업/ }));
    expect(screen.getByLabelText('로컬 세션 상세')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: /같은 서버 세션/ }));
    expect(onSelectServer).toHaveBeenCalledWith('native-one');
    fireEvent.click(screen.getByRole('button', { name: '묶기 해제' }));
    expect(screen.getByText(/두 출처를 별도로 표시함/)).toBeTruthy();
  });

  it('keeps an explicit server-detail action for a verified automatic match', async () => {
    const onSelectServer = vi.fn();
    const serverSessions: SessionOverview[] = [{
      session_id: 'native-one', user_id: 'owner-one', profile_email: 'owner@example.invalid',
      agent: 'claude', model: 'model-one', start_time: '2026-10-09T00:00:00Z',
      end_time: '2026-10-09T01:00:00Z', input_tokens: 0, output_tokens: 0,
      cost_usd: 0, event_count: 1, has_sync: true, project_name: '서버 작업',
    }];
    api.listSessions.mockResolvedValueOnce({ revision: 'list-one', sessions: [row({
      server_match: { kind: 'owner_native', owner_user_id: 'owner-one', profile_fingerprint: 'profile-one',
        server_profile_email: 'owner@example.invalid', agent: 'claude', native_session_id: 'native-one' },
    })], next_cursor: null });
    render(<LocalSessionsPanel embedded serverSessions={serverSessions} ownerScope={{ user_id: 'owner-one' }} onSelectServer={onSelectServer} />);
    connect();
    await waitFor(() => expect(screen.getByRole('button', { name: '매칭된 서버 세션 보기' })).toBeTruthy());
    expect(screen.getByRole('button', { name: /내 작업/ })).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '매칭된 서버 세션 보기' }));
    expect(onSelectServer).toHaveBeenCalledWith('native-one');
  });

  it('clears the grouping after reconnecting to another local source', async () => {
    const serverSessions: SessionOverview[] = [{
      session_id: 'native-one', user_id: 'owner-one', profile_email: 'owner@example.invalid',
      agent: 'claude', model: 'model-one', start_time: '2026-10-09T00:00:00Z',
      end_time: '2026-10-09T01:00:00Z', input_tokens: 0, output_tokens: 0,
      cost_usd: 0, event_count: 1, has_sync: true, project_name: '서버 작업',
    }];
    render(<LocalSessionsPanel embedded serverSessions={serverSessions} ownerScope={{ user_id: 'owner-one' }} />);
    connect();
    await waitFor(() => expect(screen.getByRole('button', { name: '같은 세션으로 묶기' })).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: '같은 세션으로 묶기' }));
    fireEvent.change(screen.getByLabelText('접속 토큰'), { target: { value: 'another-token' } });
    fireEvent.click(screen.getByRole('button', { name: '연결' }));
    await waitFor(() => expect(screen.getByRole('button', { name: '같은 세션으로 묶기' })).toBeTruthy());
    expect(screen.queryByRole('button', { name: '묶기 해제' })).toBeNull();
  });

  it('does not offer grouping when multiple local profiles share one native ID', async () => {
    api.listSessions.mockResolvedValueOnce({ revision: 'list-one', sessions: [row(), row({
      local_id: 'other-local', canonical_id: 'other-canonical', profile_fingerprint: 'other-profile', title: '다른 로컬 세션',
    })], next_cursor: null });
    const serverSessions: SessionOverview[] = [{
      session_id: 'native-one', user_id: 'owner-one', profile_email: 'owner@example.invalid',
      agent: 'claude', model: 'model-one', start_time: '2026-10-09T00:00:00Z',
      end_time: '2026-10-09T01:00:00Z', input_tokens: 0, output_tokens: 0,
      cost_usd: 0, event_count: 1, has_sync: true, project_name: '서버 작업',
    }];
    render(<LocalSessionsPanel embedded serverSessions={serverSessions} ownerScope={{ user_id: 'owner-one' }} />);
    connect();
    await waitFor(() => expect(screen.getByText('다른 로컬 세션')).toBeTruthy());
    expect(screen.queryByRole('button', { name: '같은 세션으로 묶기' })).toBeNull();
    expect(screen.getByRole('button', { name: /서버 작업/ })).toBeTruthy();
  });

  it('drops the grouping when the owner scope changes and excludes the previous owner row', async () => {
    const serverSessions: SessionOverview[] = [{
      session_id: 'native-one', user_id: 'owner-one', profile_email: 'owner@example.invalid',
      agent: 'claude', model: 'model-one', start_time: '2026-10-09T00:00:00Z',
      end_time: '2026-10-09T01:00:00Z', input_tokens: 0, output_tokens: 0,
      cost_usd: 0, event_count: 1, has_sync: true, project_name: '서버 작업',
    }];
    const props = { embedded: true, serverSessions };
    const view = render(<LocalSessionsPanel {...props} ownerScope={{ user_id: 'owner-one' }} />);
    connect();
    await waitFor(() => expect(screen.getByRole('button', { name: '같은 세션으로 묶기' })).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: '같은 세션으로 묶기' }));
    view.rerender(<LocalSessionsPanel {...props} ownerScope={{ user_id: 'other-owner' }} />);
    expect(screen.queryByRole('button', { name: '묶기 해제' })).toBeNull();
    expect(screen.queryByRole('button', { name: '같은 세션으로 묶기' })).toBeNull();
    view.rerender(<LocalSessionsPanel {...props} ownerScope={{ user_id: 'owner-one' }} />);
    expect(screen.getByRole('button', { name: '같은 세션으로 묶기' })).toBeTruthy();
  });

  it('shows zero separately from missing local counters and accepts a null model in search', async () => {
    api.listSessions.mockResolvedValueOnce({ revision: 'list-one', sessions: [row({
      model: null, start_time: null, end_time: null, input_tokens: 0, output_tokens: null,
      event_count: null, cost_usd: null,
    })], next_cursor: null });
    render(<LocalSessionsPanel />);
    connect();
    await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
    expect(screen.getByText(/입력 0 · 출력 미집계 · 기록 미집계 · 비용 미집계/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText('로컬 세션 검색'), { target: { value: 'native-one' } });
    expect(screen.getByText('내 작업')).toBeTruthy();
  });

  it('shows and selects a local row without a native ID and offers no grouping', async () => {
    api.listSessions.mockResolvedValueOnce({ revision: 'list-one', sessions: [row({
      local_id: 'opaque-unobserved', canonical_id: 'canonical-unobserved', native_session_id: null,
    })], next_cursor: null });
    const serverSessions: SessionOverview[] = [{
      session_id: 'native-one', user_id: 'owner-one', profile_email: 'owner@example.invalid',
      agent: 'claude', model: 'model-one', start_time: '2026-10-09T00:00:00Z',
      end_time: '2026-10-09T01:00:00Z', input_tokens: 0, output_tokens: 0,
      cost_usd: 0, event_count: 1, has_sync: true, project_name: '서버 작업',
    }];
    render(<LocalSessionsPanel embedded serverSessions={serverSessions} ownerScope={{ user_id: 'owner-one' }} />);
    connect();
    await waitFor(() => expect(screen.getByRole('button', { name: /내 작업/ })).toBeTruthy());
    expect(screen.queryByRole('button', { name: '같은 세션으로 묶기' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: /내 작업/ }));
    await waitFor(() => expect(screen.getByLabelText('로컬 세션 상세')).toBeTruthy());
    expect(screen.getByLabelText('로컬 세션 상세').getAttribute('data-local-id')).toBe('opaque-unobserved');
    expect(api.listRecords).toHaveBeenCalledWith('opaque-unobserved', { revision: 'r1' });
  });

  it('ignores an old connection page after reconnecting', async () => {
    let resolvePage!: (page: { revision: string; sessions: LocalSessionOverview[]; next_cursor: null }) => void;
    const pending = new Promise<{ revision: string; sessions: LocalSessionOverview[]; next_cursor: null }>((resolve) => { resolvePage = resolve; });
    api.listSessions.mockResolvedValueOnce({ revision: 'list-one', sessions: [row()], next_cursor: 'cursor-one' })
      .mockReturnValueOnce(pending)
      .mockResolvedValueOnce({ revision: 'list-two', sessions: [row({
        local_id: 'new', canonical_id: 'new', title: '새 연결',
      })], next_cursor: null });
    render(<LocalSessionsPanel />);
    connect();
    await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: '세션 더 보기' }));
    fireEvent.change(screen.getByLabelText('접속 토큰'), { target: { value: 'another-token' } });
    fireEvent.click(screen.getByRole('button', { name: '연결' }));
    await waitFor(() => expect(screen.getByText('새 연결')).toBeTruthy());
    await act(async () => { resolvePage({ revision: 'list-one', sessions: [row({
      local_id: 'stale', canonical_id: 'stale', title: '오래된 응답',
    })], next_cursor: null }); });
    expect(screen.queryByText('오래된 응답')).toBeNull();
    expect(screen.getByText('새 연결')).toBeTruthy();
  });

  it('keeps unauthorized visible rather than presenting an empty list', async () => {
    api.health.mockRejectedValueOnce(new LocalSessionApiError('unauthorized'));
    render(<LocalSessionsPanel />);
    connect();
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('인증에 실패'));
    expect(screen.queryByLabelText('로컬 세션 목록')).toBeNull();
  });

  it('connects and saves the 127.0.0.1 form when localhost is typed', async () => {
    render(<LocalSessionsPanel />);
    fireEvent.click(screen.getByLabelText('이 탭에 연결 저장'));
    connect('http://localhost:4567');
    await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
    const saved = window.sessionStorage.getItem('cctrace:local-connection') ?? '';
    expect(saved).toContain('http://127.0.0.1:4567');
    expect(saved).not.toContain('localhost');
    expect(screen.getByLabelText('로컬 서비스 주소')).toHaveProperty('value', 'http://127.0.0.1:4567');
  });

  it('connects once to localhost with the default port, whose origin has no port left to validate again', async () => {
    render(<LocalSessionsPanel />);
    connect('http://localhost:80');
    await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('rewrites a pairing an earlier visit saved with localhost when it reconnects', async () => {
    window.sessionStorage.setItem('cctrace:local-connection',
      JSON.stringify({ origin: 'http://localhost:4567', token: 'temporary-value' }));
    render(<LocalSessionsPanel />);
    await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
    const saved = window.sessionStorage.getItem('cctrace:local-connection') ?? '';
    expect(saved).toContain('http://127.0.0.1:4567');
    expect(saved).not.toContain('localhost');
    expect(screen.getByLabelText('로컬 서비스 주소')).toHaveProperty('value', 'http://127.0.0.1:4567');
  });

  it('tells the user to type the issued address when the daemon refuses with 403', async () => {
    api.health.mockRejectedValueOnce(new LocalSessionApiError('forbidden'));
    render(<LocalSessionsPanel />);
    connect();
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('발급된 주소'));
  });

  it('tells the user to type the issued address and open the page at --origin when a connection attempt cannot reach the daemon', async () => {
    api.health.mockRejectedValueOnce(new LocalSessionApiError('unreachable'));
    render(<LocalSessionsPanel />);
    connect();
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('발급된 주소'));
    expect(screen.getByRole('alert').textContent).toContain('--origin');
  });

  it('reports a dropped connection, not an address problem, when a poll fails after connecting', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      render(<LocalSessionsPanel />);
      connect();
      await waitFor(() => expect(screen.getByText('내 작업')).toBeTruthy());
      api.listSessions.mockRejectedValueOnce(new LocalSessionApiError('unreachable'));
      await act(async () => { await vi.advanceTimersByTimeAsync(3_000); });
      await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('연결이 끊겼습니다'));
      expect(screen.getByRole('alert').textContent).not.toContain('발급된 주소');
      expect(screen.getByRole('alert').textContent).not.toContain('--origin');
    } finally {
      vi.useRealTimers();
    }
  });

  it('does not recommend localhost when the address is rejected', async () => {
    render(<LocalSessionsPanel />);
    connect('http://example.com:4567');
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('발급된 주소'));
    expect(screen.getByRole('alert').textContent).not.toContain('localhost');
  });
});
