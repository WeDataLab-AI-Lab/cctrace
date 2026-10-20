'use client';

import { useEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { FlaskConical, ShieldCheck, UserRound } from 'lucide-react';
import { cn } from '@/lib/utils';
import { useAppearance, type Accent } from '@/components/common/appearance-context';
import { TooltipProvider } from '@/components/ui/tooltip';
import { SessionAnalysisTab } from '@/components/sessions/analysis/analysis-tab';
import { SessionCalendar } from '@/components/sessions/calendar/session-calendar';
import { SessionToolbar } from '@/components/sessions/calendar/session-toolbar';
import { statusChipCounts, chipOfStatus, type StatusChipKey } from '@/components/sessions/calendar/status';
import { todayWeekStart, weekQueryRange } from '@/components/sessions/calendar/week';
import { formatClockIn, type LocalDate } from '@/components/sessions/calendar/time-zone';
import { DEFAULT_PX_PER_HOUR } from '@/components/sessions/calendar/zoom';
import type { SessionsView } from '@/components/sessions/calendar/view-mode-switch';
import type { CalendarColorKey, SummaryGeneration } from '@/lib/session-calendar-types';
import { CALENDAR_SESSIONS, DATASET } from '../calendar/dataset';
import { readCalendar } from '../calendar/read-calendar';
import { DST_JUMPS } from '../calendar/dst-demo';
import { DST_DETAIL_SESSIONS } from './dst-fixtures';
import { adaptPassA } from './adapt';
import { calendarOfDetail } from './calendar';
import { generateDemoRecord } from './record-generation';
import { BucketCountChips } from '@/components/sessions/analysis/bucket-chips';
import { ClassificationSelect } from '@/components/sessions/analysis/classification-select';
import { ClassificationTabs, type ClassificationFilter } from '@/components/sessions/analysis/classification-tabs';
import { titleView } from '@/components/sessions/analysis/format';
import { isFullAnalysis } from '@/components/sessions/analysis/guards';
import { EndedGroupHeader, RunningGroupHeader } from '@/components/sessions/analysis/list-groups';
import {
  classificationCounts,
  classificationOf,
  groupRows,
  runningCounts,
  type EndedGroup,
} from '@/components/sessions/analysis/list-model';
import { SessionNoteEditor, type NoteListEntry, type NoteSaveState, type NoteTab } from '@/components/sessions/analysis/note-editor';
import { hasRecordContent } from '@/components/sessions/analysis/record';
import { SessionAnalysisRow } from '@/components/sessions/analysis/session-analysis-row';
import { AnalysisUnavailable } from '@/components/sessions/analysis/state-notices';
import { FrameStatusBar, FrameTopBar, searchSessions, type SearchDoc } from '@/components/sessions/frame';
import { TimelinePane, type TimelineFocus } from '@/components/sessions/timeline';
import type { AnalysisTurn, JudgementBucket, SessionClassification, SessionSort, SessionRecord } from '@/lib/session-analysis-types';
import { deriveJudgement, rowJudgementOf } from './judgement-derive';
import type { SessionOverview } from '@/lib/types';
import {
  DETAIL_NOW,
  DETAIL_SESSIONS,
  FLAGSHIP_ID,
  FRAME_ACCOUNT_LABELS,
  SEED_GLOBAL_NOTE,
  SEED_NOTES,
  analysisFor,
  frameFor,
  rowFor,
  timelineFor,
  type DetailFixture,
  type ViewerRole,
} from './fixtures';

/**
 * 세션 상세 분석 미리보기(패스 C): 위에 프레임(계정별 사용량, 5H·7D 차트, 검색), 왼쪽에 목록 카드, 가운데에 상세,
 * 턴 선택으로 열리는 합성 원문 칸, 맨 아래에 상태 표시줄. 합성 픽스처만 올린다. 서버에 연결하지 않는다.
 * 이 화면의 컴포넌트는 통합 패스가 그대로 실제 API에 연결한다.
 */

/* ---------------- 작은 제어 ---------------- */

interface SegOption<T extends string> {
  value: T;
  label: string;
  hint: string;
}

const Segmented = <T extends string>({
  label,
  value,
  onChange,
  options,
}: {
  label: string;
  value: T;
  onChange: (next: T) => void;
  options: readonly SegOption<T>[];
}) => (
  <div role="group" aria-label={label} className="flex shrink-0 items-center gap-1.5">
    <span className="text-[11px] text-warning-strong">{label}</span>
    <div className="flex gap-0.5">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={value === o.value}
          title={o.hint}
          onClick={() => onChange(o.value)}
          className={cn(
            'rounded px-2 py-0.5 text-[11px] font-medium whitespace-nowrap transition-colors',
            value === o.value ? 'bg-brand text-brand-ink' : 'bg-surface/70 text-ink-2 hover:bg-surface',
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  </div>
);

const ROLE_OPTIONS: readonly SegOption<ViewerRole>[] = [
  { value: 'owner', label: '세션 주인', hint: '자기 세션을 봅니다. 판정, 신호, 관측 범주, 실시간 상태, 메모, 분류가 모두 옵니다.' },
  {
    value: 'admin',
    label: '주인이 아닌 관리자',
    hint: '다른 사람의 세션을 봅니다. 판정·신호·범주·실시간·메모·분류와 그 개수를 API가 돌려주지 않고, 대신 신원 정보가 옵니다. 대화와 기록, 커밋은 읽을 수 있습니다.',
  },
];

const ACCENT_OPTIONS: readonly SegOption<Accent>[] = [
  { value: 'mono', label: 'Mono', hint: '기본 액센트' },
  { value: 'indigo', label: 'Indigo', hint: 'Indigo 액센트' },
  { value: 'teal', label: 'Teal', hint: 'Teal 액센트' },
];

/* ---------------- 데이터 ---------------- */

const firstLine = (value: string): string => value.split('\n')[0]?.trim() ?? '';

/** 메모와 분류의 편집을 행에 반영한다. 비소유 범위의 행에는 두 값이 없으므로 건드리지 않는다. */
const withEdits = (
  row: SessionOverview,
  notes: Readonly<Record<string, string>>,
  classes: Readonly<Record<string, SessionClassification>>,
): SessionOverview => {
  if (!row.analysis || row.analysis.classification === undefined) return row;
  const text = notes[row.session_id];
  const changed = classes[row.session_id];
  return {
    ...row,
    analysis: {
      ...row.analysis,
      ...(text !== undefined ? { note: text.trim() ? firstLine(text) : null } : {}),
      ...(changed ? { classification: changed } : {}),
    },
  };
};

const titleOf = (f: DetailFixture, role: ViewerRole): string => {
  const a = analysisFor(f, role);
  const title = a?.header?.title ?? f.row.analysis?.title;
  const view = titleView(title);
  return view.text ?? view.placeholder ?? '제목 없음';
};

const searchDocOf = (f: DetailFixture, role: ViewerRole, note: string | undefined): SearchDoc => {
  const a = analysisFor(f, role);
  const full = a && isFullAnalysis(a) ? a : null;
  const record = full?.record && hasRecordContent(full.record) ? full.record : null;
  return {
    id: f.id,
    title: titleOf(f, role),
    headline: record?.headline ?? null,
    work: record ? record.work.map((w) => w.text) : [],
    commits: full?.commits ? full.commits.items.flatMap((c) => [c.subject, ...(c.short ? [c.short] : [])]) : [],
    tasks: full?.tasks ? full.tasks.items.flatMap((t) => [t.id, t.title]) : [],
    note: role === 'owner' && note ? note : null,
  };
};

const byRecency = (a: SessionOverview, b: SessionOverview): number => Date.parse(b.end_time) - Date.parse(a.end_time);

/* ---------------- 본체 ---------------- */

const LIST_PANEL_ID = 'detail-preview-list';
const DETAIL_IDS = new Set(DETAIL_SESSIONS.map((fixture) => fixture.id));
const INTEGRATED_SESSIONS = [...DETAIL_SESSIONS, ...DST_DETAIL_SESSIONS, ...DATASET.filter((entry) => !DETAIL_IDS.has(entry.fixture.id)).map((entry, index) => adaptPassA(entry.fixture, index))];
const DETAIL_CALENDAR = DETAIL_SESSIONS.flatMap((fixture) => {
  const calendar = calendarOfDetail(fixture);
  if (!calendar) return [];
  const original = CALENDAR_SESSIONS.find((session) => session.session_id === fixture.id);
  // 기존 활동 조각을 보존한다. 상세 턴은 활동 조각의 대체 원천이 아니다.
  return [original ? { ...original, requests: calendar.requests, cost: calendar.cost, colors: { ...original.colors, status: calendar.colors.status } } : calendar];
});
const INTEGRATED_CALENDAR = [...DETAIL_CALENDAR, ...CALENDAR_SESSIONS.filter((session) => !DETAIL_IDS.has(session.session_id))].map((session) => {
  const fixture = INTEGRATED_SESSIONS.find((item) => item.id === session.session_id);
  const detail = fixture ? calendarOfDetail(fixture) : null;
  return detail ? { ...session, requests: detail.requests, cost: detail.cost, colors: { ...session.colors, status: detail.colors.status } } : session;
});

const DetailPreview = () => {
  const { accent, setAccent } = useAppearance();
  const [role, setRole] = useState<ViewerRole>('owner');
  const [selectedId, setSelectedId] = useState<string>(FLAGSHIP_ID);
  const [timelineOpen, setTimelineOpen] = useState(false);
  const [focus, setFocus] = useState<TimelineFocus | null>(null);
  const [account, setAccount] = useState<string>(FRAME_ACCOUNT_LABELS[1] ?? '서브');
  const [query, setQuery] = useState('');
  const [classification, setClassification] = useState<ClassificationFilter>('all');
  const handleBucket = (bucket: JudgementBucket | null) => setStatusFilter(bucket === 'ended_midway' ? 'midway' : bucket);
  const [dismissed, setDismissed] = useState<ReadonlySet<string>>(new Set());
  const [notes, setNotes] = useState<Readonly<Record<string, string>>>(SEED_NOTES);
  const [globalNote, setGlobalNote] = useState(SEED_GLOBAL_NOTE);
  const [noteOpen, setNoteOpen] = useState(false);
  const [noteTab, setNoteTab] = useState<NoteTab>('session');
  const [saveState, setSaveState] = useState<NoteSaveState>('saved');
  const [classes, setClasses] = useState<Readonly<Record<string, SessionClassification>>>({});
  const [view, setView] = useState<SessionsView>('calendar');
  const [statusFilter, setStatusFilter] = useState<StatusChipKey | null>(null);
  const [showRequestless, setShowRequestless] = useState(false);
  const [planFileOnly, setPlanFileOnly] = useState(false);
  const [sort, setSort] = useState<SessionSort>('start');
  const [reloads, setReloads] = useState(0);
  const [timeZone, setTimeZone] = useState('Asia/Seoul');
  const [explicitWeek, setExplicitWeek] = useState<LocalDate | null>(null);
  const [pxPerHour, setPxPerHour] = useState(DEFAULT_PX_PER_HOUR);
  const [colorKey, setColorKey] = useState<CalendarColorKey>('screen');
  const [summaryState, setSummaryState] = useState<{ key: string; value: SummaryGeneration }>({ key: '', value: { state: 'stale' } });
  const [records, setRecords] = useState<Readonly<Record<string, SessionRecord>>>({});
  const recordTimers = useRef(new Map<string, ReturnType<typeof setTimeout>>());
  const summaryTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const saveTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const listRef = useRef<HTMLDivElement>(null);

  const isOwner = role === 'owner';
  const frame = frameFor(account);
  const fixtures = INTEGRATED_SESSIONS.map((fixture) => {
    const record = records[fixture.id];
    if (!record || !fixture.analysis || !isFullAnalysis(fixture.analysis)) return fixture;
    const previous = fixture.analysis.judgement;
    const judgement = deriveJudgement({ alive: previous?.status === 'running' || previous?.status === 'interrupted', interrupted: previous?.status === 'interrupted', signals: fixture.analysis.signals, record });
    return {
      ...fixture,
      analysis: { ...fixture.analysis, record, judgement },
      row: { ...fixture.row, analysis: fixture.row.analysis ? { ...fixture.row.analysis, judgement: rowJudgementOf(judgement), headline: hasRecordContent(record) ? record.headline : null } : undefined },
    };
  });
  const allRows = fixtures.map((f) => withEdits(rowFor(f, role), notes, classes));
  const requestless = allRows.filter((row) => row.analysis?.requests === 0 && (!planFileOnly || row.analysis.plan_flags?.has_plan_file)).length;
  const baseRows = allRows.filter((row) => (showRequestless || row.analysis?.requests !== 0) && (!planFileOnly || row.analysis?.plan_flags?.has_plan_file));
  const calendarById = new Map(INTEGRATED_CALENDAR.map((session) => [session.session_id, session]));
  const rowStatus = (row: SessionOverview) => {
    const judgement = row.analysis?.judgement;
    if (!judgement) return calendarById.get(row.session_id)?.colors.status;
    if (judgement.status === 'running' || judgement.status === 'interrupted') return 'running' as const;
    if (judgement.bucket === 'ended_midway') return 'midway' as const;
    return judgement.bucket;
  };
  // 검색은 제목·개요·작업 항목·커밋·task·메모를 가로지른다(A4). 상태별 개수 필터는 주인 범위에서만 있다.
  const hits = searchSessions(
    fixtures.map((f) => searchDocOf(f, role, notes[f.id])),
    query,
  );
  const hitIds = hits ? new Set(hits.map((h) => h.id)) : null;
  const searched = hitIds ? baseRows.filter((r) => hitIds.has(r.session_id)) : baseRows;
  const toolbarCounts = statusChipCounts(searched.map(rowStatus));
  const visible = searched.filter((r) => !isOwner || statusFilter === null || chipOfStatus(rowStatus(r)) === statusFilter).sort((a, b) => {
    if (sort === 'activity') return byRecency(a, b);
    if (sort === 'cost') return (b.cost_usd ?? -1) - (a.cost_usd ?? -1);
    if (sort === 'requests') return (b.analysis?.requests ?? -1) - (a.analysis?.requests ?? -1);
    return Date.parse(b.start_time) - Date.parse(a.start_time);
  });

  // 분류 탭도 묶음보다 먼저 건다: 탭의 개수와 화면의 카드 수가 어긋나지 않게(실행 중·함께 끝난 묶음도 그 분류의 세션만 남는다).
  const classified = isOwner ? visible.filter((r) => classification === 'all' || classificationOf(r) === classification) : visible;
  const groups = isOwner ? groupRows(classified, dismissed) : { running: [], ended: [], rest: classified };
  const rest = groups.rest;
  const shownCount = groups.running.length + groups.ended.reduce((n, g) => n + g.rows.length, 0) + rest.length;

  const selected = fixtures.find((f) => f.id === selectedId);
  const analysis = selected ? analysisFor(selected, role) : null;
  const timeline = selected ? timelineFor(selected, role) : [];
  const selectedTitle = selected ? titleOf(selected, role) : '';
  const selectedClass: SessionClassification =
    (selected ? (classes[selected.id] ?? selected.row.analysis?.classification) : undefined) ?? 'unsorted';

  // 고른 카드가 목록 밖에 있으면 보이게 한다(처음 열 때, 역할을 바꿀 때, 메모 목록에서 고를 때).
  useEffect(() => {
    const list = listRef.current;
    const el = list?.querySelector<HTMLElement>(`[data-session-id="${selectedId}"]`);
    if (!list || !el) return;
    // 카드의 윗부분이 이미 보이면 그대로 둔다(처음 열 때 목록 맨 위의 묶음 머리가 잘리지 않게).
    const box = list.getBoundingClientRect();
    const top = el.getBoundingClientRect().top;
    if (top >= box.top && top <= box.bottom - 48) return;
    el.scrollIntoView({ block: 'nearest' });
  }, [selectedId, role]);

  const handleSelect = (session: SessionOverview) => {
    setSelectedId(session.session_id);
    setFocus(null);
  };
  const handleOpenRequest = (turn: AnalysisTurn) => {
    setTimelineOpen(true);
    setFocus((prev) => ({ turnKey: turn.key, nonce: (prev?.nonce ?? 0) + 1 }));
  };
  const handleOpenSession = (sessionId: string) => {
    if (INTEGRATED_SESSIONS.some((f) => f.id === sessionId)) {
      setSelectedId(sessionId);
      setFocus(null);
    }
  };
  const handleDismiss = (session: SessionOverview) => setDismissed((prev) => new Set([...prev, session.session_id]));
  const handleDismissAll = (group: EndedGroup) => setDismissed((prev) => new Set([...prev, ...group.rows.map((r) => r.session_id)]));

  // 합성 시연의 메모는 이 화면의 상태에만 남는다. 서버 저장 완료로 오해하지 않게 편집기 옆에도 범위를 표시한다.
  const scheduleSave = () => {
    setSaveState('dirty');
    if (saveTimer.current) clearTimeout(saveTimer.current);
    saveTimer.current = setTimeout(() => setSaveState('saved'), 700);
  };
  const handleSessionNote = (text: string) => {
    if (!selected) return;
    setNotes((prev) => ({ ...prev, [selected.id]: text }));
    scheduleSave();
  };
  const handleGlobalNote = (text: string) => {
    setGlobalNote(text);
    scheduleSave();
  };

  const fixtureById = new Map(fixtures.map((f) => [f.id, f] as const));
  const noteList: NoteListEntry[] = allRows.flatMap((r) => {
    const f = fixtureById.get(r.session_id);
    const text = notes[r.session_id] ?? '';
    return f && text.trim() !== '' ? [{ sessionId: r.session_id, shortId: r.session_id.slice(0, 8), title: titleOf(f, role), firstLine: firstLine(text) }] : [];
  });

  const counts = { done: toolbarCounts.counts.done, leftover: toolbarCounts.counts.leftover, ended_midway: toolbarCounts.counts.midway, unknown: toolbarCounts.counts.unknown };
  const classCounts = classificationCounts(visible);
  const running = runningCounts(groups.running);
  const weekStart = explicitWeek ?? todayWeekStart(timeZone, DETAIL_NOW);
  const range = weekQueryRange(timeZone, weekStart);
  const calendarIds = new Set(classified.map((row) => row.session_id));
  const calendarData = readCalendar(INTEGRATED_CALENDAR.filter((session) => calendarIds.has(session.session_id)).map((session) => {
    const fixture = fixtureById.get(session.session_id);
    const detail = fixture ? calendarOfDetail(fixture) : null;
    return detail ? { ...session, requests: detail.requests, cost: detail.cost, colors: { ...session.colors, status: detail.colors.status, ...(detail.colors.components.length > 0 ? { components: detail.colors.components } : {}), ...(detail.colors.screens.length > 0 ? { screens: detail.colors.screens } : {}), ...(detail.colors.kinds.length > 0 ? { kinds: detail.colors.kinds } : {}) } } : session;
  }), range.from, range.to, role);
  const summaryKey = `${timeZone}|${range.from}|${range.to}|${role}|${[...calendarIds].sort().join(',')}`;
  const summary = summaryState.key === summaryKey ? summaryState.value : { state: 'stale' as const };
  const regenerateRecord = () => {
    if (!selected || !analysis || !isFullAnalysis(analysis) || !isOwner) return;
    const sessionId = selected.id;
    const current = analysis.record;
    if (current?.state === 'redacted' || current?.state === 'withheld') return;
    setRecords((previous) => ({ ...previous, [sessionId]: { state: 'generating', generated_at: current?.generated_at ?? null, covers_until: current?.covers_until ?? null } }));
    const pending = recordTimers.current.get(sessionId);
    if (pending) clearTimeout(pending);
    recordTimers.current.set(sessionId, setTimeout(() => {
      const record = generateDemoRecord(analysis, selectedTitle, DETAIL_NOW);
      setRecords((previous) => ({ ...previous, [sessionId]: record }));
      recordTimers.current.delete(sessionId);
    }, 700));
  };
  const regenerate = () => {
    setSummaryState({ key: summaryKey, value: { state: 'writing' } });
    if (summaryTimer.current) clearTimeout(summaryTimer.current);
    const targets = fixtures.filter((fixture) => {
      if (!calendarIds.has(fixture.id) || !calendarData.sessions.some((session) => session.session_id === fixture.id)) return false;
      const analysis = fixture.analysis;
      return analysis && isFullAnalysis(analysis) && analysis.record?.state !== 'ready' && analysis.record?.state !== 'redacted' && analysis.record?.state !== 'withheld';
    });
    setRecords((previous) => ({ ...previous, ...Object.fromEntries(targets.map((fixture) => [fixture.id, { state: 'generating', generated_at: null, covers_until: null }])) }));
    summaryTimer.current = setTimeout(() => {
      const generated = targets.flatMap((fixture) => fixture.analysis && isFullAnalysis(fixture.analysis) ? [[fixture.id, generateDemoRecord(fixture.analysis, titleOf(fixture, role), DETAIL_NOW)] as const] : []);
      setRecords((previous) => ({ ...previous, ...Object.fromEntries(generated) }));
      setSummaryState({ key: summaryKey, value: { state: 'fresh', written: generated.length } });
    }, 700);
  };
  useEffect(() => () => {
    if (saveTimer.current) clearTimeout(saveTimer.current);
    if (summaryTimer.current) clearTimeout(summaryTimer.current);
    for (const timer of recordTimers.current.values()) clearTimeout(timer);
  }, []);

  return (
    <TooltipProvider delayDuration={200}>
      <div className="flex h-screen min-w-0 flex-col bg-canvas">
        {/* 미리보기 안내: 한 줄, 늘 보인다. 오른쪽에 보는 사람과 액센트 전환이 붙는다. */}
        <div role="note" className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-b border-warning/30 bg-warning-soft px-3 py-1 text-[12px] text-warning-strong">
          <FlaskConical size={14} strokeWidth={1.75} className="shrink-0" aria-hidden="true" />
          <p className="min-w-0 flex-1 basis-full truncate sm:basis-auto">
            <b className="font-semibold">미리보기</b> · 합성 데이터로 그린 세션 분석 화면입니다. 서버에 연결하지 않았고 실제 세션과 무관합니다.
          </p>
          <Segmented label="보는 사람" value={role} onChange={setRole} options={ROLE_OPTIONS} />
          <Segmented label="액센트" value={accent} onChange={setAccent} options={ACCENT_OPTIONS} />
        </div>

        <FrameTopBar
          frame={frame}
          now={DETAIL_NOW}
          onSelectAccount={setAccount}
          query={query}
          onQueryChange={setQuery}
          resultCount={hits ? hits.length : null}
          leading={<div className="flex shrink-0 items-center gap-3"><span className="text-[12px] font-semibold whitespace-nowrap text-ink">cctrace · 세션</span><nav aria-label="제품 화면" className="hidden items-center gap-2 text-[11px] text-ink-3 xl:flex"><Link href="/" className="hover:text-ink">대시보드</Link><Link href="/sessions/" className="hover:text-ink">세션</Link><Link href="/rules/" className="hover:text-ink">규칙</Link><Link href="/settings/" className="hover:text-ink">설정</Link><Link href="/versions/" className="hover:text-ink" title="이 미리보기는 서버에 연결하지 않아 실행 서버 버전은 알 수 없습니다.">버전 미연결</Link></nav></div>}
        />

        <div className="shrink-0 border-b border-border bg-surface px-3 py-1.5">
          <SessionToolbar view={view} onViewChange={setView} total={searched.length} statusCounts={toolbarCounts} statusFilter={statusFilter} onStatusFilterChange={setStatusFilter} statusUnavailable={!isOwner} scopeLabel="모든 프로젝트 · 합성 재조회" readAt={formatClockIn(timeZone, DETAIL_NOW + reloads * 60_000)} requestlessCount={requestless} showRequestless={showRequestless} onShowRequestlessChange={setShowRequestless} planFileOnly={planFileOnly} onPlanFileOnlyChange={setPlanFileOnly} sort={sort} onSortChange={setSort} onReload={() => setReloads((count) => count + 1)} />
        </div>
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden lg:flex-row">
          {view === 'calendar' ? (
            <aside aria-label="주간 세션 캘린더" className={cn('flex min-h-[260px] flex-col border-r border-border lg:min-h-0', timelineOpen ? 'lg:w-[25%]' : 'lg:w-[46%]')}>
              <div className="flex shrink-0 items-center justify-between border-b border-border px-3 py-1 text-[11px] text-ink-3">
                <label>시간대 <select aria-label="캘린더 시간대" value={timeZone} onChange={(event) => { setTimeZone(event.target.value); setExplicitWeek(null); }} className="rounded border border-border bg-surface px-1 text-ink"><option value="Asia/Seoul">서울</option><option value="America/New_York">뉴욕</option></select></label>
                <div className="flex items-center gap-1">
                  {DST_JUMPS.map((jump) => <button key={jump.key} type="button" onClick={() => { setTimeZone(jump.timeZone); setExplicitWeek(jump.weekStart); }} className="rounded border border-border px-1 py-px text-ink-2 hover:bg-surface-sunk">{jump.key === 'spring' ? '봄 23시간' : '가을 25시간'}</button>)}
                  <span>합성 데이터</span>
                </div>
              </div>
              <SessionCalendar timeZone={timeZone} weekStart={weekStart} nowMs={DETAIL_NOW} data={calendarData} onWeekChange={setExplicitWeek} pxPerHour={pxPerHour} onPxPerHourChange={setPxPerHour} colorKey={colorKey} onColorKeyChange={setColorKey} selectedId={selectedId} onSelect={handleOpenSession} onReload={() => setReloads((count) => count + 1)} summary={summary} onRegenerateSummaries={isOwner ? regenerate : undefined} />
            </aside>
          ) : (
          <aside aria-label="세션 목록" className="flex min-h-[240px] shrink-0 flex-col border-r border-border bg-surface lg:min-h-0 lg:w-[280px] xl:w-[300px]">
            <div className="shrink-0 space-y-1.5 border-b border-border px-3 pt-2">
              <div className="flex items-center justify-between gap-2 pb-1.5">
                <BucketCountChips counts={counts} value={isOwner && statusFilter !== 'unknown' ? (statusFilter === 'midway' ? 'ended_midway' : statusFilter) : null} onChange={handleBucket} unavailable={!isOwner} />
              </div>
              <ClassificationTabs
                value={classification}
                counts={classCounts}
                total={visible.length}
                onChange={setClassification}
                panelId={LIST_PANEL_ID}
                unavailable={!isOwner}
              />
            </div>

            <div ref={listRef} id={LIST_PANEL_ID} role="list" aria-label="세션 카드" className="min-h-0 flex-1 overflow-y-auto">
              {shownCount === 0 ? (
                <div className="px-4 py-8 text-center text-[12px] text-ink-3">조건에 맞는 세션이 없습니다</div>
              ) : (
                <>
                  {groups.running.length > 0 && (
                    <section aria-label="실행 중인 세션">
                      <RunningGroupHeader counts={running} />
                      {groups.running.map((s) => (
                        <div role="listitem" key={s.session_id}>
                          <SessionAnalysisRow session={s} selected={s.session_id === selectedId} onSelect={handleSelect} now={DETAIL_NOW} />
                        </div>
                      ))}
                    </section>
                  )}
                  {groups.ended.map((g) => (
                    <section key={g.key} aria-label={g.label}>
                      <EndedGroupHeader group={g} onDismissAll={handleDismissAll} />
                      {g.rows.map((s) => (
                        <div role="listitem" key={s.session_id}>
                          <SessionAnalysisRow session={s} selected={s.session_id === selectedId} onSelect={handleSelect} now={DETAIL_NOW} onDismiss={handleDismiss} />
                        </div>
                      ))}
                    </section>
                  ))}
                  {rest.map((s) => (
                    <div role="listitem" key={s.session_id}>
                      <SessionAnalysisRow session={s} selected={s.session_id === selectedId} onSelect={handleSelect} now={DETAIL_NOW} />
                    </div>
                  ))}
                </>
              )}
            </div>

            <div className="shrink-0 border-t border-border bg-canvas px-3 py-1 text-[11px] text-ink-3">
              {shownCount.toLocaleString()} / {INTEGRATED_SESSIONS.length.toLocaleString()}개{query.trim() !== '' && ' · 검색 결과'}
            </div>
            <p className="shrink-0 border-t border-border px-3 py-1 text-[10px] text-ink-3">메모·분류 시연값은 새로고침하면 초기화됩니다. 서버에 저장하지 않습니다.</p>
            <SessionNoteEditor
              open={noteOpen}
              onOpenChange={setNoteOpen}
              headerSlot={
                isOwner && selected ? (
                  <ClassificationSelect
                    value={selectedClass}
                    sessionLabel={selected.id.slice(0, 8)}
                    onChange={(next) => setClasses((prev) => ({ ...prev, [selected.id]: next }))}
                  />
                ) : undefined
              }
              tab={noteTab}
              onTabChange={setNoteTab}
              session={selected ? { shortId: selected.id.slice(0, 8), title: selectedTitle } : null}
              sessionNote={selected ? (notes[selected.id] ?? '') : ''}
              onSessionNoteChange={handleSessionNote}
              globalNote={globalNote}
              onGlobalNoteChange={handleGlobalNote}
              saveState={saveState}
              notes={noteList}
              onPickSession={handleOpenSession}
              unavailable={!isOwner}
            />
          </aside>
          )}

          {/* 상세 */}
          <main className="relative flex min-h-0 min-w-0 flex-1 flex-col bg-canvas">
            {selected && (!calendarIds.has(selected.id) || (view === 'calendar' && !calendarData.sessions.some((session) => session.session_id === selected.id))) && <p role="status" className="shrink-0 border-b border-warning/30 bg-warning-soft px-3 py-1 text-[11px] text-warning-strong">선택한 세션은 현재 필터 또는 표시 주 밖에 있습니다. 상세 선택은 유지했습니다.</p>}
            {selected ? (
              <>
                <div className="flex shrink-0 items-center gap-2 border-b border-border bg-surface px-3 py-1">
                  <p className="min-w-0 flex-1 truncate text-[11px] text-ink-3" title={`${selected.label}\n\n${selected.covers.join(' · ')}`}>
                    <span className="font-medium text-ink-2">픽스처</span> · {selected.label}
                  </p>
                  <span
                    className={cn(
                      'inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-medium',
                      isOwner ? 'bg-surface-sunk text-ink-3' : 'bg-warning-soft text-warning-strong',
                    )}
                  >
                    {isOwner ? <UserRound size={10} aria-hidden="true" /> : <ShieldCheck size={10} aria-hidden="true" />}
                    {isOwner ? '세션 주인으로 봄' : '주인이 아닌 관리자로 봄'}
                  </span>
                </div>
                {analysis ? (
                  <SessionAnalysisTab
                    key={`${selected.id}:${role}`}
                    analysis={analysis}
                    now={DETAIL_NOW}
                    timeZone={timeZone}
                    onOpenRequest={handleOpenRequest}
                    onOpenSession={handleOpenSession}
                    onRegenerateRecord={isOwner ? regenerateRecord : undefined}
                  />
                ) : (
                  <div className="flex-1 overflow-y-auto bg-canvas p-4">
                    <AnalysisUnavailable reason="mixed_identity" />
                  </div>
                )}
              </>
            ) : (
              <div className="flex flex-1 items-center justify-center text-sm text-ink-3">세션을 고르세요</div>
            )}
          </main>

          {/* 타임라인: 상세 옆 세 번째 칸 */}
          {timelineOpen && selected && (
            <div className="flex min-h-[320px] shrink-0 flex-col border-l border-border bg-surface lg:min-h-0 lg:w-[29%]">
              <TimelinePane
                key={`${selected.id}:${role}`}
                shortId={selected.id.slice(0, 8)}
                title={selectedTitle}
                messages={timeline}
                now={DETAIL_NOW}
                timeZone={timeZone}
                focus={focus}
                onClose={() => setTimelineOpen(false)}
              />
            </div>
          )}
        </div>

        <FrameStatusBar repo={frame.repo} />
      </div>
    </TooltipProvider>
  );
};

export { DetailPreview };
