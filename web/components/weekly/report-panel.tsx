'use client';

import { useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertCircle } from 'lucide-react';
import { CollectingLoader } from '@/components/common/collecting-loader';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog';
import { Skeleton } from '@/components/ui/skeleton';
import { AIReportRequestError, cancelAIReportRun, fetchAIReports, openAIReportRunEvents, startAIReport } from '@/lib/api';
import { aiReportPollInterval, aiReportQueryKey, reportPanelState } from '@/lib/ai-report-state';
import type { ReportPanelState } from '@/lib/ai-report-state';
import { consumeAIReportStream, mergeToolCallEvent } from '@/lib/ai-report-stream';
import type { AIReportStreamEvent, AIReportStreamOutcome } from '@/lib/ai-report-stream';
import type { AIReport, AIReportRun, AIReportsResponse } from '@/lib/types';
import { AnalysisProcess, ToolCallList } from './analysis-process';
import { ConsentDialog } from './consent-dialog';
import { formatDuration, GenerationInfo } from './generation-info';
import { ReportItems } from './report-items';
import { reportCardClass, ReportSummary } from './report-summary';
import { ScheduleLine } from './schedule-line';
import { ScheduleSettingsDialog } from './schedule-settings-dialog';

const numberFormat = new Intl.NumberFormat('en-US');

type AIReportReadState = 'loading' | 'failed' | 'read';

const START_ERROR_LABELS: Record<string, string> = {
  invalid_week: '잘못된 주입니다',
  future_week: '아직 시작하지 않은 주입니다',
  consent_required: '데이터 전송 동의가 필요합니다',
  already_running: '이미 생성 중인 리포트가 있습니다',
  no_records: '이 주에는 작업 구간이 없습니다',
  runtime_disabled: 'AI 리포트가 꺼져 있습니다',
  runtime_unconfigured: 'AI 런타임이 설정되지 않았습니다',
  runtime_unavailable: 'AI 런타임에 연결할 수 없습니다',
  runtime_account_changing: 'AI 연결 계정을 바꾸는 중이라 리포트를 생성할 수 없습니다',
};

const startErrorLabel = (code: string): string => START_ERROR_LABELS[code] ?? `요청이 거부되었습니다 (${code})`;

/** A refused start, a refused stop and a dropped connection each say their own
 *  thing; none of them borrows another's words. */
const actionErrorLabel = (error: Error | null, action: 'start' | 'cancel'): string | null => {
  if (!error) return null;
  if (!(error instanceof AIReportRequestError)) return '서버에 연결하지 못했습니다';
  return action === 'cancel' ? `중지하지 못했습니다 (${error.code})` : startErrorLabel(error.code);
};

/** The screen names a failure by its code; the server's message text is never
 *  shown, so runtime output cannot reach the page. */

const runFailureReason = (run: AIReportRun | null): string => {
  if (!run) return '원인 미확인';
  const err = run.error;
  if (!err) return '원인 미확인';
  // The server already answers with the reader's message for every code it
  // knows, and a general one for the rest. A second map here only drifted:
  // not_configured had a message on the server and none here, empty_report
  // the other way round, and fifteen codes had one in neither.
  return err.message || `오류 코드 ${err.code}`;
};

/** A start refused for consent opens the dialog even when the screen still
 *  believed consent was given. */
const opensConsent = (error: Error): boolean =>
  error instanceof AIReportRequestError && error.code === 'consent_required';

const primaryButtonClass = 'bg-brand text-brand-ink hover:bg-brand-hover';

interface StatusCardProps {
  state: ReportPanelState;
  title: string;
  children: ReactNode;
}

/** Holds the report's place at the report's width, never shrunk to a notice
 *  line (spec §3.4). */
const StatusCard = ({ state, title, children }: StatusCardProps) => (
  <section className={reportCardClass} data-state={state}>
    <h3 className="text-[17px] font-semibold tracking-[-0.02em] text-ink">{title}</h3>
    <div className="mt-3 space-y-2 text-[13px] text-ink-2">{children}</div>
  </section>
);

/** Elapsed time in the words the finished report uses for its duration, so the
 *  number watched climbing is the number left behind. Empty without a start
 *  time, and never negative: the browser clock is not the server's, and a few
 *  seconds of skew must not print a run that has not started. */
const elapsedLabel = (startedAt: string, now: number): string => {
  if (!startedAt) return '';
  const started = Date.parse(startedAt);
  if (Number.isNaN(started)) return '';
  return formatDuration(Math.max(0, now - started));
};

interface ElapsedTimeProps {
  startedAt: string;
}

/** The one line on the running card that changes on its own. Split out so the
 *  panel around it stays a plain render and only this subtree re-renders each
 *  second. */
const ElapsedTime = ({ startedAt }: ElapsedTimeProps) => {
  const [now, setNow] = useState(() => Date.now());
  const label = elapsedLabel(startedAt, now);

  /** Ticks the displayed elapsed time once a second while the card is mounted.
   *  The card unmounts when the run leaves 'running', which clears it. */
  useEffect(() => {
    if (!startedAt) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [startedAt]);

  if (!label) return null;
  return <p className="tabular-nums text-ink-3">경과 {label}</p>;
};

const PAGE_PURPOSE = '이 자리에는 AI 가 이 주의 작업 구간을 읽고 쓴 요약과 다시 볼 작업이 표시됩니다.';

interface ReportPanelBodyProps {
  data: AIReportsResponse | undefined;
  readState: AIReportReadState;
  timeZone: string;
  processOpen: boolean;
  onToggleProcess: () => void;
  /** Starts analysis; before consent the container opens the consent dialog. */
  onStart: () => void;
  onRegenerate: () => void;
  onCancel: () => void;
  /** Opens the dialog for this user's own run time. */
  onOpenSettings: () => void;
  pending: boolean;
  actionError: string | null;
}

interface CompletedReportProps {
  report: AIReport;
  props: ReportPanelBodyProps;
  inProgress: boolean;
}

const CompletedReport = ({ report, props, inProgress }: CompletedReportProps) => (
  <>
    <ReportSummary
      summary={report.summary}
      action={
        /* xs, not sm: this sits in the summary card's header beside an 11px
           tag, where a 32px tall button with 14px text outweighs both the tag
           and the heading it follows. */
        <Button type="button" variant="outline" size="xs" onClick={props.onRegenerate} disabled={props.pending}>
          재분석
        </Button>
      }
    />
    <ReportItems items={report.items} timeZone={props.timeZone} />
    <AnalysisProcess
      toolCalls={report.process.tool_calls}
      segmentsRead={report.process.segments_read}
      open={props.processOpen}
      onToggle={props.onToggleProcess}
    />
    <GenerationInfo report={report} inProgress={inProgress} timeZone={props.timeZone} />
  </>
);

interface StartButtonProps {
  label: string;
  onClick: () => void;
  disabled: boolean;
}

const StartButton = ({ label, onClick, disabled }: StartButtonProps) => (
  <Button type="button" size="sm" className={primaryButtonClass} onClick={onClick} disabled={disabled}>
    {label}
  </Button>
);

const panelContent = (data: AIReportsResponse, props: ReportPanelBodyProps) => {
  const view = reportPanelState(data);
  switch (view.state) {
    case 'disabled':
      return (
        <StatusCard state={view.state} title="주간 리포트">
          <p>{PAGE_PURPOSE}</p>
          <p className="text-ink-3">AI 리포트가 꺼져 있습니다. 관리자에게 요청하세요</p>
        </StatusCard>
      );
    case 'unconfigured':
      return (
        <StatusCard state={view.state} title="주간 리포트">
          <p>{PAGE_PURPOSE}</p>
          <p className="text-ink-3">AI 런타임이 설정되지 않았습니다. 관리자에게 요청하세요</p>
        </StatusCard>
      );
    case 'unavailable':
      return (
        <StatusCard state={view.state} title="주간 리포트">
          <p>{PAGE_PURPOSE}</p>
          <p className="text-ink-3">AI 런타임에 연결할 수 없습니다 · 원인은 관리자 화면에서 확인할 수 있습니다</p>
        </StatusCard>
      );
    case 'consent_required':
      return (
        <StatusCard state={view.state} title="주간 리포트">
          <p>{PAGE_PURPOSE}</p>
          <p className="text-ink-3">분석을 시작하면 외부로 보내는 데이터를 먼저 확인하고 동의합니다.</p>
          <StartButton label="분석 시작" onClick={props.onStart} disabled={props.pending} />
        </StatusCard>
      );
    case 'no_records':
      return (
        <StatusCard state={view.state} title="주간 리포트">
          <p>{PAGE_PURPOSE}</p>
          <p className="text-ink-3">이 주에는 작업 구간이 없어 분석할 대상이 없습니다</p>
          <StartButton label="분석 시작" onClick={props.onStart} disabled />
        </StatusCard>
      );
    case 'not_generated':
      return (
        <StatusCard state={view.state} title="주간 리포트">
          <p>{PAGE_PURPOSE}</p>
          <p className="tabular-nums text-ink-3">구간 {numberFormat.format(data.segment_count)}개 중 일부를 읽음</p>
          <StartButton label="분석 시작" onClick={props.onStart} disabled={props.pending} />
        </StatusCard>
      );
    case 'running': {
      const calls = data.run?.tool_calls ?? [];
      return (
        <StatusCard state={view.state} title="주간 리포트 생성 중">
          <CollectingLoader compact label={calls.length === 0 ? '분석 준비 중' : '분석 중'} className="justify-start" />
          <ElapsedTime startedAt={data.run?.started_at ?? ''} />
          {calls.length > 0 && <ToolCallList toolCalls={calls} />}
          <Button type="button" variant="outline" size="sm" onClick={props.onCancel} disabled={props.pending}>
            중지
          </Button>
        </StatusCard>
      );
    }
    case 'failed':
    case 'completed':
      if (!view.report) {
        return (
          <StatusCard state={view.state} title="주간 리포트">
            <p className="text-danger">생성 실패 · {runFailureReason(data.run)}</p>
            <StartButton label="다시 시도" onClick={props.onStart} disabled={props.pending} />
          </StatusCard>
        );
      }
      return (
        <div className="space-y-4" data-state={view.state}>
          {view.state === 'failed' && (
            <p className="flex items-center gap-2 rounded-[var(--r-md)] border border-danger/30 bg-danger-soft px-4 py-3 text-[13px] text-danger-strong">
              <AlertCircle size={16} /> 마지막 생성이 실패했습니다 · {runFailureReason(data.run)} · 아래는 이전 리포트입니다
            </p>
          )}
          <CompletedReport report={view.report} props={props} inProgress={data.in_progress} />
        </div>
      );
  }
};

/** The report's place: skeleton while unread, then exactly one §3.4 state. */
const ReportPanelBody = (props: ReportPanelBodyProps) => {
  const { data, readState, actionError } = props;

  return (
    <div className="space-y-3">
      {readState === 'loading' && (
        <section className={reportCardClass} aria-busy="true">
          <p className="text-[13px] text-ink-3">불러오는 중</p>
          <Skeleton className="mt-3 h-16 w-full" />
        </section>
      )}
      {readState === 'failed' && (
        <section className={reportCardClass}>
          <p className="text-[13px] text-danger">리포트 상태를 불러오지 못했습니다</p>
        </section>
      )}
      {readState === 'read' && data && panelContent(data, props)}
      {/* Outside the state switch: a user wants to know a report is coming
          whether or not one exists yet. Hidden when AI reports are switched off
          entirely, since nothing runs by itself then either. */}
      {readState === 'read' && data?.runtime.enabled && (
        <ScheduleLine schedule={data.schedule} timeZone={props.timeZone} onOpenSettings={props.onOpenSettings} />
      )}
      {actionError && <p className="text-[12.5px] text-danger">{actionError}</p>}
    </div>
  );
};

interface RegenerateConfirmDialogProps {
  onConfirm: () => void;
  onCancel: () => void;
}

const RegenerateConfirmDialog = ({ onConfirm, onCancel }: RegenerateConfirmDialogProps) => {
  const handleOpenChange = (open: boolean) => {
    if (!open) onCancel();
  };

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogContent showCloseButton={false} className="block max-w-[420px] space-y-4 border-border bg-surface">
        <DialogTitle className="text-[15px] font-semibold text-ink">리포트 다시 생성</DialogTitle>
        <p className="text-[13px] text-ink-2">현재 리포트가 대체됩니다</p>
        <footer className="flex justify-end gap-2">
          <Button type="button" variant="outline" size="sm" onClick={onCancel}>
            취소
          </Button>
          <Button type="button" size="sm" className={primaryButtonClass} onClick={onConfirm}>
            다시 생성
          </Button>
        </footer>
      </DialogContent>
    </Dialog>
  );
};

interface ReportPanelProps {
  weekId: string;
  timeZone: string;
  userId: number | null;
}

const ReportPanel = ({ weekId, timeZone, userId }: ReportPanelProps) => {
  const queryClient = useQueryClient();
  const queryKey = aiReportQueryKey(userId, weekId, timeZone);
  const [streamFailedRunId, setStreamFailedRunId] = useState<number | null>(null);
  const [processOpen, setProcessOpen] = useState(false);
  const [consentOpen, setConsentOpen] = useState(false);
  const [confirmingRegenerate, setConfirmingRegenerate] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);

  const handleOpenSettings = () => setSettingsOpen(true);
  const handleCloseSettings = () => setSettingsOpen(false);
  /** Invalidates through queryClient rather than the invalidate helper below:
   *  that helper is declared after this point, so calling it here would read a
   *  const in its temporal dead zone. */
  const handleSavedSettings = () => {
    setSettingsOpen(false);
    void queryClient.invalidateQueries({ queryKey: ['ai-report', userId] });
  };

  const { data, isError } = useQuery<AIReportsResponse>({
    queryKey,
    queryFn: () => fetchAIReports(weekId, timeZone),
    enabled: userId !== null,
    refetchInterval: (query) => aiReportPollInterval(query.state.data, streamFailedRunId),
  });

  // By the user prefix: a request that settles after the reader moved to another
  // week must still refresh the week it was sent for.
  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['ai-report', userId] });
  const handleStartError = (error: Error) => {
    if (opensConsent(error)) setConsentOpen(true);
  };
  const start = useMutation({
    mutationFn: () => startAIReport({ week: weekId, tz: timeZone }),
    onError: handleStartError,
    onSettled: invalidate,
  });
  const cancel = useMutation({ mutationFn: (runId: number) => cancelAIReportRun(runId), onSettled: invalidate });

  const readState: AIReportReadState = data ? 'read' : isError ? 'failed' : 'loading';
  const state = data ? reportPanelState(data).state : null;
  const runningRunId = data?.run?.status === 'running' ? data.run.id : null;

  const handleStart = () => {
    if (state === 'consent_required') {
      setConsentOpen(true);
      return;
    }
    start.mutate();
  };
  const handleToggleProcess = () => setProcessOpen((open) => !open);
  const handleRegenerate = () => setConfirmingRegenerate(true);
  const handleCancelRegenerate = () => setConfirmingRegenerate(false);
  const handleConfirmRegenerate = () => {
    setConfirmingRegenerate(false);
    start.mutate();
  };
  const handleCancelRun = () => {
    if (runningRunId !== null) cancel.mutate(runningRunId);
  };
  const handleCloseConsent = () => setConsentOpen(false);
  const handleAgreed = () => {
    setConsentOpen(false);
    start.mutate();
  };

  /** Follows the running run's progress stream. Tool events are folded into the
   *  cached response so the list keeps one row per seq; a terminal status
   *  refetches the report. When the stream cannot be read the run is handed to
   *  POLL_LIVE polling until it stops running (spec §3.6). Leaving the page or
   *  the run changing aborts the stream; the run itself continues on the server. */
  useEffect(() => {
    if (runningRunId === null) return;
    const key = aiReportQueryKey(userId, weekId, timeZone);
    const controller = new AbortController();

    const handleEvent = (event: AIReportStreamEvent) => {
      if (event.type !== 'tool_call' && event.type !== 'tool_result') return;
      queryClient.setQueryData<AIReportsResponse>(key, (current) => {
        if (!current?.run || current.run.id !== runningRunId) return current;
        return { ...current, run: { ...current.run, tool_calls: mergeToolCallEvent(current.run.tool_calls, event) } };
      });
    };

    const follow = async () => {
      const outcome: AIReportStreamOutcome = await openAIReportRunEvents(runningRunId, controller.signal)
        .then((res) => consumeAIReportStream(res, handleEvent, controller.signal))
        .catch(() => (controller.signal.aborted ? 'aborted' : 'fallback'));
      if (outcome === 'aborted' || controller.signal.aborted) return;
      // Also after a terminal status: should the refetch still read the run as
      // running, polling carries it to its end instead of the screen stalling.
      setStreamFailedRunId(runningRunId);
      void queryClient.invalidateQueries({ queryKey: key });
    };

    void follow();
    return () => controller.abort();
  }, [runningRunId, queryClient, userId, weekId, timeZone]);

  return (
    <>
      <ReportPanelBody
        data={data}
        readState={readState}
        timeZone={timeZone}
        processOpen={processOpen}
        onToggleProcess={handleToggleProcess}
        onStart={handleStart}
        onRegenerate={handleRegenerate}
        onCancel={handleCancelRun}
        onOpenSettings={handleOpenSettings}
        pending={start.isPending || cancel.isPending}
        actionError={actionErrorLabel(start.error, 'start') ?? actionErrorLabel(cancel.error, 'cancel')}
      />
      {settingsOpen && data && (
        <ScheduleSettingsDialog
          schedule={data.schedule}
          timeZone={timeZone}
          onClose={handleCloseSettings}
          onSaved={handleSavedSettings}
        />
      )}
      {consentOpen && <ConsentDialog userId={userId} onCancel={handleCloseConsent} onAgreed={handleAgreed} />}
      {confirmingRegenerate && (
        <RegenerateConfirmDialog onConfirm={handleConfirmRegenerate} onCancel={handleCancelRegenerate} />
      )}
    </>
  );
};

export { actionErrorLabel, elapsedLabel, opensConsent, ReportPanel, ReportPanelBody, runFailureReason, startErrorLabel };
export type { AIReportReadState, ReportPanelBodyProps };
