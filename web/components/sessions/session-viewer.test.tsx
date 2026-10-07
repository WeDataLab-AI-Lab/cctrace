// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SessionOverview } from '@/lib/types';

vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ user: null, isAdmin: false }) }));
vi.mock('@/lib/api', () => ({
  fetchAppVersionInfo: async () => ({ session_record_version_since: 'v0.5.0' }),
  fetchDeletionPolicy: async () => ({ allow_owner_delete: false }),
}));
// 이 테스트의 관심사는 상단 identity 줄의 하네스 표기뿐이다.
vi.mock('./session-detail', () => ({ SessionDetail: () => null }));
vi.mock('./account-segments', () => ({ AccountSegments: () => null }));
vi.mock('./delete-session-dialog', () => ({ DeleteSessionDialog: () => null }));
vi.mock('./blocked-projects-dialog', () => ({ BlockedProjectsDialog: () => null }));

import { SessionViewer } from './session-viewer';

const session = (over: Partial<SessionOverview>): SessionOverview => ({
  session_id: 's-1',
  profile_email: 'a@example.com',
  model: 'm',
  agent: 'claude',
  start_time: '2026-09-30T00:00:00Z',
  end_time: '2026-09-30T00:01:00Z',
  input_tokens: 0,
  output_tokens: 0,
  cost_usd: 0,
  event_count: 1,
  has_sync: true,
  ...over,
});

// 헤더 줄의 cctrace/하네스 버전 span. 버전 조회가 끝나면 나타난다.
const harnessLine = async (over: Partial<SessionOverview>): Promise<string> => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(createElement(QueryClientProvider, { client },
    createElement(SessionViewer, { session: session(over), mode: 'assembled', onNavigate: () => undefined })));
  return (await screen.findByText(/cctrace /)).textContent ?? '';
};

afterEach(cleanup);

describe('SessionViewer 헤더의 하네스 표기', () => {
  it('claude 세션은 Claude Code 와 버전을 보인다', async () => {
    expect(await harnessLine({ agent: 'claude', claude_version: '2.1.0' })).toContain('Claude Code 2.1.0');
  });

  it('agent 가 비어 있는 기존 세션은 서버 기본값(claude)대로 읽는다', async () => {
    expect(await harnessLine({ agent: '', claude_version: '2.1.0' })).toContain('Claude Code 2.1.0');
  });

  it('claude 세션의 버전이 비면 기존대로 — 를 보인다', async () => {
    expect(await harnessLine({ agent: 'claude' })).toContain('Claude Code —');
  });

  it('codex 세션은 Claude Code 문구 없이 Codex 만 보인다', async () => {
    const line = await harnessLine({ agent: 'codex' });
    expect(line).not.toContain('Claude Code');
    expect(line).not.toContain('—');
    expect(line).toContain('Codex');
  });

  it('codex 세션에 버전이 있으면 함께 보인다', async () => {
    expect(await harnessLine({ agent: 'codex', claude_version: '0.9.1' })).toContain('Codex 0.9.1');
  });

  it('알 수 없는 에이전트는 값 그대로 보이고 Claude Code 로 단정하지 않는다', async () => {
    const line = await harnessLine({ agent: 'gjc' });
    expect(line).not.toContain('Claude Code');
    expect(line).toContain('gjc');
  });
});
