'use client';

import { Check, CircleCheck, CircleDot, CirclePlus, GitCommitHorizontal, GitPullRequest, Minus, Pencil, X } from 'lucide-react';
import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';
import type { TaskKind, TaskUpdate } from '@/lib/session-analysis-types';
import { Hint } from './atoms';
import type { CheckChipView, CheckGlyph, CheckTone, StatusTone, StatusView } from './judgement';
import { taskActionStates, taskActionsText } from './outcomes';

/**
 * 패스 C의 작은 부품: 해시 칩, task 칩, 동작 아이콘 넷, 판정 칩, 상태 칩, 지표 칩.
 *
 * 해시와 task 칩은 행 안에 글처럼 흐르는 인라인 요소라 상호작용을 갖지 않는다(눌리는 줄 안에 들어가는 일이 많다).
 * 자세한 설명은 `title`로 준다. 판정 칩과 지표 칩은 단독으로 놓이므로 키보드로 닿는 툴팁(Hint)을 쓴다.
 */

/* ---------------- 해시 · task ---------------- */

/** 커밋 해시 칩. 해시를 모르면(레퍼런스의 "ハッシュ不明" 행) 점선 칩이다. */
const HashChip = ({ short, title, className }: { short: string | null; title?: string; className?: string }) =>
  short ? (
    <span
      title={title ?? `커밋 ${short}`}
      className={cn(
        'inline-flex shrink-0 items-center gap-1 rounded-md border border-border-subtle bg-surface-sunk px-1.5 py-px align-baseline font-mono text-[10.5px] leading-4 text-cat-1',
        className,
      )}
    >
      <GitCommitHorizontal size={11} strokeWidth={2} className="shrink-0 text-cat-6" aria-hidden="true" />
      {short}
    </span>
  ) : (
    <span
      title="이 커밋의 해시를 알 수 없습니다. 명령에서 제목만 읽었고 저장소에서 같은 제목의 커밋을 찾지 못했습니다."
      className={cn(
        'inline-flex shrink-0 items-center gap-1 rounded-md border border-dashed border-border-strong px-1.5 py-px align-baseline text-[10.5px] leading-4 text-ink-3',
        className,
      )}
    >
      <GitCommitHorizontal size={11} strokeWidth={2} className="shrink-0" aria-hidden="true" />
      해시 불명
    </span>
  );

const TASK_ICON: Record<TaskKind, typeof CircleCheck> = { task: CircleCheck, issue: CircleDot, pr: GitPullRequest };

/** task·이슈·PR 칩. 점선 둥근 칩에 id가 글자다. */
const TaskChip = ({ id, kind = 'task', title, className }: { id: string; kind?: TaskKind; title?: string; className?: string }) => {
  const Icon = TASK_ICON[kind] ?? CircleCheck;
  return (
    <span
      title={title ?? id}
      className={cn(
        'inline-flex shrink-0 items-center gap-1 rounded-full border border-dashed border-cat-1/55 px-1.5 py-px align-baseline text-[10.5px] leading-4 font-medium text-cat-1',
        className,
      )}
    >
      <Icon size={11} strokeWidth={2} className="shrink-0" aria-hidden="true" />
      {id}
    </span>
  );
};

const ACTION_ICON = { added: CirclePlus, completed: CircleCheck, edited: Pencil, commit_linked: GitCommitHorizontal } as const;
const ACTION_LIT_CLASS = {
  added: 'text-cat-1',
  completed: 'text-success',
  edited: 'text-warning-strong',
  commit_linked: 'text-cat-6',
} as const;

/**
 * 동작 아이콘 넷: 추가 · 완료 · 수정 · 커밋 연결. 켜진 것은 색이 있고 꺼진 것은 흐리다.
 * 색만으로 구분하지 않도록 접근성 이름에 켜진 동작을 글자로 적는다.
 */
const TaskActionIcons = ({ task, className }: { task: TaskUpdate; className?: string }) => (
  <span role="img" aria-label={`동작: ${taskActionsText(task)}`} className={cn('inline-flex items-center gap-1', className)}>
    {taskActionStates(task).map((a) => {
      const Icon = ACTION_ICON[a.key];
      return (
        <Icon
          key={a.key}
          size={14}
          strokeWidth={a.lit ? 2.25 : 1.75}
          className={a.lit ? ACTION_LIT_CLASS[a.key] : 'text-ink-4'}
          aria-hidden="true"
        >
          <title>{`${a.label}${a.lit ? '' : ' 없음'}`}</title>
        </Icon>
      );
    })}
  </span>
);

/* ---------------- 판정 칩 (D8) ---------------- */

const CHECK_CLASS: Record<CheckTone, string> = {
  pass: 'border-success/35 bg-success-soft text-success-strong',
  fail: 'border-danger/35 bg-danger-soft text-danger-strong',
  unknown: 'border-dashed border-border-strong bg-transparent text-ink-3',
};

const GLYPH_ICON: Record<CheckGlyph, typeof Check> = { check: Check, cross: X, dash: Minus };

/** 칩 안의 기호. 목록 카드는 이것만 쓴다. */
const CheckGlyphIcon = ({ glyph, size = 12, className }: { glyph: CheckGlyph; size?: number; className?: string }) => {
  const Icon = GLYPH_ICON[glyph];
  return <Icon size={size} strokeWidth={2.25} className={cn('shrink-0', className)} aria-hidden="true" />;
};

const EvidenceBody = ({ lines }: { lines: readonly string[] }) => (
  <span className="block space-y-0.5 text-left">
    {lines.map((line, i) => (
      <span key={`${i}-${line}`} className={cn('block', i === 0 && 'font-medium')}>
        {line}
      </span>
    ))}
  </span>
);

/** 판정 칩 하나. 통과는 초록 확인, 실패는 빨강 엑스와 남은 것, 판단 불가는 회색 점선 대시다. */
const JudgementChip = ({ view }: { view: CheckChipView }) => (
  <Hint label={<EvidenceBody lines={view.evidence} />} ariaLabel={view.ariaLabel} className="rounded-full">
    <span
      data-check={view.key}
      data-state={view.state}
      className={cn(
        'inline-flex items-center gap-1 rounded-full border px-2.5 py-[3px] text-[11.5px] leading-4 font-medium whitespace-nowrap',
        CHECK_CLASS[view.tone],
      )}
    >
      <CheckGlyphIcon glyph={view.glyph} />
      {view.label}
    </span>
  </Hint>
);

/* ---------------- 상태 칩 (D2) ---------------- */

const STATUS_CLASS: Record<StatusTone, string> = {
  live: 'border-border-strong bg-surface text-ink',
  success: 'border-success/35 bg-success-soft text-success-strong',
  warning: 'border-warning/45 bg-warning-soft text-warning-strong',
  danger: 'border-danger/35 bg-danger-soft text-danger-strong',
  unknown: 'border-dashed border-border-strong bg-transparent text-ink-3',
};

/** 머리의 상태 칩: 실행 중 · 완료 · 작업 도중 · 중단 중 · 도중 종료 · 판정 불가. */
const StatusChip = ({ view, className, titled = true }: { view: StatusView; className?: string; titled?: boolean }) => (
  <span
    title={titled ? `${view.label} — ${view.hint}` : undefined}
    data-status-tone={view.tone}
    className={cn(
      'inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-px text-[11px] leading-4 font-medium whitespace-nowrap',
      STATUS_CLASS[view.tone],
      className,
    )}
  >
    {view.tone === 'live' && <span aria-hidden="true" className="size-1.5 rounded-full bg-success motion-safe:animate-pulse" />}
    {view.label}
  </span>
);

/* ---------------- 지표 칩 (D7) ---------------- */

type MetricTone = 'tokens' | 'cost' | 'context' | 'unknown';

const METRIC_CLASS: Record<MetricTone, string> = {
  tokens: 'border-cat-6/45 bg-cat-6/5 text-cat-6',
  cost: 'border-cat-1/45 bg-cat-1/5 text-cat-1',
  context: 'border-border bg-surface-sunk text-ink-2',
  unknown: 'border-dashed border-border-strong bg-transparent text-ink-3',
};

/** 머리 오른쪽의 지표 칩 하나. 툴팁(`hint`)이 구성과 출처를 말한다. */
const MetricChip = ({ tone, hint, ariaLabel, children }: { tone: MetricTone; hint: ReactNode; ariaLabel: string; children: ReactNode }) => (
  <Hint label={hint} ariaLabel={ariaLabel} className="rounded-md">
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-md border px-1.5 py-px text-[11px] leading-4 font-medium whitespace-nowrap tabular-nums',
        METRIC_CLASS[tone],
      )}
    >
      {children}
    </span>
  </Hint>
);

export { HashChip, TaskChip, TaskActionIcons, CheckGlyphIcon, EvidenceBody, JudgementChip, StatusChip, MetricChip };
export type { MetricTone };
