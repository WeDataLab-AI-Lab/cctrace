'use client';

import Link from 'next/link';
import { useRouter, useSearchParams } from 'next/navigation';
import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { AlertCircle, ChevronLeft, ChevronRight } from 'lucide-react';
import { fetchProjects, fetchWeeklyInsights } from '@/lib/api';
import type { Project, WeeklyInsights } from '@/lib/types';
import { ProjectShareDonut } from '@/components/weekly/project-share-donut';
import { ReportPanel } from '@/components/weekly/report-panel';
import { TaskSegmentModal } from '@/components/weekly/task-segment-modal';
import { TaskTypesList } from '@/components/weekly/task-types-list';
import { currentWeekId, formatWeekLabel, isFutureWeek, parseWeekId, shiftWeek, weekRange } from '@/lib/weekly-period';
import { weeklyQueryKey, WEEKLY_STALE_TIME } from '@/lib/weekly-query';
import { cn } from '@/lib/utils';
import { CODEX_TOOL_SCOPE } from '@/lib/codex-tool-scope';
import { useAuth } from '@/components/common/auth-context';

const numberFormat = new Intl.NumberFormat('en-US');

/** What the reader is looking at, which is four things and not one.
 *
 *  The cards read `data?.x ?? []`, so a failed or pending read rendered as an
 *  empty week: "No sessions in this period", "Active projects 0", "No failures".
 *  That is #670 -- an unread week and an idle week drawn the same way. Every card
 *  asks this before it prints a number. */
type WeeklyReadState = 'loading' | 'failed' | 'read';

const weeklyReadState = (isLoading: boolean, isError: boolean, hasData: boolean): WeeklyReadState => {
  // A failed refetch keeps the week already read; the page's banner reports it.
  if (isError && hasData) return 'read';
  if (isError) return 'failed';
  if (isLoading || !hasData) return 'loading';
  return 'read';
};

/** What a card shows in place of a figure it cannot state. Never "0", never "–"
 *  alone: the dash without a reason is how the first version read as an empty
 *  week. */
const unreadLabel = (state: WeeklyReadState): string | null => {
  switch (state) {
    case 'loading':
      return '불러오는 중';
    case 'failed':
      return '불러오지 못함';
    default:
      return null;
  }
};

/** The week the page shows. A fixed week replaces the old sliding seven-day
 *  window so a report read on Monday opens unchanged on Friday (spec §3.1).
 *
 *  Without a week to open, the page opens the one that just ended rather than the
 *  one in progress: a week still being written holds a fraction of its work, so
 *  every card reads as a collapse against the week before, and it is the finished
 *  week the AI report covers. The week in progress stays one click away through
 *  the next link, or by naming it in the URL. Malformed and future weeks land
 *  there too -- a week that has not started would draw as an idle week. */
const resolveWeekId = (param: string | null, now: number, timeZone: string): string => {
  const weekId = param === null ? null : parseWeekId(param);
  if (weekId === null || isFutureWeek(weekId, now, timeZone)) {
    return shiftWeek(currentWeekId(now, timeZone), -1);
  }
  return weekId;
};

interface WeekNavigation {
  prev: string;
  next: string | null;
}

const weekNavigation = (weekId: string, now: number, timeZone: string): WeekNavigation => {
  const next = shiftWeek(weekId, 1);
  return { prev: shiftWeek(weekId, -1), next: isFutureWeek(next, now, timeZone) ? null : next };
};

const weekHref = (weekId: string): string => `/weekly?week=${weekId}`;

/** Where to send a `?week=` that named a week other than the one shown -- a
 *  malformed or future one -- so the address stays a way back to this screen. */
const weekRedirect = (param: string | null, weekId: string): string | null =>
  param === null || param === weekId ? null : weekHref(weekId);

const AGENT_LABELS: Record<string, string> = {
  claude: 'Claude Code',
  codex: 'Codex',
};

const agentLabel = (agent: string): string => AGENT_LABELS[agent] ?? agent;

const RuleTag = () => (
  <span className="rounded-[var(--r-xs)] bg-surface-sunk px-1.5 py-0.5 text-[11px] font-semibold tracking-[0.05em] text-ink-3">
    규칙 산출
  </span>
);

const sessionSummary = (data: WeeklyInsights): string => {
  const sessions = data.agent_sessions.length === 0
    ? ['세션 0']
    : data.agent_sessions.map((a) => `${agentLabel(a.agent)} 세션 ${numberFormat.format(a.session_count)}`);
  return [...sessions, `작업 구간 ${numberFormat.format(data.segment_count)}`].join(' · ');
};

interface ScopeBlockProps {
  weekId: string;
  timeZone: string;
  inProgress: boolean;
  data: WeeklyInsights | undefined;
  readState: WeeklyReadState;
}

/** What the week's reading rests on: rule-derived, no model involved, shown in
 *  every state (spec §3.3). A sentence under the page title rather than a card --
 *  it fixes the population, it does not compete with the report for weight. */
/** Caveats that belong to one agent, listed only when that agent actually ran
 *  this week. A warning about an agent with no sessions explains nothing, and
 *  standing boilerplate teaches the reader to skip the caveats that do apply. */
const agentCaveats = (data: WeeklyInsights | undefined): string[] => {
  const ran = (agent: string) => !!data?.agent_sessions.some((a) => a.agent === agent && a.session_count > 0);
  // Codex outcomes arrive only as the codex.tool.call metric (#698). It carries
  // no session_id, so a segment cannot show them; it counts calls made inside a
  // code-mode exec script; and only TUI and VS Code runs emit it -- Desktop,
  // codex_cli_rs and `codex exec` send next to none.
  return ran('codex')
    ? [`${CODEX_TOOL_SCOPE}, 작업 구간별 성공·실패는 보이지 않음`]
    : [];
};

const ScopeBlock = ({ weekId, timeZone, inProgress, data, readState }: ScopeBlockProps) => {
  const unread = unreadLabel(readState);
  const uncovered = !unread && data && data.uncovered_session_count > 0
    ? [`구간 사실이 덮지 못한 세션 ${numberFormat.format(data.uncovered_session_count)}개는 작업 구간 수에 들어가지 않음`]
    : [];
  // Said when nothing of this reader's was excluded, it is a standing disclaimer
  // rather than a fact about their week. With the count it becomes a fact, and
  // below one it disappears -- the same rule the agent caveats follow.
  const excluded = !unread && data && data.excluded_record_count > 0
    ? [`관리자 제외 설정으로 이번 주 기록 ${numberFormat.format(data.excluded_record_count)}건이 분석에서 빠짐`]
    : [];
  const caveats = [...agentCaveats(data), ...excluded, ...uncovered];

  return (
    <section aria-label="범위" className="space-y-1">
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[13px] tabular-nums text-ink-2">
        <span>
          {formatWeekLabel(weekId, timeZone)}
          {inProgress && ' · 진행 중'} · 내 기록 · {timeZone} · {unread ?? (data && sessionSummary(data))}
        </span>
        <RuleTag />
      </p>
      <p className="text-[11.5px] text-ink-3">{caveats.join(' · ')}</p>
    </section>
  );
};

/** Title of the rule-derived figures above the report. One heading level with
 *  the report blocks, never above them (spec §3.0). */
const FiguresHeader = () => (
  <header className="mb-4 flex items-center gap-2">
    <h3 className="text-[11px] font-semibold tracking-[0.05em] text-ink-3">이번 주 수치</h3>
    <RuleTag />
  </header>
);

const navButtonClass = 'inline-flex size-7 items-center justify-center rounded-[var(--r-sm)] border border-border bg-surface text-ink-2';

interface WeekNavProps {
  weekId: string;
  timeZone: string;
  navigation: WeekNavigation;
}

const WeekNav = ({ weekId, timeZone, navigation }: WeekNavProps) => (
  <nav aria-label="주 이동" className="flex items-center gap-2 text-[12.5px] text-ink-2">
    <Link href={weekHref(navigation.prev)} aria-label="이전 주" className={cn(navButtonClass, 'hover:bg-surface-2')}>
      <ChevronLeft size={14} />
    </Link>
    <span className="tabular-nums">
      {weekId} · {formatWeekLabel(weekId, timeZone)}
    </span>
    {navigation.next ? (
      <Link href={weekHref(navigation.next)} aria-label="다음 주" className={cn(navButtonClass, 'hover:bg-surface-2')}>
        <ChevronRight size={14} />
      </Link>
    ) : (
      <button type="button" aria-label="다음 주" disabled className={cn(navButtonClass, 'opacity-40')}>
        <ChevronRight size={14} />
      </button>
    )}
  </nav>
);

interface WeeklyReportProps {
  weekParam: string | null;
}

const WeeklyReport = ({ weekParam }: WeeklyReportProps) => {
  // Pinned at mount: read during render the clock would move the current week
  // under the reader and the query key with it. The page remounts this per week
  // parameter, so moving to another week pins a fresh clock.
  const [now] = useState(() => Date.now());
  const [tz] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone);
  const [openTaskType, setOpenTaskType] = useState<string | null>(null);
  const router = useRouter();
  const { user } = useAuth();

  const weekId = resolveWeekId(weekParam, now, tz);
  const redirect = weekRedirect(weekParam, weekId);
  const { since, until } = weekRange(weekId, tz);
  const { data, isLoading, isError } = useQuery<WeeklyInsights>({
    queryKey: weeklyQueryKey({ since, until, timeZone: tz, userId: user?.id ?? null }),
    queryFn: () => fetchWeeklyInsights(since, until, tz),
    staleTime: WEEKLY_STALE_TIME,
    enabled: user !== null,
  });
  const {
    data: projectRegistry = [],
    isLoading: isProjectRegistryLoading,
    isError: isProjectRegistryError,
  } = useQuery<Project[]>({
    queryKey: ['projects', 'identity-registry'],
    queryFn: () => fetchProjects(),
    staleTime: 60_000,
  });

  const readState = weeklyReadState(isLoading, isError, data !== undefined);
  const handleCloseTaskSegments = () => setOpenTaskType(null);

  /** Rewrites a malformed or future `?week=` to the week actually shown, so the
   *  address bar and the screen name the same week. Navigation is a side effect,
   *  so it runs after render rather than during it. */
  useEffect(() => {
    if (redirect) router.replace(redirect);
  }, [redirect, router]);

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h2 className="text-[16px] font-semibold text-ink">Weekly report</h2>
          <p className="mt-1 text-[13px] text-ink-3">이 페이지의 AI 리포트는 선택된 작업 구간의 대화 내용을 읽고 작성됩니다</p>
        </div>
        <WeekNav weekId={weekId} timeZone={tz} navigation={weekNavigation(weekId, now, tz)} />
      </header>

      {isError && (
        <div className="flex items-center gap-2 rounded-md border border-danger/30 bg-danger/5 px-4 py-3 text-[13px] text-danger">
          <AlertCircle size={16} /> Could not load the weekly report. Try refreshing the page.
        </div>
      )}

      <ScopeBlock
        weekId={weekId}
        timeZone={tz}
        inProgress={weekId === currentWeekId(now, tz)}
        data={data}
        readState={readState}
      />
      {/* Above the report as the week's overview, flat on the canvas with a rule
          under it, so the rule-derived figures never read with the report's
          cards' weight (spec §3.0). */}
      <section className="border-b border-border pb-6">
        <FiguresHeader />
        <div className="grid gap-8 md:grid-cols-2">
          <ProjectShareDonut
            projects={data?.projects ?? []}
            registry={projectRegistry}
            loading={readState === 'loading' || isProjectRegistryLoading}
            // The donut has no failed state of its own; an unread week must not
            // draw as "no projects" (#670).
            registryError={isProjectRegistryError || readState === 'failed'}
          />
          <TaskTypesList
            tasks={data?.tasks ?? []}
            typedTurnCount={data?.typed_turn_count ?? 0}
            readState={readState}
            onOpenTaskType={setOpenTaskType}
          />
        </div>
      </section>

      <ReportPanel weekId={weekId} timeZone={tz} userId={user?.id ?? null} />

      {openTaskType && (
        <TaskSegmentModal taskType={openTaskType} since={since} until={until} onClose={handleCloseTaskSegments} />
      )}
    </div>
  );
};

export default function WeeklyReportPage() {
  const weekParam = useSearchParams().get('week');

  // Keyed by the parameter so each week mounts with its own clock instead of
  // syncing a pinned one through an effect.
  return <WeeklyReport key={weekParam ?? ''} weekParam={weekParam} />;
}

export {
  FiguresHeader,
  resolveWeekId,
  ScopeBlock,
  unreadLabel,
  weekHref,
  weekNavigation,
  weeklyReadState,
  weekRedirect,
};
export type { WeeklyReadState };
