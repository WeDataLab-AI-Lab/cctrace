// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import type { ComponentProps } from 'react';
import type { ConversationReturnState } from '@/lib/sessions-return-state';
import { ConversationReader } from './conversation-reader';
import { clearReadingMemory, rememberReading } from '@/lib/conversation-reading';

const fetchWindow = vi.hoisted(() => vi.fn());
const detailProps = vi.hoisted(() => ({ value: undefined as unknown }));
vi.mock('@/lib/conversation-window', () => ({ fetchConversationWindow: fetchWindow }));
vi.mock('./session-detail', () => ({ SessionDetail: (props: {
  initialPage?: { offset: number; revision?: number }; scrollAnchor?: string; restoredState?: unknown;
  onLatestRequest?: () => void; onRequestNavigate?: (direction: string, anchor: string) => void;
  onOpenTurn?: (turnKey: string) => void;
}) => {
  detailProps.value = props;
  return <div>
    <span>window:{props.initialPage?.offset}:{props.scrollAnchor}</span>
    <span data-testid="revision">{props.initialPage?.revision ?? 0}</span>
    <button type="button" onClick={props.onLatestRequest}>최근으로</button>
    <button type="button" onClick={() => props.onRequestNavigate?.('previous-request', 'reading-message')}>이전 요청</button>
    <button type="button" onClick={() => props.onRequestNavigate?.('next-request', 'reading-message')}>다음 요청</button>
    <button type="button" onClick={() => props.onOpenTurn?.('turn-behind-window')}>인덱스 요청</button>
  </div>;
} }));
afterEach(() => { cleanup(); fetchWindow.mockReset(); clearReadingMemory(); });
type Props = ComponentProps<typeof ConversationReader>;
const mount = (over: Partial<Props> = {}) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { client, ...render(<QueryClientProvider client={client}>
    <ConversationReader sessionId="A" memoryKey="owner:1:A" onNavigate={vi.fn()} active {...over} />
  </QueryClientProvider>) };
};

it('opens the recent request without fetching the whole session', async () => {
  fetchWindow.mockResolvedValue({ records: [], offset: 9000 });
  mount();
  expect(await screen.findByText('window:9000:')).toBeTruthy();
  expect(fetchWindow.mock.calls[0][0]).toMatchObject({ sessionId: 'A', start: 'latest-request' });
});
it('resolves an explicit message independently of loaded pages', async () => {
  fetchWindow.mockResolvedValue({ records: [], offset: 4200 });
  mount({ scrollAnchor: 'exact-message' });
  expect(await screen.findByText('window:4200:exact-message')).toBeTruthy();
  expect(fetchWindow.mock.calls[0][0]).toMatchObject({ anchor: 'exact-message' });
});
it.each([{ scrollAnchor: 'exact-message' }, { turnKey: 'turn-2' }, { scrollFromTs: '2026-10-01T00:00:00Z' }])('gives explicit destination %j priority over the saved scroll, tools and asides', async destination => {
  rememberReading('owner:1:A', { offset: 500, readingAnchor: 'old', scrollTop: 300, visible: 80, promptsOnly: false, openTools: ['old:tool'], openAsides: ['old:aside'] });
  fetchWindow.mockResolvedValue({ records: [], offset: 4200 });
  mount(destination);
  await screen.findByText(/window:4200:/);
  expect((detailProps.value as { restoredState?: unknown }).restoredState).toBeUndefined();
});
it('restores the last read anchor, pixel offset and expanded tools and asides on revisit', async () => {
  const saved = { offset: 500, readingAnchor: 'read', anchorOffset: -24, scrollTop: 300, visible: 80, promptsOnly: false, openTools: ['read:tool'], openAsides: ['read:aside'] };
  rememberReading('owner:1:A', saved);
  fetchWindow.mockResolvedValue({ records: [], offset: 4200 });
  mount();
  await screen.findByText(/window:4200:/);
  expect(fetchWindow.mock.calls[0][0]).toMatchObject({ anchor: 'read' });
  expect((detailProps.value as { restoredState?: unknown }).restoredState).toMatchObject(saved);
});
it('requests the actual latest window again and resets selection even when the resolved offset is unchanged', async () => {
  fetchWindow.mockResolvedValue({ records: [], offset: 4200 });
  mount({ scrollAnchor: 'historical' });
  await screen.findByText('window:4200:historical');
  fireEvent.click(screen.getByRole('button', { name: '최근으로' }));
  await waitFor(() => expect(fetchWindow).toHaveBeenCalledTimes(2));
  expect(fetchWindow.mock.calls[1][0]).toMatchObject({ start: 'latest-request' });
  expect(fetchWindow.mock.calls[1][0]).not.toHaveProperty('anchor');
  await waitFor(() => expect(screen.getByTestId('revision').textContent).toBe('1'));
  expect(screen.getByText('window:4200:')).toBeTruthy();
});
it.each(['이전 요청', '다음 요청'])('resolves %s from the message currently being read without losing scope', async label => {
  const scope = { userID: 'owner', loginEmail: 'account@example.test' };
  fetchWindow.mockResolvedValueOnce({ records: [], offset: 4200 }).mockResolvedValueOnce({ records: [], offset: 3000 });
  mount({ scope });
  await screen.findByText('window:4200:');
  fireEvent.click(screen.getByRole('button', { name: label }));
  await screen.findByText('window:3000:');
  expect(fetchWindow.mock.calls[1][0]).toMatchObject({ scope, anchor: 'reading-message', direction: label === '이전 요청' ? 'previous-request' : 'next-request' });
});
it('retains the readable conversation when an adjacent request is unavailable', async () => {
  fetchWindow.mockResolvedValueOnce({ records: [], offset: 4200 }).mockRejectedValueOnce(Object.assign(new Error('no adjacent'), { code: 'no_adjacent_request' }));
  mount();
  await screen.findByText('window:4200:');
  fireEvent.click(screen.getByRole('button', { name: '다음 요청' }));
  expect(await screen.findByRole('status')).toHaveProperty('textContent', '다음 요청을 확인할 수 없습니다.');
  expect(screen.getByText('window:4200:')).toBeTruthy();
  expect(screen.queryByRole('alert')).toBeNull();
});
it('opens a request known only from the session index by its turn key', async () => {
  fetchWindow.mockResolvedValueOnce({ records: [], offset: 4200 }).mockResolvedValueOnce({ records: [], offset: 120 });
  mount();
  await screen.findByText('window:4200:');
  fireEvent.click(screen.getByRole('button', { name: '인덱스 요청' }));
  await screen.findByText('window:120:');
  expect(fetchWindow.mock.calls[1][0]).toMatchObject({ turnKey: 'turn-behind-window' });
});
it('shows a failed initial request as an error with retry', async () => {
  fetchWindow.mockRejectedValue(new Error('503'));
  mount();
  expect(await screen.findByRole('alert')).toBeTruthy();
  expect(screen.getByRole('button', { name: '다시 시도' })).toBeTruthy();
  expect(screen.queryByText(/대화 기록이 없습니다/)).toBeNull();
});

it('cancels a hidden request jump and enables navigation again on return', async () => {
  fetchWindow.mockResolvedValueOnce({ records: [], offset: 4200 }).mockImplementationOnce(({ signal }: { signal: AbortSignal }) => new Promise((_, reject) => signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')))));
  const { client, rerender } = mount();
  await screen.findByText('window:4200:');
  fireEvent.click(screen.getByRole('button', { name: '다음 요청' }));
  await waitFor(() => expect((detailProps.value as { requestNavigationPending?: boolean }).requestNavigationPending).toBe(true));
  rerender(<QueryClientProvider client={client}><ConversationReader sessionId="A" memoryKey="owner:1:A" onNavigate={vi.fn()} active={false} /></QueryClientProvider>);
  await waitFor(() => expect((detailProps.value as { requestNavigationPending?: boolean }).requestNavigationPending).toBe(false));
  expect(screen.queryByRole('alert')).toBeNull();
  expect(screen.getByText('window:4200:')).toBeTruthy();
});
it('a new explicit link supersedes a late request-navigation result', async () => {
  let resolveJump!: (window: { records: never[]; offset: number }) => void;
  fetchWindow.mockResolvedValueOnce({ records: [], offset: 4200 }).mockImplementationOnce(() => new Promise(resolve => { resolveJump = resolve; })).mockResolvedValueOnce({ records: [], offset: 5000 });
  const { client, rerender } = mount({ scrollAnchor: 'old' });
  await screen.findByText('window:4200:old');
  fireEvent.click(screen.getByRole('button', { name: '다음 요청' }));
  rerender(<QueryClientProvider client={client}><ConversationReader sessionId="A" memoryKey="owner:1:A" onNavigate={vi.fn()} active scrollAnchor="new" /></QueryClientProvider>);
  await screen.findByText('window:5000:new');
  await act(async () => { resolveJump({ records: [], offset: 9999 }); });
  expect(screen.getByText('window:5000:new')).toBeTruthy();
});

it.each([undefined, 'origin-link'])('restores investigation origin %s after inspecting another message and going back', async initialAnchor => {
  fetchWindow.mockResolvedValueOnce({ records: [], offset: 5000 })
    .mockResolvedValueOnce({ records: [], offset: 4000 })
    .mockResolvedValueOnce({ records: [], offset: 3000 })
    .mockResolvedValueOnce({ records: [], offset: 4050 });
  const { client, rerender } = mount({ scrollAnchor: initialAnchor });
  const view = (active: boolean, scrollAnchor?: string) => <QueryClientProvider client={client}><ConversationReader sessionId="A" memoryKey="owner:1:A" onNavigate={vi.fn()} active={active} scrollAnchor={scrollAnchor} /></QueryClientProvider>;
  await screen.findByText(`window:5000:${initialAnchor ?? ''}`);
  fireEvent.click(screen.getByRole('button', { name: '이전 요청' }));
  await screen.findByText('window:4000:');
  const origin: ConversationReturnState = { sessionId: 'A', mode: 'assembled', readingAnchor: 'origin-reading', anchorOffset: -16, scrollTop: 750, visible: 80, promptsOnly: false, openTools: ['origin:tool'], openAsides: ['origin:aside'] };
  act(() => (detailProps.value as { onRememberState: (state: ConversationReturnState) => void }).onRememberState(origin));
  rerender(view(false, initialAnchor)); // inspect from the reading origin
  rerender(view(true, 'other-message')); // show the inspected item in conversation
  await screen.findByText('window:3000:other-message');
  act(() => (detailProps.value as { onRememberState: (state: ConversationReturnState) => void }).onRememberState({ ...origin, readingAnchor: 'other-message', scrollTop: 0, openTools: [] }));
  rerender(view(false, initialAnchor)); // browser Back to the original inspection entry
  rerender(view(true, initialAnchor)); // return to the conversation where investigation began
  await screen.findByText('window:4050:');
  expect(fetchWindow.mock.calls.at(-1)?.[0]).toMatchObject({ anchor: 'origin-reading' });
  expect((detailProps.value as { restoredState?: unknown }).restoredState).toMatchObject(origin);
});
it('an active explicit link still wins over an earlier snapshot for that destination', async () => {
  fetchWindow.mockResolvedValueOnce({ records: [], offset: 100 }).mockResolvedValueOnce({ records: [], offset: 200 });
  const { client, rerender } = mount({ scrollAnchor: 'destination' });
  const view = (anchor: string) => <QueryClientProvider client={client}><ConversationReader sessionId="A" memoryKey="owner:1:A" onNavigate={vi.fn()} active scrollAnchor={anchor} /></QueryClientProvider>;
  await screen.findByText('window:100:destination');
  act(() => (detailProps.value as { onRememberState: (state: ConversationReturnState) => void }).onRememberState({ sessionId: 'A', mode: 'assembled', readingAnchor: 'later-reading', scrollTop: 100, visible: 20, promptsOnly: false }));
  rerender(view('elsewhere'));
  await screen.findByText('window:200:elsewhere');
  rerender(view('destination'));
  await screen.findByText('window:100:destination');
  expect((detailProps.value as { restoredState?: unknown }).restoredState).toBeUndefined();
});

it('keeps identical anchors from different source files as distinct destinations', async () => {
  fetchWindow.mockResolvedValueOnce({ records: [], offset: 100 }).mockResolvedValueOnce({ records: [], offset: 200 });
  const { client, rerender } = mount({ scrollAnchor: 'same-record', scrollSourceFile: '/one/session.jsonl' });
  await screen.findByText('window:100:same-record');
  expect(fetchWindow.mock.calls[0][0]).toMatchObject({ anchor: 'same-record', sourceFile: '/one/session.jsonl' });
  rerender(<QueryClientProvider client={client}><ConversationReader sessionId="A" memoryKey="owner:1:A" onNavigate={vi.fn()} active scrollAnchor="same-record" scrollSourceFile="/two/session.jsonl" /></QueryClientProvider>);
  await screen.findByText('window:200:same-record');
  expect(fetchWindow.mock.calls[1][0]).toMatchObject({ anchor: 'same-record', sourceFile: '/two/session.jsonl' });
});

it('forwards the optional turn-key trace callback to the loaded conversation', async () => {
  fetchWindow.mockResolvedValue({ records: [], offset: 100 });
  const onOpenTraceTurn = vi.fn();
  mount({ onOpenTraceTurn });
  await screen.findByText('window:100:');
  expect((detailProps.value as { onOpenTrace?: unknown }).onOpenTrace).toBeTruthy();
});
