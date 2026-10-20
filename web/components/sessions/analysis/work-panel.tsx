'use client';

import { Square, SquareCheckBig, SquareDashed } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { AnalysisTurn, SessionRecord, WorkKind } from '@/lib/session-analysis-types';
import { Panel } from './atoms';
import { HashChip, TaskChip } from './chips';
import { formatCount } from './format';
import { RECORD_STATE_WORD, hasRecordContent } from './record';
import {
  CHECKBOX_LABEL,
  workRows,
  workSummary,
  type CheckboxState,
  type WorkChildView,
  type WorkRowView,
} from './work-items';

/**
 * "작업 상황"(D15): 각 줄에 모델이 제안한 완료 상태를 보여 주되 P1.5 검증 전에는 점선 글리프로 판단 불가를 유지한다.
 * 줄을 누르면 워터폴이 그 줄을 연 턴으로 옮겨 간다 — 턴을 모르는 줄은 눌리지 않는다.
 */

const KIND_TAG_CLASS: Record<WorkKind, string> = {
  consultation: 'border-ink-2 text-ink',
  request: 'border-cat-1/70 text-cat-1',
  followup: 'border-cat-6/70 text-cat-6',
  finishing: 'border-cat-2/80 text-cat-2',
};

const KindTag = ({ kind, label }: { kind: WorkKind; label: string }) => (
  <span
    className={cn(
      'mr-1.5 inline-flex shrink-0 items-center rounded-[4px] border bg-surface px-1 text-[10.5px] leading-[15px] font-medium whitespace-nowrap align-[1px]',
      KIND_TAG_CLASS[kind] ?? 'border-border-strong text-ink-2',
    )}
  >
    {label}
  </span>
);

const BOX_ICON = { done: SquareCheckBig, open: Square, unknown: SquareDashed } as const;
const BOX_CLASS: Record<CheckboxState, string> = {
  done: 'text-ink-2',
  open: 'text-warning-strong',
  unknown: 'text-ink-4',
};

const SUGGESTION_LABEL: Record<CheckboxState, string> = { done: '완료', open: '미완', unknown: '완료 여부 판단 불가' };

const Box = ({ state, className }: { state: CheckboxState; className?: string }) => {
  const Icon = BOX_ICON[state];
  return (
    <span role="img" aria-label={CHECKBOX_LABEL[state]} data-state={state} className={cn('flex shrink-0 items-center', className)}>
      <Icon size={14} strokeWidth={state === 'unknown' ? 1.75 : 2} className={BOX_CLASS[state]} aria-hidden="true" />
    </span>
  );
};

/** 줄 본문: 글, 결정, 커밋 칩, task 칩, 메모. 칩은 글처럼 이어 흐른다. */
const RowBody = ({ row }: { row: WorkRowView }) => (
  <>
    <KindTag kind={row.kind} label={row.kindLabel} />
    <span className="text-ink break-keep [overflow-wrap:anywhere]">{row.text}</span>
    {row.decision && <span className="text-ink-2 break-keep [overflow-wrap:anywhere]"> → 결정: {row.decision}</span>}
    {row.commits.length > 0 && (
      <span className="ml-1 inline-flex flex-wrap items-center gap-1 align-middle">
        <span className="text-ink-3">(</span>
        {row.commits.map((short, i) => (
          <span key={short} className="inline-flex items-center gap-1">
            {i > 0 && <span className="text-ink-4">,</span>}
            <HashChip short={short} />
          </span>
        ))}
        <span className="text-ink-3">)</span>
      </span>
    )}
    {row.tasks.length > 0 && (
      <span className="ml-1 inline-flex flex-wrap items-center gap-1 align-middle">
        {row.tasks.map((id) => (
          <TaskChip key={`${row.id}:${id}`} id={id} title={row.taskKeys.length > 0 ? `${id} · P4 item_key: ${row.taskKeys.join(', ')}` : undefined} />
        ))}
      </span>
    )}
    {row.note && <span className="text-ink-3"> · {row.note}</span>}
    <span className="ml-1 text-[10.5px] text-ink-3">모델 제안: {SUGGESTION_LABEL[row.suggestedState]}</span>
  </>
);

const ChildRow = ({ child }: { child: WorkChildView }) => (
  <li className="grid grid-cols-[14px_minmax(0,1fr)] items-start gap-1.5 py-px">
    <Box state={child.state} className="mt-[3px]" />
    <span className="text-[11.5px] leading-[1.5]">
      <span className="mr-1.5 inline-flex items-center rounded-[4px] border border-border bg-surface px-1 text-[10px] leading-[14px] text-ink-3">
        {child.kindLabel}
      </span>
      <span className={cn('break-keep [overflow-wrap:anywhere]', child.state === 'open' ? 'text-ink' : 'text-ink-2')}>{child.text}</span>
      <span className="ml-1 text-[10.5px] text-ink-3">모델 제안: {SUGGESTION_LABEL[child.suggestedState]}</span>
    </span>
  </li>
);

interface WorkRowProps {
  row: WorkRowView;
  /** 이 줄을 연 턴. 있으면 줄이 눌린다. */
  turn: AnalysisTurn | undefined;
  onOpen: (turn: AnalysisTurn) => void;
  openTarget?: 'conversation' | 'waterfall';
}

const WorkRow = ({ row, turn, onOpen, openTarget = 'waterfall' }: WorkRowProps) => {
  const select = () => { if (turn) onOpen(turn); };
  return (
  <li className="py-px">
    <div className="grid grid-cols-[14px_minmax(0,1fr)] items-start gap-1.5">
      <Box state={row.state} className="mt-[3px]" />
      {turn ? (
        <button
          type="button"
          onClick={select}
          title={openTarget === 'conversation' ? '대화에서 이 요청 보기' : '턴 워터폴에서 선택'}
          className="-mx-1 block w-[calc(100%+0.5rem)] rounded px-1 text-left text-[11.5px] leading-[1.4] hover:bg-surface-2 focus-visible:-outline-offset-2!"
        >
          <RowBody row={row} />
        </button>
      ) : (
        <div className="text-[11.5px] leading-[1.4]">
          <RowBody row={row} />
        </div>
      )}
    </div>
    {row.children.length > 0 && (
      <ul aria-label="하위 항목" className="mt-0.5 ml-[22px] space-y-px border-l border-border-subtle pl-2">
        {row.children.map((child, i) => (
          <ChildRow key={`${child.kind}-${i}`} child={child} />
        ))}
      </ul>
    )}
  </li>
  );
};

interface WorkPanelProps {
  id: string;
  record: SessionRecord | undefined;
  turns: readonly AnalysisTurn[];
  onOpenRequest: (turn: AnalysisTurn) => void;
  openTarget?: 'conversation' | 'waterfall';
}

const WorkPanel = ({ id, record, turns, onOpenRequest, openTarget }: WorkPanelProps) => {
  if (!record || !hasRecordContent(record)) {
    // 기록이 없으면 체크리스트도 없다. 판정의 "작업 내용 미기입"과 같은 이유다.
    return (
      <Panel id={id} title="작업 상황">
        <p className="text-[12px] leading-relaxed text-ink-3">
          {record
            ? `세션 기록이 없어 작업 상황을 보일 수 없습니다(${RECORD_STATE_WORD[record.state]}).`
            : '이 응답에는 세션 기록이 담겨 있지 않아 작업 상황을 보일 수 없습니다.'}{' '}
          작업이 없었다는 뜻이 아닙니다.
        </p>
      </Panel>
    );
  }
  const rows = workRows(record.work);
  const summary = workSummary(record.work);
  const turnByKey = new Map(turns.map((t) => [t.key, t] as const));
  return (
    <Panel
      id={id}
      title="작업 상황"
      aside={
        <span className="tabular-nums" title="최상위 항목 기준">
          {formatCount(summary.total)}건 · 판정 검증 대기
        </span>
      }
      bodyClassName="py-1.5"
    >
      {rows.length === 0 ? (
        <p className="py-1 text-[12px] text-ink-3">기록된 작업 항목이 없습니다.</p>
      ) : (
        <>
          <p className="pb-1 text-[10.5px] leading-relaxed text-ink-3">출처: 세션 기록의 work[].done 모델 제안입니다. 사유: unvalidated_model (P1.5 사람 평가 검증 기준 미통과). 완료·미완 판정으로 확정하지 않습니다.</p>
          <ul aria-label="작업 항목">
            {rows.map((row) => (
              <WorkRow key={row.id} row={row} turn={row.turnKey ? turnByKey.get(row.turnKey) : undefined} onOpen={onOpenRequest} openTarget={openTarget} />
            ))}
          </ul>
        </>
      )}
    </Panel>
  );
};

export { WorkPanel };
