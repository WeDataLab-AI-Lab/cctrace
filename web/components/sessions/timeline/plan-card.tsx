'use client';

import { Lock } from 'lucide-react';
import { useId, useState } from 'react';
import { cn } from '@/lib/utils';
import type { AnalysisPlan, PlanDecision } from '@/lib/session-analysis-types';
import { Chip, type ChipTone } from '@/components/sessions/analysis/atoms';
import { formatClock } from '@/components/sessions/analysis/format';
import { PLAN_DECISION_HINT, PLAN_DECISION_LABEL, UNKNOWN_TEXT } from '@/components/sessions/analysis/labels';
import { ConversationMarkdown } from '@/components/sessions/conversation-markdown';

/**
 * 플랜 카드(F7). ExitPlanMode로 올라온 플랜과 사용자의 결정.
 *   머리  결정 칩(승인·반려·대기 중·중단) · 시각 · 플랜 파일 경로
 *   본문  마크다운. 길면 접어 두고 "전체 보기"로 편다. 가려졌거나 보류됐거나 비었으면 이유를 적는다.
 */

const DECISION_TONE: Record<PlanDecision, ChipTone> = {
  approved: 'success',
  rejected: 'danger',
  interrupted: 'warning',
  pending: 'unknown',
};

/** 이 줄 수나 글자 수를 넘는 본문은 접는다. 마크다운은 줄 수가 길이를 더 잘 말한다. */
const COLLAPSE_LINES = 9;
const COLLAPSE_CHARS = 700;

const NOTE_CLASS = 'rounded bg-surface-sunk px-2.5 py-2 text-[11.5px] leading-relaxed text-ink-3';

const PlanBody = ({ plan }: { plan: AnalysisPlan }) => {
  const bodyId = useId();
  const [open, setOpen] = useState(false);
  const body = plan.body?.trim() ?? '';

  if (plan.state === 'redacted') {
    return (
      <p className={cn(NOTE_CLASS, 'flex items-start gap-2')}>
        <Lock size={12} strokeWidth={1.75} className="mt-0.5 shrink-0" aria-hidden="true" />
        비식별화로 플랜 본문이 가려졌습니다.
      </p>
    );
  }
  if (plan.state === 'withheld') {
    return <p className={NOTE_CLASS}>플랜 본문을 서버에 올리지 않아 보이지 않습니다(보류됨). 본문이 없다는 뜻이 아닙니다.</p>;
  }
  if (!body) return <p className={NOTE_CLASS}>플랜 본문이 기록되지 않았습니다.</p>;

  const long = body.split('\n').length > COLLAPSE_LINES || body.length > COLLAPSE_CHARS;
  return (
    <div>
      <div
        id={bodyId}
        // 접힌 동안 잘려 보이지 않는 링크가 Tab 순서에 남지 않게 한다.
        inert={long && !open}
        className={cn(
          'text-[12px] leading-5 text-ink-2',
          long && !open && 'relative max-h-[7.5rem] overflow-hidden after:absolute after:inset-x-0 after:bottom-0 after:h-10 after:bg-linear-to-t after:from-surface after:to-transparent',
        )}
      >
        <ConversationMarkdown text={body} />
      </div>
      {long && (
        <button
          type="button"
          aria-expanded={open}
          aria-controls={bodyId}
          onClick={() => setOpen((v) => !v)}
          className="mt-1 rounded px-1 py-0.5 text-[11px] font-medium text-ink-2 underline decoration-dotted underline-offset-2 hover:text-ink"
        >
          {open ? '접기' : '전체 보기'}
        </button>
      )}
    </div>
  );
};

const PlanCard = ({ plan }: { plan: AnalysisPlan }) => {
  const time = formatClock(plan.ts);
  return (
    <div
      role="group"
      aria-label="플랜"
      data-plan-decision={plan.decision}
      className="w-full rounded-xl border border-border bg-surface px-3 py-2.5"
    >
      <div className="mb-2 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <span className="text-[12px] font-semibold text-ink">플랜</span>
        <Chip tone={DECISION_TONE[plan.decision] ?? 'neutral'} title={PLAN_DECISION_HINT[plan.decision]}>
          {PLAN_DECISION_LABEL[plan.decision] ?? plan.decision}
        </Chip>
        {time !== UNKNOWN_TEXT && (
          <time dateTime={plan.ts} className="font-mono text-[11px] tabular-nums text-ink-3">
            {time}
          </time>
        )}
        {plan.file_path ? (
          <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-ink-3" title={plan.file_path}>
            {plan.file_path}
          </span>
        ) : (
          <span className="text-[11px] text-ink-4">플랜 파일 경로 없음</span>
        )}
      </div>
      <PlanBody plan={plan} />
    </div>
  );
};

export { PlanCard };
