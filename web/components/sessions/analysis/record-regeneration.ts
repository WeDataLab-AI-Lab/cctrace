'use client';

import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { regenerateSessionRecord, SessionRecordRequestError, type SessionRecordRegenerationResult } from '@/lib/api';

const recordRequestErrorText = (error: unknown): string => {
  if (!(error instanceof SessionRecordRequestError)) return '세션 기록 재생성 요청을 처리하지 못했습니다.';
  if (error.code === 'rate_limited') return '10분 안에 다시 만들기 요청을 보냈습니다. 잠시 뒤 다시 시도해 주세요.';
  if (error.code === 'generation_in_progress') return '이 세션의 기록을 이미 만들고 있습니다.';
  if (error.code === 'consent_required') return '현재 런타임의 세션 기록 동의가 필요합니다.';
  if (error.code === 'records_disabled') return '관리자가 세션 기록 생성을 껐습니다.';
  return `재생성 요청이 ${error.code} 응답을 받았습니다.`;
};
const recordRegenerationNotice = (result: SessionRecordRegenerationResult): string | null => {
  if (result.state !== 'waiting_budget') return null;
  const retryAt = result.retry_at ? Date.parse(result.retry_at) : Number.NaN;
  const retryText = Number.isFinite(retryAt) ? new Date(retryAt).toLocaleString('ko-KR') : '재시도 시각을 알 수 없습니다';
  return `일일 토큰 한도로 대기 중입니다. 서버가 안내한 재시도 시각은 ${retryText}입니다.`;
};
const useSessionRecordRegeneration = (userId: number | null, sessionId: string | null, recordState?: string) => {
  const client = useQueryClient();
  const [accepted, setAccepted] = useState<{ sessionId: string; result: SessionRecordRegenerationResult } | null>(null);
  const mutation = useMutation({
    mutationFn: (id: string) => regenerateSessionRecord(id),
    onSuccess: (result, id) => {
      setAccepted({ sessionId: id, result });
      void client.invalidateQueries({ queryKey: ['session-analysis', userId] });
      for (const root of ['session-record-status', 'session-overview', 'session-overview-count', 'session-calendar']) {
        void client.invalidateQueries({ queryKey: [root] });
      }
    },
  });
  const regenerate = () => { if (sessionId) { setAccepted(null); mutation.mutate(sessionId); } };
  return { regenerate, pending: mutation.isPending && mutation.variables === sessionId,
    accepted: accepted?.sessionId === sessionId && (accepted?.result.state === 'waiting_budget' || recordState === 'none' || recordState === 'failed'),
    acceptedMessage: accepted?.sessionId === sessionId ? recordRegenerationNotice(accepted.result) : null,
    error: mutation.error && mutation.variables === sessionId ? recordRequestErrorText(mutation.error) : null };
};

export { useSessionRecordRegeneration };
