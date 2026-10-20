import { describe, expect, it } from 'vitest';
import type { TraceNodeDetail } from '@/lib/session-trace-node-types';
import { traceNodePresentation, traceTurnGroupingInfo } from './trace-node-presentation';

const detail = (overrides: Partial<TraceNodeDetail>): TraceNodeDetail => ({
  session_id: 'session-1',
  node: { node_id: 'node-1', parent_id: null, kind: 'event', title: 'record', timestamp: null, started_at: null, ended_at: null, status: 'unknown', has_children: false, child_count: null, source_ref: 'source', evidence: [], availability: 'observed' },
  input: { state: 'not_applicable' }, output: { state: 'not_applicable' }, context: { state: 'not_applicable' },
  usage: null, raw_records: [], notices: [], ...overrides,
});

describe('trace node presentation contract', () => {
  it('selects the matching content renderer without exposing category tabs', () => {
    expect(traceNodePresentation(detail({ node: { ...detail({}).node, kind: 'turn' } })).content).toBe('turn');
    expect(traceNodePresentation(detail({ node: { ...detail({}).node, kind: 'hook' } })).content).toBe('hook');
    expect(traceNodePresentation(detail({ node: { ...detail({}).node, kind: 'agent' } })).content).toBe('agent');
    expect(traceNodePresentation(detail({ node: { ...detail({}).node, kind: 'context' } })).content).toBe('context');
    expect(traceNodePresentation(detail({ node: { ...detail({}).node, kind: 'metadata' } })).content).toBe('metadata');
    expect(traceNodePresentation(detail({ node: { ...detail({}).node, kind: 'event' } })).content).toBe('event');
  });

  it('uses an explicit runtime notice semantic before the broader instructions kind', () => {
    expect(traceNodePresentation(detail({ node: { ...detail({}).node, kind: 'instructions', semantic: 'runtime_notice' } })).content).toBe('event');
  });

  it('shows request and usage only as material attached to their selected response or tool', () => {
    const response = detail({ node: { ...detail({}).node, kind: 'response' } });
    expect(traceNodePresentation(response)).toMatchObject({ showRequest: false, showUsage: false });
    expect(traceNodePresentation({ ...response, usage: { values: { output_tokens: 12 } } }).showUsage).toBe(true);
    expect(traceNodePresentation({ ...response, usage: { agent: 'codex' } }).showUsage).toBe(false);
    expect(traceNodePresentation({ ...response, request: {
      state: 'observed', input_page: { items: [{ request_path: 'system', kind: 'system', value: 'rules', state: 'observed', source_refs: [] }], next_cursor: null, digest: { state: 'not_checked' }, coverage: 'page' },
      digest: { state: 'not_checked' }, missing_refs: [], source_refs: [],
    } }).showRequest).toBe(true);
    const usageNode = detail({ node: { ...detail({}).node, kind: 'usage' }, usage: { values: { output_tokens: 12 } } });
    expect(traceNodePresentation(usageNode).showUsage).toBe(false);
  });

  it('exposes raw source only when selected detail actually contains records', () => {
    expect(traceNodePresentation(detail({})).showSource).toBe(false);
    expect(traceNodePresentation(detail({ raw_records: [{ record_type: 'message', ts: '2026-01-01T00:00:00Z', raw: { text: 'source' } } as never] })).showSource).toBe(true);
  });

  it('distinguishes an exact turn id from a source-sequence input range', () => {
    expect(traceTurnGroupingInfo({ kind: 'turn', grouping_basis: 'exact_turn_id' })).toEqual({
      label: '턴', description: '원자료의 동일 turn_id로 연결된 기록입니다.',
    });
    expect(traceTurnGroupingInfo({ kind: 'turn', grouping_basis: 'source_sequence' })).toEqual({
      label: '입력 구간', description: '같은 원천의 입력부터 다음 입력 전까지 묶은 기록입니다. API 인과관계를 뜻하지 않습니다.',
    });
    expect(traceTurnGroupingInfo({ kind: 'turn' })).toBeUndefined();
  });
});
