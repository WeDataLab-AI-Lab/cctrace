'use client';

import { useId, useState } from 'react';
import { Lock } from 'lucide-react';
import type { AnalysisPlan, AnalysisTurn, RulesFileVersion } from '@/lib/session-analysis-types';
import { ConversationMarkdown } from '../conversation-markdown';
import { Chip, Panel, type ChipTone } from './atoms';
import { formatCount, formatDateTime } from './format';
import { PLAN_DECISION_HINT, PLAN_DECISION_LABEL } from './labels';

/**
 * 플랜(F7)과 규칙 파일 판(D14): 턴 상세 아래에 조밀한 패널로 붙는다.
 *
 * 플랜 카드: 본문(마크다운), 플랜 파일 경로, 승인·반려·중단·대기 결정. 본문이 길면 접어서 둔다.
 * 규칙 파일 판: "세션 시작 전에 서버가 마지막으로 본 판"이다. 세션이 실제로 읽은 판이라고 말하지 않는다.
 */

const DECISION_TONE: Record<AnalysisPlan['decision'], ChipTone> = {
  approved: 'success',
  rejected: 'danger',
  interrupted: 'warning',
  pending: 'unknown',
};

/** 이 줄 수나 글자 수를 넘는 본문은 접는다. 마크다운은 줄 수가 길이를 더 잘 말한다. */
const COLLAPSE_LINES = 9;
const COLLAPSE_CHARS = 700;

interface PlanCardProps {
  plan: AnalysisPlan;
  requestNo: number | null;
  /** 본문 합계 상한에 걸려 본문이 비었는가(truncated.bodies). */
  bodiesTruncated: boolean;
  timeZone?: string;
}

const PlanBody = ({ plan, bodiesTruncated }: { plan: AnalysisPlan; bodiesTruncated: boolean }) => {
  const bodyId = useId();
  const [open, setOpen] = useState(false);
  const body = plan.body?.trim() ?? '';

  if (plan.state === 'redacted') {
    return (
      <p className="flex items-start gap-2 rounded bg-surface-sunk px-2.5 py-2 text-[11.5px] leading-relaxed text-ink-3">
        <Lock size={12} strokeWidth={1.75} className="mt-0.5 shrink-0" aria-hidden="true" />
        비식별화로 플랜 본문이 가려졌습니다.
      </p>
    );
  }
  if (plan.state === 'withheld') {
    return (
      <p className="rounded bg-surface-sunk px-2.5 py-2 text-[11.5px] leading-relaxed text-ink-3">
        플랜 본문을 서버에 올리지 않아 보이지 않습니다(보류됨). 본문이 없다는 뜻이 아닙니다.
      </p>
    );
  }
  if (!body) {
    return (
      <p className="rounded bg-surface-sunk px-2.5 py-2 text-[11.5px] leading-relaxed text-ink-3">
        {bodiesTruncated ? '본문 합계 상한을 넘어 이 플랜의 본문은 담기지 않았습니다.' : '플랜 본문이 기록되지 않았습니다.'}
      </p>
    );
  }
  const long = body.split('\n').length > COLLAPSE_LINES || body.length > COLLAPSE_CHARS;
  return (
    <div>
      <div
        id={bodyId}
        // 접힌 동안 잘려 보이지 않는 링크가 Tab 순서에 남지 않게 한다.
        inert={long && !open}
        className={
          long && !open
            ? 'relative max-h-[7.5rem] overflow-hidden text-[12px] leading-5 text-ink-2 after:absolute after:inset-x-0 after:bottom-0 after:h-10 after:bg-linear-to-t after:from-surface after:to-transparent'
            : 'text-[12px] leading-5 text-ink-2'
        }
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

const PlanCard = ({ plan, requestNo, bodiesTruncated, timeZone }: PlanCardProps) => (
  <li className="rounded-[var(--r-sm)] border border-border-subtle bg-surface px-3 py-2.5">
    <div className="mb-2 flex flex-wrap items-center gap-x-2 gap-y-1">
      <Chip tone={DECISION_TONE[plan.decision]} title={PLAN_DECISION_HINT[plan.decision]}>
        {PLAN_DECISION_LABEL[plan.decision]}
      </Chip>
      {requestNo != null && <span className="font-mono text-[11px] font-medium text-ink-2">요청 #{requestNo}</span>}
      <time dateTime={plan.ts} className="font-mono text-[11px] tabular-nums text-ink-3">
        {formatDateTime(plan.ts, timeZone)}
      </time>
      {plan.file_path ? (
        <span className="min-w-0 truncate font-mono text-[11px] text-ink-3" title={plan.file_path}>
          {plan.file_path}
          <span className="ml-2 font-sans" title="파일의 마지막 갱신 시각이며 승인·반려 시각과는 다릅니다.">파일 갱신 {plan.file_updated_at ? formatDateTime(plan.file_updated_at, timeZone) : '시각 미수집'}</span>
        </span>
      ) : (
        <span className="text-[11px] text-ink-4">플랜 파일 경로 없음</span>
      )}
    </div>
    <PlanBody plan={plan} bodiesTruncated={bodiesTruncated} />
  </li>
);

interface PlansPanelProps {
  id: string;
  plans: readonly AnalysisPlan[];
  turns: readonly AnalysisTurn[];
  truncated: boolean;
  bodiesTruncated: boolean;
  timeZone?: string;
}

const PlansPanel = ({ id, plans, turns, truncated, bodiesTruncated, timeZone }: PlansPanelProps) => {
  const requestOf = (turnKey: string): number | null => turns.find((t) => t.key === turnKey)?.request_no ?? null;
  const ordered = [...plans].sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts));
  return (
    <Panel
      id={id}
      title="플랜"
      aside={ordered.length > 0 ? <span className="tabular-nums">{formatCount(ordered.length)}건 · ExitPlanMode로 올라온 플랜과 사용자의 결정</span> : undefined}
    >
      {ordered.length === 0 ? (
        <p className="text-[12px] text-ink-3">이 세션에서 올라온 플랜이 없습니다.</p>
      ) : (
        <ul className="space-y-2">
          {ordered.map((plan) => (
            <PlanCard key={`${plan.turn_key}-${plan.ts}`} plan={plan} requestNo={requestOf(plan.turn_key)} bodiesTruncated={bodiesTruncated} timeZone={timeZone} />
          ))}
        </ul>
      )}
      {truncated && <p className="mt-2 text-[11px] text-ink-3">플랜이 많아 일부만 담았습니다.</p>}
    </Panel>
  );
};

/* ---------------- 규칙 파일 판 ---------------- */

interface RulesPanelProps {
  id: string;
  files: readonly RulesFileVersion[] | undefined;
  timeZone?: string;
}

const RulesPanel = ({ id, files, timeZone }: RulesPanelProps) => (
  <Panel
    id={id}
    title="규칙 파일 판"
    aside={files && files.length > 0 ? <span className="tabular-nums">{formatCount(files.length)}개 · 세션 시작 전에 서버가 마지막으로 본 판</span> : undefined}
  >
    {!files || files.length === 0 ? (
      <p className="text-[12px] text-ink-3">세션 시작 전에 서버가 본 규칙 파일(CLAUDE.md, AGENTS.md)이 없습니다.</p>
    ) : (
      <ul className="divide-y divide-border-subtle">
        {files.map((f) => (
          <li key={f.path} className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 py-1 text-[12px]">
            <span className="min-w-0 truncate font-mono text-ink" title={f.path}>
              {f.path}
            </span>
            <span className="tabular-nums font-medium text-ink-2">판 {f.version}</span>
            <span className="tabular-nums text-[11px] text-ink-3">서버가 처음 본 시각 {formatDateTime(f.discovered_at, timeZone)}</span>
          </li>
        ))}
      </ul>
    )}
    <p className="mt-1.5 text-[11px] leading-relaxed text-ink-3">
      세션이 시작되기 전에 서버가 마지막으로 본 판입니다. 세션이 실제로 읽은 판이라고 단정할 수는 없습니다.
    </p>
  </Panel>
);

export { PlansPanel, RulesPanel };
