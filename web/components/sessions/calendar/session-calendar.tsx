'use client';

import { useEffect, useEffectEvent, useLayoutEffect, useRef, useState } from 'react';
import { cn } from '@/lib/utils';
import { CALENDAR_MAX_DAYS, type CalendarColorKey, type SessionCalendar as SessionCalendarData, type SummaryGeneration } from '@/lib/session-calendar-types';
import { BlockBreakerOverlay, type GameStart } from './block-breaker-overlay';
import type { GameBrick } from './block-breaker';
import { CalendarGrid, AXIS_WIDTH, HEADER_HEIGHT } from './calendar-grid';
import { CalendarToolbar } from './calendar-toolbar';
import { ColorKeyBar } from './color-key-bar';
import { COLOR_KEYS, assignColors, colorKeyAvailable } from './colors';
import { weekColumns, weekWindow } from './day-grid';
import { CALENDAR_TEXT } from './labels';
import { layoutWeek } from './layout';
import { prepareSessions } from './prepare';
import { calendarTimeZone, dateKey, isValidTimeZone, DAY_MS, type LocalDate } from './time-zone';
import { shiftWeek, todayWeekStart, weekLabel } from './week';
import { DEFAULT_PX_PER_HOUR, canZoomIn, canZoomOut, clampZoom, wheelZoom, zoomIn, zoomOut } from './zoom';
import { initialScrollHour } from './working-hours';

/**
 * 주간 세션 캘린더(B1 이후의 왼쪽 칸). 제어형이고 아무것도 가져오지 않는다: 보이는 주, 배율, 색 기준, 선택은 부모가 갖고, 응답
 * (`SessionCalendar`)은 부모가 읽어 넘긴다. 시간대는 IANA 이름으로 받아 날짜 열을 그 시간대의 지역 자정으로 자른다.
 *
 * 한 화면에 위에서 아래로: 툴바(주 이동, 배율, 개요 생성 상태, 블록 깨기, 다시 읽기), 색 기준과 범례, 격자.
 */

interface SessionCalendarProps {
  /** IANA 시간대 이름. 날짜 열과 시각 눈금이 이 시간대를 따른다. */
  timeZone: string;
  /** 보이는 주의 월요일(그 시간대의 지역 날짜). */
  weekStart: LocalDate;
  /** 현재 시각(ms). 부모가 실제 시계를 갱신해 넘겨야 오늘 열과 현재 시각 선이 움직인다. 이 컴포넌트는 타이머를 소유하지 않는다. */
  nowMs: number;
  /** 이 주의 응답. 없으면 읽는 중이다. 목록 툴바의 필터는 부모가 이미 걸어서 넘긴다. */
  data: SessionCalendarData | undefined;
  onWeekChange: (next: LocalDate) => void;
  pxPerHour: number;
  onPxPerHourChange: (px: number) => void;
  colorKey: CalendarColorKey;
  onColorKeyChange: (key: CalendarColorKey) => void;
  selectedId: string | null;
  onSelect: (sessionId: string, turnKey?: string, anchorTs?: string) => void;
  onReload?: () => void;
  reloading?: boolean;
  summary?: SummaryGeneration;
  onRegenerateSummaries?: () => void;
  summaryRefreshing?: boolean;
  className?: string;
}

/** 게임 벽돌의 상한. 이보다 많으면 앞에서부터 쓴다. */
const MAX_BRICKS = 400;

/**
 * 격자에 보이는 띠를 벽돌로 바꾼다. 좌표는 날짜 머리 아래의 판(오버레이의 캔버스)에 맞춘다. 범위만 그리는 흐린 띠는 활동이 아니라
 * 범위라서 벽돌로 쓰지 않는다.
 */
const collectBricks = (wrapper: HTMLElement, scroller: HTMLElement): GameStart => {
  const wrap = wrapper.getBoundingClientRect();
  const view = scroller.getBoundingClientRect();
  const field = { left: wrap.left, top: wrap.top + HEADER_HEIGHT, width: wrap.width, height: wrap.height - HEADER_HEIGHT };
  const left = view.left + AXIS_WIDTH;
  const top = view.top + HEADER_HEIGHT;
  const bricks: GameBrick[] = [];
  scroller.querySelectorAll<HTMLElement>('[data-calendar-band]:not([data-faint])').forEach((el, i) => {
    if (bricks.length >= MAX_BRICKS) return;
    const r = el.getBoundingClientRect();
    const x0 = Math.max(r.left, left);
    const x1 = Math.min(r.right, view.right);
    const y0 = Math.max(r.top, top);
    const y1 = Math.min(r.bottom, view.bottom);
    if (x1 - x0 < 2 || y1 - y0 < 1) return;
    bricks.push({
      id: `${el.dataset.sessionId ?? 'band'}-${i}`,
      x: x0 - field.left,
      y: y0 - field.top,
      w: x1 - x0,
      h: y1 - y0,
      // 계산된 테두리 색: 토큰(`var(--cat-3)`)이 실제 색으로 풀려 있어 캔버스가 그대로 쓴다.
      token: getComputedStyle(el).borderLeftColor,
    });
  });
  return { width: field.width, height: field.height, bricks };
};

const SessionCalendar = ({
  timeZone,
  weekStart,
  nowMs,
  data,
  onWeekChange,
  pxPerHour,
  onPxPerHourChange,
  colorKey,
  onColorKeyChange,
  selectedId,
  onSelect,
  onReload,
  reloading,
  summary,
  onRegenerateSummaries,
  summaryRefreshing = false,
  className,
}: SessionCalendarProps) => {
  const scrollerRef = useRef<HTMLDivElement | null>(null);
  const wrapperRef = useRef<HTMLDivElement | null>(null);
  const anchorRef = useRef<{ hours: number; viewY: number } | null>(null);
  const [highlightKey, setHighlightKey] = useState<string | null>(null);
  const [game, setGame] = useState<GameStart | null>(null);
  const gameLauncherRef = useRef<HTMLElement | null>(null);
  const zoomRef = useRef(pxPerHour);

  const effectiveTimeZone = calendarTimeZone(timeZone);
  const invalidTimeZone = !isValidTimeZone(timeZone);
  const columns = weekColumns(effectiveTimeZone, weekStart, nowMs);
  const expected = weekWindow(effectiveTimeZone, weekStart);
  const responseFrom = typeof data?.from === 'string' ? Date.parse(data.from) : NaN;
  const responseTo = typeof data?.to === 'string' ? Date.parse(data.to) : NaN;
  const rangeInvalid = data !== undefined && (!Number.isFinite(responseFrom) || !Number.isFinite(responseTo) ||
    responseTo <= responseFrom || responseTo - responseFrom > CALENDAR_MAX_DAYS * DAY_MS ||
    responseFrom !== expected.from || responseTo !== expected.to);
  // 보이는 주보다 넓거나 좁은 응답도 범례·세션 개수를 왜곡하므로 다른 주의 자료처럼 억제한다.
  const visibleData = rangeInvalid ? undefined : data;
  const ownerView = visibleData?.owner_view === true;
  const sessions = Array.isArray(visibleData?.sessions) ? visibleData.sessions : [];
  const prepared = prepareSessions(sessions);
  const omittedSessions = sessions.length - prepared.length;
  // '판별 불가'와 상한, 응답 형식 오류는 서로 다른 원인이다. 하나의 세션이 여러 항목에 포함될 수 있다.
  const incomplete = prepared.filter((session) => session.skipped > 0 || session.inputIncomplete || (session.faint && (session.source.facts === 'ready' || session.source.facts === 'stale')));
  const incompleteReasons = [
    { label: '입력 시각 상한', count: prepared.filter((session) => session.source.inputs.truncated).length },
    { label: '입력 시각 미확인', count: prepared.filter((session) => session.source.inputs.state === 'undeterminable').length },
    { label: '활동 구간 미확인', count: prepared.filter((session) => session.faint && (session.source.facts === 'ready' || session.source.facts === 'stale')).length },
    { label: '응답 형식 확인', count: prepared.filter((session) => session.skipped > 0).length },
  ].filter((reason) => reason.count > 0);
  const sessionsById = new Map(prepared.map((s) => [s.id, s]));
  const effectiveKey = colorKeyAvailable(colorKey, ownerView) ? colorKey : 'project';
  const assignment = assignColors(prepared.map((session) => session.source), effectiveKey);
  const layout = layoutWeek({ timeZone: effectiveTimeZone, columns, sessions: prepared, pxPerHour, nowMs });

  /* ---------- 처음 그릴 때 일한 시간대로 스크롤 ---------- */
  // 주나 시간대가 바뀌거나 데이터가 처음 도착하면 그 주의 일한 시간대 시작으로 간다. 배율을 바꿀 때는 가지 않는다.
  const scrollKey = `${dateKey(weekStart)}|${effectiveTimeZone}|${visibleData ? 'ready' : 'loading'}`;
  const scrollToWorkingHours = useEffectEvent(() => {
    const el = scrollerRef.current;
    if (el) el.scrollTop = initialScrollHour(layout.working) * pxPerHour;
  });
  useLayoutEffect(() => {
    scrollToWorkingHours();
  }, [scrollKey]);

  /* ---------- 확대·축소: 보던 시각이 제자리에 남도록 ---------- */
  /** 배율을 바꾼다. 포인터(또는 보이는 영역의 가운데) 아래의 시각이 같은 자리에 남도록 스크롤을 맞춘다. */
  const requestZoom = (next: number, viewY?: number) => {
    const target = clampZoom(next);
    if (target === zoomRef.current) return;
    const el = scrollerRef.current;
    if (el) {
      const y = viewY ?? HEADER_HEIGHT + (el.clientHeight - HEADER_HEIGHT) / 2;
      anchorRef.current = { hours: (el.scrollTop + y - HEADER_HEIGHT) / pxPerHour, viewY: y };
    }
    zoomRef.current = target;
    onPxPerHourChange(target);
  };
  useLayoutEffect(() => {
    zoomRef.current = pxPerHour;
    const el = scrollerRef.current;
    const anchor = anchorRef.current;
    if (!el || !anchor) return;
    el.scrollTop = Math.max(0, anchor.hours * pxPerHour - (anchor.viewY - HEADER_HEIGHT));
    anchorRef.current = null;
  }, [pxPerHour]);

  // Ctrl/Cmd + 휠(C3). React의 onWheel은 수동(passive)이라 브라우저 확대를 막지 못해서 직접 붙인다.
  const handleWheel = useEffectEvent((event: WheelEvent) => {
    if (!(event.ctrlKey || event.metaKey)) return;
    event.preventDefault();
    const el = scrollerRef.current;
    if (!el) return;
    requestZoom(wheelZoom(zoomRef.current, event.deltaY), event.clientY - el.getBoundingClientRect().top);
  });
  useEffect(() => {
    const el = scrollerRef.current;
    if (!el) return;
    const listener = (event: WheelEvent) => handleWheel(event);
    el.addEventListener('wheel', listener, { passive: false });
    return () => el.removeEventListener('wheel', listener);
  }, []);

  const openGame = (launcher: HTMLButtonElement) => {
    const wrapper = wrapperRef.current;
    const scroller = scrollerRef.current;
    if (!wrapper || !scroller) return;
    gameLauncherRef.current = launcher;
    setGame(collectBricks(wrapper, scroller));
  };
  const closeGame = () => setGame(null);
  useLayoutEffect(() => {
    if (game !== null) return;
    const launcher = gameLauncherRef.current;
    gameLauncherRef.current = null;
    if (launcher?.isConnected) launcher.focus();
  }, [game]);

  const empty = visibleData !== undefined && Array.isArray(visibleData.sessions) && sessions.length === 0;
  return (
    <div className={cn('flex min-h-0 flex-1 flex-col bg-surface', className)} data-calendar="">
      <div inert={game !== null} className="shrink-0 space-y-2 border-b border-border-subtle px-3 py-2.5">
        <CalendarToolbar
          weekLabel={weekLabel(columns)}
          sessionCount={prepared.length}
          isCurrentWeek={columns.some((c) => c.isToday)}
          onToday={() => onWeekChange(todayWeekStart(effectiveTimeZone, nowMs))}
          onPrev={() => onWeekChange(shiftWeek(weekStart, -1))}
          onNext={() => onWeekChange(shiftWeek(weekStart, 1))}
          canZoomIn={canZoomIn(pxPerHour)}
          canZoomOut={canZoomOut(pxPerHour)}
          isDefaultZoom={Math.abs(pxPerHour - DEFAULT_PX_PER_HOUR) < 1e-6}
          onZoomIn={() => requestZoom(zoomIn(pxPerHour))}
          onZoomOut={() => requestZoom(zoomOut(pxPerHour))}
          onZoomReset={() => requestZoom(DEFAULT_PX_PER_HOUR)}
          summary={summary}
          onRegenerateSummaries={onRegenerateSummaries}
          summaryRefreshing={summaryRefreshing}
          onOpenGame={openGame}
          gameDisabled={!visibleData || prepared.length === 0}
          onReload={onReload}
          reloading={reloading}
        />
        {invalidTimeZone && <p role="status" className="text-[11px] text-warning-strong">시간대 이름을 확인할 수 없어 UTC로 표시합니다.</p>}
        {rangeInvalid && <p role="status" className="text-[11px] text-warning-strong">응답 구간이 이 주와 다르거나 14일 상한을 넘었습니다. 다른 주의 자료를 표시하지 않습니다. 다시 읽어 주세요.</p>}
        {visibleData && !Array.isArray(visibleData.sessions) && <p role="status" className="text-[11px] text-warning-strong">세션 목록을 읽을 수 없어 자료를 표시하지 않습니다.</p>}
        {omittedSessions > 0 && <p role="status" className="text-[11px] text-warning-strong">읽을 수 없는 세션 {omittedSessions}건을 표시하지 못했습니다.</p>}
        <details className="group text-[11px] text-ink-3">
          <summary className="cursor-pointer rounded py-0.5 hover:text-ink">
            <span className="font-medium">색상 기준·범례</span>
            <span className="ml-2">{COLOR_KEYS.find(key => key.key === effectiveKey)?.label}</span>
            {incomplete.length > 0 && <span className="ml-3 text-warning-strong">자료 확인 필요 {incomplete.length}개</span>}
          </summary>
          <div className="max-h-48 space-y-2 overflow-y-auto pt-2">
            {incomplete.length > 0 && (
              <p role="status" className="text-[11px] text-warning-strong">
                확인이 필요한 세션 {incomplete.length}개: {incompleteReasons.map((reason) => `${reason.label} ${reason.count}개`).join(' · ')}.
                한 세션에 여러 사유가 있을 수 있습니다. 띠를 선택하거나 포커스하면 해당 세션의 내용을 확인할 수 있습니다.
              </p>
            )}
            <ColorKeyBar
              colorKey={colorKey}
              onColorKeyChange={onColorKeyChange}
              ownerView={ownerView}
              assignment={assignment}
              highlightKey={highlightKey}
              onHighlight={setHighlightKey}
            />
            <p className="text-[11px] text-ink-3">좁은 캘린더에서는 격자 안을 가로로 스크롤하여 다른 요일을 볼 수 있습니다.</p>
          </div>
        </details>
      </div>

      <div ref={wrapperRef} className="relative flex min-h-0 flex-1 flex-col">
        <CalendarGrid
          layout={layout}
          sessionsById={sessionsById}
          assignment={assignment}
          highlightKey={highlightKey}
          selectedId={selectedId}
          onSelect={onSelect}
          timeZone={effectiveTimeZone}
          pxPerHour={pxPerHour}
          scrollerRef={scrollerRef}
          inert={game !== null}
        />
        {(visibleData === undefined || !Array.isArray(visibleData.sessions) || empty) && (
          <div className="pointer-events-none absolute inset-x-0 top-24 z-40 flex justify-center px-4" role="status">
            <div className="rounded-[var(--r-md)] border border-border bg-surface px-4 py-3 text-center shadow-[var(--sh-sm)]">
              <p className="text-[12.5px] font-medium text-ink-2">{rangeInvalid ? '응답 구간이 현재 주와 맞지 않습니다' : visibleData === undefined ? CALENDAR_TEXT.loading : !Array.isArray(visibleData.sessions) ? '세션 목록을 읽을 수 없습니다' : CALENDAR_TEXT.emptyWeek}</p>
              {empty && <p className="mt-0.5 text-[11.5px] text-ink-3">{CALENDAR_TEXT.emptyWeekHint}</p>}
            </div>
          </div>
        )}
        {game && <BlockBreakerOverlay start={game} onClose={closeGame} />}
      </div>
    </div>
  );
};

export { SessionCalendar, collectBricks };
export type { SessionCalendarProps };
