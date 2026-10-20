'use client';

import { Search, X } from 'lucide-react';
import type { ChangeEvent, KeyboardEvent, MouseEvent } from 'react';
import type { SessionRecordSearchField, SessionRecordSearchMatch, SessionRecordSearchResult } from '@/lib/session-analysis-types';
import { cn } from '@/lib/utils';

/**
 * 전역 검색 상자(A4). 제어형이다: 값과 바꾸는 법은 부모가 갖고, 무엇을 가로질러 찾는지(`search.ts`)도 부모가 정한다.
 * Esc는 지운다(상자가 비어 있으면 아무것도 하지 않는다). 검색어가 있으면 맞은 건수를 옆에 적는다.
 */

interface GlobalSearchProps {
  value: string;
  onChange: (value: string) => void;
  /** 맞은 세션 수. 검색어가 없으면 거르지 않는다는 뜻이라 적지 않는다. */
  resultCount?: number | null;
  results?: readonly SessionRecordSearchResult[];
  loading?: boolean;
  errorText?: string | null;
  helperText?: string;
  onSelectResult?: (result: SessionRecordSearchResult, match: SessionRecordSearchMatch) => void;
  className?: string;
}

const MATCH_LABEL: Record<SessionRecordSearchField, string> = {
  title: '제목', headline: '개요', bullet: '글머리표', review: '검토 사항', work: '작업 항목', commit: '커밋', task: 'task',
};

const matchKey = (match: SessionRecordSearchMatch): string =>
  `${match.field}:${match.turn_key ?? ''}:${match.item_key ?? ''}:${match.text}`;

const GlobalSearch = ({ value, onChange, resultCount, results, loading = false, errorText, helperText, onSelectResult, className }: GlobalSearchProps) => {
  const active = value.trim() !== '';
  const showCount = active && typeof resultCount === 'number';
  const handleChange = (event: ChangeEvent<HTMLInputElement>) => onChange(event.target.value);
  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Escape' && value !== '') {
      event.preventDefault();
      onChange('');
    }
  };
  const handleMatchClick = (event: MouseEvent<HTMLButtonElement>) => {
    const sessionId = event.currentTarget.dataset.sessionId;
    const matchIndex = Number(event.currentTarget.dataset.matchIndex);
    const result = results?.find((item) => item.session_id === sessionId);
    const match = Number.isSafeInteger(matchIndex) ? result?.matches[matchIndex] : undefined;
    if (result && match) onSelectResult?.(result, match);
  };
  return (
    <div className={cn('relative flex shrink-0 items-center', className)}>
      <Search size={12} strokeWidth={1.75} aria-hidden="true" className="pointer-events-none absolute left-2 text-ink-3" />
      <input
        type="search"
        value={value}
        onChange={handleChange}
        onKeyDown={handleKeyDown}
        placeholder="검색"
        aria-label="전체 검색"
        title="로그인한 사용자의 세션 제목·개요·작업 항목·커밋·task를 검색합니다. 대화 원문과 다른 사용자의 세션은 검색하지 않습니다."
        className="h-7 w-48 appearance-none rounded-md border border-border bg-surface pr-14 pl-7 text-[12px] text-ink placeholder:text-ink-3 [&::-webkit-search-cancel-button]:hidden"
      />
      {value !== '' && (
        <span className="absolute right-1.5 flex items-center gap-1">
          {showCount && <span className="text-[10.5px] tabular-nums text-ink-3">{resultCount}건</span>}
          <button
            type="button"
            onClick={() => onChange('')}
            aria-label="검색어 지우기"
            className="inline-flex size-4 items-center justify-center rounded text-ink-3 hover:bg-surface-sunk hover:text-ink"
          >
            <X size={11} strokeWidth={2} aria-hidden="true" />
          </button>
        </span>
      )}
      {active && (results !== undefined || loading || errorText || helperText) && (
        <div role="region" aria-label="전체 검색 결과" className="absolute top-full right-0 z-50 mt-1 max-h-[min(70vh,32rem)] w-[min(30rem,calc(100vw-2rem))] overflow-y-auto rounded-md border border-border bg-surface p-2 shadow-[var(--sh-pop)]">
          {loading ? <p role="status" className="px-2 py-3 text-[12px] text-ink-3">세션을 검색하는 중입니다.</p>
            : errorText ? <p role="alert" className="px-2 py-3 text-[12px] text-danger">{errorText}</p>
              : helperText ? <p role="status" className="px-2 py-3 text-[12px] text-ink-3">{helperText}</p>
                : results?.length === 0 ? <p role="status" className="px-2 py-3 text-[12px] text-ink-3">검색 결과가 없습니다.</p>
                  : <ul className="space-y-2">
                    {(results ?? []).map((result) => (
                      <li key={result.session_id} className="rounded border border-border-subtle px-2 py-1.5">
                        <div className="flex min-w-0 items-baseline justify-between gap-3">
                          <span className="min-w-0 truncate text-[12px] font-medium text-ink">{result.title || `세션 ${result.session_id.slice(0, 8)}`}</span>
                          <span className="shrink-0 font-mono text-[10px] text-ink-3">{result.session_id.slice(0, 8)}</span>
                        </div>
                        {result.last_activity_at && <p className="text-[10px] text-ink-3">최근 활동 {result.last_activity_at}</p>}
                        <ul className="mt-1 space-y-0.5">
                          {result.matches.map((match, matchIndex) => (
                            <li key={matchKey(match)}>
                              <button
                                type="button"
                                data-session-id={result.session_id}
                                data-match-index={matchIndex}
                                onClick={handleMatchClick}
                                title={match.item_key ? `${match.text} · item_key ${match.item_key}` : undefined}
                                aria-label={`${MATCH_LABEL[match.field]} 일치: ${match.text}${match.turn_key ? ' · 해당 턴으로 이동' : ' · 세션 열기'}`}
                                className="block w-full rounded px-1 py-0.5 text-left text-[11px] leading-4 text-ink-2 hover:bg-canvas focus-visible:-outline-offset-2!"
                              >
                                <span className="mr-1 rounded bg-surface-sunk px-1 text-[10px] text-ink-3">{MATCH_LABEL[match.field]}</span>
                                <span className="break-words">{match.text}</span>
                                {match.turn_key && <span className="ml-1 font-mono text-[10px] text-ink-3">턴 근거</span>}
                              </button>
                            </li>
                          ))}
                        </ul>
                      </li>
                    ))}
                  </ul>}
          {resultCount === 50 && <p role="note" className="px-2 pt-2 text-[10px] text-ink-3">최대 50건을 표시합니다. 더 좁은 검색어로 찾을 수 있습니다.</p>}
        </div>
      )}
    </div>
  );
};

export { GlobalSearch };
