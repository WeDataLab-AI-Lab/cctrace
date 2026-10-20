'use client';

import type { LucideIcon } from 'lucide-react';
import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { UNKNOWN_TEXT } from './labels';

/**
 * 분석 화면이 함께 쓰는 작은 부품들.
 *
 * 시각 규칙 하나가 이 파일의 이유다: "판단 불가"는 0과도 오류와도 다르게 보여야 한다.
 *   - 0        그냥 숫자 0 (잉크색)
 *   - 오류     danger 색 칩
 *   - 판단 불가  회색 점선(Unknown, 점선 점). 어디서나 같은 모양이다.
 * 동적 값(막대 길이)은 SVG 속성으로 그려서 인라인 style 없이 해결한다(FRONT-RULE).
 */

/* ---------------- Chip ---------------- */

type ChipTone =
  | 'neutral'
  | 'outline'
  | 'brand'
  | 'success'
  | 'warning'
  | 'danger'
  | 'unknown'
  | 'claude'
  | 'codex';

const CHIP_TONE: Record<ChipTone, string> = {
  neutral: 'bg-surface-sunk text-ink-2',
  outline: 'border-border bg-surface text-ink-2',
  brand: 'bg-brand-soft text-brand',
  success: 'bg-success-soft text-success-strong',
  warning: 'bg-warning-soft text-warning-strong',
  danger: 'bg-danger-soft text-danger-strong',
  unknown: 'border-dashed border-border-strong bg-transparent text-ink-3',
  claude: 'bg-agent-claude-soft text-agent-claude',
  codex: 'bg-agent-codex-soft text-agent-codex',
};

const CHIP_SIZE = {
  sm: 'px-1.5 py-px text-[10px] leading-4',
  md: 'px-1.5 py-0.5 text-[10.5px] leading-4',
} as const;

interface ChipProps {
  tone?: ChipTone;
  size?: keyof typeof CHIP_SIZE;
  icon?: LucideIcon;
  title?: string;
  className?: string;
  children: ReactNode;
}

/** 모든 칩이 1px 테두리(투명)를 가져, 점선 칩과 채운 칩의 높이가 같다. */
const Chip = ({ tone = 'neutral', size = 'md', icon: Icon, title, className, children }: ChipProps) => (
  <span
    title={title}
    className={cn(
      'inline-flex max-w-full shrink-0 items-center gap-1 rounded border border-transparent font-medium whitespace-nowrap',
      CHIP_SIZE[size],
      CHIP_TONE[tone],
      className,
    )}
  >
    {Icon && <Icon size={size === 'sm' ? 10 : 11} strokeWidth={2} className="shrink-0" aria-hidden="true" />}
    <span className="truncate">{children}</span>
  </span>
);

/* ---------------- Hint (키보드로도 읽히는 툴팁) ---------------- */

interface HintProps {
  /** 툴팁에 들어갈 문장. */
  label: ReactNode;
  children: ReactNode;
  className?: string;
  /** 스크린리더가 읽는 이름. 생략하면 children 의 텍스트를 쓴다. */
  ariaLabel?: string;
}

/**
 * 툴팁 트리거. 버튼이라 Tab 으로 닿고, 포커스만으로도 툴팁이 열린다. 다른 버튼 안에는 넣지 않는다
 * (중첩된 상호작용 요소) — 그런 자리에서는 title 속성을 쓴다.
 */
const Hint = ({ label, children, className, ariaLabel }: HintProps) => (
  <Tooltip>
    <TooltipTrigger asChild>
      <button type="button" aria-label={ariaLabel} className={cn('cursor-help rounded-sm text-left align-baseline', className)}>
        {children}
      </button>
    </TooltipTrigger>
    <TooltipContent className="max-w-[260px] leading-relaxed" side="top">
      {label}
    </TooltipContent>
  </Tooltip>
);

/* ---------------- Unknown ---------------- */

interface UnknownProps {
  /** 보이는 문구. 기본은 "알 수 없음". */
  label?: string;
  /** 왜 모르는지. 있으면 툴팁이 된다. */
  reason?: string;
  /** "?" 한 글자로 줄인다(목록 행처럼 좁은 자리). */
  compact?: boolean;
  /**
   * 버튼 안처럼 상호작용 요소를 중첩할 수 없는 자리. 키보드 툴팁(Hint) 대신 title 속성을 쓴다.
   * title은 마우스로만 열리므로, 이 자리를 쓰는 쪽은 이유를 글자로도 어딘가에 보여 줘야 한다
   * (예: 요청 목록 아래의 "알 수 없는 값의 이유").
   */
  inert?: boolean;
  className?: string;
}

/** 값이 없는 것(모름). 0이 아니다. 회색 점선 알약. */
const Unknown = ({ label = UNKNOWN_TEXT, reason, compact = false, inert = false, className }: UnknownProps) => {
  const classes = cn(
    'inline-flex items-center rounded border border-dashed border-border-strong px-1 text-[10.5px] leading-4 font-medium text-ink-3',
    compact && 'min-w-4 justify-center px-0.5',
    className,
  );
  // "?" 한 글자는 소리 내어 읽을 글자가 아니라서 이름을 붙인다. 글자가 보이는 쪽은 그 글자가 이름이다.
  const body = compact ? (
    <span role="img" aria-label={label} className={classes}>
      ?
    </span>
  ) : (
    <span className={classes}>{label}</span>
  );
  if (inert || !reason) {
    return (
      <span title={reason} className="inline-flex">
        {body}
      </span>
    );
  }
  return (
    <Hint label={reason} ariaLabel={`${label}. ${reason}`}>
      {body}
    </Hint>
  );
};

/* ---------------- Dot ---------------- */

type DotKind = 'solid-warning' | 'solid-danger' | 'solid-success' | 'solid-brand' | 'solid-muted' | 'ring' | 'ring-warning' | 'dashed';

const DOT_CLASS: Record<DotKind, string> = {
  'solid-warning': 'bg-warning',
  'solid-danger': 'bg-danger',
  'solid-success': 'bg-success motion-safe:animate-pulse',
  'solid-brand': 'bg-brand motion-safe:animate-pulse',
  'solid-muted': 'bg-ink-3',
  ring: 'border border-border-strong',
  'ring-warning': 'border border-warning',
  dashed: 'border border-dashed border-ink-3',
};

const Dot = ({ kind, className }: { kind: DotKind; className?: string }) => (
  <span aria-hidden="true" className={cn('inline-block size-2 shrink-0 rounded-full', DOT_CLASS[kind], className)} />
);

/* ---------------- Panel (패스 C) ---------------- */

interface PanelProps {
  id?: string;
  title: string;
  /** 머리띠 오른쪽(개수, 보조 문구). */
  aside?: ReactNode;
  className?: string;
  bodyClassName?: string;
  children: ReactNode;
}

/**
 * 레퍼런스의 패널: 회색 머리띠에 작은 굵은 제목, 아래가 본문. 그림자 없이 헤어라인 테두리만 쓴다.
 * 패스 A의 섹션 패널보다 머리가 낮고 아이콘이 없다 — 한 화면에 패널 여섯 개가 함께 보여야 해서다.
 */
const Panel = ({ id, title, aside, className, bodyClassName, children }: PanelProps) => (
  <section
    id={id}
    aria-labelledby={id ? `${id}-title` : undefined}
    aria-label={id ? undefined : title}
    className={cn('scroll-mt-20 rounded-[var(--r-sm)] border border-border bg-surface', className)}
  >
    <header className="flex min-h-[22px] items-center gap-2 rounded-t-[var(--r-sm)] border-b border-border-subtle bg-surface-2 px-2.5 py-0.5">
      <h3 id={id ? `${id}-title` : undefined} className="shrink-0 text-[11px] font-semibold text-ink-2">
        {title}
      </h3>
      {aside && <div className="ml-auto flex min-w-0 items-center gap-2 pl-2 text-[10.5px] text-ink-3">{aside}</div>}
    </header>
    <div className={cn('px-2.5 py-1.5', bodyClassName)}>{children}</div>
  </section>
);

/* ---------------- Stat ---------------- */

interface StatProps {
  label: ReactNode;
  children: ReactNode;
  caption?: ReactNode;
  className?: string;
}

/** 머리의 지표 한 칸. 라벨은 작게, 값은 크게, 보충은 그 아래. */
const Stat = ({ label, children, caption, className }: StatProps) => (
  <div className={cn('min-w-0', className)}>
    <div className="flex items-center gap-1.5 text-[10.5px] font-medium text-ink-3">{label}</div>
    <div className="mt-0.5 text-[15px] leading-5 font-semibold tabular-nums text-ink">{children}</div>
    {caption && <div className="mt-0.5 text-[10.5px] leading-4 text-ink-3">{caption}</div>}
  </div>
);

/* ---------------- 막대 (SVG) ---------------- */

interface PercentBarProps {
  /** 0~100. */
  value: number;
  label: string;
  className?: string;
  /** 채움 색(fill-*). 기본은 잉크색. */
  fillClass?: string;
}

/** 채움 막대 하나. 길이는 SVG 속성(%)이라 인라인 style 이 필요 없다. */
const PercentBar = ({ value, label, className, fillClass = 'fill-ink-2' }: PercentBarProps) => (
  <svg role="img" aria-label={label} className={cn('block h-1.5 w-full', className)}>
    <rect width="100%" height="100%" rx="3" className="fill-border" />
    <rect width={`${value}%`} height="100%" rx="3" className={fillClass} />
  </svg>
);

interface StackSegment {
  key: string;
  label: string;
  value: number;
  /** 채움 클래스(fill-*). */
  fill: string;
}

/** 여러 칸을 이어 붙인 막대. 합이 0이면 그리지 않는다. */
const StackedBar = ({ segments, label, className }: { segments: StackSegment[]; label: string; className?: string }) => {
  const total = segments.reduce((sum, s) => sum + s.value, 0);
  if (total <= 0) return null;
  return (
    <div className={cn('overflow-hidden rounded-full', className)}>
      <svg role="img" aria-label={label} className="block h-1.5 w-full">
        <rect width="100%" height="100%" className="fill-border" />
        {segments.map((s, i) => {
          const before = segments.slice(0, i).reduce((sum, p) => sum + p.value, 0);
          return (
            <rect
              key={s.key}
              x={`${(before / total) * 100}%`}
              width={`${(s.value / total) * 100}%`}
              height="100%"
              className={s.fill}
            />
          );
        })}
      </svg>
    </div>
  );
};

export { Chip, Hint, Unknown, Dot, Panel, Stat, PercentBar, StackedBar };
export type { ChipTone, DotKind, StackSegment };
