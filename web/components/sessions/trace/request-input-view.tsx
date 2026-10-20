'use client';

import { useId, useState } from 'react';
import type { FormEvent, ChangeEvent } from 'react';
import { cn } from '@/lib/utils';
import type { NativeRequest, TraceComparison, TraceInputItem, TraceProvenance, TraceSourceLocation } from '@/lib/session-trace-node-types';
import { readableContentValue } from './trace-node-instructions';

interface RequestInputViewProps {
  nodeId: string;
  request: NativeRequest;
  sourceLocations?: TraceSourceLocation[];
  loading?: boolean;
  error?: unknown;
  onLoadPage?: (nodeId: string, cursor?: string) => void;
  onCompareRequest?: (nodeId: string, previousRequestId?: string) => void;
  onOpenSource?: (sourceRef: string) => void;
}
interface RequestDisplayItem {
  requestPath: string;
  kind: string;
  role?: string;
  messageOrdinal?: number;
  blockOrdinal?: number;
  value?: unknown;
  state: string;
  sourceRefs: string[];
  offsetBytes?: number;
  totalBytes?: number;
  truncated?: boolean;
}
interface InputItemProps {
  item: RequestDisplayItem;
  provenance: TraceProvenance[];
  sourceLocations: TraceSourceLocation[];
  onOpenSource?: (sourceRef: string) => void;
}
interface SourceEvidenceProps { sourceRef: string; location?: TraceSourceLocation; provenance?: TraceProvenance[]; onOpenSource?: (sourceRef: string) => void }

const recordValue = (value: unknown): Record<string, unknown> | undefined => value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
const jsonText = (value: unknown): string => {
  if (typeof value === 'string') return value;
  return JSON.stringify(value, null, 2) ?? String(value);
};
const inputStateLabel = (state: string): string => ({
  observed: '수집됨', collected: '수집됨', available: '수집됨', redacted: '가림', withheld: '가림',
  truncated: '잘림', unresolved: '미해결', missing: '미수집', not_collected: '미수집',
  not_queried: '미조회', not_checked: '미검증', not_applicable: '해당 없음', error: '오류', unknown: '확인 불가',
  match: '일치', matched: '일치', mismatch: '불일치', different: '불일치',
}[state] ?? `상태: ${state}`);
const itemOrder = (kind: string): number => ({ system: 0, message: 1, tools: 2, params: 3 }[kind] ?? 4);
const requestItems = (items: TraceInputItem[]): RequestDisplayItem[] => [...items].sort((left, right) => {
  const order = itemOrder(left.kind) - itemOrder(right.kind);
  if (order !== 0) return order;
  if (left.message_ordinal !== right.message_ordinal) return (left.message_ordinal ?? -1) - (right.message_ordinal ?? -1);
  if (left.block_ordinal !== right.block_ordinal) return (left.block_ordinal ?? -1) - (right.block_ordinal ?? -1);
  return left.request_path.localeCompare(right.request_path);
}).map(item => ({
  requestPath: item.request_path, kind: item.kind, role: item.role, value: item.value, state: item.state,
  messageOrdinal: item.message_ordinal, blockOrdinal: item.block_ordinal,
  sourceRefs: item.source_refs, offsetBytes: item.offset_bytes, totalBytes: item.total_bytes, truncated: item.truncated,
}));
const legacyMessageValue = (value: unknown): unknown => {
  const object = recordValue(value);
  if (!object) return value;
  const fields = Object.fromEntries(Object.entries(object).filter(([key]) => key !== 'role'));
  const keys = Object.keys(fields);
  if (keys.length === 0) return undefined;
  if (keys.length === 1 && Object.prototype.hasOwnProperty.call(fields, 'content')) return fields.content;
  return fields;
};
const legacyRequestItems = (request: NativeRequest): RequestDisplayItem[] => {
  const body = request.body ?? {};
  const items: RequestDisplayItem[] = [];
  if (Object.prototype.hasOwnProperty.call(body, 'system')) {
    items.push({ requestPath: 'system', kind: 'system', value: body.system, state: 'observed', sourceRefs: [] });
  }
  const messages = Array.isArray(body.messages) ? body.messages : [];
  messages.forEach((message, index) => {
    const object = recordValue(message);
    items.push({
      requestPath: `messages[${index}]`, kind: 'message', role: typeof object?.role === 'string' ? object.role : undefined,
      messageOrdinal: index, value: legacyMessageValue(message), state: 'observed', sourceRefs: [],
    });
  });
  if (Object.prototype.hasOwnProperty.call(body, 'tools')) {
    const tools = Array.isArray(body.tools) ? body.tools : [body.tools];
    tools.forEach((value, index) => items.push({ requestPath: `tools[${index}]`, kind: 'tools', value, state: 'observed', sourceRefs: [] }));
  }
  const settings = Object.fromEntries(Object.entries(body).filter(([key]) => !['system', 'messages', 'tools'].includes(key)));
  if (Object.keys(settings).length > 0) items.push({ requestPath: 'settings', kind: 'params', value: settings, state: 'observed', sourceRefs: [] });
  if (request.params !== undefined) items.push({ requestPath: 'params', kind: 'params', value: request.params, state: 'observed', sourceRefs: [] });
  return items;
};
const itemLabel = (item: RequestDisplayItem): string => {
  if (item.kind === 'system') return `system · ${item.requestPath}`;
  if (item.kind === 'message') {
    const message = item.messageOrdinal === undefined ? '' : `messages[${item.messageOrdinal}]`;
    const pathLabel = message || item.requestPath;
    return `${pathLabel} · role: ${item.role ?? '미수집'}`;
  }
  if (item.kind === 'tools') return `tools · ${item.requestPath}`;
  if (item.kind === 'params') return `요청 설정 · ${item.requestPath}`;
  return `${item.kind} · ${item.requestPath}`;
};
const explicitMeaning = (value: unknown): string | undefined => {
  const object = recordValue(value);
  const meaning = object?.meaning;
  return typeof meaning === 'string' && meaning.trim() ? meaning : undefined;
};
const displayValue = (value: unknown): string => {
  const readable = readableContentValue(value);
  return readable === undefined ? '' : jsonText(readable);
};

const SourceEvidence = ({ sourceRef, location, provenance, onOpenSource }: SourceEvidenceProps) => {
  const openSource = () => onOpenSource?.(sourceRef);
  const paths = [...new Set([location?.raw_path, ...(provenance ?? []).map(entry => entry.raw_path)].filter((path): path is string => !!path))];
  const uuids = [...new Set([location?.uuid, ...(provenance ?? []).map(entry => entry.uuid)].filter((uuid): uuid is string => !!uuid))];
  const blockRanges = [...new Set([
    location?.block_start === undefined && location?.block_end === undefined ? undefined : `${location.block_start ?? '?'}–${location.block_end ?? '?'}`,
    ...(provenance ?? []).map(entry => entry.block_start === undefined && entry.block_end === undefined ? undefined : `${entry.block_start ?? '?'}–${entry.block_end ?? '?'}`),
  ].filter((range): range is string => !!range))];
  const ordinals = [...new Set((provenance ?? []).map(entry => entry.message_ordinal === undefined && entry.block_ordinal === undefined
    ? undefined : `메시지 ${entry.message_ordinal ?? '—'} · 블록 ${entry.block_ordinal ?? '—'}`).filter((value): value is string => !!value))];
  const states = [...new Set([location?.state, ...(provenance ?? []).map(entry => entry.state)].filter((state): state is string => !!state))];
  return <li className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[10px] text-ink-3">
    {paths.map(path => <code key={path} className="break-all">{path}</code>)}
    {uuids.map(uuid => <code key={uuid} className="break-all">UUID · {uuid}</code>)}
    {ordinals.map(ordinal => <span key={ordinal}>{ordinal}</span>)}
    {blockRanges.map(range => <span key={range}>원문 블록 범위 · {range}</span>)}
    {states.map(state => <span key={state}>{inputStateLabel(state)}</span>)}
    {onOpenSource ? <button type="button" onClick={openSource} className="break-all font-mono text-accent underline underline-offset-2 focus-visible:ring-1 focus-visible:ring-accent">원문 열기 · {sourceRef}</button>
      : <code className="break-all">{sourceRef}</code>}
  </li>;
};

const RequestInputItemView = ({ item, provenance, sourceLocations, onOpenSource }: InputItemProps) => {
  const itemProvenance = provenance.filter(entry => entry.request_path === item.requestPath);
  const sources = [...new Set([...item.sourceRefs, ...itemProvenance.map(entry => entry.source_ref)])];
  const meaning = explicitMeaning(item.value);
  const visibleValue = displayValue(item.value);
  const partial = item.truncated || item.state === 'truncated';
  return <section className="min-w-0 border-b border-border-subtle py-3 last:border-b-0">
    <div className="mb-1 flex flex-wrap items-center gap-2">
      <h4 className="text-xs font-medium text-ink">{itemLabel(item)}</h4>
      {item.state !== 'observed' && <span className="rounded border border-border px-1.5 py-0.5 text-[10px] text-ink-3">{inputStateLabel(item.state)}</span>}
      {meaning && <span className="text-[10px] text-ink-3">의미: {meaning}</span>}
    </div>
    {partial && <p className="mb-2 text-[10px] text-warning-strong">
      {item.totalBytes !== undefined && item.offsetBytes !== undefined
        ? `입력 블록 일부 · offset ${item.offsetBytes.toLocaleString('ko-KR')} / ${item.totalBytes.toLocaleString('ko-KR')} bytes`
        : '입력 블록이 일부만 조회되었습니다.'}
    </p>}
    {item.value !== undefined ? <pre className="max-h-[32rem] min-w-0 overflow-auto whitespace-pre-wrap break-words rounded bg-surface-sunk p-3 font-mono text-[11px] text-ink-2">{visibleValue}</pre>
      : <p className="text-[11px] text-ink-3">{item.state === 'observed' ? '이 입력 위치에 값이 기록되지 않았습니다.' : inputStateLabel(item.state)}</p>}
    {sources.length > 0 && <details className="mt-2 rounded border border-border">
      <summary className="cursor-pointer px-2 py-1 text-[10px] text-ink-3 focus-visible:ring-1 focus-visible:ring-accent">원자료 근거 {sources.length}건</summary>
      <ul className="space-y-1 border-t border-border-subtle px-2 py-2">
        {sources.map(sourceRef => <SourceEvidence key={sourceRef} sourceRef={sourceRef}
          location={sourceLocations.find(location => location.source_ref === sourceRef)}
          provenance={itemProvenance.filter(entry => entry.source_ref === sourceRef)} onOpenSource={onOpenSource} />)}
      </ul>
    </details>}
  </section>;
};

const comparisonLabel = (change: string): string => ({ common: '공통', added: '추가', changed: '변경', removed: '제거', unknown: '비교 미확인' }[change] ?? change);

interface RequestComparisonViewProps { comparison?: TraceComparison }
const RequestComparisonView = ({ comparison }: RequestComparisonViewProps) => {
  if (!comparison) return null;
  return <section aria-label="요청 비교 결과" className="space-y-2 border-t border-border-subtle py-3">
    <div className="flex flex-wrap items-center gap-2">
      <h3 className="text-xs font-medium text-ink">요청 비교</h3>
      <span className="text-[10px] text-ink-3">비교 대상 · <code className="font-mono">{comparison.compare_to}</code></span>
      <span className="rounded border border-border px-1.5 py-0.5 text-[10px] text-ink-3">{inputStateLabel(comparison.state)}</span>
    </div>
    {comparison.items.length === 0 ? <p className="text-[11px] text-ink-3">비교할 입력 항목이 없습니다.</p>
      : <ul className="space-y-1">
        {comparison.items.map((item, index) => <li key={`${item.request_path}:${index}`} className="rounded border border-border px-2 py-1.5 text-[10px]">
          <span className="mr-2 rounded border border-border px-1 py-0.5 text-ink-2">{comparisonLabel(item.change)}</span>
          <code className="break-all text-ink-2">{item.request_path}</code>
          {item.reason && <p className="mt-1 break-words text-ink-3">{item.reason}</p>}
        </li>)}
      </ul>}
  </section>;
};

interface RequestSourceListProps { sourceRefs: string[]; sourceLocations: TraceSourceLocation[]; onOpenSource?: (sourceRef: string) => void }
const RequestSourceList = ({ sourceRefs, sourceLocations, onOpenSource }: RequestSourceListProps) => {
  if (sourceRefs.length === 0) return null;
  return <details className="rounded border border-border">
    <summary className="cursor-pointer px-2 py-1 text-[10px] text-ink-3 focus-visible:ring-1 focus-visible:ring-accent">요청 단위 원자료 {sourceRefs.length}건</summary>
    <ul className="space-y-1 border-t border-border-subtle px-2 py-2">
      {sourceRefs.map(sourceRef => <SourceEvidence key={sourceRef} sourceRef={sourceRef} location={sourceLocations.find(location => location.source_ref === sourceRef)} onOpenSource={onOpenSource} />)}
    </ul>
  </details>;
};

const RequestInputView = ({ nodeId, request, sourceLocations = [], loading = false, error, onLoadPage, onCompareRequest, onOpenSource }: RequestInputViewProps) => {
  const [compareTo, setCompareTo] = useState('');
  const page = request.input_page;
  const items = page ? requestItems(page.items) : legacyRequestItems(request);
  const provenance = request.provenance ?? [];
  const itemSourceRefs = new Set([...items.flatMap(item => item.sourceRefs), ...provenance.map(entry => entry.source_ref)]);
  const requestSourceRefs = request.source_refs.filter(sourceRef => !itemSourceRefs.has(sourceRef));
  const hasError = error !== undefined && error !== null && error !== false;
  const digestMatched = request.digest.state === 'match' || request.digest.state === 'matched';
  const digestMismatch = request.digest.state === 'mismatch' || request.digest.state === 'different';
  const digestValues = [
    ...(request.digest.expected === undefined ? [] : [{ label: '기대값', value: request.digest.expected }]),
    ...(request.digest.actual === undefined ? [] : [{ label: '재구성값', value: request.digest.actual }]),
  ];
  const changeCompareTo = (event: ChangeEvent<HTMLInputElement>) => setCompareTo(event.target.value);
  const submitComparison = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const previousRequestId = compareTo.trim();
    if (previousRequestId) onCompareRequest?.(nodeId, previousRequestId);
  };
  const loadMore = () => {
    if (page?.next_cursor) onLoadPage?.(nodeId, page.next_cursor);
  };
  const retryPage = () => onLoadPage?.(nodeId, page?.next_cursor ?? undefined);
  const canCompare = !!onCompareRequest;
  const compareInputId = useId();
  return <div className="space-y-2 py-2">
    <div className="space-y-1 border-b border-border-subtle pb-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-xs font-medium text-ink">하네스 기록 요청 구성</h3>
        <span className="rounded border border-border px-1.5 py-0.5 text-[10px] text-ink-3">{inputStateLabel(request.state)}</span>
        <span className={cn('rounded border border-border px-1.5 py-0.5 text-[10px] text-ink-3', digestMismatch && 'border-danger text-danger-strong', digestMatched && 'border-success text-success-strong')}>전체 digest · {inputStateLabel(request.digest.state)}</span>
        {page && <span className="rounded border border-border px-1.5 py-0.5 text-[10px] text-ink-3">{page.coverage === 'page' ? '입력 일부 조회' : inputStateLabel(page.coverage)}</span>}
      </div>
      <p className="text-[10px] text-ink-3">로그에서 재구성한 모델 요청 구성입니다. HTTP 헤더나 네트워크 전송 전체를 캡처한 자료가 아닙니다.</p>
    </div>
    {items.map(item => <RequestInputItemView key={`${item.requestPath}:${item.messageOrdinal ?? ''}:${item.blockOrdinal ?? ''}:${item.offsetBytes ?? ''}:${item.totalBytes ?? ''}:${item.sourceRefs.join(',')}`} item={item} provenance={provenance} sourceLocations={sourceLocations} onOpenSource={onOpenSource} />)}
    <RequestSourceList sourceRefs={requestSourceRefs} sourceLocations={sourceLocations} onOpenSource={onOpenSource} />
    {digestValues.length > 0 && <section className="border-t border-border-subtle pt-3">
      <h4 className="mb-1 text-[10px] font-medium text-ink-3">digest 근거</h4>
      <dl className="grid min-w-0 gap-2 text-[10px] sm:grid-cols-2">
        {digestValues.map(entry => <div key={entry.label} className="min-w-0"><dt className="text-ink-3">{entry.label}</dt><dd className="break-all font-mono text-ink-2">{entry.value}</dd></div>)}
      </dl>
    </section>}
    {items.length === 0 && !loading && !hasError && <p role="status" className="py-2 text-[11px] text-ink-3">요청 입력 본문을 확인할 수 없습니다. 수집 상태와 원자료 근거를 확인하세요.</p>}
    {loading && <p role="status" className="py-2 text-[11px] text-ink-3">요청 입력을 읽는 중…</p>}
    {hasError && <div role="alert" className="flex flex-wrap items-center gap-2 py-2 text-[11px] text-danger-strong">
      <span>요청 입력 페이지를 읽지 못했습니다.</span>
      {onLoadPage && <button type="button" onClick={retryPage} disabled={loading} className="rounded border border-border px-2 py-1 text-ink-2 focus-visible:ring-1 focus-visible:ring-accent">다시 시도</button>}
    </div>}
    {page?.next_cursor && <div className="border-t border-border-subtle pt-2">
      {onLoadPage ? <button type="button" onClick={loadMore} disabled={loading} className="rounded border border-border px-2.5 py-1.5 text-[11px] text-ink-2 hover:bg-surface-sunk focus-visible:ring-1 focus-visible:ring-accent disabled:opacity-50">
        {loading ? '다음 입력 읽는 중…' : '요청 입력 더 읽기'}
      </button> : <p className="text-[10px] text-ink-3">다음 입력 페이지 조회가 연결되지 않았습니다.</p>}
    </div>}
    {request.missing_refs.length > 0 && <section className="border-t border-border-subtle pt-3">
      <h4 className="mb-1 text-[10px] font-medium text-warning-strong">해결되지 않은 입력 참조 {request.missing_refs.length}건</h4>
      <ul className="space-y-1 break-all font-mono text-[10px] text-ink-3">{request.missing_refs.map(ref => <li key={ref}>{ref}</li>)}</ul>
    </section>}
    {canCompare && <form onSubmit={submitComparison} className="flex flex-wrap items-end gap-2 border-t border-border-subtle pt-3">
      <label className="grid min-w-0 flex-1 gap-1 text-[10px] text-ink-3" htmlFor={compareInputId}>
        비교할 이전 요청 ID
        <input id={compareInputId} value={compareTo} onChange={changeCompareTo} placeholder="요청 ID 입력" className="min-w-0 rounded border border-border bg-surface px-2 py-1.5 text-xs text-ink focus-visible:ring-1 focus-visible:ring-accent" />
      </label>
      <button type="submit" disabled={!compareTo.trim()} className="rounded border border-border px-2.5 py-1.5 text-[11px] text-ink-2 hover:bg-surface-sunk focus-visible:ring-1 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-50">비교</button>
    </form>}
    <RequestComparisonView comparison={request.comparison} />
  </div>;
};

export { RequestInputView };
export type { RequestInputViewProps };
