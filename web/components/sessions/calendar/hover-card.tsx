'use client';

import { cn } from '@/lib/utils';
import { Abs } from './css-box';
import { bandHover, inputHover, type BandHover, type InputHover } from './hover';
import type { PreparedSession } from './prepare';

/**
 * 띠와 입력선의 호버 카드(C14, C16). 마우스나 포커스가 닿은 곳 옆에 뜬다. 카드는 포인터를 받지 않는다(`pointer-events-none`)
 * — 카드가 띠를 가려 호버가 깜빡이는 일이 없다. 위치는 격자 안의 좌표(px)로 받아 CSS 변수로 쓴다.
 */

/** 카드 너비(px). 오른쪽 가장자리에서 넘치면 왼쪽으로 뒤집는다. */
const CARD_WIDTH = 264;
/** 카드 높이의 어림(px). 보이는 영역 아래로 넘칠 것 같으면 포인터 위로 뒤집는다. */
const CARD_HEIGHT = { band: 290, input: 240 } as const;
/** 포인터와 카드 사이 간격(px). */
const CARD_GAP = 14;

interface Placed {
  /** 격자 안의 좌표(px). */
  x: number;
  y: number;
  /** 카드를 포인터 왼쪽에 둔다. */
  flip: boolean;
  /** 카드를 포인터 위쪽에 둔다. */
  flipY: boolean;
}

export type HoverTarget =
  | ({ kind: 'band'; key: string; sessionId: string; segmentIndex: number; keyboard?: boolean } & Placed)
  | ({ kind: 'input'; key: string; sessionId: string; inputIndex: number; keyboard?: boolean } & Placed);

const Stat = ({ label, value, unknown }: { label: string; value: string; unknown: boolean }) => (
  <div className="flex items-baseline justify-between gap-3">
    <dt className="text-ink-3">{label}</dt>
    <dd className={cn('tabular-nums text-ink', unknown && 'rounded border border-dashed border-border-strong px-1 text-[10.5px] text-ink-3')}>{value}</dd>
  </div>
);

const BandCard = ({ content }: { content: BandHover }) => (
  <>
    <div className="flex items-baseline justify-between gap-2">
      <p className={cn('min-w-0 break-words text-[12.5px] font-semibold', content.titleMuted ? 'font-normal text-ink-3 italic' : 'text-ink')}>{content.title}</p>
      <span className="shrink-0 font-mono text-[10px] text-ink-3">{content.shortId}</span>
    </div>
    <p className="mt-0.5 tabular-nums text-ink-2">{content.time}</p>
    {content.note && <p className="mt-1 leading-relaxed text-ink-3">{content.note}</p>}
    <dl className="mt-1.5 space-y-0.5 border-t border-border-subtle pt-1.5">
      {content.stats.map((s) => (
        <Stat key={s.label} {...s} />
      ))}
    </dl>
    {content.tags.length > 0 && (
      <dl className="mt-1.5 space-y-0.5 border-t border-border-subtle pt-1.5">
        {content.tags.map((tag) => (
          <div key={tag.label} className="flex items-baseline justify-between gap-3">
            <dt className="shrink-0 text-ink-3">{tag.label}</dt>
            <dd className="min-w-0 truncate text-right text-ink-2">{tag.value}</dd>
          </div>
        ))}
      </dl>
    )}
    {content.whole && <p className="mt-1.5 border-t border-border-subtle pt-1.5 text-[11px] text-ink-3 tabular-nums">{content.whole}</p>}
    {content.warnings.map((warning) => <p key={warning} className="mt-1.5 border-t border-warning/30 pt-1.5 text-[11px] text-warning-strong">{warning}</p>)}
  </>
);

const InputCard = ({ content }: { content: InputHover }) => (
  <>
    <p className="tabular-nums text-ink-2">
      <span className="font-semibold text-ink">{content.time}</span> · 입력
    </p>
    {content.line ? (
      <p className="mt-1 leading-relaxed break-words text-ink">{content.line}</p>
    ) : (
      <p className="mt-1 leading-relaxed text-ink-3 italic">{content.note}</p>
    )}
    <p className={cn('mt-1.5 break-words text-[11px]', content.titleMuted ? 'text-ink-4 italic' : 'text-ink-3')}>{content.title}</p>
    <p className="mt-1.5 border-t border-border-subtle pt-1.5 text-ink-2">활동 {content.band.time}</p>
    <dl className="mt-1 space-y-0.5">{content.band.stats.map((stat) => <Stat key={stat.label} {...stat} />)}</dl>
    {content.band.warnings.map((warning) => <p key={warning} className="mt-1 text-[11px] text-warning-strong">{warning}</p>)}
  </>
);

interface HoverCardProps {
  id: string;
  target: HoverTarget;
  session: PreparedSession;
  timeZone: string;
}

const HoverCard = ({ id, target, session, timeZone }: HoverCardProps) => {
  const x = target.flip ? target.x - CARD_GAP : target.x + CARD_GAP;
  const y = target.flipY ? target.y - CARD_GAP : target.y + CARD_GAP + 2;
  const content =
    target.kind === 'band'
      ? bandHover(session, target.segmentIndex, timeZone)
      : session.inputs[target.inputIndex]
        ? inputHover(session, session.inputs[target.inputIndex], timeZone)
        : null;
  if (!content) return null;
  return (
    <Abs
      id={id}
      role="tooltip"
      vars={{ '--x': `${x}px`, '--y': `${y}px`, '--w': `${CARD_WIDTH}px` }}
      className={cn(
        'pointer-events-none absolute top-[var(--y)] left-[var(--x)] z-50 w-[var(--w)] rounded-[var(--r-md)] border border-border bg-surface px-3 py-2.5 text-[11.5px] shadow-[var(--sh-pop)]',
        target.flip && '-translate-x-full',
        target.flipY && '-translate-y-full',
      )}
    >
      {content.kind === 'band' ? <BandCard content={content} /> : <InputCard content={content} />}
    </Abs>
  );
};

export { CARD_GAP, CARD_HEIGHT, CARD_WIDTH, HoverCard };
export type { Placed };
