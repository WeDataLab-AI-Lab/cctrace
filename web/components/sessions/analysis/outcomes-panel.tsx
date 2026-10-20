'use client';

import { Check, ChevronRight, Minus } from 'lucide-react';
import { useState, type ReactNode } from 'react';
import { cn } from '@/lib/utils';
import type { AnalysisOutputs, AnalysisTurn, SessionCommits, SessionTasks } from '@/lib/session-analysis-types';
import { Hint, Panel } from './atoms';
import { HashChip, TaskActionIcons, TaskChip } from './chips';
import { formatClockNear, formatCount, safeHref } from './format';
import { EditedFilesBlock, LinesBlock, PrBlock } from './outputs-blocks';
import { commitHashText, commitsSummary, filesSummary, tasksSummary, type OutcomeMark, type OutcomeSummary } from './outcomes';

/**
 * "세션 성과"(D9, D10, D11): 줄 셋. 앞의 기호는 관측해서 있다(확인), 관측해서 없다(빈 기호), 모른다(점선)를 가른다.
 *   작업·이슈 갱신  task 칩, 동작 아이콘 넷(추가·완료·수정·커밋 연결: 켜진 것은 색, 꺼진 것은 흐림), 한 줄 설명
 *   커밋 N건        해시 칩, task 칩, 메시지. 해시를 모르는 커밋은 "해시 불명" 줄로 그린다
 *   변경한 파일 N건  패스 A의 편집 파일 표(줄 수 포함)와 PR
 * 처음에는 첫 줄만 펼쳐 둔다(레퍼런스와 같다).
 */

const MarkGlyph = ({ mark }: { mark: OutcomeMark }) => {
  if (mark === 'present') return <Check size={13} strokeWidth={2.25} className="shrink-0 text-success" aria-hidden="true" />;
  if (mark === 'absent') return <Minus size={13} strokeWidth={2} className="shrink-0 text-ink-4" aria-hidden="true" />;
  return (
    <span aria-hidden="true" className="flex size-[13px] shrink-0 items-center justify-center rounded-[3px] border border-dashed border-border-strong text-ink-3">
      <Minus size={9} strokeWidth={2.25} />
    </span>
  );
};

const TITLE_CLASS: Record<OutcomeMark, string> = {
  present: 'font-medium text-success-strong',
  absent: 'text-ink-3',
  unknown: 'text-ink-3 italic',
};

interface OutcomeRowProps {
  id: string;
  summary: OutcomeSummary;
  open: boolean;
  onToggle: () => void;
  children?: ReactNode;
}

const OutcomeRow = ({ id, summary, open, onToggle, children }: OutcomeRowProps) => {
  const title = <span className={cn('text-[12px] leading-5', TITLE_CLASS[summary.mark])}>{summary.title}</span>;
  return (
    <li className="py-0.5">
      {summary.expandable ? (
        <button
          type="button"
          aria-expanded={open}
          aria-controls={`${id}-body`}
          title={summary.hint ?? undefined}
          onClick={onToggle}
          className="flex w-full items-center gap-1.5 rounded text-left hover:bg-surface-2 focus-visible:-outline-offset-2!"
        >
          <MarkGlyph mark={summary.mark} />
          {title}
          <ChevronRight
            size={12}
            strokeWidth={2}
            aria-hidden="true"
            className={cn('text-ink-3 transition-transform motion-reduce:transition-none', open && 'rotate-90')}
          />
        </button>
      ) : (
        <div className="flex items-center gap-1.5">
          <MarkGlyph mark={summary.mark} />
          {summary.hint ? (
            <Hint label={summary.hint} ariaLabel={`${summary.title}. ${summary.hint}`} className="underline decoration-dotted decoration-ink-4 underline-offset-[3px]">
              {title}
            </Hint>
          ) : (
            title
          )}
        </div>
      )}
      {summary.expandable && open && (
        <div id={`${id}-body`} className="mt-1 pl-[19px]">
          {children}
        </div>
      )}
    </li>
  );
};

/* ---------------- 펼친 내용 ---------------- */

const TasksBody = ({ tasks }: { tasks: SessionTasks }) => (
  <ul aria-label="갱신한 task·이슈" className="space-y-1">
    {tasks.items.map((task) => {
      const href = safeHref(task.url);
      const chip = <TaskChip id={task.id} kind={task.kind} title={href ? `${task.title} (새 창에서 열기)` : task.title} />;
      return (
        <li key={task.id} className="flex min-w-0 items-center gap-2">
          <span className="inline-flex shrink-0 items-center gap-1.5 rounded-full border border-border bg-surface px-1 py-px">
            {href ? (
              <a href={href} target="_blank" rel="noopener noreferrer" aria-label={`${task.id} ${task.title} 새 창에서 열기`}>
                {chip}
              </a>
            ) : (
              chip
            )}
            <TaskActionIcons task={task} />
          </span>
          {task.summary ? (
            <span className="min-w-0 flex-1 truncate text-[11.5px] text-ink-2" title={task.summary}>
              {task.summary}
            </span>
          ) : (
            <span className="min-w-0 flex-1 truncate text-[11.5px] text-ink-3">{task.title}</span>
          )}
        </li>
      );
    })}
  </ul>
);

const CommitsBody = ({ commits, now }: { commits: SessionCommits; now: number }) => (
  <>
    <ul aria-label="커밋 목록" className="divide-y divide-border-subtle">
      {commits.items.map((item, i) => {
        const short = commitHashText(item);
        return (
          <li key={`${item.hash ?? item.short ?? 'unknown'}-${i}`} className="flex min-w-0 items-center gap-1.5 py-1">
            <HashChip short={short} title={item.hash ? `${item.hash}${item.repository ? ` · ${item.repository}` : ''}${item.branch ? ` · ${item.branch}` : ''}` : undefined} />
            {item.tasks.map((id) => (
              <TaskChip key={id} id={id} />
            ))}
            <span className="min-w-0 flex-1 truncate text-[11.5px] text-ink" title={item.subject}>
              {item.subject}
            </span>
            <time dateTime={item.ts} className="shrink-0 font-mono text-[10.5px] tabular-nums text-ink-3">
              {formatClockNear(item.ts, now)}
            </time>
          </li>
        );
      })}
    </ul>
    {commits.truncated && typeof commits.total === 'number' && (
      <p className="mt-1 text-[11px] text-ink-3">
        커밋이 많아 앞의 {formatCount(commits.items.length)}건만 담았습니다. 합계는 전체 {formatCount(commits.total)}건입니다.
      </p>
    )}
  </>
);

const FilesBody = ({ outputs, turns, agent }: { outputs: AnalysisOutputs; turns: readonly AnalysisTurn[]; agent?: string }) => (
  <div className="grid gap-x-8 gap-y-3 @xl:grid-cols-[minmax(0,1fr)_minmax(0,230px)]">
    <EditedFilesBlock files={outputs.edited_files} agent={agent} />
    <div className="space-y-3">
      <LinesBlock lines={outputs.lines} agent={agent} />
      <PrBlock prs={outputs.prs} turns={turns} />
    </div>
  </div>
);

/* ---------------- 패널 ---------------- */

interface OutcomesPanelProps {
  id: string;
  outputs: AnalysisOutputs;
  tasks: SessionTasks | undefined;
  commits: SessionCommits | undefined;
  /** PR이 처음 보인 턴의 요청 번호를 찾는 데 쓴다. */
  turns: readonly AnalysisTurn[];
  agent?: string;
  now: number;
}

type RowId = 'tasks' | 'commits' | 'files';

const OutcomesPanel = ({ id, outputs, tasks, commits, turns, agent, now }: OutcomesPanelProps) => {
  // 첫 줄만 펼쳐 둔다. 세션이 바뀌면 호출하는 쪽의 key가 이 상태를 새로 시작한다.
  const [open, setOpen] = useState<Record<RowId, boolean>>({ tasks: true, commits: false, files: false });
  const toggle = (row: RowId) => setOpen((prev) => ({ ...prev, [row]: !prev[row] }));
  const taskSummary = tasksSummary(tasks);
  const commitSummary = commitsSummary(commits);
  const fileSummary = filesSummary(outputs, agent);
  return (
    <Panel id={id} title="세션 성과">
      <ul aria-label="세션 성과">
        <OutcomeRow id={`${id}-tasks`} summary={taskSummary} open={open.tasks} onToggle={() => toggle('tasks')}>
          {tasks && <TasksBody tasks={tasks} />}
        </OutcomeRow>
        <OutcomeRow id={`${id}-commits`} summary={commitSummary} open={open.commits} onToggle={() => toggle('commits')}>
          {commits && <CommitsBody commits={commits} now={now} />}
        </OutcomeRow>
        <OutcomeRow id={`${id}-files`} summary={fileSummary} open={open.files} onToggle={() => toggle('files')}>
          <FilesBody outputs={outputs} turns={turns} agent={agent} />
        </OutcomeRow>
      </ul>
    </Panel>
  );
};

export { OutcomesPanel };
