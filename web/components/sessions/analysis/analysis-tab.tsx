'use client';

import { useId, useState, type ReactNode } from 'react';
import { CatchingUpNote } from '@/components/common/catching-up-note';
import { TooltipProvider } from '@/components/ui/tooltip';
import type { AnalysisTurn, SessionAnalysis } from '@/lib/session-analysis-types';
import { DetailsStrip } from './details-strip';
import { isFullAnalysis } from './guards';
import { OutcomesPanel } from './outcomes-panel';
import { OverviewPanel } from './overview-panel';
import { PlansPanel } from './plans-panel';
import { hasRecordContent, recordNotice } from './record';
import { SessionHead } from './session-head';
import { StatusPanel } from './status-panel';
import { CopiesBanner, PendingNotice } from './state-notices';
import { TurnsPanel } from './turns-panel';
import { WorkPanel } from './work-panel';
import { SessionRecordControls } from './session-record-controls';

/**
 * 세션 상세(분석) 패널. 응답(`SessionAnalysis`)을 받아 그리기만 한다 — 아무것도 가져오지 않는다.
 *
 * 위에서 아래로 레퍼런스의 순서를 따른다:
 *   머리(두 줄, 위에 붙어 있다) → [세부 띠] → 세션 상태 → 세션 성과 → 세션 개요 → 작업 상황 → 턴 상세 → 플랜
 * 턴 줄과 작업 항목 줄은 `onOpenRequest(turn)`을 불러 워터폴과 선택 원문을 같은 턴으로 옮긴다.
 *
 * 응답의 `state`마다 화면이 있다.
 *   ready      전부 보인다
 *   stale      전부 보이고, 위에 "분석 규칙 갱신 중"이 붙는다
 *   building   만드는 중 안내 + 롤업 머리(있는 만큼)
 *   truncated  합계만 안내 + 롤업 머리
 *   unsupported / none   안내
 * 비소유 관리자(`owner_view = false`)에게는 세션 상태 패널을 표시하지 않는다.
 * 판정·본문·성과·작업 항목이 없으면 빈 패널을 만들지 않는다. 저장소의 규칙 판 목록은 세션 주입의 근거가 아니므로 표시하지 않는다.
 */

interface SessionAnalysisTabProps {
  analysis: SessionAnalysis;
  /** Embedded workspace inserts its independently loaded execution trace after identity and metrics. */
  executionFlow?: ReactNode;
  selectedTurnKey?: string;
  /** 상대 시각("3분 전")의 기준. 호출하는 쪽이 정한다(렌더 중에 시계를 읽지 않는다). */
  now: number;
  timeZone?: string;
  /** 턴 줄이나 작업 항목 줄을 눌렀다. 선택을 그 턴으로 옮기는 일은 호출하는 쪽의 몫이다(`turn.anchor_ts`, `turn.key`). */
  onOpenRequest: (turn: AnalysisTurn) => void;
  /** 턴 버튼의 이동 대상. 기본값은 워터폴이다. */
  openTarget?: 'conversation' | 'waterfall';
  /** 복사 원본 세션으로 옮긴다. */
  onOpenSession?: (sessionId: string) => void;
  /** 있으면 세션 기록이 낡았거나 없을 때 "다시 만들기"를 보인다. */
  onRegenerateRecord?: () => void;
  recordActionPending?: boolean;
  recordActionAccepted?: boolean;
  recordActionAcceptedMessage?: string | null;
  recordActionError?: string | null;
  /** Mounts live consent/delete controls only in the authenticated product viewer. */
  recordControlsEnabled?: boolean;
}

const AnalysisBody = ({ analysis, executionFlow, selectedTurnKey, now, timeZone, onOpenRequest, openTarget, onOpenSession, onRegenerateRecord, recordActionPending, recordActionAccepted, recordActionAcceptedMessage, recordActionError, recordControlsEnabled = false }: SessionAnalysisTabProps) => {
  const uid = useId();
  const sid = (name: string) => `${uid}-${name}`;
  const [detailsOpen, setDetailsOpen] = useState(false);
  const toggleDetails = () => setDetailsOpen(value => !value);
  const full = isFullAnalysis(analysis) ? analysis : undefined;
  const record = full?.record;
  const content = hasRecordContent(record) ? record : undefined;
  const hasOverview = content
    ? Boolean(content.headline.trim() || content.bullets.length || content.review.length || Object.values(content.tags).some((tags) => tags.length > 0))
    : Boolean(full?.recaps.items.some((recap) => recap.text.trim()) && (!record || recordNotice(record.state, record.reason)?.recapFallback));
  const showRecordAction = !hasOverview && onRegenerateRecord && record && ['none', 'missing', 'failed', 'generating'].includes(record.state);
  const hasOutcomes = Boolean(full && (full.tasks?.items.length || full.commits?.items.length ||
    full.commits?.state === 'observed' && (full.commits.total ?? 0) > 0 || full.outputs.edited_files.items.length ||
    (full.outputs.edited_files.active_total ?? 0) > 0 || full.outputs.prs.items.length ||
    (full.outputs.lines?.added ?? 0) > 0 || (full.outputs.lines?.removed ?? 0) > 0));

  return (
    <div
      role="region"
      aria-label="세션 분석"
      tabIndex={0}
      className="min-h-0 flex-1 overflow-y-auto focus-visible:-outline-offset-2!"
    >
      {isFullAnalysis(analysis) ? (
        <>
          <div className="sticky top-0 z-20">
            <SessionHead
              shortId={analysis.short_id}
              state={analysis.state}
              header={analysis.header}
              judgement={analysis.judgement}
              ownerView={analysis.owner_view}
              now={now}
              timeZone={timeZone}
              detailsToggle={{ open: detailsOpen, onToggle: toggleDetails, controls: sid('details') }}
            />
          </div>
          {detailsOpen && (
            <DetailsStrip
              id={sid('details')}
              header={analysis.header}
              ownerView={analysis.owner_view}
              now={now}
              timeZone={timeZone}
              live={analysis.live}
              identities={analysis.identities}
              computedAt={analysis.computed_at}
              pendingSince={analysis.pending_since}
            />
          )}
          <div className="space-y-1.5 p-1.5 pb-4 @md:p-2 @md:pb-5">
            {executionFlow}
            {analysis.state === 'stale' && (
              <CatchingUpNote
                pending
                message="분석 규칙 갱신 중 — 아래 값은 한 단계 이전 규칙으로 계산돼 있어 곧 바뀔 수 있습니다."
              />
            )}
            {analysis.copies && <CopiesBanner copies={analysis.copies} onOpenSession={onOpenSession} />}
            {analysis.owner_view && analysis.judgement?.checks.some((check) => check.state !== 'undeterminable') && <StatusPanel id={sid('status')} analysis={analysis} now={now} />}
            {hasOutcomes && <OutcomesPanel
              id={sid('outcomes')}
              outputs={analysis.outputs}
              tasks={analysis.tasks}
              commits={analysis.commits}
              turns={[...analysis.turns, ...analysis.abandoned]}
              agent={analysis.header.agent}
              now={now}
            />}
            {hasOverview && <OverviewPanel
              id={sid('overview')}
              record={analysis.record}
              recaps={analysis.recaps}
              recapsTruncated={analysis.truncated.recaps}
              bodiesTruncated={analysis.truncated.bodies}
              agent={analysis.header.agent}
              rulesFiles={[]}
              now={now}
              turns={[...analysis.turns, ...analysis.abandoned]}
              onOpenRequest={onOpenRequest}
              onRegenerate={onRegenerateRecord}
              recordActionPending={recordActionPending}
              recordActionAccepted={recordActionAccepted}
              recordActionAcceptedMessage={recordActionAcceptedMessage}
              recordActionError={recordActionError}
            />}
            {content && content.work.length > 0 && <WorkPanel id={sid('work')} record={analysis.record} turns={[...analysis.turns, ...analysis.abandoned]} onOpenRequest={onOpenRequest} openTarget={openTarget} />}
            {showRecordAction && (
              <div data-record-state={record.state} className="flex flex-wrap items-center gap-2 px-1 py-1 text-[11px] text-ink-3">
                <span>세션 요약</span>
                <button type="button" onClick={onRegenerateRecord} disabled={recordActionPending || record.state === 'generating'} className="rounded border border-border px-1.5 py-0.5 text-ink-2 hover:bg-surface-sunk disabled:cursor-not-allowed disabled:opacity-50">
                  {record.state === 'generating' ? '만드는 중' : recordActionPending ? '요청 중' : '다시 만들기'}
                </button>
                {recordActionError && <span role="alert" className="text-danger">{recordActionError}</span>}
                {recordActionAccepted && <span role="status">{recordActionAcceptedMessage ?? '재생성 요청을 접수했습니다.'}</span>}
              </div>
            )}
            {recordControlsEnabled && <SessionRecordControls ownerView={analysis.owner_view} />}
            <TurnsPanel
              id={sid('turns')}
              turns={analysis.turns}
              selectedTurnKey={selectedTurnKey}
              abandoned={analysis.abandoned}
              counts={analysis.header.counts}
              truncated={analysis.truncated}
              subagents={analysis.subagents}
              questions={analysis.questions}
              timeZone={timeZone}
              onOpenRequest={onOpenRequest}
              openTarget={openTarget}
            />
            <PlansPanel
              id={sid('plans')}
              plans={analysis.plans}
              turns={analysis.turns}
              truncated={analysis.truncated.plans}
              bodiesTruncated={analysis.truncated.bodies}
              timeZone={timeZone}
            />
          </div>
        </>
      ) : (
        <>
          <div className="sticky top-0 z-20">
            <SessionHead
              shortId={analysis.short_id}
              state={analysis.state}
              header={analysis.header}
              ownerView={analysis.owner_view}
              now={now}
              timeZone={timeZone}
              notApplicable={analysis.state === 'unsupported'}
            />
          </div>
          <div className="space-y-2 p-2 @md:p-3">
            {executionFlow}
            <PendingNotice state={analysis.state} agent={analysis.header?.agent} />
            {recordControlsEnabled && <SessionRecordControls ownerView={analysis.owner_view} />}
          </div>
          {analysis.header && (
            <DetailsStrip
              id={sid('details')}
              notApplicable={analysis.state === 'unsupported'}
              header={analysis.header}
              ownerView={analysis.owner_view}
              now={now}
              timeZone={timeZone}
              identities={analysis.identities}
              computedAt={analysis.computed_at}
              pendingSince={analysis.state === 'building' ? null : analysis.pending_since}
            />
          )}
        </>
      )}
    </div>
  );
};

const SessionAnalysisTab = (props: SessionAnalysisTabProps) => (
  <TooltipProvider delayDuration={200}>
    <div className="@container flex min-h-0 flex-1 flex-col bg-canvas">
      {/* 세션이 바뀌면 세부 띠, 성과 줄, 더 보기 같은 패널별 상태와 스크롤 위치를 새로 시작한다. */}
      <AnalysisBody key={props.analysis.session_id} {...props} />
    </div>
  </TooltipProvider>
);

export { SessionAnalysisTab };
