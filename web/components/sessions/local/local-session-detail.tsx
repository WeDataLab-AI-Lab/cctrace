'use client';

import { useState } from 'react';
import type { SessionRecord } from '@/lib/types';
import type { LocalSessionAnalysis } from '@/lib/local-analysis-types';
import { ConversationContent } from '../conversation-markdown';
import { buildConversation, conversationItemKey, dedupeKeys, dedupeRecords } from '../session-utils';
import { ToolPairBlock } from '../tool-pair-block';

export interface LocalSessionDetailProps {
  localId: string;
  title: string;
  revision: string;
  records: SessionRecord[][];
  recordsRevision?: string;
  recordsLoading?: boolean;
  recordsError?: boolean;
  hasMore?: boolean;
  loadingMore?: boolean;
  onLoadMore?: () => void;
  analysis?: LocalSessionAnalysis;
  analysisLoading?: boolean;
  analysisError?: boolean;
}

const basename = (path: string): string => path.split(/[\\/]/).filter(Boolean).at(-1) ?? '';
// The daemon applies the source disclosure policy; this component never reconstructs
// a hidden view from raw content and never puts the source's full path in the UI.

const count = (value: number | null): string => value === null ? '확인할 수 없음' : value.toLocaleString('ko-KR');
const seconds = (value: number | null): string => value === null ? '확인할 수 없음' : `${value.toLocaleString('ko-KR')}초`;
const analysisState: Record<LocalSessionAnalysis['state'], string> = {
  observed: '관측된 로컬 기록으로 계산했습니다.',
  undeterminable: '기록만으로 일부 값을 판단할 수 없습니다.',
  truncated: '원본 기록이 일부만 수집되어 결과가 불완전합니다.',
  not_collected: '분석에 필요한 원본 기록이 수집되지 않았습니다.',
};
const turnStatus: Record<LocalSessionAnalysis['turns'][number]['status'], string> = {
  ended: '종료', interrupted: '중단', error: '오류', no_response: '응답 없음', open: '진행 중', undeterminable: '판단 불가',
};

export const LocalSessionDetail = ({ localId, title, revision, records, recordsRevision, recordsLoading, recordsError, hasMore, loadingMore, onLoadMore, analysis, analysisLoading, analysisError }: LocalSessionDetailProps) => {
  const [tab, setTab] = useState<'conversation' | 'raw' | 'analysis'>('conversation');
  const currentRecords = recordsRevision === undefined || recordsRevision === revision;
  const currentAnalysis = analysis?.revision === revision;
  const loaded = records.flat();
  const conversation = buildConversation(dedupeRecords(currentRecords ? loaded : []));
  const mode = tab === 'analysis' ? 'analysis' : tab;

  return (
    <section aria-label="로컬 세션 상세" className="flex min-h-0 min-w-0 flex-1 flex-col bg-canvas text-ink" data-local-id={localId}>
      <header className="border-b border-border px-4 py-3">
        <div className="truncate text-sm font-semibold" title={title}>{title}</div>
        <p className="mt-1 text-xs text-ink-3">로컬 기록 · 서버 동기화 없이 표시</p>
      </header>
      <div role="tablist" aria-label="로컬 세션 보기" className="flex shrink-0 gap-1 border-b border-border px-4">
        {(['conversation', 'raw', 'analysis'] as const).map((item) => (
          <button key={item} type="button" role="tab" aria-selected={mode === item} onClick={() => setTab(item)}
            className="px-3 py-2 text-xs aria-selected:border-b-2 aria-selected:border-brand aria-selected:text-brand">
            {{ conversation: '대화', raw: '개별 원문', analysis: '분석' }[item]}
          </button>
        ))}
      </div>
      <div role="tabpanel" aria-label={{ conversation: '대화', raw: '개별 원문', analysis: '분석' }[mode]} className="min-h-0 flex-1 overflow-y-auto p-4">
        {mode === 'analysis' ? (
          analysisError ? <div role="alert" className="text-sm text-danger">로컬 분석을 읽지 못했습니다. 다시 시도해 주세요.</div>
            : analysisLoading && !currentAnalysis ? <div role="status" className="text-sm text-ink-3">로컬 분석을 불러오는 중입니다.</div>
              : !analysis || !currentAnalysis ? <div role="status" className="text-sm text-ink-3">{analysis ? '원본이 변경되었습니다. 최신 분석을 기다리는 중입니다.' : '분석 결과가 아직 없습니다.'}</div>
                : <div className="space-y-4 text-xs">
                  <p role="status" className="rounded border border-border bg-surface px-3 py-2 text-ink-2">{analysisState[analysis.state]} · 규칙 버전 {analysis.rules_version}</p>
                  {analysis.session && <dl className="grid grid-cols-2 gap-3 rounded border border-border bg-surface p-3 sm:grid-cols-3">
                    <div><dt className="text-ink-3">입력 토큰</dt><dd>{count(analysis.session.input_tokens)}</dd></div>
                    <div><dt className="text-ink-3">출력 토큰</dt><dd>{count(analysis.session.output_tokens)}</dd></div>
                    <div><dt className="text-ink-3">활동 시간</dt><dd>{seconds(analysis.session.active_seconds)}</dd></div>
                    <div><dt className="text-ink-3">턴</dt><dd>{count(analysis.session.turns)}</dd></div>
                    <div><dt className="text-ink-3">입력</dt><dd>{count(analysis.session.inputs)}</dd></div>
                    <div><dt className="text-ink-3">추정 또는 관측 비용</dt><dd>{analysis.session.cost_usd === null ? '확인할 수 없음' : `$${analysis.session.cost_usd.toFixed(4)}`}</dd></div>
                  </dl>}
                  {analysis.session?.turn_boundaries.state === 'undeterminable' && <p className="text-ink-3">턴 경계를 판단할 수 없어 턴·입력 수가 표시되지 않을 수 있습니다.</p>}
                  {analysis.turns.length > 0 && <div className="space-y-2" aria-label="분석된 턴">
                    {analysis.turns.map(turn => <article key={turn.turn_key} className="rounded border border-border bg-surface p-3">
                      <div className="font-medium">턴 {turn.ordinal} · {turnStatus[turn.status]}</div>
                      <time dateTime={turn.started_at} className="text-ink-3">{turn.started_at}</time>
                      <div className="mt-1 break-words">{turn.preview_state === 'ok' ? turn.preview ?? '' : turn.preview_state === 'withheld' ? '입력 원문 미제공' : turn.preview_state === 'redacted' ? '입력 내용이 가려졌습니다.' : '입력 미관측'}</div>
                      <div className="mt-1 text-ink-3">입력 {count(turn.input_tokens)} · 출력 {count(turn.output_tokens)}{turn.active_seconds != null ? ` · 활동 ${seconds(turn.active_seconds)}` : ''}</div>
                    </article>)}
                  </div>}
                  {!analysis.session && <p className="text-ink-3">계산된 세션 요약이 없습니다.</p>}
                </div>
        ) : recordsError ? <div role="alert" className="text-sm text-danger">로컬 기록을 읽지 못했습니다. 다시 시도해 주세요.</div>
          : !currentRecords ? <div role="status" className="text-sm text-ink-3">원본이 변경되었습니다. 최신 기록을 기다리는 중입니다.</div>
            : recordsLoading && loaded.length === 0 ? <div role="status" className="text-sm text-ink-3">로컬 기록을 불러오는 중입니다.</div>
              : <div className="space-y-3 text-xs">
                {mode === 'conversation' ? dedupeKeys(conversation.map(item => ({ key: conversationItemKey(item), item }))).map(({ key, item }) => (
                  <article key={key} className="rounded border border-border bg-surface p-3">
                    <div className="mb-2 text-[11px] font-semibold text-ink-2">{item.role === 'assistant' ? '어시스턴트' : '사용자'}{item.agentTask ? ' · 상위 에이전트 지시' : ''}</div>
                    {item.command && <div className="font-mono">{item.command} {item.commandArgs ?? ''}</div>}
                    {item.compactSummary ? <details><summary>압축 요약</summary>{item.text && <ConversationContent text={item.text} />}</details>
                      : item.text && <ConversationContent text={item.text} />}
                    {item.tools.map((pair, index) => <ToolPairBlock key={pair.id ?? `${pair.name}-${index}`} pair={pair} />)}
                  </article>
                )) : loaded.map((record, index) => (
                  <article key={`${record.view?.anchor_id ?? record.ts}-${index}`} className="rounded border border-border bg-surface">
                    <div className="flex flex-wrap gap-x-3 border-b border-border px-3 py-2 text-ink-2">
                      <strong>{record.record_type}</strong><time dateTime={record.ts}>{record.ts}</time>
                      {record.source_file && <span className="font-mono">{basename(record.source_file)}</span>}
                    </div>
                    <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words p-3 font-mono">{record.raw === undefined || !record.view || record.view.kind === 'hidden' ? '원문 미제공' : JSON.stringify(record.raw, null, 2)}</pre>
                  </article>
                ))}
                {loaded.length === 0 && <p role="status" className="text-ink-3">관측된 기록이 없습니다.</p>}
                {hasMore && <button type="button" onClick={onLoadMore} disabled={!onLoadMore || loadingMore}
                  className="rounded border border-border px-3 py-2 text-ink-2 hover:border-brand disabled:opacity-50">
                  {loadingMore ? '다음 기록을 불러오는 중' : '다음 기록 불러오기'}
                </button>}
              </div>}
      </div>
    </section>
  );
};
