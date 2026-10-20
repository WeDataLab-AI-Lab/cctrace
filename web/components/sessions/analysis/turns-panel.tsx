'use client';

import { useState } from 'react';
import { ChevronRight, CircleDashed, FilePenLine, GitPullRequest, ImageIcon } from 'lucide-react';
import { cn } from '@/lib/utils';
import type {
  AnalysisCounts,
  AnalysisQuestion,
  AnalysisSubagents,
  AnalysisTruncated,
  AnalysisTurn,
  DeepPartial,
} from '@/lib/session-analysis-types';
import { Chip, Dot, Panel, type ChipTone } from './atoms';
import { HashChip, TaskChip } from './chips';
import { formatCount, formatTokenCount, formatUsd } from './format';
import { MODE_LABEL, TURN_STATUS_HINT, TURN_STATUS_LABEL, TURN_STATUS_REASON_TEXT, UNKNOWN_TEXT } from './labels';
import {
  editedView,
  orderTurns,
  previewView,
  repeatedTurnTimes,
  spansMultipleDays,
  turnExtraTasks,
  turnKindBadge,
  turnMetrics,
  turnQuestionView,
  turnTimeLabel,
  turnTotals,
  unknownReasons,
} from './requests';

/**
 * "턴 상세 N건"(D16, F8, E5): 한 줄이 한 턴이다. 레퍼런스처럼 한 화면에 열네 줄이 보이게 조밀하다.
 *   첫 줄   "1." 번호 · 시각 · 상태 · (출처·모드·반려·이미지) · 요청 첫 줄 ····· [6.2M tok · $2.26 · ctx 18%]
 *   둘째 줄 "질문: 머리글 / 머리글"
 *   그 아래  그 턴의 커밋마다 한 줄: 해시 칩 · 메시지 · (task 칩)
 * 버려진 분기는 따로 모아 흐리게 보인다. 요청 수와 컨텍스트에서는 빠지지만 비용·토큰은 세션 합계에 남는다.
 * 행을 누르면 부모의 `onOpenRequest(turn)`이 불린다(워터폴이 그 턴으로 옮겨 간다).
 */

const PAGE = 100;

/** 상태 칩. 끝남은 조용한 윤곽선, 예외만 색, 판단 불가는 점선. */
const STATUS_TONE: Record<AnalysisTurn['status'], ChipTone> = {
  ended: 'outline',
  open: 'brand',
  interrupted: 'warning',
  error: 'danger',
  no_response: 'warning',
  undeterminable: 'unknown',
};

const StatusChip = ({ turn }: { turn: AnalysisTurn }) => {
  // 계약에 없는 상태가 와도 빈 칩이 아니라 받은 값 그대로를 보인다.
  const label: string = TURN_STATUS_LABEL[turn.status] ?? String(turn.status);
  const reason = turn.status_reason ? TURN_STATUS_REASON_TEXT[turn.status_reason] : undefined;
  const title = `${label} — ${reason ?? TURN_STATUS_HINT[turn.status] ?? ''}`;
  if (turn.status === 'ended') {
    return (
      <span title={title} className="inline-flex shrink-0 items-center rounded border border-border px-1 text-[10px] leading-[15px] text-ink-3">
        {label}
      </span>
    );
  }
  return (
    <Chip tone={STATUS_TONE[turn.status] ?? 'neutral'} size="sm" title={title}>
      {turn.status === 'open' && <Dot kind="solid-brand" className="size-1.5" />}
      {label}
    </Chip>
  );
};

const modeText = (mode: string): string => MODE_LABEL[mode] ?? mode;

/** 줄 끝의 지표 칩. 모르는 조각은 0이 아니라 점선 밑줄 "?"다. */
const MetricsChip = ({ turn }: { turn: AnalysisTurn }) => {
  const metrics = turnMetrics(turn);
  if (metrics.parts.length === 0) {
    return (
      <span title={metrics.title} className="shrink-0 text-[10.5px] text-ink-4">
        API 호출 없음
      </span>
    );
  }
  return (
    <span
      title={metrics.title}
      className="inline-flex shrink-0 items-center gap-1 rounded-full bg-surface-sunk px-1.5 py-px text-[10.5px] leading-4 whitespace-nowrap tabular-nums text-ink-3"
    >
      {metrics.parts.map((part, i) => (
        <span key={part.key} className="inline-flex items-center gap-1">
          {i > 0 && (
            <span aria-hidden="true" className="text-ink-4">
              ·
            </span>
          )}
          <span className={cn(part.unknown && 'italic underline decoration-dotted decoration-ink-4 underline-offset-2')}>{part.text}</span>
        </span>
      ))}
    </span>
  );
};

/** 번호 칸. 요청이면 "3.", 아니면 점. */
const ordinal = (turn: AnalysisTurn): string => (turn.request_no != null ? `${turn.request_no}.` : '·');

interface RowProps {
  turn: AnalysisTurn;
  selected?: boolean;
  multiDay: boolean;
  timeZone?: string;
  showOffset: boolean;
  abandoned?: boolean;
  /** tool_use_id → 질문 문장. 질문 줄의 툴팁에 쓴다. */
  questionText: ReadonlyMap<string, string>;
  onOpen: (turn: AnalysisTurn) => void;
  openTarget?: 'conversation' | 'waterfall';
}

const TurnRow = ({ turn, selected = false, multiDay, timeZone, showOffset, abandoned = false, questionText, onOpen, openTarget = 'waterfall' }: RowProps) => {
  const kind = turnKindBadge(turn);
  const preview = previewView(turn.preview);
  const edited = editedView(turn.edited);
  const question = turnQuestionView(turn);
  const commits = turn.commits ?? [];
  const extraTasks = turnExtraTasks(turn);
  const request = turn.is_request;
  const questionTitle = turn.question_headers
    .map((q) => questionText.get(q.tool_use_id))
    .filter((t): t is string => Boolean(t))
    .join('\n');

  const select = () => onOpen(turn);

  return (
    <li className="border-t border-border-subtle first:border-t-0">
      <button
        type="button"
        onClick={select}
        aria-pressed={selected}
        title={openTarget === 'conversation' ? `대화에서 이 ${request ? '요청' : '턴'} 보기` : '턴 워터폴에서 선택'}
        className={cn(
          'grid w-full grid-cols-[1.75rem_minmax(0,1fr)] gap-x-1 px-3 py-1.5 text-left transition-colors hover:bg-surface-2 focus-visible:-outline-offset-2!',
          selected && 'bg-brand-soft',
          abandoned && 'border-l-2 border-dashed border-border-strong bg-surface-2/60 pl-2.5',
        )}
      >
        <span className={cn('pt-px text-right font-mono text-[11.5px] tabular-nums', abandoned ? 'text-ink-3' : request ? 'font-medium text-ink' : 'text-ink-4')}>
          {ordinal(turn)}
        </span>

        <span className={cn('flex min-w-0 flex-col gap-0.5', abandoned && 'opacity-70')}>
          <span className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5">
            <span title={timeZone ? turnTimeLabel(turn.started_at, multiDay, timeZone) : undefined} className="shrink-0 font-mono text-[11px] whitespace-nowrap tabular-nums text-ink-3">
              {turnTimeLabel(turn.started_at, multiDay, timeZone, showOffset)}
            </span>
            <StatusChip turn={turn} />
            {kind && (
              <Chip tone="outline" size="sm">
                {kind}
              </Chip>
            )}
            {turn.mode && (
              <Chip tone={turn.mode === 'plan' ? 'brand' : 'outline'} size="sm" title={`권한 모드: ${turn.mode}`}>
                {modeText(turn.mode)}
              </Chip>
            )}
            {turn.denied && (
              <Chip tone="danger" size="sm" title="사용자가 도구 사용이나 플랜을 반려했습니다(toolDenialKind = user-rejected).">
                반려
              </Chip>
            )}
            {turn.images > 0 && (
              <Chip tone="neutral" size="sm" icon={ImageIcon} title={`이미지 ${turn.images}개를 붙였습니다.`}>
                {turn.images}
              </Chip>
            )}
            {turn.opener_copied && (
              <Chip tone="outline" size="sm" title="이 턴을 연 입력은 다른 세션에서 복사된 줄입니다.">
                복사된 입력
              </Chip>
            )}
            {turn.extra_inputs > 0 && (
              <Chip tone="outline" size="sm" title="이 턴이 진행되는 동안 들어와 새 턴을 열지 않은 입력입니다.">
                작업 중 입력 +{turn.extra_inputs}
              </Chip>
            )}
            <span
              className={cn(
                'min-w-[10ch] flex-1 basis-[12ch] truncate text-[12px] leading-[1.5]',
                preview.kind === 'text' ? (request ? 'text-ink' : 'text-ink-2') : 'text-ink-3 italic',
              )}
              title={preview.text}
            >
              {preview.text}
            </span>
            {edited && (
              <span
                className="inline-flex shrink-0 items-center gap-1 rounded-full border border-border-subtle px-1.5 py-px text-[10.5px] leading-4 text-ink-3"
                title={`이 턴에서 편집 ${formatCount(edited.count)}회${edited.files ? ` · 경로 표본 ${edited.files}${edited.more > 0 ? ` · 외 편집 ${formatCount(edited.more)}회` : ''}` : ''}`}
              >
                <FilePenLine size={10} strokeWidth={2} aria-hidden="true" />
                <span className="tabular-nums">{formatCount(edited.count)}회</span>
              </span>
            )}
            {turn.prs.map((pr) => (
              <Chip key={pr.url} tone="neutral" size="sm" icon={GitPullRequest} title={`PR #${pr.number} ${pr.repository}`}>
                #{pr.number}
              </Chip>
            ))}
            <MetricsChip turn={turn} />
          </span>

          {question && (
            <span className="text-[11px] leading-4 text-ink-3" title={questionTitle || undefined}>
              {question.text}
              {question.flags.length > 0 && <span className="ml-1 text-warning-strong">· {question.flags.join(', ')}</span>}
            </span>
          )}

          {commits.map((commit, i) => (
            <span key={`${commit.short ?? 'unknown'}-${i}`} className="flex min-w-0 items-center gap-1.5 pl-2">
              <HashChip short={commit.short} />
              <span className="min-w-0 truncate text-[11.5px] leading-4 text-ink-2" title={commit.subject}>
                {commit.subject}
              </span>
              {commit.tasks.map((id) => (
                <TaskChip key={id} id={id} />
              ))}
            </span>
          ))}

          {((edited && edited.abandoned > 0) || extraTasks.length > 0) && (
            <span className="flex flex-wrap items-center gap-1 pl-2">
              {edited && edited.abandoned > 0 && (
                <Chip tone="unknown" size="sm" title="되감기로 버려진 분기에서 한 편집 이벤트(따로 센다)">
                  버려진 편집 {edited.abandoned}회
                </Chip>
              )}
              {extraTasks.map((id) => (
                <TaskChip key={id} id={id} />
              ))}
            </span>
          )}
        </span>
      </button>
    </li>
  );
};

const BOUNDARY_REASON: Record<string, string> = {
  pre_lineage: '구형 기록에 계보 키가 없는 메시지가 있어 턴과 요청을 만들지 않았습니다. 토큰과 비용은 관측한 요청 합계이며, 요청이 없다는 뜻은 아닙니다.',
  redacted:
    '프롬프트 비식별화로 사람이 친 입력과 도구 결과를 가를 수 없어, 턴과 요청을 만들지 않았습니다. 요청이 없다는 뜻이 아닙니다.',
};

const MoreButton = ({ onClick, children }: { onClick: () => void; children: React.ReactNode }) => (
  <div className="border-t border-border-subtle px-3 py-1.5 text-center">
    <button
      type="button"
      onClick={onClick}
      className="rounded px-2 py-1 text-[11.5px] font-medium text-ink-2 hover:bg-surface-sunk hover:text-ink"
    >
      {children}
    </button>
  </div>
);

interface TurnsPanelProps {
  id: string;
  turns: readonly AnalysisTurn[];
  selectedTurnKey?: string;
  abandoned: readonly AnalysisTurn[];
  counts: DeepPartial<AnalysisCounts> | undefined;
  truncated: Pick<AnalysisTruncated, 'turns' | 'abandoned'>;
  subagents: AnalysisSubagents;
  questions: readonly AnalysisQuestion[];
  timeZone?: string;
  onOpenRequest: (turn: AnalysisTurn) => void;
  openTarget?: 'conversation' | 'waterfall';
}

const TurnsPanel = ({ id, turns, selectedTurnKey, abandoned, counts, truncated, subagents, questions, timeZone, onOpenRequest, openTarget }: TurnsPanelProps) => {
  const [shown, setShown] = useState(PAGE);
  const [abandonedShown, setAbandonedShown] = useState(PAGE);
  const [abandonedOpen, setAbandonedOpen] = useState(true);
  const ordered = orderTurns(turns, abandoned);
  const allTurns = [...ordered.active, ...ordered.abandoned];
  const multiDay = spansMultipleDays(allTurns, timeZone);
  const repeatedTimes = repeatedTurnTimes(allTurns, timeZone);
  const abandonedTotals = turnTotals(ordered.abandoned);
  // 서버가 센 전체 수가 있으면 그것을 쓴다. 받은 배열은 상한에서 잘렸을 수 있다.
  const abandonedCount = typeof counts?.abandoned_turns === 'number' ? counts.abandoned_turns : ordered.abandoned.length;
  const abandonedPartial = truncated.abandoned && abandonedCount > ordered.abandoned.length;
  const reasons = unknownReasons([...ordered.active, ...ordered.abandoned]);
  const questionText = new Map<string, string>();
  for (const q of questions) {
    const first = q.items[0];
    if (first?.question) questionText.set(q.tool_use_id, first.question);
  }

  const undeterminable = counts?.turn_boundaries?.state === 'undeterminable';
  const visible = ordered.active.slice(0, shown);
  const remaining = ordered.active.length - visible.length;
  const visibleAbandoned = ordered.abandoned.slice(0, abandonedShown);
  const abandonedRemaining = ordered.abandoned.length - visibleAbandoned.length;
  const total = typeof counts?.turns === 'number' ? counts.turns : undeterminable ? null : ordered.active.length;

  return (
    <Panel id={id} title={total === null ? '턴 상세' : `턴 상세 ${formatCount(total)}건`} bodyClassName="p-0">
      {undeterminable ? (
        <div className="flex flex-col items-center gap-2 px-6 py-6 text-center">
          <CircleDashed size={24} strokeWidth={1.5} className="text-ink-4" aria-hidden="true" />
          <p className="text-[12.5px] font-medium text-ink-2">턴 목록을 만들지 않았습니다</p>
          <p className="max-w-[44ch] text-[11.5px] leading-relaxed text-ink-3">
            {(counts?.turn_boundaries?.reason ? BOUNDARY_REASON[counts.turn_boundaries.reason] : undefined) ??
              '턴의 경계를 판단할 수 없어 턴과 요청을 만들지 않았습니다. 요청이 없다는 뜻이 아닙니다.'}{' '}
            토큰, 비용, 활동 범위는 위 머리에 그대로 있습니다.
          </p>
        </div>
      ) : ordered.active.length === 0 && ordered.abandoned.length === 0 ? (
        <p className="px-3 py-5 text-center text-[12px] text-ink-3">기록된 턴이 없습니다.</p>
      ) : (
        <>
          {ordered.active.length > 0 && (
            <ul aria-label="활성 경로의 턴">
              {visible.map((turn) => (
                <TurnRow key={turn.key} turn={turn} selected={turn.key === selectedTurnKey} multiDay={multiDay} timeZone={timeZone} showOffset={repeatedTimes.has(turn.started_at)} questionText={questionText} onOpen={onOpenRequest} openTarget={openTarget} />
              ))}
            </ul>
          )}
          {remaining > 0 && <MoreButton onClick={() => setShown((n) => n + 200)}>{formatCount(remaining)}개 더 보기</MoreButton>}
          {truncated.turns && (
            <p className="border-t border-border-subtle px-3 py-1.5 text-[11px] text-ink-3">
              턴이 많아 서버가 앞의 {formatCount(ordered.active.length)}개만 보냈습니다. 요청·입력·턴 수는 전체 기준입니다.
            </p>
          )}

          {ordered.abandoned.length > 0 && (
            <div className="border-t border-border bg-surface-2/60">
              <button
                type="button"
                aria-expanded={abandonedOpen}
                onClick={() => setAbandonedOpen((open) => !open)}
                className="flex w-full flex-wrap items-center gap-x-2 gap-y-0.5 px-3 py-1.5 text-left text-[11.5px] text-ink-2 hover:bg-surface-sunk/60 focus-visible:-outline-offset-2!"
              >
                <ChevronRight
                  size={13}
                  strokeWidth={2}
                  aria-hidden="true"
                  className={cn('shrink-0 text-ink-3 transition-transform motion-reduce:transition-none', abandonedOpen && 'rotate-90')}
                />
                <span className="font-semibold text-ink">버려진 분기 {formatCount(abandonedCount)}턴</span>
                <span className="text-ink-3">
                  되감기로 대화에서 빠졌습니다 · 요청 수와 컨텍스트에서는 빼고,{' '}
                  {abandonedPartial && `받은 ${formatCount(ordered.abandoned.length)}턴의 `}비용{' '}
                  {abandonedTotals.knownCost === 0 && abandonedTotals.unknownCost > 0 ? UNKNOWN_TEXT : formatUsd(abandonedTotals.usd)}
                  {abandonedTotals.knownCost > 0 && abandonedTotals.unknownCost > 0 && ` (${abandonedTotals.unknownCost}턴은 비용 모름)`}과 토큰{' '}
                  {formatTokenCount(abandonedTotals.tokens)}은 세션 합계에 포함
                </span>
              </button>
              {abandonedOpen && (
                <>
                  <ul aria-label="버려진 분기의 턴" className="border-t border-border-subtle">
                    {visibleAbandoned.map((turn) => (
                      <TurnRow key={turn.key} turn={turn} selected={turn.key === selectedTurnKey} multiDay={multiDay} timeZone={timeZone} showOffset={repeatedTimes.has(turn.started_at)} abandoned questionText={questionText} onOpen={onOpenRequest} openTarget={openTarget} />
                    ))}
                  </ul>
                  {abandonedRemaining > 0 && (
                    <MoreButton onClick={() => setAbandonedShown((n) => n + 200)}>버려진 분기 {formatCount(abandonedRemaining)}개 더 보기</MoreButton>
                  )}
                </>
              )}
              {truncated.abandoned && <p className="border-t border-border-subtle px-3 py-1.5 text-[11px] text-ink-3">버려진 분기가 많아 일부만 담았습니다.</p>}
            </div>
          )}

          {reasons.length > 0 && (
            <div className="border-t border-border-subtle px-3 py-2">
              <p className="mb-1 text-[11px] font-medium text-ink-2">알 수 없는 값의 이유</p>
              <ul className="space-y-1 text-[11px] leading-relaxed text-ink-3">
                {reasons.map((reason) => (
                  <li key={reason.key}>
                    <span className="font-medium text-ink-2">
                      {reason.label} {formatCount(reason.turns)}턴
                    </span>{' '}
                    — {reason.text}
                  </li>
                ))}
              </ul>
            </div>
          )}

          {subagents.unattributed_requests > 0 && (
            <p className="border-t border-border-subtle px-3 py-1.5 text-[11px] leading-relaxed text-ink-3">
              서브에이전트 호출 {formatCount(subagents.unattributed_requests)}건(토큰 {formatTokenCount(subagents.unattributed_tokens)})은 어느 요청에도 귀속하지
              못해 세션 합계에만 들어 있습니다.
            </p>
          )}
        </>
      )}
    </Panel>
  );
};

export { TurnsPanel };
