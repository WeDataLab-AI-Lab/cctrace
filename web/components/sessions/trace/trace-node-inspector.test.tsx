// @vitest-environment jsdom
import { createElement } from 'react';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import type { TraceNodeDetail } from '@/lib/session-trace-node-types';
import { TraceNodeInspector } from './trace-node-inspector';
import { toolContextRecords } from './trace-context-test-fixture';

const node = { node_id: 'response-1', parent_id: null, kind: 'response', title: '운영 가이드 검토 답변', timestamp: '2026-10-18T08:32:18.000Z', started_at: null, ended_at: null, status: 'completed', has_children: false, child_count: 0, source_ref: 'assistant:7', evidence: ['session_log'], availability: 'observed' };
const detail: TraceNodeDetail = {
  session_id: 'session-1', node,
  input: { state: 'observed', value: { messages: [{ role: 'user', content: '요청 본문' }] } },
  output: { state: 'observed', value: '응답 본문' },
  context: { state: 'not_collected', reason: 'instruction_context_not_recorded' },
  usage: { source: 'session_log', scope: 'response', values: { input_tokens: 24000, output_tokens: 256, cache_read_tokens: 22100, cache_create_tokens: 200 } },
  raw_records: toolContextRecords(),
  request: { state: 'observed', body: { model: 'claude-sonnet-5', messages: [{ role: 'user', content: '하네스 기록 요청 구성' }] }, params: { max_tokens: 4096 }, digest: { expected: 'hash-a', actual: 'hash-a', state: 'match' }, missing_refs: [], source_refs: ['api-request:7', 'api-request-blob:3'] },
  notices: [],
};

afterEach(() => cleanup());

it('opens timing analysis from the selected detail header when a callback is connected', () => {
  const onOpenTiming = vi.fn();
  render(createElement(TraceNodeInspector, {
    selectedNodeId: node.node_id, detail, loading: false, error: false, onRetry: vi.fn(), onOpenTiming,
  }));

  const header = screen.getByRole('region', { name: '선택 노드 상세' }).querySelector('header');
  const button = screen.getByRole('button', { name: '시간 분석' });
  expect(header?.contains(button)).toBe(true);
  expect(button.hasAttribute('disabled')).toBe(false);
  fireEvent.click(button);
  expect(onOpenTiming).toHaveBeenCalledOnce();
});

it('explains why timing analysis is unavailable when the group reason or callback is missing', () => {
  const props = { selectedNodeId: node.node_id, detail, loading: false, error: false, onRetry: vi.fn() };
  const view = render(createElement(TraceNodeInspector, { ...props, timingDisabledReason: '요청 그룹을 확인할 수 없어 시간 분석을 열 수 없습니다.' }));
  expect(screen.getByRole('button', { name: '시간 분석' }).hasAttribute('disabled')).toBe(true);
  expect(screen.getByText('요청 그룹을 확인할 수 없어 시간 분석을 열 수 없습니다.')).toBeTruthy();

  view.rerender(createElement(TraceNodeInspector, props));
  expect(screen.getByRole('button', { name: '시간 분석' }).hasAttribute('disabled')).toBe(true);
  expect(screen.getByText('시간 분석 화면이 아직 연결되지 않았습니다.')).toBeTruthy();
});

it('shows selected request input in one untabbed panel, ordered as system, messages, tools, then settings', () => {
  const requestDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'request-input', kind: 'request', title: '모델 요청' },
    input: { state: 'not_applicable' }, output: { state: 'not_applicable' }, context: { state: 'not_applicable' }, usage: null,
    request: {
      ...detail.request!,
      body: {
        model: 'claude-sonnet-5',
        system: 'System instructions',
        messages: [{ role: 'developer', content: 'AGENTS.md body', cache_control: { type: 'ephemeral' } }, { role: 'user', content: 'Work request' }],
        tools: [{ name: 'Read', input_schema: { type: 'object' } }],
      },
      params: { max_tokens: 2048, temperature: 0.2 },
    },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'request-input', detail: requestDetail, loading: false, error: false, onRetry: vi.fn() }));

  expect(screen.queryByRole('tablist', { name: '선택 노드 자료' })).toBeNull();
  const panel = screen.getByRole('tabpanel');
  const content = panel.textContent ?? '';
  expect(content.indexOf('system')).toBeLessThan(content.indexOf('messages'));
  expect(content.indexOf('messages')).toBeLessThan(content.indexOf('tools'));
  expect(content.indexOf('tools')).toBeLessThan(content.indexOf('요청 설정'));
  expect(content).toContain('role: developer');
  expect(content).toContain('AGENTS.md body');
  expect(content).toContain('cache_control');
  expect(content).toContain('role: user');
  expect(content).toContain('Work request');
});

it('renders developer runtime notices as events while keeping role and meaning separate', () => {
  const runtimeNotice: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'runtime-notice', kind: 'instructions', title: '개발자 알림', role: 'developer', semantic: 'runtime_notice' },
    input: { state: 'observed', value: [{ type: 'text', text: 'The request was interrupted.' }] },
    output: { state: 'not_applicable' }, context: { state: 'not_applicable' }, usage: null,
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'runtime-notice', detail: runtimeNotice, loading: false, error: false, onRetry: vi.fn() }));

  const header = screen.getByRole('region', { name: '선택 노드 상세' }).querySelector('header');
  expect(header?.textContent).toContain('역할 · developer');
  expect(header?.textContent).toContain('의미 · runtime_notice');
  const panel = screen.getByRole('tabpanel', { name: '기록 내용' });
  expect(panel.textContent).toContain('The request was interrupted.');
  expect(within(panel).queryByRole('region', { name: '지시문' })).toBeNull();
});

it('loads more request input, opens its raw source, and compares only an explicitly entered request ID', () => {
  const onLoadInputPage = vi.fn();
  const onCompareRequest = vi.fn();
  const onOpenSource = vi.fn();
  const requestDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'paged-request', kind: 'request', title: '모델 요청', role: 'assistant', semantic: 'tool_dispatch', source_kind: 'api_request' },
    input: { state: 'not_applicable' }, output: { state: 'not_applicable' }, context: { state: 'not_applicable' }, usage: null,
    source_locations: [{ source_ref: 'raw-message', node_id: 'paged-request', raw_path: 'events[3].message.content', uuid: 'uuid-message-1', block_start: 2, block_end: 5, state: 'observed' }],
    request: {
      state: 'observed', digest: { state: 'not_checked' }, missing_refs: [], source_refs: [],
      input_page: {
        coverage: 'page', next_cursor: 'cursor-next', digest: { state: 'not_checked' },
        items: [
          { request_path: 'tools[0]', kind: 'tools', value: [{ name: 'Read' }], state: 'observed', source_refs: [] },
          { request_path: 'messages[0].content', kind: 'message', role: 'developer', message_ordinal: 0, block_ordinal: 0, value: [{ type: 'text', text: 'AGENTS.md body' }], state: 'observed', source_refs: ['raw-message'] },
          { request_path: 'params', kind: 'params', value: { max_tokens: 1024 }, state: 'observed', source_refs: [] },
          { request_path: 'system', kind: 'system', value: 'System instructions', state: 'truncated', source_refs: [], offset_bytes: 20, total_bytes: 100, truncated: true },
        ],
      },
      provenance: [{ request_path: 'messages[0].content', source_ref: 'raw-message', raw_path: 'events[3].message.content', uuid: 'uuid-message-1', block_start: 2, block_end: 5, message_ordinal: 0, block_ordinal: 0, state: 'observed' }],
      comparison: { compare_to: 'previous-request-6', state: 'observed', items: [{ request_path: 'messages[0].content', change: 'changed', reason: 'Recorded message content differs.' }] },
    },
    notices: [],
  };

  const view = render(createElement(TraceNodeInspector, {
    selectedNodeId: 'paged-request', detail: requestDetail, loading: false, error: false, onRetry: vi.fn(),
    onLoadInputPage, onCompareRequest, onOpenSource,
  }));

  const header = screen.getByRole('region', { name: '선택 노드 상세' }).querySelector('header');
  expect(header?.textContent).toContain('역할 · assistant');
  expect(header?.textContent).toContain('의미 · tool_dispatch');
  expect(header?.textContent).toContain('원천 · api_request');
  const panel = screen.getByRole('tabpanel', { name: 'API 요청' });
  const content = panel.textContent ?? '';
  expect(content.indexOf('system')).toBeLessThan(content.indexOf('messages'));
  expect(content.indexOf('messages')).toBeLessThan(content.indexOf('tools'));
  expect(content.indexOf('tools')).toBeLessThan(content.indexOf('요청 설정'));
  expect(content).toContain('role: developer');
  expect(content).toContain('AGENTS.md body');
  expect(content).toContain('previous-request-6');
  expect(content).toContain('Recorded message content differs.');
  expect(content).toContain('offset 20 / 100 bytes');
  expect(content).toContain('UUID · uuid-message-1');
  expect(content).toContain('원문 블록 범위 · 2–5');
  expect(content).toContain('메시지 0 · 블록 0');

  fireEvent.click(within(panel).getByRole('button', { name: '원문 열기 · raw-message' }));
  expect(onOpenSource).toHaveBeenCalledWith('raw-message');
  fireEvent.click(within(panel).getByRole('button', { name: '요청 입력 더 읽기' }));
  expect(onLoadInputPage).toHaveBeenCalledWith('paged-request', 'cursor-next');
  fireEvent.change(within(panel).getByLabelText('비교할 이전 요청 ID'), { target: { value: 'previous-request-7' } });
  fireEvent.click(within(panel).getByRole('button', { name: '비교' }));
  expect(onCompareRequest).toHaveBeenCalledWith('paged-request', 'previous-request-7');

  view.rerender(createElement(TraceNodeInspector, {
    selectedNodeId: 'paged-request', detail: requestDetail, loading: false, error: false, onRetry: vi.fn(),
    onLoadInputPage, onCompareRequest, onOpenSource, inputPageError: new Error('page failed'),
  }));
  fireEvent.click(screen.getByRole('button', { name: '다시 시도' }));
  expect(onLoadInputPage).toHaveBeenLastCalledWith('paged-request', 'cursor-next');
});

it('shows response usage as part of the response content without a usage tab', () => {
  render(createElement(TraceNodeInspector, { selectedNodeId: node.node_id, detail, loading: false, error: false, onRetry: vi.fn() }));

  expect(screen.queryByRole('tablist', { name: '선택 노드 자료' })).toBeNull();
  const panel = screen.getByRole('tabpanel');
  expect(panel.textContent).toContain('응답 본문');
  expect(panel.textContent).toContain('응답별 사용량·캐시');
  expect(panel.textContent).toContain('24000');
});

it('opens recorded hook execution immediately instead of empty message inputs', () => {
  const hookDetail: TraceNodeDetail = {
    ...detail, node: { ...node, node_id: 'hook-open', kind: 'hook', title: '훅 · policy' },
    input: { state: 'not_collected' }, output: { state: 'not_collected' }, usage: { agent: 'claude' },
    context: { state: 'observed', value: { attachment: { hookEvent: 'PreToolUse', command: 'guard.sh', stdout: 'allowed' } } },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'hook-open', detail: hookDetail, loading: false, error: false, onRetry: vi.fn() }));
  expect(screen.getByRole('tabpanel', { name: '훅 실행' }).textContent).toContain('guard.sh');
  expect(screen.queryByRole('tabpanel', { name: '입출력' })).toBeNull();
});

it('keeps raw source available as a collapsed section while selected content changes', () => {
  const props = { loading: false, error: false, onRetry: vi.fn() };
  const view = render(createElement(TraceNodeInspector, { ...props, selectedNodeId: node.node_id, detail }));
  const source = screen.getByText(/연결된 원자료 2건/).closest('details');
  expect(source?.hasAttribute('open')).toBe(false);
  fireEvent.click(screen.getByText(/연결된 원자료 2건/));
  expect(source?.hasAttribute('open')).toBe(true);

  const hookDetail = { ...detail, node: { ...node, node_id: 'hook-next', kind: 'hook' }, context: { state: 'observed', value: { attachment: { command: 'guard.sh' } } } };
  view.rerender(createElement(TraceNodeInspector, { ...props, selectedNodeId: 'hook-next', detail: hookDetail }));
  expect(screen.getByRole('tabpanel', { name: '훅 실행' }).textContent).toContain('guard.sh');
});

it('shows an exact harness-recorded API request only in its own detail section with digest provenance', () => {
  render(createElement(TraceNodeInspector, { selectedNodeId: 'response-1', detail, loading: false, error: false, onRetry: vi.fn() }));
  const request = screen.getByRole('tabpanel', { name: '응답' });
  expect(within(request).getByRole('heading', { name: '하네스 기록 요청 구성' })).toBeTruthy();
  expect(within(request).getByText(/HTTP 헤더나 네트워크 전송 전체를 캡처한 자료가 아닙니다/)).toBeTruthy();
  expect(within(request).getAllByText('hash-a')).toHaveLength(2);
  expect(within(request).getAllByText('api-request-blob:3').length).toBeGreaterThan(0);
});

it('shows response content and linked usage together while keeping raw records collapsed', () => {
  render(createElement(TraceNodeInspector, { selectedNodeId: 'response-1', detail, loading: false, error: false, onRetry: vi.fn() }));
  expect(screen.getByRole('tabpanel', { name: '응답' }).textContent).toContain('응답 본문');
  const panel = screen.getByRole('tabpanel', { name: '응답' });
  expect(panel.textContent).toContain('응답별 사용량·캐시');
  expect(screen.queryByRole('tablist', { name: '선택 노드 자료' })).toBeNull();
  const source = within(panel).getByText(/연결된 원자료 2건/).closest('details');
  expect(source?.hasAttribute('open')).toBe(false);
  expect(source?.textContent).toContain('private_fixture');
});

it('keeps unknown status and missing timestamps inside the collapsed source details', () => {
  const unknownDetail: TraceNodeDetail = {
    ...detail, node: { ...node, status: 'unknown', timestamp: null },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'response-1', detail: unknownDetail, loading: false, error: false, onRetry: vi.fn() }));
  const header = screen.getByRole('region', { name: '선택 노드 상세' }).querySelector('header');
  const statusLine = header?.querySelector('div.mt-2');
  expect(statusLine?.textContent).not.toContain('확인 불가');
  expect(statusLine?.textContent).not.toContain('시각 확인 불가');
  const sources = header?.querySelector('details');
  expect(sources?.hasAttribute('open')).toBe(false);
  expect(sources?.textContent).toContain('상태');
  expect(sources?.textContent).toContain('확인 불가');
  expect(sources?.textContent).toContain('시각 확인 불가');
});

it('states when this node has no exact request reference instead of inferring a request body', () => {
  const withoutRequest = { ...detail, request: undefined };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'response-1', detail: withoutRequest, loading: false, error: false, onRetry: vi.fn() }));
  expect(screen.queryByRole('tab', { name: 'API 요청' })).toBeNull();
});

it('preserves provider usage keys and compares only fields observed on the linked previous response', () => {
  const providerUsage = {
    source: 'session_log', scope: 'response',
    values: { input_tokens: 5000, output_tokens: 200, cache_read_input_tokens: 4200, cache_creation_input_tokens: 300 },
    previous: { node_id: 'response-0', timestamp: '2026-10-18T08:30:00.000Z', scope: 'response', evidence: 'previous_observed_response', values: { input_tokens: 4800, output_tokens: 180, cache_read_input_tokens: 4000 } },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'response-1', detail: { ...detail, usage: providerUsage }, loading: false, error: false, onRetry: vi.fn() }));
  const panel = screen.getByRole('tabpanel', { name: '응답' });
  expect(panel.textContent).toContain('cache_read_input_tokens');
  expect(panel.textContent).toContain('cache_creation_input_tokens');
  expect(panel.textContent).toContain('200');
  expect(panel.textContent).toContain('직전 관측 응답과 비교');
  expect(panel.textContent).toContain('4200');
  expect(panel.textContent).toContain('4000');
  expect(panel.textContent).toContain('+200');
  expect(panel.textContent).not.toContain('일반 입력 토큰 · 미수집');
  expect(panel.textContent).toContain('input_tokens');
});

it('keeps redacted, truncated, and failed material states distinct for applicable tool input and result', () => {
  const exceptional: TraceNodeDetail = {
    ...detail, node: { ...node, kind: 'tool', availability: 'redacted' }, request: undefined, usage: null,
    input: { state: 'redacted', reason: 'prompt_redacted' }, output: { state: 'truncated', reason: 'record_limit' },
    context: { state: 'not_applicable' },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'response-1', detail: exceptional, loading: false, error: false, onRetry: vi.fn() }));
  expect(screen.getByRole('region', { name: '선택 노드 상세' }).textContent).toContain('가림');
  const panel = screen.getByRole('tabpanel', { name: '입력·결과' });
  expect(panel.textContent).toContain('가림');
  expect(panel.textContent).toContain('잘림');
});

it('structures hook execution evidence without dumping its full raw record', () => {
  const hookDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'hook-1', kind: 'hook', title: '훅 · PreToolUse:Read' },
    context: { state: 'observed', value: { type: 'attachment', attachment: {
      type: 'hook_success', hookName: 'PreToolUse:Read', hookEvent: 'PreToolUse', command: 'guard.sh', exitCode: 0,
      durationMs: 24, stdout: 'allowed', stderr: '', hookAdditionalContext: ['checked path'], privateExtra: 'must not appear',
    } } },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'hook-1', detail: hookDetail, loading: false, error: false, onRetry: vi.fn() }));
  const panel = screen.getByRole('tabpanel', { name: '훅 실행' });
  expect(panel.textContent).toContain('실행 명령');
  expect(panel.textContent).toContain('guard.sh');
  expect(panel.textContent).toContain('훅 이름');
  expect(panel.textContent).not.toContain('훅 이름·필터');
  expect(panel.textContent).not.toContain('매칭 조건·stdin은 이 기록에서 확인할 수 없습니다.');
  expect(panel.textContent).toContain('PreToolUse');
  expect(panel.textContent).toContain('종료 코드');
  expect(panel.textContent).toContain('24 ms');
  expect(panel.textContent).toContain('stdout');
  expect(panel.textContent).toContain('allowed');
  expect(panel.textContent).toContain('stderr');
  expect(panel.textContent).toContain('(빈 문자열)');
  expect(panel.textContent).toContain('checked path');
  expect(panel.textContent).not.toContain('privateExtra');
});

it('omits hook detail fields that the record did not contain', () => {
  const sparseHookDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'hook-sparse', kind: 'hook', title: '훅 · PreToolUse' },
    input: { state: 'not_applicable' }, output: { state: 'not_applicable' }, usage: null,
    context: { state: 'observed', value: { attachment: { hookEvent: 'PreToolUse', command: 'guard.sh' } } },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'hook-sparse', detail: sparseHookDetail, loading: false, error: false, onRetry: vi.fn() }));
  const panel = screen.getByRole('tabpanel');
  expect(panel.textContent).toContain('PreToolUse');
  expect(panel.textContent).toContain('guard.sh');
  expect(panel.textContent).not.toContain('종료 코드');
  expect(panel.textContent).not.toContain('stdout');
  expect(panel.textContent).not.toContain('stderr');
  expect(panel.textContent).not.toContain('매칭 조건·stdin');
});

it('shows instruction file paths and opens the first recorded body', () => {
  const instructionDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'instructions-1', kind: 'instructions', title: '주입된 지시문' },
    context: { state: 'observed', value: { type: 'attachment', attachment: { type: 'instructions', files: [
      { path: '/work/CLAUDE.md', content: 'project instruction body' },
      { path: '/work/AGENTS.md', content: 'imported instruction body' },
    ] } } },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'instructions-1', detail: instructionDetail, loading: false, error: false, onRetry: vi.fn() }));
  const panel = screen.getByRole('tabpanel', { name: '지시문' });
  const claudeFile = within(panel).getByText(/CLAUDE\.md/).closest('details');
  expect(claudeFile).toBeTruthy();
  expect(claudeFile?.hasAttribute('open')).toBe(true);
  const agentsFile = within(panel).getByText(/AGENTS\.md/).closest('details');
  expect(agentsFile?.hasAttribute('open')).toBe(false);
});

it('shows the exact agent prompt and parent while identifying unavailable inherited-history scope', () => {
  const agentDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'agent-1', parent_id: 'tool-1', kind: 'agent', title: '에이전트 전달' },
    input: { state: 'observed', value: { description: '검사', prompt: 'Read the changed files and report findings', subagent_type: 'Explore' } },
    context: { state: 'not_collected', reason: 'agent_history_boundary_not_linked' },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'agent-1', detail: agentDetail, loading: false, error: false, onRetry: vi.fn() }));
  const panel = screen.getByRole('tabpanel', { name: '하위 에이전트' });
  expect(panel.textContent).toContain('전달 프롬프트');
  expect(panel.textContent).toContain('Read the changed files and report findings');
  expect(panel.textContent).toContain('상위 실행 노드');
  expect(panel.textContent).toContain('tool-1');
  expect(panel.textContent).toContain('상속 이력 범위');
  expect(panel.textContent).toContain('미수집');
});

it('renders only source-linked agent parent and inherited-history metadata', () => {
  const agentDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'agent-1', parent_id: 'tool-1', kind: 'agent', title: '에이전트 전달' },
    input: { state: 'observed', value: { prompt: 'Inspect the changed files' } },
    context: { state: 'observed', value: {
      dispatch: { prompt: 'Inspect the changed files', description: 'Inspect recent edits', subagent_type: 'Explore', fork_context: 'same-turn', fork_turns: 2 },
      parent: { state: 'observed', value: { node_id: 'tool-1', evidence: 'exact_dispatch_tool' } },
      history: { state: 'not_collected', reason: 'child_context_not_linked' },
    } },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'agent-1', detail: agentDetail, loading: false, error: false, onRetry: vi.fn() }));
  const panel = screen.getByRole('tabpanel', { name: '하위 에이전트' });
  expect(panel.textContent).toContain('exact_dispatch_tool');
  expect(panel.textContent).toContain('fork_context');
  expect(panel.textContent).toContain('실제 복사 범위 아님');
  expect(panel.textContent).toContain('child_context_not_linked');
  expect(panel.textContent).toContain('미수집');
  expect(panel.textContent).toContain('Inspect the changed files');
});

it('decodes JSON-string agent input, keeps raw arguments folded, and shows the actual returned result', () => {
  const rawArguments = JSON.stringify({
    description: 'Inspect changed files', prompt: 'Read the implementation and report findings',
    subagent_type: 'Explore', agent_id: 'agent-returned-17', extra: 'preserve this argument',
  });
  const agentDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'agent-json-input', parent_id: 'dispatch-tool-4', kind: 'agent', title: 'Explore 하위 에이전트' },
    input: { state: 'observed', value: rawArguments },
    output: { state: 'observed', value: { agent_id: 'agent-returned-17', status: 'completed', result: 'Found two relevant files.' } },
    context: { state: 'not_collected', reason: 'child_history_not_linked' },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'agent-json-input', detail: agentDetail, loading: false, error: false, onRetry: vi.fn() }));

  const panel = screen.getByRole('tabpanel', { name: '하위 에이전트' });
  expect(panel.textContent).toContain('Read the implementation and report findings');
  expect(panel.textContent).toContain('에이전트 종류');
  expect(panel.textContent).toContain('Explore');
  expect(within(panel).getByRole('heading', { name: '호출 반환 결과' })).toBeTruthy();
  expect(panel.textContent).toContain('agent-returned-17');
  expect(panel.textContent).toContain('Found two relevant files.');
  const rawInput = within(panel).getByText('원래 도구 인수 보기').closest('details');
  expect(rawInput?.hasAttribute('open')).toBe(false);
  expect(rawInput?.textContent).toContain('preserve this argument');
  expect(panel.textContent).toContain('child_history_not_linked');
  expect(panel.textContent).not.toContain('가정한 자식 ID');
});

it('omits hook duration and prompt rows when raw hook entries do not contain those fields', () => {
  const hookDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'hook-raw-sparse', kind: 'hook', title: '훅 · PreToolUse' },
    input: { state: 'not_applicable' }, output: { state: 'not_applicable' }, usage: null,
    context: { state: 'observed', value: { hookInfos: [{ command: 'guard.sh' }] } },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'hook-raw-sparse', detail: hookDetail, loading: false, error: false, onRetry: vi.fn() }));

  const panel = screen.getByRole('tabpanel', { name: '훅 실행' });
  expect(panel.textContent).toContain('guard.sh');
  expect(panel.textContent).not.toContain('실행 시간');
  expect(panel.textContent).not.toContain('훅 프롬프트');
});

it('labels exact cache and reasoning counters including nested Claude ephemeral windows', () => {
  const providerUsage = {
    source: 'session_log', scope: 'response', agent: 'claude',
    values: {
      input_tokens: 5000, output_tokens: 200, cached_input_tokens: 4200, cache_write_input_tokens: 300,
      reasoning_output_tokens: 10, cache_creation: { ephemeral_5m_input_tokens: 80, ephemeral_1h_input_tokens: 220 },
    },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'response-1', detail: { ...detail, usage: providerUsage }, loading: false, error: false, onRetry: vi.fn() }));
  const panel = screen.getByRole('tabpanel', { name: '응답' });
  expect(panel.textContent).toContain('일반 입력');
  expect(panel.textContent).toContain('cached_input_tokens');
  expect(panel.textContent).toContain('cache_write_input_tokens');
  expect(panel.textContent).toContain('reasoning_output_tokens');
  expect(panel.textContent).toContain('ephemeral_5m_input_tokens');
  expect(panel.textContent).toContain('ephemeral_1h_input_tokens');
});

it('keeps Codex input_tokens at its source meaning without calling it uncached input', () => {
  const codexUsage = { source: 'session_log', scope: 'usage_record', agent: 'codex', values: { input_tokens: 100, cached_input_tokens: 30 } };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'response-1', detail: { ...detail, usage: codexUsage }, loading: false, error: false, onRetry: vi.fn() }));
  const panel = screen.getByRole('tabpanel', { name: '응답' });
  expect(panel.textContent).toContain('입력 (input_tokens)');
  expect(panel.textContent).toContain('캐시 입력 (cached_input_tokens)');
  expect(panel.textContent).not.toContain('일반 입력');
});

it('keeps usage visible on responses and omits standalone usage records', () => {
  const usageDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'usage-record-1', kind: 'usage', title: '사용량 관측' },
    usage: { source: 'session_log', scope: 'usage_record', values: { input_tokens: 100 } },
  };
  const view = render(createElement(TraceNodeInspector, { selectedNodeId: 'usage-record-1', detail: usageDetail, loading: false, error: false, onRetry: vi.fn() }));
  expect(screen.queryByText('사용량·캐시 기록')).toBeNull();
  expect(screen.getByRole('tabpanel').textContent).toContain('사용량은 해당 모델 응답의 속성으로 표시됩니다.');

  view.rerender(createElement(TraceNodeInspector, { selectedNodeId: 'response-1', detail: { ...detail, usage: { ...detail.usage, scope: 'response' } }, loading: false, error: false, onRetry: vi.fn() }));
  expect(within(screen.getByRole('tabpanel', { name: '응답' })).getByRole('heading', { name: '응답별 사용량·캐시' })).toBeTruthy();
});

it('does not show an empty usage card for instructions with agent metadata only', () => {
  const instructionDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'instructions-usage-empty', kind: 'instructions', title: '주입된 지시문' },
    context: { state: 'observed', value: { type: 'attachment', attachment: { type: 'instructions', files: [{ path: '/work/CLAUDE.md', content: 'rules' }] } } },
    usage: { agent: 'claude' },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'instructions-usage-empty', detail: instructionDetail, loading: false, error: false, onRetry: vi.fn() }));
  const panel = screen.getByRole('tabpanel', { name: '지시문' });
  expect(within(panel).getByRole('region', { name: '지시문' })).toBeTruthy();
  expect(within(panel).queryByRole('heading', { name: '응답별 사용량·캐시' })).toBeNull();
  expect(panel.textContent).not.toContain('응답 사용량 수치가 수집되지 않았습니다.');
  expect(panel.textContent).not.toContain('이 노드에 연결된 직전 응답 비교 자료가 없습니다.');
});

it('opens the first typed instruction body as readable text without inapplicable IO or usage panels', () => {
  const instructionDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'typed-instructions', kind: 'instructions', title: 'AGENTS.md' },
    input: { state: 'not_applicable' }, output: { state: 'not_applicable' }, usage: { agent: 'codex' },
    context: { state: 'observed', value: { instructions: [
      { kind: 'project', source_path: 'payload.message.content', path: '/repo/AGENTS.md', body: { state: 'observed', value: [{ type: 'text', text: '# Project rules' }, { type: 'text', text: 'Run tests before reporting.' }] } },
    ] } },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'typed-instructions', detail: instructionDetail, loading: false, error: false, onRetry: vi.fn() }));

  expect(screen.queryByRole('tablist', { name: '선택 노드 자료' })).toBeNull();
  expect(screen.getByRole('tabpanel', { name: '지시문' }).textContent).toContain('Run tests before reporting.');
  expect(screen.getByText(/프로젝트 지시문 · \/repo\/AGENTS\.md/)).toBeTruthy();
  expect(screen.getByRole('tabpanel', { name: '지시문' }).querySelector('details')?.hasAttribute('open')).toBe(true);
  expect(screen.queryByText(/not_applicable/)).toBeNull();
});

it('renders message text blocks as readable prose while keeping tool arguments structured', () => {
  const messageDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'turn-message-blocks', kind: 'turn', title: '사용자 입력' },
    input: { state: 'observed', value: [
      { type: 'input_text', text: 'Please inspect the trace.' },
      { type: 'summary_text', text: 'Focus on the selected command.' },
    ] },
    output: { state: 'not_applicable' }, context: { state: 'not_applicable' }, usage: null,
  };
  const view = render(createElement(TraceNodeInspector, { selectedNodeId: 'turn-message-blocks', detail: messageDetail, loading: false, error: false, onRetry: vi.fn() }));
  const input = screen.getByRole('tabpanel', { name: '입력' });
  expect(input.textContent).toContain('Please inspect the trace.');
  expect(input.textContent).toContain('Focus on the selected command.');
  expect(input.textContent).not.toContain('input_text');
  expect(input.textContent).not.toContain('summary_text');

  const toolDetail: TraceNodeDetail = {
    ...detail, node: { ...node, node_id: 'tool-arguments-structured', kind: 'tool', title: 'Read' },
    input: { state: 'observed', value: { command: 'Read', file_path: '/tmp/report.md' } }, output: { state: 'not_applicable' }, context: { state: 'not_applicable' }, usage: null,
  };
  view.rerender(createElement(TraceNodeInspector, { selectedNodeId: 'tool-arguments-structured', detail: toolDetail, loading: false, error: false, onRetry: vi.fn() }));
  expect(screen.getByRole('tabpanel', { name: '입력·결과' }).textContent).toContain('"file_path"');
});

it('labels all typed instruction source kinds emitted by the trace API', () => {
  const instructionDetail: TraceNodeDetail = {
    ...detail, node: { ...node, node_id: 'typed-instruction-kinds', kind: 'instructions', title: '지시문' },
    context: { state: 'observed', value: { instructions: [
      { kind: 'base_instructions', source_path: 'payload.base_instructions', body: { state: 'observed', value: 'base text' } },
      { kind: 'developer_instructions', source_path: 'payload.developer_instructions', body: { state: 'observed', value: 'developer text' } },
      { kind: 'user_instructions', source_path: 'payload.user_instructions', body: { state: 'observed', value: 'user text' } },
      { kind: 'agents_md', source_path: 'payload.message.content', path: '/repo/AGENTS.md', body: { state: 'observed', value: 'agent rules' } },
      { kind: 'instruction_file', source_path: 'attachment.files', path: '/repo/CLAUDE.md', body: { state: 'observed', value: 'project rules' } },
    ] } },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'typed-instruction-kinds', detail: instructionDetail, loading: false, error: false, onRetry: vi.fn() }));
  const panel = screen.getByRole('tabpanel', { name: '지시문' });
  for (const label of ['기본 지시문', '개발자 지시문', '사용자 지시문', 'AGENTS.md', '지시문 파일']) {
    expect(within(panel).getByText(new RegExp(label.replace('.', '\\.')))).toBeTruthy();
  }
});

it('gives settings and session metadata distinct content panels', () => {
  const contextDetail: TraceNodeDetail = {
    ...detail, node: { ...node, node_id: 'context-settings', kind: 'context', title: '실행 설정 · 승인 정책' },
    input: { state: 'not_applicable' }, output: { state: 'not_applicable' }, context: { state: 'observed', value: {
      settings: { state: 'observed', value: { approval_policy: 'never' } }, provenance: { state: 'observed', value: { source: 'session_log' } },
    } },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'context-settings', detail: contextDetail, loading: false, error: false, onRetry: vi.fn() }));
  expect(screen.queryByRole('tablist', { name: '선택 노드 자료' })).toBeNull();
  expect(screen.getByRole('tabpanel', { name: '실행 설정' }).textContent).toContain('approval_policy');
  cleanup();

  const metadataDetail: TraceNodeDetail = {
    ...detail, node: { ...node, node_id: 'session-metadata', kind: 'metadata', title: '마지막 프롬프트' },
    input: { state: 'not_applicable' }, output: { state: 'not_applicable' }, context: { state: 'observed', value: { subtype: 'last-prompt', permission_mode: 'default' } },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'session-metadata', detail: metadataDetail, loading: false, error: false, onRetry: vi.fn() }));
  expect(screen.queryByRole('tablist', { name: '선택 노드 자료' })).toBeNull();
  expect(screen.getByRole('tabpanel', { name: '기록 내용' }).textContent).toContain('last-prompt');
  expect(screen.getByRole('tabpanel', { name: '기록 내용' }).textContent).toContain('permission_mode');
  expect(screen.queryByRole('tab', { name: '입출력' })).toBeNull();
});

it('shows observed input and output on event records without adding empty IO or metadata placeholders', () => {
  const eventDetail: TraceNodeDetail = {
    ...detail,
    node: { ...node, node_id: 'event-user-1', kind: 'event', title: '사용자 입력 · 테스트 계획' },
    input: { state: 'observed', value: [{ type: 'input_text', text: 'actual user input body' }] },
    output: { state: 'not_applicable' },
    context: { state: 'not_collected', reason: 'event_context_not_recorded' },
    usage: null,
  };
  const view = render(createElement(TraceNodeInspector, { selectedNodeId: 'event-user-1', detail: eventDetail, loading: false, error: false, onRetry: vi.fn() }));
  const content = screen.getByRole('tabpanel', { name: '기록 내용' });
  expect(content.textContent).toContain('actual user input body');
  expect(content.textContent).not.toContain('이벤트 문맥');
  expect(content.textContent).not.toContain('event_context_not_recorded');
  expect(content.textContent).not.toContain('결과');

  const toolResult: TraceNodeDetail = {
    ...eventDetail, node: { ...node, node_id: 'event-tool-result-1', kind: 'event', title: '도구 결과 · 검사 로그' },
    input: { state: 'not_applicable' }, output: { state: 'observed', value: [{ type: 'output_text', text: 'actual tool result body' }] },
    context: { state: 'not_applicable' },
  };
  view.rerender(createElement(TraceNodeInspector, { selectedNodeId: 'event-tool-result-1', detail: toolResult, loading: false, error: false, onRetry: vi.fn() }));
  const resultPanel = screen.getByRole('tabpanel', { name: '기록 내용' });
  expect(resultPanel.textContent).toContain('actual tool result body');
  expect(resultPanel.textContent).toContain('결과');
  view.unmount();
});

it('does not expose usage panes for metadata-only usage nodes or responses without usage evidence', () => {
  const metadataUsage: TraceNodeDetail = {
    ...detail, node: { ...node, node_id: 'usage-empty', kind: 'usage', title: '사용량 기록' },
    input: { state: 'not_applicable' }, output: { state: 'not_applicable' }, usage: { agent: 'claude' },
  };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'usage-empty', detail: metadataUsage, loading: false, error: false, onRetry: vi.fn() }));
  expect(screen.queryByRole('tablist', { name: '선택 노드 자료' })).toBeNull();
  expect(screen.queryByText('응답 사용량 수치가 수집되지 않았습니다.')).toBeNull();
  cleanup();

  const responseWithoutUsage = { ...detail, usage: { agent: 'codex' } };
  render(createElement(TraceNodeInspector, { selectedNodeId: 'response-1', detail: responseWithoutUsage, loading: false, error: false, onRetry: vi.fn() }));
  expect(screen.queryByRole('tablist', { name: '선택 노드 자료' })).toBeNull();
  expect(screen.getByRole('tabpanel', { name: '응답' }).textContent).not.toContain('응답별 사용량·캐시');
  expect(screen.queryByText('이 노드에 연결된 직전 응답 비교 자료가 없습니다.')).toBeNull();
});

it('shows the complete recorded agent call and return value, including JSON-string arguments', () => {
  const agentDetail: TraceNodeDetail = { ...detail, node: { ...node, kind: 'agent' }, raw_records: [], request: undefined };
  agentDetail.input = { state: 'observed', value: JSON.stringify({ prompt: 'TASK_PROMPT_CANARY', model: 'recorded-model', custom_option: 'PRESERVED_ARGUMENT' }) };
  agentDetail.output = { state: 'observed', value: { agent_id: 'recorded-child-id', status: 'spawned' } };
  render(createElement(TraceNodeInspector, { selectedNodeId: agentDetail.node.node_id, detail: agentDetail, loading: false, error: false, onRetry: vi.fn() }));
  const html = screen.getByRole('tabpanel', { name: '하위 에이전트' }).textContent;
  expect(html).toContain('PRESERVED_ARGUMENT');
  expect(html).toContain('recorded-child-id');
  expect(html).toContain('호출 인수');
  expect(html).toContain('반환 결과');
});

it('exposes record actions beside the exact selected source record for conversation round trips', () => {
  const recordActions = (record: (typeof detail.raw_records)[number]) => createElement('button', null, `대화에서 보기 · ${record.record_type}`);
  render(createElement(TraceNodeInspector, { selectedNodeId: node.node_id, detail, loading: false, error: false, onRetry: vi.fn(), recordActions }));
  fireEvent.click(screen.getByText(/^연결된 원자료 [0-9]+건$/));
  fireEvent.click(screen.getByText(/tool_call · /));
  expect(screen.getByRole('button', { name: '대화에서 보기 · tool_call' })).toBeTruthy();
});
