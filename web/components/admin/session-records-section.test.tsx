// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AdminAIResponse } from '@/lib/types';

const mocks = vi.hoisted(() => ({ projects: vi.fn(), models: vi.fn(), vocabulary: vi.fn(), setVocabulary: vi.fn(), mergeVocabulary: vi.fn(), updateSettings: vi.fn(), isAdmin: true }));
vi.mock('@/lib/api', () => ({
  fetchProjects: mocks.projects,
  fetchAdminAIModels: mocks.models,
  fetchAdminSessionRecordVocabulary: mocks.vocabulary,
  setAdminSessionRecordVocabulary: mocks.setVocabulary,
  mergeAdminSessionRecordVocabulary: mocks.mergeVocabulary,
  updateAdminSessionRecordSettings: mocks.updateSettings,
}));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ isAdmin: mocks.isAdmin }) }));

import { SessionRecordsAdminPanel } from './session-records-section';

const DATA = {
  enabled: false, enabled_source: 'default', env_enabled: null, runtimes: [], selected_runtime: '', runtime_source: 'default',
  selection_reason: null, selection_reason_code: null, secrets_reason: null, env_runtime: '', model: '', settings: null,
  schedule: { enabled: false, weekday: 1, hour: 6, minute: 0 }, usage_this_week: { runs: 0, failed: 0, input_tokens: 0, output_tokens: 0 },
  records: {
    flag: true, enabled: true, prompt_version: 1, runtime: 'openai-api', runtime_key: 'openai-api:api_key', runtime_ok: true,
    runtime_reason: '', model: '', reasoning_effort: '', consents: { users: 8, current: 4 },
    today: { day: '2026-10-09', tz: 'UTC', cap_tokens: 1000, used_tokens: 240, reserved_tokens: 50,
      estimated_cap_tokens: 80, unreported_calls: 1, input_tokens: 200, cached_input_tokens: 20, output_tokens: 40,
      by_lane: { live: { sessions: 1, calls: 1, input_tokens: 200, cached_input_tokens: 20, output_tokens: 40 } } },
    last_7_days: [], backlog: { due: 3, waiting_quiet: 2, backfill_remaining: 4, requested: 1 },
    backfill: { enabled: false, days: 30, token_budget: null, used_tokens: 0, since: null },
    failures: { sessions: 1, by_code: { schema_violation: 1 } },
    quality_7d: { dropped_items: 0, uncovered_requests: 0, new_terms: 0, redacted: 0 },
    worker: { state: 'running', paused_reason: '' },
  },
} as unknown as AdminAIResponse;

const VOCABULARY = { repository_key: 'github.com/acme/app', components: ['web'], screens: ['sessions'], components_closed: true, screens_closed: true };

const mount = (data: AdminAIResponse = DATA) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  const view = render(createElement(QueryClientProvider, { client }, createElement(SessionRecordsAdminPanel, { data })));
  return { ...view, client };
};

afterEach(() => {
  cleanup();
  mocks.projects.mockReset();
  mocks.models.mockReset();
  mocks.vocabulary.mockReset();
  mocks.setVocabulary.mockReset();
  mocks.mergeVocabulary.mockReset();
  mocks.updateSettings.mockReset();
  mocks.isAdmin = true;
});

beforeEach(() => {
  mocks.projects.mockResolvedValue([]);
  mocks.models.mockResolvedValue({ models: [] });
});

describe('P5 관리자 세션 기록 패널', () => {
  it('토큰 예산·대기·실패를 실제 합계로 보이고 금액은 판단 불가로 둔다', () => {
    mount();
    const usage = screen.getByText(/확정 사용량:/).textContent ?? '';
    expect(usage).toContain('입력 200');
    expect(usage).toContain('출력 40');
    expect(screen.getByText(/대기 3 · 조용한 구간 2 · 백필 잔여 4/)).toBeTruthy();
    expect(screen.getByText(/schema_violation/)).toBeTruthy();
    expect(screen.getByText(/달러 비용은 API가 제공하는 요율·금액 정보가 없어 판단할 수 없습니다/)).toBeTruthy();
  });

  it('read-back 사유 값이 없으면 전체 레코드를 덮어쓸 설정 저장을 막는다', () => {
    const missingEffort = { ...DATA, records: { ...DATA.records, reasoning_effort: undefined } } as unknown as AdminAIResponse;
    mount(missingEffort);
    expect(screen.getByRole('button', { name: '세션 기록 설정 저장' })).toHaveProperty('disabled', true);
    expect(screen.getByText(/현재 설정 전체를 안전하게 읽지 못해 저장을 막았습니다/)).toBeTruthy();
  });

  it('저장 시 nullable 기본값과 runtime 위임을 원본 그대로 보존한다', async () => {
    const stored = { ...DATA, records: { ...DATA.records,
      enabled: false,
      settings: { enabled: false, runtime: '', daily_token_cap: null, backfill_enabled: false, backfill_days: null, backfill_token_budget: null },
      runtime_settings: { runtime: 'openai-api', model: '', reasoning_effort: '' },
    } } as unknown as AdminAIResponse;
    mocks.updateSettings.mockResolvedValue(undefined);
    mount(stored);
    const user = userEvent.setup();
    await user.click(screen.getByRole('switch', { name: '세션 기록 생성' }));
    await user.click(screen.getByRole('button', { name: '세션 기록 설정 저장' }));
    await waitFor(() => expect(mocks.updateSettings.mock.calls[0]?.[0]).toEqual({
      enabled: true, runtime: '', daily_token_cap: null, backfill_enabled: false,
      backfill_days: null, backfill_token_budget: null, model: '', reasoning_effort: '',
    }));
  });

  it('저장소 어휘를 읽고 두 축을 저장하거나 항목을 병합한다', async () => {
    mocks.projects.mockResolvedValue([{ project_hash: 'p1', project_name: 'app', git_remote_url: '', repository_id: 'github.com/acme/app', repository_name: 'app', repo_subpath: '', updated_at: '', last_session_at: null }]);
    mocks.vocabulary.mockResolvedValue(VOCABULARY);
    mocks.setVocabulary.mockResolvedValue(VOCABULARY);
    mocks.mergeVocabulary.mockResolvedValue(VOCABULARY);
    mount();
    const user = userEvent.setup();
    await user.type(screen.getByLabelText('저장소 키'), 'github.com/acme/app');
    await screen.findByDisplayValue('web');
    expect(mocks.vocabulary).toHaveBeenCalledWith('github.com/acme/app', expect.any(AbortSignal));
    await user.clear(screen.getByRole('textbox', { name: '컴포넌트 어휘' }));
    await user.type(screen.getByRole('textbox', { name: '컴포넌트 어휘' }), 'frontend\nweb');
    await user.click(screen.getByRole('button', { name: '어휘 저장' }));
    await waitFor(() => expect(mocks.setVocabulary).toHaveBeenCalledWith('github.com/acme/app', ['frontend', 'web'], ['sessions']));
    await user.type(screen.getByRole('textbox', { name: '기존 항목' }), 'web');
    await user.type(screen.getByRole('textbox', { name: '새 항목' }), 'frontend');
    await user.click(screen.getByRole('button', { name: '항목 병합' }));
    expect(mocks.mergeVocabulary).toHaveBeenCalledWith('github.com/acme/app', 'component', 'web', 'frontend');
  });

  it('비관리자는 관리자 기록 API를 호출하지 않는다', () => {
    mocks.isAdmin = false;
    mount();
    expect(screen.getByText('관리자 전용 설정입니다.')).toBeTruthy();
    expect(mocks.projects).not.toHaveBeenCalled();
    expect(mocks.vocabulary).not.toHaveBeenCalled();
  });
});
