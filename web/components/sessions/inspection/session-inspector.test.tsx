// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { SessionInspector } from './session-inspector';
import { fetchInspectionPage } from '@/lib/session-inspection';
import type { SessionRecord } from '@/lib/types';

vi.mock('@/lib/session-inspection', async importOriginal => ({ ...await importOriginal<object>(), fetchInspectionPage: vi.fn() }));
const auth = vi.hoisted(() => ({ user: { id: 1, role: 'user' } as { id: number; role: string } | null }));
vi.mock('@/components/common/auth-context', () => ({ useAuth: () => auth }));
const fetchPage = vi.mocked(fetchInspectionPage);
const makeRecord = (uuid: string, raw: unknown, overrides: Partial<SessionRecord> = {}): SessionRecord => ({ uuid, raw, ts:'2026-10-17T12:00:00Z',session_id:'s',project_hash:'p',record_type:'assistant',profile_email:'',model:'',view:{anchor_id:uuid,dedupe_key:'',kind:'message',role:'assistant'},...overrides });
const mount = (props: Partial<React.ComponentProps<typeof SessionInspector>> = {}) => {
 const client = new QueryClient({defaultOptions:{queries:{retry:false}}});
 return { ...render(<QueryClientProvider client={client}><SessionInspector sessionId="s" onReturnToConversation={vi.fn()} {...props} /></QueryClientProvider>), client };
};
afterEach(cleanup);
beforeEach(() => { fetchPage.mockReset(); auth.user = { id: 1, role: 'user' }; });

describe('SessionInspector', () => {
 it('selects an exact call inside a multi-block record and shows its real result before source details', async () => {
  const call=makeRecord('call-row',{message:{role:'assistant',content:[{type:'text',text:'lead in'},{type:'tool_use',id:'c1',name:'Read',input:{file:'one'}},{type:'tool_use',id:'c2',name:'Bash',input:{cmd:'two'}}]}});
  const result=makeRecord('result-row',{message:{role:'user',content:[{type:'tool_result',tool_use_id:'c2',content:'actual stdout'}]}},{record_type:'user'});
  fetchPage.mockImplementation(async request => request.toolUseId ? {records:[call,result]} : {records:[call],selected_record:call});
  const onReturn=vi.fn();const onConversation=vi.fn();
  mount({target:{recordId:'call-row',callId:'c2'},onReturnToConversation:onReturn,onShowInConversation:onConversation});
  const detail=await screen.findByRole('article',{name:'기록 내용'});
  expect(within(detail).getByRole('heading',{name:'Bash'})).toBeTruthy();
  expect(await within(detail).findByText('actual stdout',{selector:'pre'})).toBeTruthy();
  expect(within(detail).getByText('/message/content/2')).toBeTruthy();
  const json=within(detail).getByText('수집 JSON').closest('details');expect(json?.open).toBe(false);
  fireEvent.click(screen.getByRole('button',{name:'대화에서 보기'}));expect(onConversation).toHaveBeenCalledWith({recordId:'call-row',sourceFile:undefined});
  fireEvent.click(screen.getByRole('button',{name:'대화로 돌아가기'}));expect(onReturn).toHaveBeenCalledOnce();
 });
 it('loads a direct target outside the loaded page and does not replace an invalid content pointer', async () => {
  const record=makeRecord('outside',{payload:{role:'developer',content:'actual developer policy'}},{agent:'codex',record_type:'developer',view:{anchor_id:'outside',dedupe_key:'',kind:'hidden'}});
  fetchPage.mockImplementation(async request => request.target ? {records:[],selected_record:record} : {records:[]});
  mount({target:{recordId:'outside',contentPath:'/wrong'}});
  expect(await screen.findByRole('alert')).toHaveProperty('textContent',expect.stringContaining('내용 위치'));
  expect(screen.queryByRole('article',{name:'기록 내용'})).toBeNull();
  expect(fetchPage).toHaveBeenCalledWith(expect.objectContaining({target:{recordId:'outside',contentPath:'/wrong'}}));
 });
 it('shows a failed first page as an error with a working retry', async () => {
  fetchPage.mockRejectedValueOnce(new Error('forbidden source')).mockResolvedValue({records:[]});
  mount();
  expect(await screen.findByRole('alert')).toHaveProperty('textContent',expect.stringContaining('forbidden source'));
  fireEvent.click(screen.getByRole('button',{name:'다시 시도'}));
  expect(await screen.findByText('이 범위에 수집된 기록이 없습니다.')).toBeTruthy();
 });
});

it('keeps already read records after a next-page failure and retries the failed boundary', async () => {
 const first=makeRecord('first',{message:{role:'assistant',content:'already read'}});
 const second=makeRecord('second',{message:{role:'assistant',content:'next record'}});
 let failNext=true;
 fetchPage.mockImplementation(async request => {
  if (!request.offset) return {records:[first],next_offset:100};
  if (failNext) throw new Error('next page failed');
  return {records:[second]};
 });
 mount();
 expect(await screen.findByText('already read')).toBeTruthy();
 fireEvent.click(screen.getByRole('button',{name:'다음 기록 읽기'}));
 expect(await screen.findByRole('alert')).toHaveProperty('textContent',expect.stringContaining('읽던 내용은 유지'));
 expect(screen.getByText('already read')).toBeTruthy();
 failNext=false;
 fireEvent.click(screen.getByRole('button',{name:'추가 기록 다시 시도'}));
 expect(await screen.findByText('next record')).toBeTruthy();
 expect(screen.getByText('already read')).toBeTruthy();
});

it('isolates collected raw across authenticated users and does not query without an identity', async () => {
 fetchPage.mockImplementation(async () => ({records:[makeRecord(`owner-${auth.user?.id}`,{message:{role:'assistant',content:`private owner ${auth.user?.id}`}})]}));
 const {client,rerender}=mount();
 expect(await screen.findByText('private owner 1')).toBeTruthy();
 auth.user={id:2,role:'user'};
 rerender(<QueryClientProvider client={client}><SessionInspector sessionId="s" onReturnToConversation={vi.fn()} /></QueryClientProvider>);
 expect(screen.queryByText('private owner 1')).toBeNull();
 expect(await screen.findByText('private owner 2')).toBeTruthy();
 const requests=fetchPage.mock.calls.length;
 auth.user=null;
 rerender(<QueryClientProvider client={client}><SessionInspector sessionId="s" onReturnToConversation={vi.fn()} /></QueryClientProvider>);
 expect(screen.queryByText('private owner 2')).toBeNull();
 expect(fetchPage.mock.calls).toHaveLength(requests);
});

it('aborts an in-flight snapshot when inspection is hidden', async () => {
 fetchPage.mockImplementation(() => new Promise(() => {}));
 const {client,rerender}=mount();
 await waitFor(() => expect(fetchPage).toHaveBeenCalledOnce());
 const signal=fetchPage.mock.calls[0][0].signal;
 expect(signal?.aborted).toBe(false);
 rerender(<QueryClientProvider client={client}><SessionInspector sessionId="s" enabled={false} onReturnToConversation={vi.fn()} /></QueryClientProvider>);
 expect(signal?.aborted).toBe(true);
});

it('clears a local selection when controlled navigation explicitly supplies a null target', async () => {
 const call=makeRecord('call-row',{message:{role:'assistant',content:[{type:'tool_use',id:'c',name:'Read',input:{file:'private'}}]}});
 const instructions=makeRecord('instructions-row',{attachment:{type:'instructions',files:[{path:'AGENTS.md',content:'session instructions'}]}},{record_type:'attachment'});
 fetchPage.mockImplementation(async request => request.target ? {records:[],selected_record:call} : {records:request.kind==='context'?[instructions]:[call]});
 const {client,rerender}=mount();
 fireEvent.click(await screen.findByRole('button',{name:/Read/}));
 expect(await screen.findByRole('article',{name:'기록 내용'})).toBeTruthy();
 rerender(<QueryClientProvider client={client}><SessionInspector sessionId="s" target={null} initialKind="context" onKindChange={vi.fn()} onReturnToConversation={vi.fn()} /></QueryClientProvider>);
 expect(await screen.findByRole('button',{name:/AGENTS.md/})).toBeTruthy();
 expect(screen.queryByRole('article',{name:'기록 내용'})).toBeNull();
 expect(screen.queryByText('private')).toBeNull();
 rerender(<QueryClientProvider client={client}><SessionInspector sessionId="s" target={{recordId:'call-row',callId:'c'}} initialKind="all" onKindChange={vi.fn()} onReturnToConversation={vi.fn()} /></QueryClientProvider>);
 expect(await screen.findByRole('article',{name:'기록 내용'})).toBeTruthy();
});

it('labels inherited records in both list and detail with their original session id', async () => {
 const inherited=makeRecord('ancestor-rules',{attachment:{type:'instructions',files:[{path:'AGENTS.md',content:'inherited instructions'}]}},{session_id:'ancestor-session-exact',record_type:'attachment'});
 fetchPage.mockResolvedValue({records:[inherited]});
 mount({target:{recordId:'ancestor-rules'}});
 const list=await screen.findByRole('region',{name:'수집 기록 목록'});
 expect(await within(list).findByText('이전 세션 기록')).toBeTruthy();
 expect(within(list).getByText('ancestor-session-exact')).toBeTruthy();
 const detail=await screen.findByRole('article',{name:'기록 내용'});
 expect(within(detail).getByText('이전 세션 기록')).toBeTruthy();
 expect(within(detail).getAllByText('ancestor-session-exact').length).toBeGreaterThan(0);
 expect(within(detail).getByText('inherited instructions')).toBeTruthy();
});

it('limits a call scope to matching events without hiding their complete collected JSON', async () => {
 const record=makeRecord('multi-call',{message:{role:'assistant',content:[{type:'tool_use',id:'read-id',name:'Read',input:{file:'wanted'}},{type:'tool_use',id:'bash-id',name:'Bash',input:{cmd:'other'}},{type:'tool_use',id:'agent-id',name:'Agent',input:{task:'other'}}]}});
 fetchPage.mockResolvedValue({records:[record]});
 mount({toolScope:'read-id',onToolScopeChange:vi.fn()});
 expect(await screen.findByRole('button',{name:/^Read/})).toBeTruthy();
 expect(screen.queryByRole('button',{name:/^Bash/})).toBeNull();
 expect(screen.queryByRole('button',{name:/^Agent/})).toBeNull();
 fireEvent.click(screen.getByRole('button',{name:/^Read/}));
 const detail=await screen.findByRole('article',{name:'기록 내용'});
 expect(within(detail).getByText('수집 JSON').closest('details')?.textContent).toContain('bash-id');
});
