'use client';

import { GitCommitHorizontal, StickyNote } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { SessionOverview } from '@/lib/types';
import type { SessionRowAnalysis } from '@/lib/session-analysis-types';
import { fmt, fmtCost, fmtSessionPeriod, projectLabel } from '../session-utils';
import { Chip, PercentBar, Unknown } from './atoms';
import { CategoryChip } from './category-chip';
import { contextView, formatCount, formatRelative, formatTokensChip, formatUsdCompact, titleView } from './format';
import { rowSignalLines, type SignalLine, type SignalTone } from './signals';
import { recordReasonDescription, RECORD_STATE_WORD } from './record';

/**
 * 세션 목록의 카드(F1). 위에서 아래로:
 *   범주 칩 · 제목(두 줄까지)
 *   (세션 기록의 한 줄 요약)
 *   관측 신호 · 상대 시각 · 짧은 ID · 계정 · 요청 수
 *   토큰 · 비용 · 컨텍스트 막대 · 커밋 수
 *   메모의 첫 줄
 * 판정 결론(완료/미완료)은 그리지 않는다. 남은 질문·백그라운드·미커밋처럼 원자료로 확인되는
 * 신호만 짧은 칩으로 두고, 문장과 판단 불가 이유는 툴팁에 담는다. `analysis`가 없으면
 * 패스 A 이전의 행과 같은 정보만 그린다.
 * 모르는 값은 0이 아니라 점선 "?"다.
 */

const AGENT_BADGE: Record<string, string> = {
  claude: 'bg-agent-claude-soft text-ink-2',
  codex: 'bg-agent-codex-soft text-ink-2',
};

const SIGNAL_TONE_CLASS: Record<SignalTone, string> = {
  quiet: 'text-ink-3',
  attention: 'bg-warning-soft font-medium text-warning-strong',
  live: 'bg-brand-soft font-medium text-brand',
  danger: 'bg-danger-soft font-medium text-danger-strong',
  unknown: 'border-dashed border-border-strong text-ink-3',
};

/** 판정이 없는 응답의 대신: 신호 축약 한 칸. 남은 것이 있으면 색, 없으면 옅은 글자, 판단 불가면 점선. */
const SignalCell = ({ line }: { line: SignalLine }) => (
  <span
    role="img"
    aria-label={line.sentence}
    title={line.undeterminable ? `${line.sentence} — ${line.reason ?? ''}` : [line.sentence, line.note].filter(Boolean).join(' — ')}
    className={cn(
      'inline-flex items-center gap-1 rounded border border-transparent px-1 py-px text-[10px] leading-4 whitespace-nowrap',
      SIGNAL_TONE_CLASS[line.tone],
    )}
  >
    <span className={cn(line.tone === 'quiet' || line.tone === 'unknown' ? '' : 'opacity-80')}>{line.label}</span>
    <span className="tabular-nums">{line.value}</span>
  </span>
);

const STATE_NOTE: Partial<Record<SessionRowAnalysis['state'], { text: string; title: string }>> = {
  stale: { text: '분석 규칙 갱신 중', title: '값은 한 단계 이전 규칙으로 계산돼 곧 바뀔 수 있습니다.' },
  none: { text: '분석 없음', title: '분석 구축은 끝났지만 읽을 수 있는 기록이 없습니다.' },
  truncated: { text: '세션이 커서 분석을 생략', title: '기록이 너무 많거나 계산이 제한 시간 안에 끝나지 않아 합계만 있습니다.' },
  unsupported: { text: '분석을 지원하지 않는 에이전트', title: 'Claude Code와 Codex만 분석합니다.' },
  withheld: {
    text: '분석 보류 · 신원이 섞인 세션',
    title: '한 세션에 여러 신원의 기록이 섞여 세션 주인에게는 분석을 보이지 않습니다.',
  },
};

const ContextMini = ({ ctx }: { ctx: NonNullable<SessionRowAnalysis['context']> }) => {
  const view = contextView(ctx);
  if (view.kind === 'percent') {
    return (
      <span className="inline-flex items-center gap-1" title={`${view.summary} — ${view.sourceHint}`}>
        <PercentBar value={view.percentValue ?? 0} label={view.summary} className="w-8" fillClass="fill-cat-1" />
        <b className="font-medium tabular-nums text-ink-2">{view.percentLabel}</b>
      </span>
    );
  }
  if (view.kind === 'tokens_only') {
    return (
      <span className="inline-flex items-center gap-1" title={`${view.summary}${view.largeWindowHint ? ` — ${view.largeWindowHint}` : ''}`}>
        <span>ctx</span>
        <span className="tabular-nums text-ink-2">{view.tokensLabel}</span>
        {ctx.large_window_seen && <span className="text-ink-4">(큰 창 흔적)</span>}
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1">
      <span>ctx</span>
      <Unknown inert compact reason="마지막 요청의 컨텍스트 크기를 알 수 없습니다." />
    </span>
  );
};

/** 로그인 이메일의 아이디 부분. 카드는 좁아서 전체는 툴팁이다. */
const accountName = (session: SessionOverview): string => {
  const email = session.login_email || session.profile_email;
  return email ? email.split('@')[0] || email : '-';
};

interface SessionAnalysisRowProps {
  session: SessionOverview;
  selected: boolean;
  onSelect: (session: SessionOverview) => void;
  /** 기존 LIVE 배지(마지막 기록이 5분 이내). 범주가 오지 않는 범위에서 쓴다. 호출하는 쪽이 정한다. */
  live?: boolean;
  /** 상대 시각의 기준. 없으면 패스 A의 "시작 – 끝" 기간을 그린다(렌더 중에 시계를 읽지 않는다). */
  now?: number;
  /** "함께 끝난 세션" 묶음 안의 카드에 붙는 "떼기". */
  onDismiss?: (session: SessionOverview) => void;
}

const SessionAnalysisRow = ({ session, selected, onSelect, live = false, now, onDismiss }: SessionAnalysisRowProps) => {
  const analysis = session.analysis;
  const title = analysis?.title ? titleView(analysis.title) : null;
  const signals = rowSignalLines(analysis?.signals);
  const stateNote = analysis ? STATE_NOTE[analysis.state] : undefined;
  const recordState = analysis?.record_state;
  const recordReason = analysis?.record_reason;
  const project = projectLabel(session);
  const extraAccount = session.login_email && session.login_email !== session.profile_email ? session.login_email : '';
  const multiAccount = (session.account_count ?? 1) > 1;
  const showRequests = analysis !== undefined && analysis.requests !== undefined;
  const tokens = analysis?.tokens_total === undefined ? undefined : formatTokensChip(analysis.tokens_total);
  const commitCount = analysis?.commits;
  const note = analysis?.note?.trim() ? analysis.note.trim() : null;
  const headline = analysis?.headline?.trim() ? analysis.headline.trim() : null;
  const planFlag = analysis?.plan_flags?.has_plan_file || analysis?.plan_flags?.used_plan_mode;

  return (
    <div className="relative border-b border-border/50">
      <button
        type="button"
        data-session-id={session.session_id}
        onClick={() => onSelect(session)}
        aria-pressed={selected}
        className={cn(
          'block w-full cursor-pointer px-3 py-2 text-left hover:bg-canvas focus-visible:-outline-offset-2!',
          onDismiss && 'pr-12',
          selected && 'border-l-2 border-l-brand bg-surface-sunk hover:bg-surface-sunk',
        )}
      >
        <div className="flex min-w-0 items-start gap-1.5">
          {analysis?.category && <CategoryChip category={analysis.category} short className="mt-px" />}
          {live && (
            <span className="mt-px inline-flex shrink-0 items-center gap-1 border-l border-border pl-1.5 text-[10px] text-ink-2">
              <span className="size-1.5 rounded-full border border-ink-3" aria-hidden="true" />
              실행 중
            </span>
          )}
          {title && (
            <span
              className={cn(
                'min-w-0 flex-1 line-clamp-2 text-[12.5px] leading-[1.35] [overflow-wrap:anywhere]',
                title.text ? 'font-semibold text-ink' : 'font-normal text-ink-3 italic',
              )}
              title={title.hint ?? undefined}
            >
              {title.text ?? title.placeholder}
              {title.kind === 'withheld' && title.text && (
                <Chip tone="unknown" size="sm" className="ml-1.5 align-middle" title="제목 보류됨(첫 요청): 서버가 아직 제목을 받지 않아 첫 요청의 첫 줄을 보입니다.">
                  첫 요청
                </Chip>
              )}
            </span>
          )}
        </div>

        {headline && <div className="mt-0.5 line-clamp-1 text-[11px] leading-4 text-ink-3">{headline}</div>}
        {recordState && (
          <div
            aria-label={`세션 기록 상태: ${RECORD_STATE_WORD[recordState]}`}
            title={recordReason ? `서버 사유: ${recordReasonDescription(recordReason) ?? '설명 없음'} · 코드 ${recordReason}` : '세션 기록 생성 상태를 서버가 보고했습니다.'}
            className="mt-0.5 text-[10.5px] leading-4 text-ink-3"
          >
            기록 {RECORD_STATE_WORD[recordState]}{recordReason ? ` · ${recordReason}` : ''}
          </div>
        )}

        <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[10.5px] text-ink-3">
          {signals.length > 0 && (
            <span role="group" aria-label="관측 신호" className="inline-flex flex-wrap items-center gap-1">
              {signals.map((line) => (
                <SignalCell key={line.key} line={line} />
              ))}
            </span>
          )}
          <span className="tabular-nums">{now !== undefined ? formatRelative(session.end_time, now) : fmtSessionPeriod(session.start_time, session.end_time)}</span>
          <span className="font-mono text-ink-2">{session.session_id?.slice(0, 8) ?? '-'}</span>
          {AGENT_BADGE[session.agent] && session.agent !== 'claude' && (
            <span className={cn('inline-flex items-center rounded px-1 py-px text-[10px] font-medium', AGENT_BADGE[session.agent])}>{session.agent}</span>
          )}
          <span
            title={[session.profile_email, session.login_email, project, session.user_id ? `ID ${session.user_id}` : ''].filter(Boolean).join(' · ')}
            className="inline-flex max-w-[8rem] items-center rounded-md border border-border bg-surface-sunk px-1 py-px text-ink-2"
          >
            <span className="truncate">{accountName(session)}</span>
          </span>
          {project && <span className="min-w-0 max-w-[10rem] truncate">{project}</span>}
          {multiAccount && (
            <span
              title={`이 세션은 계정 ${session.account_count}개에 걸쳐 있습니다. 표시된 계정은 사용량이 가장 많은 쪽이며, 토큰·비용은 전체 합계입니다.`}
              className="inline-flex shrink-0 items-center rounded bg-warning-soft px-1 py-px text-[10px] font-medium text-warning-strong"
            >
              계정 {session.account_count}
            </span>
          )}
        </div>

        <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10.5px] text-ink-3">
          {showRequests && (
            <span className="inline-flex items-center gap-1">
              요청
              {analysis.requests === null || analysis.requests === undefined ? (
                <Unknown inert compact reason="턴의 경계를 판단할 수 없어 요청 수를 세지 않았습니다. 0이 아닙니다." />
              ) : (
                <b className="font-medium tabular-nums text-ink-2">{formatCount(analysis.requests)}</b>
              )}
            </span>
          )}
          {tokens !== undefined ? (
            tokens === null ? (
              <Unknown inert compact label="토큰 알 수 없음" reason="총 토큰을 알 수 없습니다. 0이 아닙니다." />
            ) : (
              <span className="tabular-nums text-ink-2">{tokens}</span>
            )
          ) : (
            <span className="tabular-nums">
              In <span className="font-medium text-ink-2">{fmt(session.input_tokens)}</span> · Out{' '}
              <span className="font-medium text-ink-2">{fmt(session.output_tokens)}</span>
            </span>
          )}
          <span className="tabular-nums text-ink-2">{analysis?.tokens_total !== undefined ? (session.cost_usd > 0 ? formatUsdCompact(session.cost_usd) : '-') : fmtCost(session.cost_usd)}</span>
          {analysis?.context && <ContextMini ctx={analysis.context} />}
          {commitCount !== undefined && (
            <span className="inline-flex items-center gap-1">
              <GitCommitHorizontal size={11} strokeWidth={2} className="shrink-0 text-cat-6" aria-hidden="true" />
              커밋
              {commitCount === null ? (
                <Unknown inert compact reason="커밋 수를 알 수 없습니다(수집하지 않았거나 판단 불가). 0이 아닙니다." />
              ) : (
                <b className="font-medium tabular-nums text-ink-2">{formatCount(commitCount)}</b>
              )}
            </span>
          )}
          {planFlag && (
            <Chip tone="brand" size="sm" title="이 세션에 플랜 파일이나 플랜 모드가 있습니다.">
              플랜
            </Chip>
          )}
        </div>

        {note && (
          <div className="mt-1 flex items-start gap-1 text-[11px] leading-4 text-ink-2" title={note}>
            <StickyNote size={11} strokeWidth={1.75} className="mt-0.5 shrink-0 text-ink-3" aria-hidden="true" />
            <span className="line-clamp-1 min-w-0">{note}</span>
          </div>
        )}

        {stateNote && (
          <div className="mt-1 text-[11px] text-ink-3 italic" title={stateNote.title}>
            {stateNote.text}
          </div>
        )}
        {extraAccount && !multiAccount && <div className="sr-only">{extraAccount}</div>}
      </button>
      {onDismiss && (
        <button
          type="button"
          onClick={() => onDismiss(session)}
          title="이 세션을 함께 끝난 묶음에서 뗍니다"
          aria-label={`${session.session_id?.slice(0, 8) ?? ''} 떼기`}
          className="absolute top-1.5 right-2 rounded border border-border bg-surface px-1.5 py-px text-[10.5px] font-medium text-ink-2 hover:bg-surface-sunk"
        >
          떼기
        </button>
      )}
    </div>
  );
};

export { SessionAnalysisRow };
