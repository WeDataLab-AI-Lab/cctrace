'use client';

import type { ReactNode } from 'react';
import type { SessionRecord, SessionRecordView } from '@/lib/types';
import { ConversationContent } from '../conversation-markdown';
import { dedupeKeys, keyRecords } from '../session-utils';
import { cn } from '@/lib/utils';

const recordLabel = (view: SessionRecordView): string => {
  if (view.kind === 'tool_call') return '도구 입력';
  if (view.kind === 'tool_result') return '도구 결과';
  if (view.agent_task) return '상위 에이전트 지시';
  if (view.compact_summary) return '압축 요약';
  if (view.command) return '명령';
  return view.role === 'assistant' ? '어시스턴트' : view.role === 'user' ? '사용자' : '기록';
};

interface ToolContentProps { label: string; content?: string; result?: boolean; isError?: boolean }
const ToolContent = ({ label, content, result = false, isError = false }: ToolContentProps) => <details open className="mt-1 rounded border border-border text-xs">
  <summary className="cursor-pointer px-2 py-1 font-mono text-ink-3">{label}{isError && <span className="ml-2 text-danger">error</span>}</summary>
  {content && <pre className={cn('max-h-[150px] overflow-y-auto whitespace-pre-wrap break-words border-t border-border/50 px-2 py-1.5 font-mono',
    result ? isError ? 'bg-danger-soft text-danger-strong' : 'bg-success-soft text-success-strong' : 'bg-surface-sunk text-ink-2')}>{content}</pre>}
</details>;

const ContextRecord = ({ record, recordActions }: { record: SessionRecord; recordActions?: (record: SessionRecord) => ReactNode }) => {
  const view = record.view;
  if (!view || view.kind === 'hidden' || view.kind === 'reasoning') return null;
  const calls = dedupeKeys((view.tool_calls ?? []).map(call => ({ key: `call:${call.id ?? call.name}`, call })));
  const results = dedupeKeys((view.tool_results ?? []).filter(result => !result.inferred).map(result => ({ key: `result:${result.id ?? 'unknown'}`, result })));
  const hasContent = !!(view.text || view.command || calls.length || results.length);

  return <article data-record-anchor={view.anchor_id} className="rounded-lg border border-border bg-surface text-xs">
    <header className="flex flex-wrap items-center gap-2 border-b border-border-subtle px-3 py-2 text-[11px] text-ink-2">
      <strong className="font-medium">{recordLabel(view)}</strong>
      <time dateTime={record.ts} className="font-mono tabular-nums text-ink-3">{record.ts}</time>
      {recordActions?.(record)}
    </header>
    <div className="space-y-2 px-3 py-2 text-ink">
      {view.command && <p className="font-mono">{view.command} {view.command_args}</p>}
      {view.text && <ConversationContent text={view.text} />}
      {calls.map(({ key, call }) => <section key={key} aria-label={`${call.name || '도구'} 입력`}>
        <ToolContent label={call.name || '도구'} content={call.input} />
        {!call.input && <p className="mt-1 text-[11px] text-ink-3">도구 입력 내용이 제공되지 않았습니다.</p>}
      </section>)}
      {results.map(({ key, result }) => <section key={key} aria-label="도구 출력">
        <ToolContent label="도구 결과" content={result.output} result isError={result.is_error} />
        {!result.output && <p className="mt-1 text-[11px] text-ink-3">도구 결과 내용이 제공되지 않았습니다.</p>}
      </section>)}
      {!hasContent && <p className="text-ink-3">내용이 제공되지 않았습니다.</p>}
    </div>
  </article>;
};

interface TraceContextPaneProps {
  records: SessionRecord[];
  loading: boolean;
  loadError: boolean;
  onRetry: () => void;
  onClose: () => void;
  state?: 'ready' | 'pending' | 'unavailable';
  updating?: boolean;
  selected?: boolean;
  recordActions?: (record: SessionRecord) => ReactNode;
}

/** Bounded context renders only the server-disclosed record view; it never reads raw or imports session-wide facts. */
const TraceContextPane = ({ records, loading, loadError, onRetry, onClose, state, updating = false, selected = true, recordActions }: TraceContextPaneProps) => {
  const visible = records.filter(record => record.view && record.view.kind !== 'hidden' && record.view.kind !== 'reasoning');
  return <section aria-label="선택한 원문" className="flex min-h-0 flex-1 flex-col">
    <header className="flex shrink-0 items-center gap-2 border-b border-border-subtle px-3 py-2 text-xs text-ink-2">
      <h2 className="font-medium">선택한 원문</h2>
      <span className="text-[11px] tabular-nums text-ink-3">불러온 기록 {records.length}건</span>
      <button type="button" onClick={onClose} className="ml-auto text-ink-3">닫기</button>
    </header>
    {loadError && <p role="alert" className="shrink-0 px-3 py-2 text-xs text-ink-3">선택한 원문을 읽지 못했습니다.
      <button type="button" onClick={onRetry} className="ml-2 text-brand underline">다시 시도</button>
    </p>}
    <div role="region" aria-label="선택한 원문 기록" tabIndex={0} className="min-h-0 flex-1 space-y-3 overflow-y-auto px-3 py-2">
      {visible.length === 0 ? <p role="status" className="py-4 text-xs text-ink-3">
        {!selected ? '턴이나 실행 행을 선택하면 원문을 표시합니다.' : updating ? '기록이 갱신되어 선택한 원문을 다시 읽는 중입니다.' : state === 'unavailable' ? '이 턴의 원문 연결을 확인할 수 없습니다.' : state === 'pending' ? '이 턴의 원문 연결을 준비 중입니다.' : loading ? '선택한 원문을 읽는 중입니다.' : state === 'ready' ? '이 턴에서 표시할 원문이 제공되지 않았습니다.' : '이 범위에서 표시할 원문이 제공되지 않았습니다.'}
      </p> : keyRecords(visible, 'trace-context').map(({ key, record }) => <ContextRecord key={key} record={record} recordActions={recordActions} />)}
    </div>
  </section>;
};

export { TraceContextPane };
