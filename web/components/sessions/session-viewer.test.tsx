// @vitest-environment jsdom
import { createElement, useState } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SessionOverview } from '@/lib/types';
import { fetchAppVersionInfo, fetchSessionAnalysis, fetchSessionRepoStatus } from '@/lib/api';
import { fetchConversationWindow } from '@/lib/conversation-window';
import { SessionViewer } from './session-viewer';

const state = vi.hoisted(() => ({ userId: 7, isAdmin: false, detailMounts: 0 }));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ user: { id: state.userId }, isAdmin: state.isAdmin }) }));
vi.mock('@/lib/api', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/lib/api')>();
  return { ...original, fetchSessionAnalysis: vi.fn(), fetchSessionRepoStatus: vi.fn(), fetchAppVersionInfo: vi.fn().mockResolvedValue(null), fetchDeletionPolicy: vi.fn().mockResolvedValue({ allow_owner_delete: false }) };
});
vi.mock('@/lib/conversation-window', () => ({ fetchConversationWindow: vi.fn() }));
vi.mock('./conversation-reader', () => ({ ConversationReader: ({ sessionId, mode, scrollFromTs, analysisAnchorTs, scrollAnchor, turnKey, active, scope, onInspect, onInspectContext }: {
  sessionId: string; mode: string; turnKey?: string; scrollFromTs?: string; analysisAnchorTs?: string; scrollAnchor?: string; active?: boolean; scope?: object;
  onInspect?: (target: { recordId: string; callId: string }) => void; onInspectContext?: () => void;
}) => {
  useState(() => { state.detailMounts += 1; });
  return createElement('div', { 'data-testid': 'conversation', 'data-active': active, 'data-turn-key': turnKey, 'data-scope': JSON.stringify(scope) }, `${sessionId}:${mode ?? 'assembled'}:${analysisAnchorTs ?? scrollAnchor ?? scrollFromTs ?? ''}`, createElement('button', { onClick: () => onInspect?.({ recordId: 'message-2', callId: 'call-2' }) }, '메시지 검사'), createElement('button', { onClick: onInspectContext }, '지시문·훅 확인'));
} }));
// The waterfall worker owns analysis requests, record controls, and turn selection.
// This shell boundary checks its identity and visibility contract without invoking trace.
vi.mock('./trace/trace-workspace', () => ({ SessionTraceWorkspace: ({ inspectionPanel, onSelectNode }: { inspectionPanel?: import('react').ReactNode; onSelectNode?: () => void }) => createElement('div', { 'data-testid': 'trace' }, inspectionPanel, createElement('button', { onClick: onSelectNode }, '실행 노드 선택')) }));
vi.mock('./inspection/session-inspector', () => ({ SessionInspector: ({ sessionId, scope, enabled, target, onReturnToConversation }: { sessionId: string; scope?: object; enabled?: boolean; target?: { recordId: string } | null; onReturnToConversation?: () => void }) =>
  createElement('div', { 'data-testid': 'workspace', 'data-session-id': sessionId, 'data-scope': JSON.stringify(scope), 'data-enabled': enabled, 'data-target': target?.recordId }, sessionId, createElement('button', { onClick: onReturnToConversation }, '대화로 돌아가기')) }));
vi.mock('./account-segments', () => ({ AccountSegments: () => null }));
vi.mock('./delete-session-dialog', () => ({ DeleteSessionDialog: ({ onDeleted }: {
  onDeleted: (result: { purged_sessions: number; project_hash: string; project_blocked: boolean }) => void;
}) => createElement('button', { onClick: () => onDeleted({ purged_sessions: 0, project_hash: '', project_blocked: false }) }, '삭제 확인') }));
vi.mock('./blocked-projects-dialog', () => ({ BlockedProjectsDialog: () => null }));

const row = (id: string): SessionOverview => ({
  session_id: id, profile_email: 'owner@example.test', model: '', agent: 'claude',
  start_time: '2026-10-08T00:00:00Z', end_time: '2026-10-08T01:00:00Z',
  input_tokens: 1, output_tokens: 2, cost_usd: 0.01, event_count: 1, has_sync: false,
});
const mount = (session: SessionOverview | null = row('A'), scope?: string, scrollAnchor?: string, searchTarget?: { sessionId: string; turnKey?: string; ownScope?: boolean }, onOpenAnalysis?: () => void) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onDeleted = vi.fn();
  const props = { session, searchTarget, mode: 'assembled' as const, onNavigate: vi.fn(), onClassificationSaved: vi.fn(),
    onDeleted, analysisLoginEmail: scope, scrollAnchor, onOpenAnalysis };
  const view = render(createElement(QueryClientProvider, { client }, createElement(SessionViewer, props)));
  return { client, ...view, onDeleted,
    update: (next: SessionOverview | null, nextScope = scope) => view.rerender(createElement(QueryClientProvider, { client }, createElement(SessionViewer, { ...props, session: next, analysisLoginEmail: nextScope }))),
  };
};

beforeEach(() => {
  state.userId = 7; state.isAdmin = false; state.detailMounts = 0;
  vi.mocked(fetchConversationWindow).mockReset();
  vi.mocked(fetchSessionAnalysis).mockReset().mockImplementation(async (id) => ({ session_id: id, short_id: id, owner_view: true, state: 'building' }));
  vi.mocked(fetchSessionRepoStatus).mockReset().mockImplementation(async (id) => ({ session_id: id, owner_view: true }));
});
afterEach(cleanup);

describe('inline session viewer boundary', () => {
  it('leaves node URL selection to the trace workspace without a second tab navigation', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const onDetailTabChange = vi.fn();
    render(createElement(QueryClientProvider, { client }, createElement(SessionViewer, {
      session: row('A'), detailTab: 'trace', mode: 'assembled', onNavigate: vi.fn(),
      onClassificationSaved: vi.fn(), onDetailTabChange,
    })));
    fireEvent.click(await screen.findByRole('button', { name: '실행 노드 선택' }));
    expect(onDetailTabChange).not.toHaveBeenCalled();
  });
  it('exposes two primary views and opens raw inspection inside trace', async () => {
    mount();
    expect(screen.getAllByRole('tab').map(tab => tab.textContent)).toEqual(['대화', '트레이스']);
    fireEvent.click(screen.getByRole('button', { name: '지시문·훅 확인' }));
    expect(screen.getByRole('tab', { name: '트레이스' }).getAttribute('aria-selected')).toBe('true');
    expect(await screen.findByTestId('workspace')).toBeTruthy();
  });
  it('returns keyboard focus to the message that opened inspection', async () => {
    mount();
    const origin = screen.getByRole('button', { name: '메시지 검사' });
    origin.focus();
    fireEvent.click(origin);
    await screen.findByTestId('workspace');
    const back = screen.getByRole('button', { name: '대화로 돌아가기' });
    back.focus();
    fireEvent.click(back);
    await waitFor(() => expect(document.activeElement).toBe(origin));
  });
  it('resolves an inspection turn link to its exact collected opener without loading analysis', async () => {
    vi.mocked(fetchConversationWindow).mockResolvedValue({ offset: 12, records: [{ ts: '2026-10-18T00:00:00Z', session_id: 'A', project_hash: '', record_type: 'user', profile_email: '', model: '', source_file: 'A.jsonl',
      view: { kind: 'message', role: 'user', text: 'request two', anchor_id: 'request-2', dedupe_key: '' } }] });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(createElement(QueryClientProvider, { client }, createElement(SessionViewer, { session: row('A'), searchTarget: { sessionId: 'A', turnKey: 'turn-2' }, detailTab: 'analysis', mode: 'assembled', onNavigate: vi.fn(), onClassificationSaved: vi.fn() })));
    await waitFor(() => expect(screen.getByTestId('workspace').dataset.target).toBe('request-2'));
    expect(fetchConversationWindow).toHaveBeenCalledWith(expect.objectContaining({ sessionId: 'A', turnKey: 'turn-2' }));
    expect(fetchSessionAnalysis).not.toHaveBeenCalled();
  });
  it('keeps ordinary conversation and layout entry free of analysis demand', async () => {
    state.isAdmin = true;
    const onOpen = vi.fn();
    mount(row('A'), 'scope@example.test', undefined, undefined, onOpen);
    expect(screen.queryByRole('link', { name: '분석' })).toBeNull();
    expect(fetchSessionAnalysis).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: '지시문·훅 확인' }));
    const workspace = await screen.findByTestId('workspace');
    expect(workspace.dataset.scope).toBe(JSON.stringify({ loginEmail: 'scope@example.test' }));
    expect(workspace.dataset.sessionId).toBe('A');
    expect(onOpen).toHaveBeenCalledOnce();
    expect(fetchSessionAnalysis).not.toHaveBeenCalled();
  });
  it('allows inspection while aggregate analysis is still building', async () => {
    mount({ ...row('A'), analysis: { state: 'building' } });
    const tab = screen.getByRole('button', { name: '지시문·훅 확인' });
    expect(tab.hasAttribute('disabled')).toBe(false);
    fireEvent.click(tab);
    expect(await screen.findByTestId('workspace')).toBeTruthy();
    expect(fetchSessionAnalysis).not.toHaveBeenCalled();
  });
  it('does not block inspection on repo-status build state', async () => {
    vi.mocked(fetchSessionRepoStatus).mockResolvedValue({ session_id: 'A', owner_view: true, analysis_state: 'building' });
    mount(row('A'));
    const tab = screen.getByRole('button', { name: '지시문·훅 확인' });
    await waitFor(() => expect(tab).toHaveProperty('disabled', false));
    fireEvent.click(tab);
    expect(await screen.findByTestId('workspace')).toBeTruthy();
    expect(fetchSessionAnalysis).not.toHaveBeenCalled();
  });
  it('keeps administrator own-search independent of an external account filter', async () => {
    state.isAdmin = true;
    mount(null, 'external@test', undefined, { sessionId: 'outside-list' });
    expect((await screen.findByTestId('workspace')).dataset.scope).toBe('{}');
  });
  it('uses the explicit scoped detail identity for an out-of-list administrator link', async () => {
    state.isAdmin = true;
    mount(null, 'scope@test', undefined, { sessionId: 'outside-list', ownScope: false });
    expect((await screen.findByTestId('workspace')).dataset.scope).toBe(JSON.stringify({ loginEmail: 'scope@test' }));
    expect(screen.getByTestId('conversation').dataset.scope).toBe(JSON.stringify({ loginEmail: 'scope@test' }));
  });
  it('connects the selected session status bar to the owner P4 repo signal', async () => {
    vi.mocked(fetchSessionRepoStatus).mockResolvedValue({ session_id: 'A', owner_view: true, repo_status: {
      branch: 'feature/session', branch_state: 'observed', branch_source: 'session_record_git_branch',
      uncommitted_edited_files: 2, uncommitted_state: 'observed', uncommitted_source: 'session_git_snapshot',
      uncommitted_scope: 'session_edited_files', uncommitted_reason: null, changed_at: '2026-10-09T08:00:00Z',
    } });
    mount();
    const status = await screen.findByRole('status', { name: '저장소 상태' });
    await waitFor(() => expect(status.textContent).toContain('feature/session'));
    expect(status.textContent).toContain('미커밋 2건');
    expect(status.getAttribute('title')).toContain('세션 편집 파일');
  });
  it('preserves the conversation instance across keyboard tab selection', async () => {
    mount();
    const conversation = screen.getByTestId('conversation');
    fireEvent.keyDown(screen.getByRole('tab', { name: '대화' }), { key: 'End' });
    await screen.findByTestId('trace');
    expect(screen.getByTestId('conversation').dataset.active).toBe('false');
    fireEvent.keyDown(screen.getByRole('tab', { name: '트레이스' }), { key: 'Home' });
    expect(screen.getByTestId('conversation').dataset.active).toBe('true');
    expect(state.detailMounts).toBe(1);
    expect(screen.getByTestId('conversation')).toBe(conversation);
  });
  it('passes an exact out-of-list turn to the bounded conversation read without demanding analysis', () => {
    mount(null, undefined, undefined, { sessionId: 'outside-list', turnKey: 'turn-two' });
    expect(screen.getByTestId('conversation').dataset.turnKey).toBe('turn-two');
    expect(fetchSessionAnalysis).not.toHaveBeenCalled();
    expect(screen.getByRole('tab', { name: '대화' }).getAttribute('aria-selected')).toBe('true');
  });
  it('passes a new selected session and account scope to the workspace', async () => {
    state.isAdmin = true;
    const { update } = mount(row('A'), 'first@test');
    fireEvent.click(screen.getByRole('button', { name: '지시문·훅 확인' }));
    await screen.findByTestId('workspace');
    update(row('B'), 'second@test');
    expect(screen.getByTestId('workspace').dataset.sessionId).toBe('B');
    expect(screen.getByTestId('workspace').dataset.scope).toBe(JSON.stringify({ loginEmail: 'second@test' }));
    expect(screen.getByTestId('conversation')).toBeTruthy();
  });
  it('switches to another session without a late analysis response or a conversation remount on inspection', async () => {
    const { update } = mount(row('A'));
    fireEvent.click(screen.getByRole('button', { name: '지시문·훅 확인' }));
    await screen.findByTestId('workspace');
    update(row('B'));
    fireEvent.click(screen.getByRole('tab', { name: '대화' }));
    expect(screen.getByTestId('conversation').textContent).toContain('B:assembled:');
    expect(fetchSessionAnalysis).not.toHaveBeenCalled();
    expect(state.detailMounts).toBe(2);
  });
  it('keeps the deletion action available for a turn whose metadata was not collected', () => {
    state.isAdmin = true;
    const { onDeleted } = mount(row('A'), undefined, undefined, { sessionId: 'A', turnKey: 'missing' });
    expect(screen.getByTestId('conversation').dataset.turnKey).toBe('missing');
    fireEvent.click(screen.getByRole('button', { name: '세션 삭제' }));
    fireEvent.click(screen.getByRole('button', { name: '삭제 확인' }));
    expect(onDeleted).toHaveBeenCalledWith('A');
    expect(fetchSessionAnalysis).not.toHaveBeenCalled();
  });
  it('invalidates inactive analysis lists after deletion without starting their requests from the conversation', async () => {
    state.isAdmin = true;
    const { client, onDeleted } = mount(row('A'));
    const analysisRows = vi.fn(async () => ({ pages: [[row('A'), row('B')]], pageParams: [0] }));
    const analysisCount = vi.fn(async () => ({ count: 2, unattributed: 0 }));
    const rowsKey = ['session-overview', 'inactive-analysis', true];
    const countKey = ['session-overview-count', 'inactive-analysis', true];
    await client.fetchQuery({ queryKey: rowsKey, queryFn: analysisRows });
    await client.fetchQuery({ queryKey: countKey, queryFn: analysisCount });
    analysisRows.mockClear();
    analysisCount.mockClear();
    fireEvent.click(screen.getByRole('button', { name: '세션 삭제' }));
    fireEvent.click(screen.getByRole('button', { name: '삭제 확인' }));
    expect(onDeleted).toHaveBeenCalledWith('A');
    expect(client.getQueryData<{ pages: SessionOverview[][] }>(rowsKey)?.pages[0].map(session => session.session_id)).toEqual(['B']);
    expect(client.getQueryState(rowsKey)?.isInvalidated).toBe(true);
    expect(client.getQueryState(countKey)?.isInvalidated).toBe(true);
    expect(analysisRows).not.toHaveBeenCalled();
    expect(analysisCount).not.toHaveBeenCalled();
    expect(fetchSessionAnalysis).not.toHaveBeenCalled();
  });
});

// 아래는 상단 identity 줄의 하네스 표기만 본다. 버전 조회가 끝나야 그 줄이 나타나므로
// 이 묶음에서만 버전 조회가 값을 돌려주게 한다.
const harnessSession = (over: Partial<SessionOverview>): SessionOverview => ({
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

// 헤더 줄의 cctrace/하네스 버전 span.
const harnessLine = async (over: Partial<SessionOverview>): Promise<string> => {
  mount(harnessSession(over));
  return (await screen.findByText(/cctrace /)).textContent ?? '';
};

describe('SessionViewer 헤더의 하네스 표기', () => {
  beforeEach(() => {
    vi.mocked(fetchAppVersionInfo).mockResolvedValue(
      { session_record_version_since: 'v0.5.0' } as Awaited<ReturnType<typeof fetchAppVersionInfo>>);
  });
  afterEach(() => {
    vi.mocked(fetchAppVersionInfo).mockResolvedValue(null as unknown as Awaited<ReturnType<typeof fetchAppVersionInfo>>);
  });

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
