'use client';

import { useEffect, useRef } from 'react';
import type { InspectionEvent, InspectionTarget } from '@/lib/session-inspection';
import { ConversationContent } from '../conversation-markdown';

interface InspectionSourceBadgeProps { event: InspectionEvent; selectedSessionId: string }
const InspectionSourceBadge = ({ event, selectedSessionId }: InspectionSourceBadgeProps) => {
  if (!event.record.session_id || event.record.session_id === selectedSessionId) return null;
  return <span className="mt-1 flex flex-wrap items-center gap-x-2 rounded border border-border px-1.5 py-0.5 text-[10px] text-ink-3"><span>이전 세션 기록</span><code className="break-all">{event.record.session_id}</code></span>;
};

interface InspectionEventButtonProps { event: InspectionEvent; selectedSessionId: string; selected?: boolean; onSelect: (target: InspectionTarget) => void }
const InspectionEventButton = ({ event, selectedSessionId, selected = false, onSelect }: InspectionEventButtonProps) => {
  const handleSelect = () => onSelect(event.target);
  return <button type="button" aria-pressed={selected} onClick={handleSelect} className={selected ? 'w-full border-l-2 border-brand bg-surface-sunk px-3 py-3 text-left focus-visible:outline-2 focus-visible:outline-brand' : 'w-full border-l-2 border-transparent px-3 py-3 text-left hover:bg-surface-sunk focus-visible:outline-2 focus-visible:outline-brand'}>
    <span className="flex items-center justify-between gap-2 text-xs"><span className="min-w-0 break-words font-medium text-ink">{event.label}</span><time className="shrink-0 font-mono text-[10px] text-ink-3" dateTime={event.record.ts}>{new Date(event.record.ts).toLocaleTimeString()}</time></span>
    <span className="mt-1 block line-clamp-2 break-words text-[11px] text-ink-3">{event.text || '본문 미확인'}</span>
    <InspectionSourceBadge event={event} selectedSessionId={selectedSessionId} />
    {event.callId && <span className="mt-1 block truncate font-mono text-[10px] text-ink-3">{event.callId}</span>}
  </button>;
};

interface InspectionDetailProps {
  event: InspectionEvent;
  selectedSessionId: string;
  related: InspectionEvent[];
  relatedLoading?: boolean;
  relatedError?: boolean;
  onRetryRelated?: () => void;
  onSelect: (target: InspectionTarget) => void;
  onShowInConversation?: (target: InspectionTarget) => void;
  onBackToList: () => void;
}
const InspectionDetail = ({ event, selectedSessionId, related, relatedLoading, relatedError, onRetryRelated, onSelect, onShowInConversation, onBackToList }: InspectionDetailProps) => {
  const titleRef = useRef<HTMLHeadingElement>(null);
  const handleConversation = () => onShowInConversation?.({ recordId: event.conversationAnchor!, sourceFile: event.record.source_file });
  const hasResult = related.some(item => item.kind === 'tool_result');
  const results = related.filter(item => item.kind === 'tool_result');
  /** Keep keyboard focus on the exact record opened, without scrolling the content. */
  useEffect(() => { titleRef.current?.focus({ preventScroll: true }); }, [event.id]);
  return <article aria-label="기록 내용" className="min-h-0 min-w-0 flex-1 overflow-y-auto bg-surface">
    <header className="sticky top-0 z-10 flex items-center gap-2 border-b border-border-subtle bg-surface px-4 py-3">
      <button type="button" onClick={onBackToList} className="rounded px-2 py-1 text-xs text-ink-2 hover:bg-surface-sunk lg:hidden">기록 목록</button>
      <h3 ref={titleRef} tabIndex={-1} className="min-w-0 flex-1 break-words text-sm font-medium text-ink">{event.label}</h3>
      <InspectionSourceBadge event={event} selectedSessionId={selectedSessionId} />
      {event.conversationAnchor && onShowInConversation && <button type="button" onClick={handleConversation} className="shrink-0 rounded border border-border px-2 py-1 text-xs text-ink-2 hover:bg-surface-sunk">대화에서 보기</button>}
    </header>
    <div className="space-y-6 p-4 text-xs">
      <section aria-label="실제 내용" className="space-y-2">
        {event.kind === 'tool_call' && <h4 className="font-medium text-ink-2">입력</h4>}
        {event.notices.map(notice => <p key={notice} className="rounded bg-warning-soft px-3 py-2 text-warning-strong">{notice}</p>)}
        {event.text ? event.kind === 'message' ? <ConversationContent text={event.text} /> : <pre className="overflow-x-auto rounded border border-border bg-surface-sunk p-3 font-mono leading-5">{event.text}</pre> : <p className="text-ink-3">이 기록의 본문을 확인할 수 없습니다.</p>}
        {event.kind === 'tool_call' && results.map(result => <section key={result.id} aria-label="대응 도구 결과" className="space-y-2 pt-3">
          <h4 className="font-medium text-ink-2">결과</h4>
          <pre className="overflow-x-auto rounded border border-border bg-surface-sunk p-3 font-mono leading-5">{result.text || '결과 본문 미확인'}</pre>
          <InspectionEventButton event={{ ...result, text: '이 결과의 출처와 수집 JSON 보기' }} selectedSessionId={selectedSessionId} onSelect={onSelect} />
        </section>)}
      </section>
      {(event.callId || related.length > 0) && <section aria-label="관련 기록" className="space-y-2">
        <h4 className="text-xs font-medium text-ink-2">관련 기록</h4>
        {relatedLoading && <p role="status" className="text-ink-3">같은 호출 식별자의 기록을 읽는 중입니다.</p>}
        {relatedError && <p role="alert" className="text-ink-3">관련 기록을 읽지 못했습니다. <button type="button" onClick={onRetryRelated} className="text-brand underline">다시 시도</button></p>}
        {!relatedLoading && !relatedError && event.kind === 'tool_call' && !hasResult && <p className="text-ink-3">결과 미확인</p>}
        {related.length > 0 && <div className="divide-y divide-border-subtle rounded border border-border">{related.map(item => <InspectionEventButton key={item.id} event={item} selectedSessionId={selectedSessionId} onSelect={onSelect} />)}</div>}
      </section>}
      <section aria-label="출처" className="space-y-2 border-t border-border-subtle pt-4">
        <h4 className="font-medium text-ink-2">출처</h4>
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-[11px] text-ink-3">
          <dt>에이전트 · 기록 유형</dt><dd>{event.record.agent || 'claude'} · {event.record.record_type}</dd>
          <dt>시각</dt><dd className="break-all font-mono">{event.record.ts}</dd>
          <dt>세션</dt><dd className="break-all font-mono">{event.record.session_id}</dd>
          <dt>로그 파일</dt><dd className="break-all font-mono">{event.record.source_file || '수집 기록에 파일명 없음'}</dd>
          <dt>기록 식별자</dt><dd className="break-all font-mono">{event.target.recordId}</dd>
          <dt>본문 위치</dt><dd className="break-all font-mono">{event.target.contentPath}</dd>
          {event.callId && <><dt>호출 식별자</dt><dd className="break-all font-mono">{event.callId}</dd></>}
        </dl>
      </section>
      <details className="rounded border border-border">
        <summary className="cursor-pointer px-3 py-2 text-xs text-ink-2">수집 JSON</summary>
        <pre className="max-h-[32rem] overflow-auto border-t border-border-subtle bg-surface-sunk p-3 font-mono text-[11px] leading-5">{JSON.stringify(event.record.raw ?? null, null, 2)}</pre>
      </details>
    </div>
  </article>;
};

export { InspectionDetail, InspectionEventButton };
