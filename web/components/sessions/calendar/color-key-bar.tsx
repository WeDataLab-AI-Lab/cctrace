'use client';

import { cn } from '@/lib/utils';
import type { CalendarColorKey } from '@/lib/session-calendar-types';
import { Abs } from './css-box';
import { COLOR_KEYS, colorKeyAvailable, type ColorAssignment } from './colors';
import { CALENDAR_TEXT } from './labels';

/**
 * 색 기준 선택(C4)과 범례(C5). 레퍼런스처럼 한 줄에 "색분류 [완료 여부|컴포넌트|화면|종별|프로필|...]"이고 그 아래 범례가
 * 점과 "이름 개수"로 이어진다. 범례는 그 주의 값을 모두, 개수 순으로 보이고 합이 그 주의 세션 수다. 범례 항목에 마우스를 올리거나
 * 포커스를 두면 그 값의 띠가 강조된다 — 값이 많아 색이 겹쳐도 가를 수 있다.
 */

interface ColorKeyBarProps {
  colorKey: CalendarColorKey;
  onColorKeyChange: (key: CalendarColorKey) => void;
  /** 세션 주인 범위인가. 아니면 "완료 여부"를 고를 수 없다(API가 남의 세션의 판정을 주지 않는다). */
  ownerView: boolean;
  assignment: ColorAssignment;
  /** 지금 강조 중인 범례 칸의 키. */
  highlightKey: string | null;
  onHighlight: (key: string | null) => void;
  className?: string;
}

const ColorKeyBar = ({ colorKey, onColorKeyChange, ownerView, assignment, highlightKey, onHighlight, className }: ColorKeyBarProps) => (
  <div className={cn('flex flex-col gap-1.5', className)}>
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
      <span className="text-[11px] text-ink-3">{CALENDAR_TEXT.colorBy}</span>
      <div role="group" aria-label={CALENDAR_TEXT.colorBy} className="flex flex-wrap gap-1">
        {COLOR_KEYS.map((k) => {
          const available = colorKeyAvailable(k.key, ownerView);
          const selected = assignment.colorKey === k.key;
          return (
            <button
              key={k.key}
              type="button"
              aria-pressed={selected}
              disabled={!available}
              title={available ? k.hint : '남의 세션의 완료 판정은 API가 돌려주지 않아 고를 수 없습니다.'}
              onClick={() => onColorKeyChange(k.key)}
              className={cn(
                'h-6 rounded border border-border px-2 text-[11px] whitespace-nowrap transition-colors focus-visible:-outline-offset-2!',
                selected ? 'bg-brand-soft font-semibold text-ink' : 'text-ink-2 hover:bg-surface-sunk',
                !available && 'cursor-not-allowed text-ink-4 hover:bg-surface',
              )}
            >
              {k.label}
            </button>
          );
        })}
      </div>
      {colorKey !== assignment.colorKey && (
        <span className="text-[11px] text-ink-3">고른 기준을 쓸 수 없어 프로젝트로 보입니다.</span>
      )}
    </div>
    <p className="text-[11px] text-ink-3">막대 색상만 바뀝니다. 표시되는 세션은 그대로입니다.</p>
    <p className="text-[11px] text-ink-2">{COLOR_KEYS.find(key => key.key === assignment.colorKey)?.hint}</p>
    <ul aria-label="범례" className="flex flex-wrap gap-x-3.5 gap-y-1">
      {assignment.legend.map((entry) => (
        <li key={entry.key}>
          <button
            type="button"
            aria-label={`${entry.label} ${entry.sessions}세션`}
            data-highlighted={highlightKey === entry.key ? '' : undefined}
            onPointerEnter={() => onHighlight(entry.key)}
            onPointerLeave={() => onHighlight(null)}
            onFocus={() => onHighlight(entry.key)}
            onBlur={() => onHighlight(null)}
            className={cn(
              'inline-flex items-center gap-1.5 rounded px-1 py-0.5 text-[11px] text-ink-2 hover:bg-surface-sunk',
              highlightKey === entry.key && 'bg-surface-sunk text-ink',
            )}
          >
            <Abs
              vars={{ '--c': entry.token }}
              aria-hidden="true"
              className={cn('size-2.5 shrink-0 rounded-[3px] bg-[var(--c)]', entry.none && 'border border-dashed border-ink-3 bg-transparent')}
            />
            <span className={cn(entry.none && 'text-ink-3')}>{entry.label}</span>
            <span className="tabular-nums text-ink-3">{entry.sessions.toLocaleString('en-US')}</span>
          </button>
        </li>
      ))}
    </ul>
  </div>
);

export { ColorKeyBar };
