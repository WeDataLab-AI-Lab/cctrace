'use client';

import type { LucideIcon } from 'lucide-react';
import { Ban, Copy, Inbox, Lock, Scissors } from 'lucide-react';
import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';
import type { AnalysisCopies, AnalysisState } from '@/lib/session-analysis-types';
import { Chip } from './atoms';
import { formatCount, formatDateTime } from './format';

/**
 * 분석이 "있는 그대로" 보이지 못하는 모든 상태의 화면. 빈칸이 아니라 무슨 일이 있는지와 그래서 무엇을 믿을 수
 * 있는지를 적는다.
 *   building     사실을 아직 만들지 않았다           truncated   세션이 너무 커서 합계만 있다
 *   unsupported  분석 대상이 아닌 에이전트             none        구축이 끝났는데 읽을 수 있는 기록이 없다
 *   withheld     (목록에서만) 신원이 섞인 세션이라 보류   copies      다른 세션의 앞부분을 이어받았다
 */

interface NoticeCardProps {
  icon: LucideIcon;
  title: string;
  children: ReactNode;
  className?: string;
}

const NoticeCard = ({ icon: Icon, title, children, className }: NoticeCardProps) => (
  <div
    role="status"
    className={cn('flex gap-3 rounded-[var(--r-md)] border border-border bg-surface px-4 py-3.5', className)}
  >
    <Icon size={20} strokeWidth={1.5} className="mt-0.5 shrink-0 text-ink-3" aria-hidden="true" />
    <div className="min-w-0 space-y-1">
      <p className="text-[13px] font-semibold text-ink">{title}</p>
      <div className="text-[12px] leading-relaxed break-keep text-ink-2">{children}</div>
    </div>
  </div>
);

interface PendingNoticeProps {
  state: Exclude<AnalysisState, 'ready' | 'stale'>;
  agent?: string;
}

/** `ready`·`stale`이 아닌 응답의 안내. 롤업 머리가 있으면 호출한 쪽이 이 아래에 그린다. */
const PendingNotice = ({ state, agent }: PendingNoticeProps) => {
  switch (state) {
    case 'building':
      // 만드는 동안에는 안내를 두지 않는다. 분석 탭이 흐리게 비활성화되어 그 사실을 대신 알린다.
      return null;
    case 'truncated':
      return (
        <NoticeCard icon={Scissors} title="세션이 너무 커서 합계만 보입니다">
          기록이 너무 많거나 계산이 제한 시간 안에 끝나지 않아, 요청·신호·결과물·플랜은 계산하지 않았습니다.
          토큰과 비용은 목록과 같은 값이고, 요청 목록을 보려면 대화 탭을 여세요. 이 값들은 0이 아니라 <b className="font-semibold text-ink">계산하지 않은 것</b>입니다.
        </NoticeCard>
      );
    case 'unsupported':
      return (
        <NoticeCard icon={Ban} title="이 에이전트는 분석을 지원하지 않습니다">
          {agent ? <Chip tone="neutral" className="mr-1 align-middle">{agent}</Chip> : null}
          세션은 Claude Code와 Codex만 분석합니다. 이 세션의 토큰과 비용은 목록에서 그대로 볼 수 있고, 대화 탭도 평소처럼 열립니다.
        </NoticeCard>
      );
    case 'none':
      return (
        <NoticeCard icon={Inbox} title="읽을 수 있는 분석이 없습니다">
          분석 구축은 끝났지만 이 세션에서 읽을 수 있는 기록이 없습니다. 계정이나 세션 제외로 기록이 가려졌거나, 원문이 모두 삭제됐을 수
          있습니다.
        </NoticeCard>
      );
  }
};

interface UnavailableProps {
  /** 분석 API가 404를 돌려주거나, 목록 행이 `withheld`일 때. */
  reason: 'mixed_identity' | 'not_found';
}

/** 분석 API가 404인 세션(신원이 섞였거나 없는 세션)의 화면. 어느 쪽인지 구별해 알려 주지 않는다. */
const AnalysisUnavailable = ({ reason }: UnavailableProps) => (
  <NoticeCard icon={Lock} title="이 세션의 분석을 볼 수 없습니다">
    {reason === 'mixed_identity' ? (
      <>
        한 세션에 여러 신원(사용자·프로필)의 기록이 섞여 있어 세션 주인에게는 분석을 보이지 않습니다. 대화 원문은 대화 탭에서 볼 수
        있고, 관리자는 분석도 볼 수 있습니다.
      </>
    ) : (
      <>이 세션의 분석이 없거나 볼 권한이 없습니다. 대화 탭에서 원문을 확인하세요.</>
    )}
  </NoticeCard>
);

interface CopiesBannerProps {
  copies: AnalysisCopies;
  onOpenSession?: (sessionId: string) => void;
}

/** 복사 세션 안내(`copies`). 복사된 앞부분은 요청·입력·토큰·활동 범위 집계에서 빠진다. */
const CopiesBanner = ({ copies, onOpenSession }: CopiesBannerProps) => {
  if (copies.state === 'skipped_large_group') {
    return (
      <NoticeCard icon={Copy} title="복사 여부를 판정하지 않았습니다" className="bg-surface-2">
        이 세션이 속한 복사 그룹이 너무 커서 같은 기록이 다른 세션에도 있는지 판정하지 않았습니다. 같은 줄이 다른 세션에도 있다면 이
        세션의 요청·토큰에 중복으로 들어 있을 수 있습니다.
      </NoticeCard>
    );
  }
  const lines = typeof copies.copied_lines === 'number' ? `${formatCount(copies.copied_lines)}줄` : '앞부분';
  const until = copies.copied_until ? ` (${formatDateTime(copies.copied_until)}까지)` : '';
  return (
    <NoticeCard icon={Copy} title="다른 세션의 앞부분을 이어받은 세션입니다" className="bg-surface-2">
      <p>
        복사된 {lines}
        {until}은 이 세션의 요청·입력·토큰·활동 범위 집계에서 뺐습니다. 그래서 시작 시각과 길이가 목록의 값과 다를 수 있습니다.
      </p>
      <p className="mt-1.5">
        {copies.original_visible && copies.original_session_id ? (
          <button
            type="button"
            onClick={() => copies.original_session_id && onOpenSession?.(copies.original_session_id)}
            disabled={!onOpenSession}
            className="rounded border border-border bg-surface px-2 py-1 text-[11.5px] font-medium text-ink hover:bg-surface-sunk disabled:cursor-not-allowed disabled:opacity-60"
          >
            원본 세션 <span className="font-mono">{copies.original_session_id.slice(0, 8)}</span> 열기
          </button>
        ) : (
          <span className="text-ink-3">원본 세션은 이 화면에서 볼 수 없습니다.</span>
        )}
      </p>
    </NoticeCard>
  );
};

export { PendingNotice, AnalysisUnavailable, CopiesBanner };
