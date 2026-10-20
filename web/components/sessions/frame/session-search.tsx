'use client';

import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { fetchSessionRecordSearch, SessionRecordRequestError } from '@/lib/api';
import type { SessionRecordSearchMatch, SessionRecordSearchResult } from '@/lib/session-analysis-types';
import { GlobalSearch } from './global-search';

interface SessionSearchBoxProps {
  /** 사용자·역할·선택 계정이 바뀌면 이전 결과를 다른 범위에 남기지 않는다. */
  scopeKey: string;
  onSelect: (result: SessionRecordSearchResult, match: SessionRecordSearchMatch) => void;
}

const searchErrorText = (error: unknown): string => {
  if (error instanceof SessionRecordRequestError && error.status === 401) return '로그인 상태를 확인한 뒤 다시 검색해 주세요.';
  if (error instanceof SessionRecordRequestError && error.status === 404) return '세션 기록 검색을 사용할 수 없습니다.';
  return '세션 검색을 읽지 못했습니다. 다시 입력해 주세요.';
};

const SessionSearchBox = ({ scopeKey, onSelect }: SessionSearchBoxProps) => {
  const [query, setQuery] = useState('');
  const trimmed = query.trim();
  const valid = trimmed.length >= 2 && trimmed.length <= 100;
  const helperText = trimmed.length === 1 ? '검색어를 2자 이상 입력하세요.'
    : trimmed.length > 100 ? '검색어는 100자 이하여야 합니다.' : undefined;
  const search = useQuery({
    queryKey: ['session-search', scopeKey, trimmed],
    queryFn: ({ signal }) => fetchSessionRecordSearch(trimmed, 50, signal),
    enabled: valid,
    staleTime: 30_000,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const handleChange = (next: string) => setQuery(next);

  return (
    <GlobalSearch
      value={query}
      onChange={handleChange}
      resultCount={search.data?.length}
      results={valid ? search.data : undefined}
      loading={valid && search.isPending}
      errorText={search.isError ? searchErrorText(search.error) : null}
      helperText={helperText}
      onSelectResult={onSelect}
    />
  );
};

export { SessionSearchBox, searchErrorText };
export type { SessionSearchBoxProps };
