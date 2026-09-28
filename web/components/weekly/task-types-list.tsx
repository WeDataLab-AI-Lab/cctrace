'use client';

import { ChartSkeleton } from '@/components/common/chart-skeleton';
import type { WeeklyInsightTask } from '@/lib/types';

// Three ways a typed turn carries no task type, and they mean different things:
//
//   non-task    it was never a request -- hook output, a slash command, an
//               interruption marker, another agent's message
//   unknown     a request the rules could not place; AI analysis classifies it
//   (absent)    never classified at all (task_type still '') -- a backfill gap
//
// Shares are taken over what remains once the first two are out: non-task is not
// work, and unknown left in the denominator was the largest row on the card and
// shrank every real category (#429).
const NON_TASK = 'non-task';
const UNPLACED = 'unknown';

const numberFormat = new Intl.NumberFormat('en-US');

type TaskTypesReadState = 'loading' | 'failed' | 'read';

interface TaskTypeRow {
  taskType: string;
  count: number;
  share: string;
}

interface TaskTypeView {
  rows: TaskTypeRow[];
  denominator: number;
  unplaced: number;
  unclassified: number;
  unclassifiedShare: string;
  nonTask: number;
}

const formatShare = (count: number, total: number) => (total > 0 ? `${Math.round((count / total) * 100)}%` : '0%');

const buildTaskTypeView = (tasks: readonly WeeklyInsightTask[], typedTurnCount: number): TaskTypeView => {
  const countOf = (type: string) => tasks.find((t) => t.task_type === type)?.prompt_count ?? 0;
  const nonTask = countOf(NON_TASK);
  const unplaced = countOf(UNPLACED);
  const classified = tasks.reduce((sum, t) => sum + t.prompt_count, 0);
  const unclassified = Math.max(0, typedTurnCount - classified);
  const denominator = Math.max(0, typedTurnCount - nonTask - unplaced);

  return {
    rows: tasks
      .filter((t) => t.task_type !== NON_TASK && t.task_type !== UNPLACED)
      .map((t) => ({ taskType: t.task_type, count: t.prompt_count, share: formatShare(t.prompt_count, denominator) })),
    denominator,
    unplaced,
    unclassified,
    unclassifiedShare: formatShare(unclassified, denominator),
    nonTask,
  };
};

interface TaskTypeButtonProps {
  row: TaskTypeRow;
  onOpenTaskType: (taskType: string) => void;
}

const TaskTypeButton = ({ row, onOpenTaskType }: TaskTypeButtonProps) => {
  const handleClick = () => onOpenTaskType(row.taskType);
  return (
    <button
      type="button"
      onClick={handleClick}
      className="flex w-full items-center justify-between gap-4 rounded-sm px-2 py-1.5 text-left hover:bg-surface-sunk"
    >
      <span className="capitalize text-ink">{row.taskType}</span>
      <span className="tabular-nums text-ink-3">
        {numberFormat.format(row.count)} · {row.share}
      </span>
    </button>
  );
};

interface TaskTypesListProps {
  tasks: readonly WeeklyInsightTask[];
  typedTurnCount: number;
  readState: TaskTypesReadState;
  onOpenTaskType: (taskType: string) => void;
}

const EmptyLine = ({ children }: { readonly children: React.ReactNode }) => (
  <p className="py-4 text-[12.5px] text-ink-3">{children}</p>
);

const TaskTypesBody = ({ tasks, typedTurnCount, readState, onOpenTaskType }: TaskTypesListProps) => {
  if (readState === 'loading') return <ChartSkeleton variant="bar" className="h-[200px]" />;
  if (readState === 'failed') return <EmptyLine>요청 분류를 불러오지 못했습니다.</EmptyLine>;
  if (typedTurnCount === 0) return <EmptyLine>이 기간에 기록된 요청이 없습니다.</EmptyLine>;

  const view = buildTaskTypeView(tasks, typedTurnCount);

  return (
    <>
      <ul className="text-[12.5px]">
        {view.rows.map((row) => (
          <li key={row.taskType}>
            <TaskTypeButton row={row} onOpenTaskType={onOpenTaskType} />
          </li>
        ))}
      </ul>
      <ul className="mt-2 space-y-1 border-t border-border-subtle px-2 pt-2 text-[11.5px] text-ink-3">
        {view.unplaced > 0 && (
          // Not a share row: the AI pass has yet to place these, so a percentage
          // beside real categories would compete with them.
          <li>규칙으로 분류하지 못한 요청 {numberFormat.format(view.unplaced)}건 · AI 분석 시 분류됨</li>
        )}
        {view.unclassified > 0 && (
          <li className="flex items-center justify-between gap-4">
            <span>
              Not yet classified
              <span className="ml-2">동기화 시 분류됨 · 과거 기록은 관리자 백필 필요</span>
            </span>
            <span className="shrink-0 tabular-nums">
              {numberFormat.format(view.unclassified)} prompts · {view.unclassifiedShare}
            </span>
          </li>
        )}
        {view.nonTask > 0 && (
          <li
            className="flex items-center justify-between gap-4"
            title="Turns that carried no request at all: hook output, slash commands, interruption markers, another agent's message. Excluded from the shares above."
          >
            <span>Not a request</span>
            <span className="shrink-0 tabular-nums">{numberFormat.format(view.nonTask)} turns</span>
          </li>
        )}
      </ul>
    </>
  );
};

const TaskTypesList = (props: TaskTypesListProps) => (
  <section>
    <header className="mb-2 flex items-baseline gap-2">
      <h3 className="text-[13px] font-semibold text-ink-2">Task types</h3>
      <span className="text-[11.5px] text-ink-3">요청 수</span>
    </header>
    <TaskTypesBody {...props} />
  </section>
);

export { buildTaskTypeView, TaskTypesList };
export type { TaskTypeRow, TaskTypesListProps, TaskTypesReadState, TaskTypeView };
