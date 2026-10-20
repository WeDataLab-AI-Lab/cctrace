'use client';

import { Check, ChevronRight, Loader2, Lock, PencilLine } from 'lucide-react';
import { useId, type ReactNode } from 'react';
import { cn } from '@/lib/utils';
import { SegmentedTabs, type TabItem } from './segmented-tabs';

/**
 * 세션 메모 편집기(F6): 탭 "이 세션" · "전체" · "목록"과 저장 상태("저장됨").
 *   이 세션  지금 고른 세션의 메모
 *   전체     세션과 무관한 하나의 메모
 *   목록     메모가 있는 세션의 첫 줄 모음. 줄을 누르면 그 세션으로 간다
 * 제어형이다. 글, 탭, 펼침, 저장 상태, 목록을 모두 부모가 갖고 이 컴포넌트는 알리기만 한다 — 저장 시점(디바운스, 서버 호출)도 부모의 몫이다.
 * 접어 두면 머리 한 줄(제목, 끼워 넣은 것, 저장 상태)만 차지한다. 목록 열은 세로가 모자라서다.
 */

type NoteTab = 'session' | 'global' | 'list';
type NoteSaveState = 'saved' | 'dirty' | 'saving';

interface NoteListEntry {
  sessionId: string;
  shortId: string;
  title: string;
  firstLine: string;
}

interface SessionNoteEditorProps {
  /** 탭과 글이 보이게 펼쳤는가. */
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 머리 줄에 끼워 넣는 것(예: 분류 선택 상자). */
  headerSlot?: ReactNode;
  tab: NoteTab;
  onTabChange: (tab: NoteTab) => void;
  /** "이 세션" 탭이 보이는 세션. 고른 세션이 없으면 null이다. */
  session: { shortId: string; title: string } | null;
  sessionNote: string;
  onSessionNoteChange: (text: string) => void;
  globalNote: string;
  onGlobalNoteChange: (text: string) => void;
  saveState: NoteSaveState;
  notes: readonly NoteListEntry[];
  onPickSession: (sessionId: string) => void;
  /** 비소유 관리자: 메모는 주인의 개인 값이라 읽지도 쓰지도 못한다. */
  unavailable?: boolean;
  className?: string;
}

const TABS: readonly TabItem<NoteTab>[] = [
  { value: 'session', label: '이 세션', hint: '고른 세션의 메모' },
  { value: 'global', label: '전체', hint: '세션과 무관한 메모' },
  { value: 'list', label: '목록', hint: '메모가 있는 세션' },
];

const SAVE_TEXT: Record<NoteSaveState, string> = { saved: '저장됨', dirty: '저장 안 됨', saving: '저장 중' };

const SaveBadge = ({ state }: { state: NoteSaveState }) => (
  <span role="status" data-save-state={state} className={cn('inline-flex items-center gap-1 text-[10.5px]', state === 'saved' ? 'text-ink-3' : 'text-warning-strong')}>
    {state === 'saved' ? (
      <Check size={11} strokeWidth={2.25} aria-hidden="true" />
    ) : state === 'saving' ? (
      <Loader2 size={11} strokeWidth={2} aria-hidden="true" className="motion-safe:animate-spin" />
    ) : (
      <PencilLine size={11} strokeWidth={2} aria-hidden="true" />
    )}
    {SAVE_TEXT[state]}
  </span>
);

const SessionNoteEditor = ({
  open,
  onOpenChange,
  headerSlot,
  tab,
  onTabChange,
  session,
  sessionNote,
  onSessionNoteChange,
  globalNote,
  onGlobalNoteChange,
  saveState,
  notes,
  onPickSession,
  unavailable = false,
  className,
}: SessionNoteEditorProps) => {
  const uid = useId();
  const panelId = `${uid}-panel`;

  if (unavailable) {
    return (
      <section aria-label="세션 메모" className={cn('border-t border-border bg-surface px-3 py-2', className)}>
        <p className="flex items-center gap-1.5 text-[11px] text-ink-3">
          <Lock size={12} strokeWidth={1.75} aria-hidden="true" />
          메모는 세션 주인에게만 제공됩니다
        </p>
      </section>
    );
  }

  return (
    <section aria-label="세션 메모" className={cn('flex flex-col border-t border-border bg-surface', className)}>
      <div className="flex items-center gap-2 px-2 py-1">
        <button
          type="button"
          aria-expanded={open}
          aria-controls={panelId}
          onClick={() => onOpenChange(!open)}
          className="inline-flex items-center gap-0.5 rounded px-1 py-0.5 text-[11px] font-medium text-ink-2 hover:bg-surface-sunk"
        >
          <ChevronRight size={12} strokeWidth={2} aria-hidden="true" className={cn('transition-transform motion-reduce:transition-none', open && 'rotate-90')} />
          메모
        </button>
        {headerSlot}
        <span className="ml-auto">
          <SaveBadge state={saveState} />
        </span>
      </div>
      {open && (
        <>
          <div className="border-t border-border-subtle px-2">
            <SegmentedTabs items={TABS} value={tab} onChange={onTabChange} ariaLabel="메모 범위" idPrefix={`${uid}-note`} panelId={panelId} />
          </div>
          <div id={panelId} role="tabpanel" aria-labelledby={`${uid}-note-tab-${tab}`} className="px-3 py-2">
            {tab === 'session' &&
              (session ? (
                <>
                  <p className="mb-1 truncate text-[10.5px] text-ink-3" title={session.title}>
                    <span className="font-mono text-ink-2">{session.shortId}</span> · {session.title}
                  </p>
                  <textarea
                    aria-label="이 세션의 메모"
                    value={sessionNote}
                    onChange={(e) => onSessionNoteChange(e.target.value)}
                    rows={3}
                    placeholder="이 세션에 남길 메모. 첫 줄이 목록 카드에 보입니다."
                    className="block w-full resize-none rounded border border-border bg-surface px-2 py-1.5 text-[12px] leading-[1.5] text-ink placeholder:text-ink-4"
                  />
                </>
              ) : (
                <p className="text-[11.5px] text-ink-3">목록에서 세션을 고르면 그 세션의 메모를 쓸 수 있습니다.</p>
              ))}
            {tab === 'global' && (
              <textarea
                aria-label="전체 메모"
                value={globalNote}
                onChange={(e) => onGlobalNoteChange(e.target.value)}
                rows={4}
                placeholder="세션과 무관하게 남길 메모."
                className="block w-full resize-none rounded border border-border bg-surface px-2 py-1.5 text-[12px] leading-[1.5] text-ink placeholder:text-ink-4"
              />
            )}
            {tab === 'list' &&
              (notes.length === 0 ? (
                <p className="text-[11.5px] text-ink-3">메모가 있는 세션이 없습니다.</p>
              ) : (
                <ul aria-label="메모가 있는 세션" className="max-h-28 divide-y divide-border-subtle overflow-y-auto">
                  {notes.map((n) => (
                    <li key={n.sessionId}>
                      <button
                        type="button"
                        onClick={() => onPickSession(n.sessionId)}
                        className="flex w-full min-w-0 items-baseline gap-2 rounded px-1 py-1 text-left hover:bg-surface-2 focus-visible:-outline-offset-2!"
                      >
                        <span className="shrink-0 font-mono text-[10.5px] text-ink-2">{n.shortId}</span>
                        <span className="min-w-0 flex-1 truncate text-[11.5px] text-ink" title={n.title}>
                          {n.firstLine}
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              ))}
          </div>
        </>
      )}
    </section>
  );
};

export { SessionNoteEditor };
export type { NoteTab, NoteSaveState, NoteListEntry };
