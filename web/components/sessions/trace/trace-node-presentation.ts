import type { TraceNode, TraceNodeDetail } from '@/lib/session-trace-node-types';

type TraceNodeContentKind = 'turn' | 'response' | 'tool' | 'request' | 'instructions' | 'context' | 'metadata' | 'event' | 'hook' | 'agent' | 'usage';
interface TraceNodePresentation { content: TraceNodeContentKind; showRequest: boolean; showUsage: boolean; showSource: boolean }
interface TraceTurnGroupingInfo { label: '턴' | '입력 구간'; description: string }

const hasEvidence = (value: unknown): boolean => {
  if (value === null || value === undefined || value === false) return false;
  if (typeof value === 'string') return value.trim().length > 0;
  if (Array.isArray(value)) return value.length > 0;
  if (typeof value === 'object') return Object.keys(value).length > 0;
  return true;
};

const hasUsageMaterial = (usage: TraceNodeDetail['usage']): boolean =>
  ['values', 'otel', 'diagnostics', 'previous'].some(key => hasEvidence(usage?.[key]));

const hasRequestMaterial = (detail: TraceNodeDetail): boolean => {
  const request = detail.request;
  return !!request && request.state !== 'not_applicable'
    && (hasEvidence(request.input_page?.items) || hasEvidence(request.provenance) || hasEvidence(request.comparison)
      || hasEvidence(request.body) || hasEvidence(request.params) || request.source_refs.length > 0 || request.missing_refs.length > 0);
};

const traceNodePresentation = (detail: TraceNodeDetail): TraceNodePresentation => {
  let content: TraceNodeContentKind;
  if (detail.node.semantic === 'runtime_notice') content = 'event';
  else switch (detail.node.kind) {
    case 'turn': content = 'turn'; break;
    case 'response': content = 'response'; break;
    case 'tool': content = 'tool'; break;
    case 'request': content = 'request'; break;
    case 'instructions': content = 'instructions'; break;
    case 'context': content = 'context'; break;
    case 'metadata': content = 'metadata'; break;
    case 'event': content = 'event'; break;
    case 'hook': content = 'hook'; break;
    case 'agent': content = 'agent'; break;
    case 'usage': content = 'usage'; break;
    default: content = 'metadata';
  }
  return {
    content,
    showRequest: (content === 'response' || content === 'tool') && hasRequestMaterial(detail),
    showUsage: content === 'response' && hasUsageMaterial(detail.usage),
    showSource: detail.raw_records.length > 0,
  };
};

const traceTurnGroupingInfo = (node: Pick<TraceNode, 'kind' | 'grouping_basis'>): TraceTurnGroupingInfo | undefined => {
  if (node.kind !== 'turn') return;
  if (node.grouping_basis === 'exact_turn_id') return { label: '턴', description: '원자료의 동일 turn_id로 연결된 기록입니다.' };
  if (node.grouping_basis === 'source_sequence') return { label: '입력 구간', description: '같은 원천의 입력부터 다음 입력 전까지 묶은 기록입니다. API 인과관계를 뜻하지 않습니다.' };
};

export { traceNodePresentation, traceTurnGroupingInfo };
export type { TraceNodeContentKind, TraceNodePresentation, TraceTurnGroupingInfo };
