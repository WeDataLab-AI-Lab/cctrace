'use client';

import { ArrowLeft, ArrowRight } from 'lucide-react';
import { useEffect, useRef, useState, type ReactNode, type UIEvent } from 'react';
import { cn } from '@/lib/utils';
import type { TimelineMessage } from '@/lib/session-analysis-types';
import type { TimelineFreshness } from './record-adapter';
import { MessageView } from './message-view';
import {
  DEFAULT_TIMELINE_FILTERS,
  filterMessages,
  firstMessageOfTurn,
  inputsOf,
  inputTotal,
  isInput,
  stepInput,
  type TimelineFilters,
} from './model';

/**
 * 타임라인 칸(E1~E7): 세션 상세 오른쪽의 세 번째 칸.
 *   머리      이전·다음 입력 버튼 · "타임라인" · 닫기
 *   대상      짧은 ID 칩과 제목
 *   띠        "입력 N건" · 두 토글(사용자 입력만 보기, 중간 출력 숨기기)
 *   메시지    사용자 말풍선(오른쪽), Claude 출력(왼쪽), 질문 카드, 플랜 카드
 *
 * 이동은 입력 번호가 기준이다. 현재 입력은 이동 버튼과 사용자의 스크롤을 따라 바뀐다. 상세에서 턴이나 작업 항목을 누르면
 * 호출하는 쪽이 `focus`를 새로 주고, 이 칸이 그 턴의 첫 메시지로 스크롤해 잠깐 배경을 바꾼다.
 * 스크롤은 `scrollTop`을 직접 쓴다(부드럽게 가는 것은 컨테이너의 CSS가 한다).
 */

/** 스크롤한 곳이 컨테이너 위에서 이만큼 아래에 오게 한다(메시지 머리 줄이 잘리지 않게). */
const SCROLL_PAD = 8;
/** 스크롤 중 "현재 입력"을 정하는 기준선: 컨테이너 맨 위에서 이만큼 아래. */
const TRACK_OFFSET = 24;
/** 프로그램이 스크롤을 시작한 뒤 이 시간 동안은 스크롤 이벤트로 현재 입력을 바꾸지 않는다(부드러운 이동 중에 깜빡이지 않게). */
const SETTLE_MS = 450;
/** 상세에서 누른 턴의 배경이 바뀌어 있는 시간. */
const FOCUS_FLASH_MS = 1600;

/**
 * 스크롤 잠금의 시계. 효과와 이벤트 처리기에서만 부르는데, 린트가 효과 안의 `Date.now()` 직접 호출을 렌더 중 호출로
 * 오인하므로 시계 읽기를 이 두 함수에 모은다.
 */
const lockEnd = (): number => Date.now() + SETTLE_MS;
const isLocked = (until: number): boolean => Date.now() < until;

/**
 * 컨테이너 안에서 요소 위쪽의 위치. `offsetParent`를 거슬러 올라가며 더하므로, 사이에 위치 지정 조상이 끼어도 맞다.
 * 컨테이너가 `relative`라 사슬은 늘 컨테이너에서 끝난다.
 */
const topWithin = (container: HTMLElement, el: HTMLElement): number => {
  let top = 0;
  let node: HTMLElement | null = el;
  while (node && node !== container) {
    top += node.offsetTop;
    node = node.offsetParent instanceof HTMLElement ? node.offsetParent : null;
  }
  return top;
};

const findMessage = (container: HTMLElement, id: string): HTMLElement | null => {
  for (const node of container.querySelectorAll<HTMLElement>('[data-message-id]')) {
    if (node.dataset.messageId === id) return node;
  }
  return null;
};

interface NavButtonProps {
  label: string;
  disabled: boolean;
  onClick: () => void;
  children: ReactNode;
}

const NavButton = ({ label, disabled, onClick, children }: NavButtonProps) => (
  <button
    type="button"
    aria-label={label}
    title={label}
    disabled={disabled}
    onClick={onClick}
    className="grid size-7 shrink-0 place-items-center rounded-md border border-border-strong bg-surface text-ink-2 transition-colors hover:bg-surface-sunk disabled:cursor-not-allowed disabled:border-border-subtle disabled:bg-surface disabled:text-ink-4"
  >
    {children}
  </button>
);

interface ToggleProps {
  label: string;
  checked: boolean;
  disabled?: boolean;
  title?: string;
  onChange: (checked: boolean) => void;
}

const Toggle = ({ label, checked, disabled = false, title, onChange }: ToggleProps) => (
  <label
    title={title}
    className={cn('inline-flex items-center gap-1.5 whitespace-nowrap', disabled ? 'cursor-not-allowed text-ink-4' : 'cursor-pointer text-ink-2')}
  >
    <input
      type="checkbox"
      checked={checked}
      disabled={disabled}
      onChange={(e) => onChange(e.target.checked)}
      className="size-3.5 accent-brand disabled:cursor-not-allowed"
    />
    {label}
  </label>
);

interface TimelineFocus {
  turnKey: string;
  /** 같은 턴을 다시 눌러도 다시 옮기도록 호출하는 쪽이 올리는 값. */
  nonce: number;
}

interface TimelinePaneProps {
  shortId: string;
  title: string | null;
  messages: readonly TimelineMessage[];
  /** 상대 시각("5시간 전")의 기준. 렌더 중에 시계를 읽지 않으려 호출하는 쪽이 정한다. */
  now: number;
  timeZone?: string;
  focus?: TimelineFocus | null;
  /** Exact total from the session analysis API; null means the count is unavailable. */
  inputCount?: number | null;
  freshness?: TimelineFreshness;
  loading?: boolean;
  loadError?: boolean;
  onRetry?: () => void;
  onClose: () => void;
  className?: string;
}

const TimelinePane = ({ shortId, title, messages, now, timeZone, focus = null, inputCount, freshness, loading = false,
  loadError = false, onRetry, onClose, className }: TimelinePaneProps) => {
  const [filters, setFilters] = useState<TimelineFilters>(DEFAULT_TIMELINE_FILTERS);
  const [currentN, setCurrentN] = useState<number | null>(null);
  const [seenFocus, setSeenFocus] = useState<string | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const lockUntil = useRef(0);

  const filteredMessages = filterMessages(messages, filters);
  const focusKey = focus ? `${focus.turnKey}#${focus.nonce}` : null;
  const focusTarget = focus ? firstMessageOfTurn(messages, focus.turnKey) : null;
  const revealFocusTarget = focusTarget && !filteredMessages.some((message) => message.id === focusTarget.id) ? focusTarget : null;
  const visible = revealFocusTarget
    ? [...filteredMessages, revealFocusTarget].sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts) || a.id.localeCompare(b.id))
    : filteredMessages;
  const total = inputCount === undefined ? inputTotal(messages) : inputCount;
  const visibleInputs = inputsOf(visible);
  // 위치를 아직 모르면 현재 거름에서 첫 입력이다. 전체 개수는 거름과 별도로 유지한다.
  const current = currentN !== null && visibleInputs.some((m) => m.n === currentN) ? currentN : (visibleInputs[0]?.n ?? null);
  const prevN = stepInput(visible, current, -1);
  const nextN = stepInput(visible, current, 1);

  // 상세에서 새로 누른 턴: 현재 입력을 그 턴의 입력으로 옮긴다(렌더 중 상태 조정). 실제 스크롤은 아래 효과가 한다.
  if (focusKey !== seenFocus) {
    setSeenFocus(focusKey);
    const target = focus ? firstMessageOfTurn(messages, focus.turnKey) : null;
    if (target && isInput(target)) setCurrentN(target.n);
  }
  // 거름 때문에 첫 메시지가 숨었으면 보이는 것 중 그 턴의 첫 메시지로 간다.
  const focusMessageId = focus ? (firstMessageOfTurn(visible, focus.turnKey)?.id ?? null) : null;

  const goToInput = (n: number | null) => {
    const container = scrollRef.current;
    if (n === null || !container) return;
    const el = container.querySelector<HTMLElement>(`[data-input-n="${n}"]`);
    if (!el) return;
    lockUntil.current = lockEnd();
    container.scrollTop = Math.max(0, topWithin(container, el) - SCROLL_PAD);
    setCurrentN(n);
  };

  const handleScroll = (e: UIEvent<HTMLDivElement>) => {
    if (isLocked(lockUntil.current)) return;
    const container = e.currentTarget;
    const edge = container.scrollTop + TRACK_OFFSET;
    let found: number | null = null;
    for (const node of container.querySelectorAll<HTMLElement>('[data-input-n]')) {
      const n = Number(node.dataset.inputN);
      if (Number.isFinite(n) && topWithin(container, node) <= edge) found = n;
    }
    if (found !== null && found !== current) setCurrentN(found);
  };

  const handleRetry = () => onRetry?.();

  /** 상세의 턴 선택에 맞춰 같은 대화 원본을 보여 주고 잠깐 강조한다. */
  useEffect(() => {
    const container = scrollRef.current;
    if (!container || focusKey === null || focusMessageId === null) return;
    const el = findMessage(container, focusMessageId);
    if (!el) return;
    lockUntil.current = lockEnd();
    container.scrollTop = Math.max(0, topWithin(container, el) - SCROLL_PAD);
    // React 상태가 아니라 DOM에 직접 단다: 잠깐 보이는 표시라 다시 그릴 이유가 없다.
    el.dataset.focused = 'true';
    const timer = window.setTimeout(() => {
      delete el.dataset.focused;
    }, FOCUS_FLASH_MS);
    return () => {
      window.clearTimeout(timer);
      delete el.dataset.focused;
    };
  }, [focusKey, focusMessageId]);

  return (
    <aside aria-label="타임라인" className={cn('flex h-full min-h-0 flex-col bg-surface', className)}>
      <div className="flex shrink-0 items-center gap-1.5 border-b border-border-subtle px-3 py-2">
        <NavButton label="이전 입력" disabled={prevN === null} onClick={() => goToInput(prevN)}>
          <ArrowLeft size={14} strokeWidth={2} aria-hidden="true" />
        </NavButton>
        <NavButton label="다음 입력" disabled={nextN === null} onClick={() => goToInput(nextN)}>
          <ArrowRight size={14} strokeWidth={2} aria-hidden="true" />
        </NavButton>
        <h2 className="ml-1 text-[13px] font-semibold text-ink">타임라인</h2>
        <button
          type="button"
          onClick={onClose}
          className="ml-auto rounded px-1 py-0.5 text-[12px] font-medium text-brand hover:underline"
        >
          닫기
        </button>
      </div>

      <div className="flex min-w-0 shrink-0 items-center gap-2 border-b border-border-subtle px-3 py-1.5">
        <span
          title="세션 ID 앞 8자"
          className="shrink-0 rounded-full border border-border-strong bg-surface px-2 py-px font-mono text-[11px] leading-4 text-ink-2"
        >
          {shortId}
        </span>
        <span className={cn('min-w-0 truncate text-[12.5px]', title ? 'font-semibold text-ink' : 'text-ink-3 italic')} title={title ?? undefined}>
          {title ?? '제목 없음'}
        </span>
      </div>

      <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-b border-border-subtle bg-surface-sunk px-3 py-1.5 text-[11px] text-ink-2">
        <span data-input-total={total ?? 'unknown'} className="font-medium">
          입력 <b className="font-semibold tabular-nums text-ink">{total === null ? '판단 불가' : total}</b>{total === null ? '' : '건'}
        </span>
        <div className="ml-auto flex flex-wrap items-center gap-x-3 gap-y-1">
          <Toggle
            label="사용자 입력만 보기"
            checked={filters.userOnly}
            onChange={(userOnly) => setFilters((f) => ({ ...f, userOnly }))}
          />
          <Toggle
            label="중간 출력 숨기기"
            checked={filters.hideIntermediate}
            // 사용자 입력만 보기가 켜져 있으면 Claude의 출력이 모두 숨어 있어 이 토글은 할 일이 없다.
            disabled={filters.userOnly}
            title={filters.userOnly ? '"사용자 입력만 보기"를 끄면 쓸 수 있습니다.' : undefined}
            onChange={(hideIntermediate) => setFilters((f) => ({ ...f, hideIntermediate }))}
          />
        </div>
      </div>

      {freshness && (
        <p
          role="status"
          data-freshness={freshness.state}
          title={`분석 계산 시각: ${freshness.computedAt ?? '알 수 없음'} · 규칙 판: ${freshness.rulesVersion ?? '알 수 없음'}`}
          className="shrink-0 border-b border-border-subtle px-3 py-1 text-[10.5px] text-ink-3"
        >
          {freshness.detail}
          {freshness.expectedInputs !== null && ` · 원문 대조 ${freshness.matchedInputs}/${freshness.expectedInputs}건`}
          {loading && ' · 원문 기록을 읽는 중입니다'}
        </p>
      )}
      {loadError && (
        <p role="alert" className="flex shrink-0 items-center gap-2 border-b border-border-subtle px-3 py-1 text-[10.5px] text-warning-strong">
          원문 페이지를 읽지 못해 입력 출처 대조가 불완전합니다.
          {onRetry && <button type="button" onClick={handleRetry} className="underline">다시 시도</button>}
        </p>
      )}
      {revealFocusTarget && (
        <p role="status" className="shrink-0 border-b border-border-subtle px-3 py-1 text-[10.5px] text-ink-3">
          선택한 턴의 원본은 현재 필터에서 숨겨져 있어 출처를 유지한 채 이 항목만 표시합니다.
        </p>
      )}

      <div
        ref={scrollRef}
        role="region"
        aria-label="타임라인 메시지"
        tabIndex={0}
        onScroll={handleScroll}
        className="relative min-h-0 flex-1 scroll-smooth overflow-y-auto px-3 py-2 motion-reduce:scroll-auto focus-visible:-outline-offset-2!"
      >
        {messages.length === 0 && loading ? (
          <p className="py-8 text-center text-[12px] text-ink-3">원문 기록을 읽는 중입니다.</p>
        ) : messages.length === 0 ? (
          <p className="py-8 text-center text-[12px] text-ink-3">표시할 메시지가 없습니다.</p>
        ) : visible.length === 0 ? (
          <p className="px-2 py-8 text-center text-[12px] leading-relaxed text-ink-3">
            직접 입력으로 확인된 메시지가 없습니다. &quot;사용자 입력만 보기&quot;를 끄면 다른 출처와 Claude 출력을 볼 수 있습니다.
          </p>
        ) : (
          visible.map((m) => <MessageView key={m.id} message={m} now={now} timeZone={timeZone} current={isInput(m) && m.n === current} />)
        )}
      </div>
    </aside>
  );
};

export { TimelinePane, SCROLL_PAD, TRACK_OFFSET, SETTLE_MS, FOCUS_FLASH_MS };
export type { TimelineFocus, TimelinePaneProps };
