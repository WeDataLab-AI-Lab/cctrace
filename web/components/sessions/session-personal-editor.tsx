'use client';

import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ClassificationSelect } from './analysis/classification-select';
import type { SessionClassification } from '@/lib/session-analysis-types';
import { fetchPersonalSession, personalKeys, PersonalStateError, savePersonalSession } from '@/lib/personal-state-api';

interface SessionPersonalEditorProps {
  sessionId: string | null;
  userId: number;
  onClassificationSaved?: (sessionId: string, classification: SessionClassification) => void;
}

/** The session's personal classification. Free-text notes were removed; only the label is kept. */
const SessionPersonalEditor = ({ sessionId, userId, onClassificationSaved }: SessionPersonalEditorProps) => {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<SessionClassification | null>(null);
  const [error, setError] = useState<string | null>(null);
  const session = useQuery({
    queryKey: personalKeys.session(userId, sessionId ?? ''),
    queryFn: () => fetchPersonalSession(sessionId ?? ''),
    enabled: !!sessionId,
    retry: false,
  });
  const denied = session.error instanceof PersonalStateError && session.error.status === 404;
  const current = session.data?.classification ?? 'unsorted';
  const value = draft ?? current;
  const dirty = value !== current;
  const save = useMutation({
    mutationFn: (next: SessionClassification) => savePersonalSession(sessionId ?? '', {
      note: session.data?.note?.text ?? '', classification: next, expected_revision: session.data?.revision ?? 0,
    }),
    onSuccess: (saved) => {
      qc.setQueryData(personalKeys.session(userId, sessionId ?? ''), saved);
      for (const key of ['session-overview', 'session-overview-count', 'me/session-groups']) qc.invalidateQueries({ queryKey: [key] });
      setDraft(null);
      setError(null);
      onClassificationSaved?.(sessionId ?? '', saved.classification);
    },
    onError: (cause) => setError(cause instanceof Error ? cause.message : '저장하지 못했습니다.'),
  });

  if (!sessionId) return null;
  return <section aria-label="세션 분류" className="shrink-0 border-b border-border bg-surface px-4 py-1.5 text-xs">
    <div className="flex flex-wrap items-center gap-2">
      <button type="button" aria-expanded={open} onClick={() => setOpen(value => !value)} className="rounded px-1.5 py-0.5 font-medium text-ink-2 hover:bg-surface-sunk">분류</button>
      {!open && <span className="text-ink-3">{current === 'unsorted' ? '미분류' : current}</span>}
      {open && <>
        {denied ? <span role="alert" className="text-ink-3">이 세션은 소유권 또는 출처를 확인할 수 없어 분류를 사용할 수 없습니다.</span>
          : <>
            <ClassificationSelect value={value} onChange={setDraft} sessionLabel={sessionId.slice(0, 8)} />
            <button type="button" disabled={!dirty || save.isPending} onClick={() => save.mutate(value)} className="rounded bg-brand px-2 py-0.5 font-medium text-brand-ink disabled:opacity-40">저장</button>
            <span role="status" className="text-ink-3">{save.isPending ? '저장 중' : dirty ? '저장 안 됨' : '저장됨'}</span>
          </>}
      </>}
      {error && <span role="alert" className="text-danger">{error}</span>}
    </div>
  </section>;
};

export { SessionPersonalEditor };
