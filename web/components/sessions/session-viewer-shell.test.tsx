// @vitest-environment jsdom
import { createElement, useEffect, useState } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { SessionViewer } from './session-viewer';
import { fetchSessionAnalysis } from '@/lib/api';
import type { SessionOverview } from '@/lib/types';

const state = vi.hoisted(() => ({ detailMounts: 0, workspaceMounts: 0, traceMounts: 0 }));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => ({ user: { id: 7 }, isAdmin: true }) }));
vi.mock('@/lib/api', () => ({
  fetchSessionAnalysis: vi.fn(), fetchSessionRepoStatus: vi.fn().mockResolvedValue({ owner_view: false }),
  fetchAppVersionInfo: vi.fn().mockResolvedValue(null), fetchDeletionPolicy: vi.fn().mockResolvedValue({ allow_owner_delete: false }),
}));
vi.mock('./conversation-reader', () => ({ ConversationReader: ({ active, onInspectContext }: { active: boolean; onInspectContext?: () => void }) => {
  useState(() => ++state.detailMounts);
  return createElement('div', { 'data-testid': 'conversation', 'data-active': active }, createElement('input', { 'aria-label': '대화 상태', defaultValue: '' }), createElement('button', { onClick: onInspectContext }, '지시문·훅 확인'));
} }));
vi.mock('./inspection/session-inspector', () => ({ SessionInspector: ({ sessionId, scope, enabled }: { sessionId: string; scope?: { loginEmail?: string }; enabled?: boolean }) => {
  useEffect(() => { state.workspaceMounts += 1; }, []);
  return createElement('div', { 'data-testid': 'workspace', 'data-enabled': enabled, 'data-scope': scope?.loginEmail }, sessionId,
    createElement('input', { 'aria-label': '분석 상태', defaultValue: '' }));
} }));
vi.mock('./trace/trace-workspace', () => ({ SessionTraceWorkspace: ({ scope, enabled, onInspect, inspectionPanel }: { inspectionPanel?: import('react').ReactNode; scope?: { loginEmail?: string }; enabled: boolean; onInspect?: (target: { recordId: string; sourceFile: string }) => void }) => {
  useEffect(() => { state.traceMounts += 1; }, []);
  return createElement('div', { 'data-testid': 'trace', 'data-enabled': enabled, 'data-scope': scope?.loginEmail },
    createElement('input', { 'aria-label': '트레이스 선택', defaultValue: '' }),
    createElement('button', { onClick: () => onInspect?.({ recordId: 'actual-record', sourceFile: 'A.jsonl' }) }, '기록검사에서 보기'), inspectionPanel);
} }));
vi.mock('./account-segments', () => ({ AccountSegments: () => null }));
vi.mock('./delete-session-dialog', () => ({ DeleteSessionDialog: () => null }));
vi.mock('./blocked-projects-dialog', () => ({ BlockedProjectsDialog: () => null }));
const row: SessionOverview = { session_id: 'A', profile_email: 'owner@test', model: '', agent: 'claude', start_time: '', end_time: '', input_tokens: 0, output_tokens: 0, cost_usd: 0, event_count: 1, has_sync: true };
const mount = () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onOpenAnalysis = vi.fn();
  const props = { session: row, mode: 'assembled' as const, analysisLoginEmail: 'scope@test', onNavigate: vi.fn(), onPickPersonalNote: vi.fn(), onOpenAnalysis };
  const view = render(createElement(QueryClientProvider, { client }, createElement(SessionViewer, props)));
  return { ...view, onOpenAnalysis, update: (extra: object) => view.rerender(createElement(QueryClientProvider, { client }, createElement(SessionViewer, { ...props, ...extra }))) };
};
beforeEach(() => { state.detailMounts = 0; state.workspaceMounts = 0; state.traceMounts = 0; vi.mocked(fetchSessionAnalysis).mockReset(); });
afterEach(cleanup);

it('opens record inspection lazily without aggregate analysis demand', async () => {
  const { onOpenAnalysis } = mount();
  expect(screen.queryByTestId('workspace')).toBeNull();
  expect(screen.queryByRole('link', { name: '분석' })).toBeNull();
  expect(screen.queryByRole('button', { name: /타임라인/ })).toBeNull();
  expect(fetchSessionAnalysis).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole('button', { name: '지시문·훅 확인' }));
  const workspace = await screen.findByTestId('workspace');
  expect(workspace.getAttribute('data-scope')).toBe('scope@test');
  expect(workspace.getAttribute('data-enabled')).toBe('true');
  expect(onOpenAnalysis).toHaveBeenCalledOnce();
  expect(fetchSessionAnalysis).not.toHaveBeenCalled();
});

it('keeps both panels mounted and pauses the hidden workspace across tabs and expansion', async () => {
  const { update } = mount();
  const conversation = screen.getByRole('textbox', { name: '대화 상태' });
  fireEvent.change(conversation, { target: { value: '대화 필터' } });
  fireEvent.click(screen.getByRole('button', { name: '지시문·훅 확인' }));
  await screen.findByTestId('workspace');
  const analysis = screen.getByRole('textbox', { name: '분석 상태' });
  fireEvent.change(analysis, { target: { value: '선택 턴' } });
  update({ expanded: true });
  expect(screen.getByRole('button', { name: '상세 영역 복원' }).querySelector('.lucide-minimize2')).toBeTruthy();
  fireEvent.click(screen.getByRole('tab', { name: '대화' }));
  await waitFor(() => expect(screen.getByTestId('workspace').getAttribute('data-enabled')).toBe('false'));
  expect(conversation).toHaveProperty('value', '대화 필터');
  fireEvent.click(screen.getByRole('button', { name: '지시문·훅 확인' }));
  expect(screen.getByRole('textbox', { name: '분석 상태' })).toBe(analysis);
  expect(analysis).toHaveProperty('value', '선택 턴');
  expect(state.detailMounts).toBe(1);
  expect(state.workspaceMounts).toBe(1);
});

it('can reopen a collapsed list when no session is selected', () => {
  const { update } = mount();
  const toggleList = vi.fn();
  update({ session: null, listCollapsed: true, onToggleList: toggleList });
  fireEvent.click(screen.getByRole('button', { name: '세션 목록 펼치기' }));
  expect(toggleList).toHaveBeenCalledOnce();
});

it('keeps raw inspection inactive when the viewer is hidden, without demanding canonical analysis', async () => {
  const { update } = mount();
  fireEvent.click(screen.getByRole('button', { name: '지시문·훅 확인' }));
  await screen.findByTestId('workspace');
  update({ enabled: false });
  expect(screen.getByTestId('workspace').dataset.enabled).toBe('false');
  expect(fetchSessionAnalysis).not.toHaveBeenCalled();
});

it('restores trace as a lazy peer of conversation and inspection and preserves its selection', async () => {
  const { update } = mount();
  expect(screen.queryByTestId('trace')).toBeNull();
  fireEvent.click(screen.getByRole('tab', { name: '트레이스' }));
  const trace = await screen.findByTestId('trace');
  expect(trace.dataset.scope).toBe('scope@test');
  expect(trace.dataset.enabled).toBe('true');
  expect(screen.getByTestId('conversation').dataset.active).toBe('false');
  const selection = screen.getByRole('textbox', { name: '트레이스 선택' });
  fireEvent.change(selection, { target: { value: 'turn-2:event-3:range' } });
  fireEvent.click(screen.getByRole('button', { name: '기록검사에서 보기' }));
  await screen.findByTestId('workspace');
  expect(trace.dataset.enabled).toBe('true');
  fireEvent.click(screen.getByRole('tab', { name: '트레이스' }));
  expect(screen.getByRole('textbox', { name: '트레이스 선택' })).toBe(selection);
  expect(selection).toHaveProperty('value', 'turn-2:event-3:range');
  expect(state.traceMounts).toBe(1);
  update({ enabled: false });
  expect(trace.dataset.enabled).toBe('false');
});

it('moves keyboard focus across the two primary views', async () => {
  mount();
  const conversation = screen.getByRole('tab', { name: '대화' });
  fireEvent.keyDown(conversation, { key: 'ArrowRight' });
  const trace = screen.getByRole('tab', { name: '트레이스' });
  expect(trace.getAttribute('aria-selected')).toBe('true');
  expect(document.activeElement).toBe(trace);
  fireEvent.keyDown(trace, { key: 'ArrowRight' });
  expect(conversation.getAttribute('aria-selected')).toBe('true');
  fireEvent.keyDown(conversation, { key: 'ArrowLeft' });
  expect(trace.getAttribute('aria-selected')).toBe('true');
  fireEvent.keyDown(trace, { key: 'Home' });
  expect(conversation.getAttribute('aria-selected')).toBe('true');
  fireEvent.keyDown(conversation, { key: 'End' });
  expect(trace.getAttribute('aria-selected')).toBe('true');
  await screen.findByTestId('trace');
});

it('marks trace source navigation to clear unrelated inspection filters', async () => {
  const { update } = mount();
  const onTarget = vi.fn();
  update({ onInspectionTargetChange: onTarget });
  fireEvent.click(screen.getByRole('tab', { name: '트레이스' }));
  fireEvent.click(await screen.findByRole('button', { name: '기록검사에서 보기' }));
  expect(onTarget).toHaveBeenCalledWith({ recordId: 'actual-record', sourceFile: 'A.jsonl' }, true);
});
