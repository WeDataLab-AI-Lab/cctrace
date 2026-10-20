// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SessionRecord } from '@/lib/types';
import { fetchSessionRecordsPage } from '@/lib/api';
import { SessionDetail } from './session-detail';

vi.mock('@/lib/api', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/lib/api')>(),
  fetchSessionRecordsPage: vi.fn(),
}));
vi.mock('./timeline-navigator', () => ({ TimelineNavigator: () => null }));

const record = (uuid: string, role: 'user' | 'assistant', text: string): SessionRecord => ({
  ts: '2026-10-20T00:00:00Z', session_id: 'layout', project_hash: 'project', record_type: role,
  profile_email: 'owner@example.test', model: 'claude', uuid,
  view: { kind: 'message', role, text, anchor_id: uuid, dedupe_key: uuid },
});

afterEach(() => { cleanup(); vi.mocked(fetchSessionRecordsPage).mockReset(); });

describe('conversation speaker layout', () => {
  it('shares one first-line baseline for user and agent turns', () => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'layout', mode: 'assembled', onNavigate: vi.fn(),
      initialPage: { offset: 0, records: [record('user', 'user', 'Request body'), record('agent', 'assistant', 'Response body')] },
    })));
    const avatar = screen.getByText('U');
    const agent = screen.getByText('A');
    expect(avatar.className).toContain('mt-[7px]');
    expect(agent.className).toContain('mt-[7px]');
    expect(avatar.className).toContain('border-transparent');
    expect(document.querySelector('div[data-conversation-role="agent"]')?.className).toContain('bg-transparent');
  });

  it('keeps the user request visually stronger than the agent reading surface', () => {
    vi.stubGlobal('IntersectionObserver', class { observe() {} disconnect() {} });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(createElement(QueryClientProvider, { client }, createElement(SessionDetail, {
      sessionId: 'layout', mode: 'assembled', onNavigate: vi.fn(),
      initialPage: { offset: 0, records: [record('user', 'user', 'Request body'), record('agent', 'assistant', 'Response body')] },
    })));
    // The request is the filled surface; the answer sits on the canvas.
    expect(screen.getByText('Request body').closest('[data-conversation-role="user"]')?.className).toContain('bg-surface');
    expect(screen.getByText('Request body').closest('[data-conversation-role="user"]')?.className).toContain('border-border-strong');
    expect(screen.getByText('Response body').closest('[data-conversation-role="agent"]')?.className).toContain('bg-transparent');
    // One type scale for both speakers; the hierarchy comes from the surface, not size.
    expect(screen.getByText('Request body').closest('[data-conversation-role="user"]')?.className).toContain('text-xs');
    expect(screen.getByText('Response body').closest('[data-conversation-role="agent"]')?.className).toContain('text-xs');
  });
});
