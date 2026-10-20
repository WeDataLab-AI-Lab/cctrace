'use client';

import { ChevronLeft, ChevronRight, Gamepad2, Loader2, Minus, Plus, RefreshCw } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { SummaryGeneration } from '@/lib/session-calendar-types';
import { CALENDAR_TEXT, summaryWrittenText } from './labels';

/**
 * 캘린더 툴바(C1~C3, C18, C19). 레퍼런스처럼 한 줄에:
 *   [이번 주] [◀] [▶] 날짜 범위 · N 세션  [−] [+] [표준]  ......  개요 생성 상태 · 블록 깨기 · [다시 읽기]
 * 모두 제어형이다 — 주, 배율, 요약 상태는 부모가 갖고 이 컴포넌트는 알리기만 한다.
 */

const BUTTON =
  'inline-flex h-7 shrink-0 items-center justify-center gap-1 rounded border border-border bg-surface px-2 text-[11.5px] font-medium text-ink transition-colors hover:bg-surface-sunk disabled:cursor-not-allowed disabled:text-ink-4 disabled:hover:bg-surface';
const ICON_BUTTON = 'w-7 px-0';

interface CalendarToolbarProps {
  /** "2026년 10/5 – 10/11" */
  weekLabel: string;
  sessionCount: number;
  /** 지금 보는 주가 오늘이 든 주인가. */
  isCurrentWeek: boolean;
  onToday: () => void;
  onPrev: () => void;
  onNext: () => void;
  canZoomIn: boolean;
  canZoomOut: boolean;
  /** 표준 배율이면 "표준"을 누를 수 없다. */
  isDefaultZoom: boolean;
  onZoomIn: () => void;
  onZoomOut: () => void;
  onZoomReset: () => void;
  /** 요약 생성 상태(C18). 없으면 그리지 않는다. */
  summary?: SummaryGeneration;
  onRegenerateSummaries?: () => void;
  summaryRefreshing?: boolean;
  /** 블록 깨기(C19). 없으면 단추를 그리지 않는다. */
  onOpenGame?: (launcher: HTMLButtonElement) => void;
  gameDisabled?: boolean;
  /** 다시 읽기(C3). */
  onReload?: () => void;
  reloading?: boolean;
  className?: string;
}

const BLOCKED_LABEL: Record<string, string> = {
  consent_required: '동의 필요', disabled: '관리자 설정 꺼짐', private_scope: '비공개 범위', redacted: '비식별화',
  no_runtime: '런타임 없음', runtime_unsupported: '지원하지 않는 런타임',
};

const summaryDetails = (summary: SummaryGeneration): string[] => {
  const details: string[] = [];
  if (typeof summary.stale === 'number' && summary.stale > 0) details.push(`미생성·갱신 대기 ${summary.stale}건`);
  if (typeof summary.writing === 'number' && summary.writing > 0) details.push(`작성 중 ${summary.writing}건`);
  for (const [reason, count] of Object.entries(summary.blocked ?? {})) {
    if (count > 0) details.push(`${BLOCKED_LABEL[reason] ?? reason} ${count}건`);
  }
  if (summary.waiting_budget) details.push('일일 토큰 한도 대기');
  return details;
};

/** 개요 생성 상태: API가 준 쓰기 수, 차단 사유, 한도 대기를 함께 표시한다. */
const SummaryState = ({ summary, onRegenerate, refreshing }: { summary: SummaryGeneration; onRegenerate?: () => void; refreshing: boolean }) => {
  const details = summaryDetails(summary);
  const written = summary.state !== 'writing' && !refreshing && typeof summary.written === 'number' ? summaryWrittenText(summary.written) : '';
  const handleRegenerate = () => onRegenerate?.();
  return (
  <div className="flex shrink-0 flex-wrap items-center gap-2" data-testid="summary-state">
    {/* 상태 변화를 보조 기술에 알린다. */}
    <span role="status" aria-live="polite" className="text-[11px] whitespace-nowrap text-ink-3">
      {[written, ...details].filter(Boolean).join(' · ')}
    </span>
    {summary.state === 'writing' || refreshing ? (
      <button type="button" disabled className={BUTTON} aria-busy="true">
        <Loader2 size={12} strokeWidth={2} className="animate-spin motion-reduce:animate-none" aria-hidden="true" />
        {refreshing && summary.state !== 'writing' ? '새로 만들기 요청 중' : CALENDAR_TEXT.summaryWriting}
      </button>
    ) : summary.state === 'fresh' ? (
      <button type="button" disabled className={BUTTON} title="세션 기록의 개요가 모두 최신이라 다시 만들 것이 없습니다.">
        {CALENDAR_TEXT.summaryFresh}
      </button>
    ) : (
      <button
        type="button"
        onClick={handleRegenerate}
        disabled={!onRegenerate || refreshing}
        className={BUTTON}
        title={details.length > 0 ? `${details.join(' · ')}. 표시한 날짜 범위를 다시 만듭니다.` : '개요를 아직 쓰지 못한 세션이 있습니다. 눌러 다시 만듭니다.'}
      >
        {CALENDAR_TEXT.summaryStale}
      </button>
    )}
  </div>
  );
};

const CalendarToolbar = ({
  weekLabel,
  sessionCount,
  isCurrentWeek,
  onToday,
  onPrev,
  onNext,
  canZoomIn,
  canZoomOut,
  isDefaultZoom,
  onZoomIn,
  onZoomOut,
  onZoomReset,
  summary,
  onRegenerateSummaries,
  summaryRefreshing = false,
  onOpenGame,
  gameDisabled = false,
  onReload,
  reloading = false,
  className,
}: CalendarToolbarProps) => (
  <div role="toolbar" aria-label="캘린더 도구" className={cn('flex flex-wrap items-center gap-x-2 gap-y-1.5', className)}>
    <button type="button" onClick={onToday} className={BUTTON} aria-current={isCurrentWeek ? 'date' : undefined}>
      {CALENDAR_TEXT.today}
    </button>
    <button type="button" onClick={onPrev} className={cn(BUTTON, ICON_BUTTON)} aria-label={CALENDAR_TEXT.prev} title={CALENDAR_TEXT.prev}>
      <ChevronLeft size={14} strokeWidth={2} aria-hidden="true" />
    </button>
    <button type="button" onClick={onNext} className={cn(BUTTON, ICON_BUTTON)} aria-label={CALENDAR_TEXT.next} title={CALENDAR_TEXT.next}>
      <ChevronRight size={14} strokeWidth={2} aria-hidden="true" />
    </button>
    <h3 className="text-[13px] font-semibold whitespace-nowrap text-ink">{weekLabel}</h3>
    <span className="text-[11px] whitespace-nowrap text-ink-3 tabular-nums" data-testid="week-session-count">
      {sessionCount.toLocaleString('en-US')} {CALENDAR_TEXT.sessions}
    </span>

    <div role="group" aria-label="확대·축소" className="ml-1 flex items-center gap-1">
      <button
        type="button"
        onClick={onZoomOut}
        disabled={!canZoomOut}
        className={cn(BUTTON, ICON_BUTTON)}
        aria-label={CALENDAR_TEXT.zoomOut}
        title="축소 (Ctrl/Cmd + 휠)"
      >
        <Minus size={13} strokeWidth={2} aria-hidden="true" />
      </button>
      <button
        type="button"
        onClick={onZoomIn}
        disabled={!canZoomIn}
        className={cn(BUTTON, ICON_BUTTON)}
        aria-label={CALENDAR_TEXT.zoomIn}
        title="확대 (Ctrl/Cmd + 휠)"
      >
        <Plus size={13} strokeWidth={2} aria-hidden="true" />
      </button>
      <button type="button" onClick={onZoomReset} disabled={isDefaultZoom} className={BUTTON} title="기본 배율로 돌아갑니다">
        {CALENDAR_TEXT.zoomReset}
      </button>
    </div>

    <div className="ml-auto flex flex-wrap items-center gap-x-2 gap-y-1.5">
      {summary && <SummaryState summary={summary} onRegenerate={onRegenerateSummaries} refreshing={summaryRefreshing} />}
      {onOpenGame && (
        <button
          type="button"
          onClick={(event) => onOpenGame(event.currentTarget)}
          disabled={gameDisabled}
          className={cn(BUTTON, ICON_BUTTON)}
          aria-label={CALENDAR_TEXT.game}
          title="블록 깨기: 지금 보이는 띠를 벽돌로 쓴다"
        >
          <Gamepad2 size={14} strokeWidth={1.75} aria-hidden="true" />
        </button>
      )}
      {onReload && (
        <button type="button" onClick={onReload} disabled={reloading} className={BUTTON} aria-busy={reloading || undefined}>
          <RefreshCw size={12} strokeWidth={2} className={cn(reloading && 'animate-spin motion-reduce:animate-none')} aria-hidden="true" />
          {CALENDAR_TEXT.reload}
        </button>
      )}
    </div>
  </div>
);

export { CalendarToolbar };
