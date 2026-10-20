'use client';

import { ChevronDown } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { AnalysisHeader, AnalysisState, DeepPartial, SessionJudgement } from '@/lib/session-analysis-types';
import { Hint, PercentBar, Unknown } from './atoms';
import { CategoryChip } from './category-chip';
import { MetricChip, StatusChip } from './chips';
import { boundaryReason, durationOf } from './details-strip';
import {
  contextView,
  costView,
  formatClockNear,
  formatCount,
  formatDuration,
  formatRelative,
  formatTokensChip,
  formatUsdCompact,
  titleView,
  tokenView,
} from './format';
import { UNATTACHED_COST_HINT, NAME_SOURCE_HINT, NAME_SOURCE_LABEL, UNKNOWN_TEXT } from './labels';
import { statusView } from './judgement';
import { tokensBasisText } from './header-stats';

/**
 * 머리(D1~D7): 두 줄이다.
 *   첫 줄  짧은 ID · 상태 칩 · 제목 · 탭 이름 칩
 *   둘째 줄 계정 칩 · "HH:MM에 시작 · 마지막 활동 N분 전 (길이 N시간 N분) · 요청 N건" ····· 지표 칩 셋(토큰, 비용, 컨텍스트) · 세부
 * 상태 칩은 판정(`judgement.status`)이다. 판정이 없는 응답(비소유 관리자, 아직 안 보내는 서버)은 관측 범주 칩이 대신한다.
 * `building`·`truncated`에서는 롤업 머리만 와서 어느 칸이든 비어 있을 수 있다. 빈 칸은 0이 아니라 "알 수 없음"이다.
 */

type Header = DeepPartial<AnalysisHeader>;

/** 롤업 머리만 있는 상태에서 제목이 없는 이유. "제목 없음"이라고 단정하지 않고 왜 못 읽었는지를 적는다. */
const TITLE_UNREAD_TEXT: Partial<Record<AnalysisState, string>> = {
  truncated: '세션이 너무 커서 제목을 읽지 않았습니다',
  unsupported: '이 에이전트는 분석하지 않아 제목을 읽지 않습니다',
};

const ShortId = ({ id, agent }: { id: string; agent: string | undefined }) => (
  <span
    title="세션 ID 앞 8자"
    className={cn(
      'inline-flex shrink-0 items-center rounded-full border px-2 py-px font-mono text-[11px] leading-4 text-ink',
      agent === 'codex' ? 'border-agent-codex/60 bg-agent-codex-soft' : 'border-agent-claude/60 bg-agent-claude-soft',
    )}
  >
    {id}
  </span>
);

const TitleText = ({ header, state }: { header: Header; state: AnalysisState }) => {
  const view = titleView(header.title);
  // 만드는 동안에는 제목 자리를 비워 둔다. 없다고 단정하지 않으므로 "제목 없음"도 쓰지 않는다.
  if (state === 'building' && !view.text) return null;
  const unread = header.title === undefined ? TITLE_UNREAD_TEXT[state] : undefined;
  const shown = view.text ?? unread ?? view.placeholder;
  // 제목의 출처("AI 제목" 등)는 평소에는 글자 없이 툴팁에만 둔다. 눈에 띄어야 하는 것은 제목이 보류된 경우뿐이다.
  const sourceHint = view.tag && !view.hint ? view.tag : undefined;
  return (
    <>
      <h2
        title={view.text ? sourceHint : (view.hint ?? undefined)}
        className={cn(
          'min-w-0 max-w-full truncate text-[13px] leading-5 break-keep',
          view.text ? 'font-semibold text-ink' : 'font-normal text-ink-3 italic',
        )}
      >
        {shown}
      </h2>
      {view.tag && view.hint && (
        <Hint label={view.hint} ariaLabel={`${view.tag}. ${view.hint}`}>
          <span className="inline-flex shrink-0 items-center rounded-full border border-dashed border-border-strong px-1.5 py-px text-[10.5px] leading-4 text-ink-3">
            {view.tag}
          </span>
        </Hint>
      )}
    </>
  );
};

/** 계정 칩: 레퍼런스의 "サブ" 자리. 로그인 이메일의 아이디 부분을 짧게 보이고 전체는 툴팁이다. */
const AccountChip = ({ header }: { header: Header }) => {
  const emails = header.accounts?.login_emails ?? [];
  if (emails.length === 0) {
    return <Unknown compact label="계정 미상" reason="이 세션은 어느 계정으로 쓰였는지 알려주는 기록이 없습니다." />;
  }
  const first = emails[0];
  const local = first.split('@')[0] || first;
  const more = emails.length > 1 ? ` +${emails.length - 1}` : '';
  const inferred = header.accounts?.inferred ? ' (추정)' : '';
  return (
    <span
      title={`${emails.join(', ')}${inferred}`}
      className={cn(
        'inline-flex max-w-[11rem] shrink-0 items-center rounded-md border px-1.5 py-px text-[10.5px] leading-4 text-ink-2',
        header.accounts?.inferred ? 'border-dashed border-border-strong' : 'border-border bg-surface-sunk',
      )}
    >
      <span className="truncate">{local}</span>
      {more}
    </span>
  );
};

const MetaText = ({ header, now, hideRequests, timeZone }: { header: Header; now: number; hideRequests: boolean; timeZone?: string }) => {
  const last = header.last_activity_at;
  const duration = durationOf(header);
  const requests = header.counts?.requests;
  const requestsKnown = typeof requests === 'number';
  return (
    <span className="min-w-0 text-[11.5px] leading-5 text-ink-2">
      {header.started_at ? <>{formatClockNear(header.started_at, now, timeZone)}에 시작</> : <Unknown label="시작 알 수 없음" />}
      <span aria-hidden="true" className="px-1 text-ink-4">
        ·
      </span>
      마지막 활동 {last ? <span className="tabular-nums">{formatRelative(last, now)}</span> : <Unknown />}
      {duration !== null && <span className="text-ink-3"> (길이 {formatDuration(duration)})</span>}
      {!hideRequests && (
        <>
          <span aria-hidden="true" className="px-1 text-ink-4">
            ·
          </span>
          요청{' '}
          {requestsKnown ? (
            <b className="font-semibold tabular-nums text-ink">{formatCount(requests)}건</b>
          ) : (
            <Unknown reason={boundaryReason(header)} />
          )}
        </>
      )}
    </span>
  );
};

const TokensChip = ({ header }: { header: Header }) => {
  const view = tokenView(header.tokens);
  const apiRequests = header.counts?.api_requests;
  if (view.total === null) {
    return (
      <MetricChip tone="unknown" ariaLabel="총 토큰 알 수 없음" hint="입력·출력·캐시 토큰 중 모르는 칸이 있어 합계를 내지 않았습니다.">
        ? tok
      </MetricChip>
    );
  }
  const chip = formatTokensChip(view.total) ?? UNKNOWN_TEXT;
  return (
    <MetricChip
      tone="tokens"
      ariaLabel={`총 토큰 ${chip}`}
      hint={
        <span className="block space-y-0.5">
          <span className="block font-medium">총 토큰 · {tokensBasisText(header.tokens?.basis)}</span>
          {view.parts.map((p) => (
            <span key={p.key} className="flex justify-between gap-4 tabular-nums">
              <span>{p.label}</span>
              <span>{p.value === null ? UNKNOWN_TEXT : p.value.toLocaleString('en-US')}</span>
            </span>
          ))}
          {typeof apiRequests === 'number' && (
            <span className="flex justify-between gap-4 border-t border-background/20 pt-0.5 tabular-nums">
              <span>API 호출</span>
              <span>{apiRequests.toLocaleString('en-US')}</span>
            </span>
          )}
        </span>
      }
    >
      {chip}
    </MetricChip>
  );
};

const CostChip = ({ header }: { header: Header }) => {
  const view = costView(header.cost);
  if (!view.known) {
    return (
      <MetricChip tone="unknown" ariaLabel="비용 알 수 없음" hint={view.sourceHint}>
        $?
      </MetricChip>
    );
  }
  const amount = formatUsdCompact(header.cost?.usd);
  const noBoundaries = header.counts?.turn_boundaries?.state === 'undeterminable';
  const mark = view.tone === 'estimated' ? '추정' : view.tone === 'partial' ? '일부' : null;
  return (
    <MetricChip
      tone="cost"
      ariaLabel={`비용 ${amount}${view.sourceLabel ? `, 출처 ${view.sourceLabel}` : ''}`}
      hint={
        <span className="block space-y-0.5">
          <span className="block font-medium">
            비용 {view.amount} · {view.sourceLabel}
          </span>
          <span className="block">{view.sourceHint}</span>
          {noBoundaries ? (
            <span className="block">턴 경계를 몰라 요청별로 나누지 않았습니다.</span>
          ) : view.split ? (
            <>
              <span className="flex justify-between gap-4 tabular-nums">
                <span>턴 귀속</span>
                <span>{view.split.attached}</span>
              </span>
              <span className="flex justify-between gap-4 tabular-nums">
                <span>붙지 않은 호출</span>
                <span>{view.split.unattached}</span>
              </span>
              <span className="block text-background/70">{UNATTACHED_COST_HINT}</span>
            </>
          ) : null}
          {view.note && <span className="block">{view.note}</span>}
        </span>
      }
    >
      {amount}
      {mark && <span className="text-[9.5px] font-normal opacity-75">{mark}</span>}
    </MetricChip>
  );
};

const ContextChip = ({ header }: { header: Header }) => {
  const view = contextView(header.context);
  if (view.kind === 'unknown') {
    return (
      <MetricChip tone="unknown" ariaLabel="컨텍스트 알 수 없음" hint={`${view.summary}. ${view.sourceHint}`}>
        ctx ?
      </MetricChip>
    );
  }
  if (view.kind === 'tokens_only') {
    return (
      <MetricChip tone="context" ariaLabel={view.summary} hint={`${view.summary}. ${view.sourceHint}${view.largeWindowHint ? ` ${view.largeWindowHint}` : ''}`}>
        ctx {view.tokensLabel}
      </MetricChip>
    );
  }
  return (
    <MetricChip tone="context" ariaLabel={view.summary} hint={`${view.summary}. ${view.sourceLabel}: ${view.sourceHint}`}>
      <span className="font-normal text-ink-3">ctx</span>
      <PercentBar value={view.percentValue ?? 0} label={view.summary} className="w-10" fillClass="fill-cat-1" />
      {view.percentLabel}
    </MetricChip>
  );
};

interface SessionHeadProps {
  shortId: string;
  state: AnalysisState;
  header: Header | undefined;
  judgement?: SessionJudgement;
  ownerView: boolean;
  now: number;
  timeZone?: string;
  /** 세부 띠를 접고 펼치는 버튼을 보인다. `building` 같은 응답은 띠가 늘 펼쳐져 있어 버튼이 없다. */
  detailsToggle?: { open: boolean; onToggle: () => void; controls: string };
  /** 이 에이전트는 분석 대상이 아니다(unsupported). 요청 수와 컨텍스트는 "모름"이 아니라 해당 없음이라 그리지 않는다. */
  notApplicable?: boolean;
}

const SessionHead = ({
  shortId,
  state,
  header,
  judgement,
  ownerView,
  now,
  timeZone,
  detailsToggle,
  notApplicable = false,
}: SessionHeadProps) => {
  const h: Header = header ?? {};
  const category = ownerView ? h.category : undefined;
  const name = h.name;
  return (
    <header aria-label="세션 머리" className="border-b border-border bg-surface px-3 py-2">
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <ShortId id={shortId} agent={h.agent} />
        {judgement ? (
          <StatusChip view={statusView(judgement.status)} />
        ) : (
          category?.key && <CategoryChip category={category.key} waitingKind={category.waiting_kind} />
        )}
        {judgement && category?.key === 'waiting' && <CategoryChip category={category.key} waitingKind={category.waiting_kind} />}
        <TitleText header={h} state={state} />
        {name?.text && (
          <span
            title={name.source ? `탭 이름 · ${NAME_SOURCE_LABEL[name.source] ?? name.source} — ${NAME_SOURCE_HINT[name.source] ?? ''}` : '탭 이름'}
            className="inline-flex max-w-[16rem] shrink-0 items-center rounded-full border border-border px-2 py-px text-[10.5px] leading-4 text-ink-3"
          >
            <span className="truncate">탭 이름: {name.text}</span>
          </span>
        )}

      </div>

      <div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <AccountChip header={h} />
        <MetaText header={h} now={now} hideRequests={notApplicable} timeZone={timeZone} />
        <div className="ml-auto flex shrink-0 flex-wrap items-center gap-1.5">
          <TokensChip header={h} />
          <CostChip header={h} />
          {!notApplicable && <ContextChip header={h} />}
          {detailsToggle && (
            <button
              type="button"
              aria-expanded={detailsToggle.open}
              aria-controls={detailsToggle.controls}
              onClick={detailsToggle.onToggle}
              className="inline-flex h-5 items-center gap-0.5 rounded px-1 text-[11px] text-ink-3 hover:bg-surface-sunk hover:text-ink-2"
            >
              세부
              <ChevronDown
                size={12}
                strokeWidth={2}
                aria-hidden="true"
                className={cn('transition-transform motion-reduce:transition-none', detailsToggle.open && 'rotate-180')}
              />
            </button>
          )}
        </div>
      </div>
    </header>
  );
};

export { SessionHead };
